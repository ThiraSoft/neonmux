package vt

import (
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

// Scrollback keeps the lines that scrolled off the top of the screen.
//
// Each line is encoded in a single []byte: trailing spaces are dropped,
// styles are grouped into runs and the text is stored as UTF-8. A typical
// log line thus goes from ~1.5 KB (cells) down to ~100 bytes. Once the ring
// is full, the buffer of the evicted line is reused: in steady state,
// pushing a line allocates nothing.
type Scrollback struct {
	max   int
	ring  [][]byte
	start int
	n     int
	total int64 // number of lines pushed since the start
	bytes int
}

func newScrollback(max int) *Scrollback { return &Scrollback{max: max} }

// Len returns the number of lines kept.
func (s *Scrollback) Len() int { return s.n }

// Bytes returns the memory used by the encoded lines.
func (s *Scrollback) Bytes() int { return s.bytes }

func (s *Scrollback) clear() {
	s.ring, s.start, s.n, s.bytes = nil, 0, 0, 0
}

func (s *Scrollback) push(cells []Cell, wrapped bool) {
	s.total++
	if s.max <= 0 {
		return
	}
	if s.n < s.max {
		buf := encodeLine(nil, cells, wrapped)
		s.ring = append(s.ring, buf)
		s.n++
		s.bytes += cap(buf)
		return
	}
	old := s.ring[s.start]
	s.bytes -= cap(old)
	buf := encodeLine(old[:0], cells, wrapped)
	// A recycled buffer keeps its capacity: after a peak (one color per
	// cell), short lines would hold on to megabytes. So we start over from a
	// buffer of the right size. The margin avoids reallocating on every line
	// when lengths vary a little.
	if cap(buf) > 4*len(buf)+256 {
		buf = append(make([]byte, 0, len(buf)), buf...)
	}
	s.ring[s.start] = buf
	s.bytes += cap(buf)
	s.start = (s.start + 1) % len(s.ring)
}

// pushEncoded works like push for a line already encoded by encodeLine.
func (s *Scrollback) pushEncoded(enc []byte) {
	s.total++
	if s.max <= 0 {
		return
	}
	if s.n < s.max {
		buf := append([]byte(nil), enc...)
		s.ring = append(s.ring, buf)
		s.n++
		s.bytes += cap(buf)
		return
	}
	old := s.ring[s.start]
	s.bytes -= cap(old)
	buf := append(old[:0], enc...)
	if cap(buf) > 4*len(buf)+256 {
		buf = append(make([]byte, 0, len(buf)), buf...)
	}
	s.ring[s.start] = buf
	s.bytes += cap(buf)
	s.start = (s.start + 1) % len(s.ring)
}

// line returns line i, 0 being the oldest. The ring's lines come from
// encodeLine or were validated when the snapshot was loaded.
func (s *Scrollback) line(i int, dst []Cell) ([]Cell, bool) {
	cells, wrapped, _ := decodeLine(s.ring[(s.start+i)%len(s.ring)], dst, math.MaxInt)
	return cells, wrapped
}

// Line format: flags (bit 0: line broken by an automatic wrap), cell count
// n, run count, the runs (starting cell, colors, attributes), then the UTF-8
// text, one rune per cell except for the right halves of wide characters.

// encodeLine appends the encoding of cells to dst. Two passes: the first
// measures the runs and the text, the second writes everything in place,
// into a buffer reserved only once at the exact size.
func encodeLine(dst []byte, cells []Cell, wrapped bool) []byte {
	n := len(cells)
	// Line ends that were never written to are the common case: skip them
	// in blocks of zero cells, then test Cell{} before IsBlank, which costs
	// much more.
	for n >= 8 && *(*[8]Cell)(cells[n-8 : n]) == [8]Cell{} {
		n -= 8
	}
	for n > 0 && (cells[n-1] == Cell{} || cells[n-1].IsBlank()) {
		n--
	}
	cells = cells[:n]

	// A run starts at each style change: attributes (the high bits of ra),
	// FG or BG. All three are compared at once with xor. The initial
	// "previous" style takes the inverse of the FG of the first encoded
	// character, so that the first run always opens.
	const attrBits = ^uint32(runeMask)
	const contBit = uint32(AttrWideCont) << runeBits
	var startFG Color
	for x := range cells {
		if cells[x].ra&contBit == 0 {
			startFG = ^cells[x].FG
			break
		}
	}
	var ps uint32
	pf, pb := startFG, Color(0)
	runs, runBytes, textBytes := 0, 0, 0
	for x := range cells {
		c := &cells[x]
		st := c.ra & attrBits
		if st&contBit != 0 {
			continue
		}
		if (c.FG^pf)|(c.BG^pb)|Color(st^ps) != 0 {
			runs++
			runBytes += uvarintLen(uint64(x)) + 8 + uvarintLen(uint64(st>>runeBits))
			ps, pf, pb = st, c.FG, c.BG
		}
		if r := c.Rune(); r < utf8.RuneSelf {
			textBytes++
		} else if l := utf8.RuneLen(r); l > 0 {
			textBytes += l
		} else {
			textBytes += 3 // invalid rune: EncodeRune writes U+FFFD
		}
	}

	size := 1 + uvarintLen(uint64(n)) + uvarintLen(uint64(runs)) + runBytes + textBytes
	p := len(dst)
	if cap(dst)-p < size {
		grown := make([]byte, p, p+size)
		copy(grown, dst)
		dst = grown
	}
	dst = dst[:p+size]
	b := dst[p:]
	b[0] = 0
	if wrapped {
		b[0] = 1
	}
	i := 1
	i += binary.PutUvarint(b[i:], uint64(n))
	i += binary.PutUvarint(b[i:], uint64(runs))
	tp := i + runBytes
	ps, pf, pb = 0, startFG, 0
	for x := range cells {
		c := &cells[x]
		st := c.ra & attrBits
		if st&contBit != 0 {
			continue
		}
		if (c.FG^pf)|(c.BG^pb)|Color(st^ps) != 0 {
			i += binary.PutUvarint(b[i:], uint64(x))
			binary.LittleEndian.PutUint32(b[i:], uint32(c.FG))
			binary.LittleEndian.PutUint32(b[i+4:], uint32(c.BG))
			i += 8
			i += binary.PutUvarint(b[i:], uint64(st>>runeBits))
			ps, pf, pb = st, c.FG, c.BG
		}
		switch r := c.Rune(); {
		case r == 0:
			b[tp] = ' '
			tp++
		case r < utf8.RuneSelf:
			b[tp] = byte(r)
			tp++
		default:
			tp += utf8.EncodeRune(b[tp:], r)
		}
	}
	return dst
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

type run struct {
	start  int
	fg, bg Color
	attr   Attr
}

var errLine = errors.New("invalid encoded line")

// readRun reads the run that starts at p. ok is false if src ends before
// the end of the run.
func readRun(src []byte, p int) (r run, next int, ok bool) {
	v, k := binary.Uvarint(src[p:])
	if k <= 0 || len(src)-p-k < 8 || v > math.MaxInt32 {
		return r, p, false
	}
	p += k
	r.start = int(v)
	r.fg = Color(binary.LittleEndian.Uint32(src[p:]))
	r.bg = Color(binary.LittleEndian.Uint32(src[p+4:]))
	p += 8
	v, k = binary.Uvarint(src[p:])
	if k <= 0 || v >= 1<<(32-runeBits) {
		return r, p, false
	}
	r.attr = Attr(v)
	return r, p + k, true
}

// lineHeader reads a line header: flags, n ≤ maxN cells and at most n
// runs. p is the position of the first run.
func lineHeader(src []byte, maxN int) (wrapped bool, n, nr, p int, err error) {
	if len(src) == 0 {
		return false, 0, 0, 0, errLine
	}
	n64, k := binary.Uvarint(src[1:])
	if k <= 0 || n64 > uint64(maxN) {
		return false, 0, 0, 0, errLine
	}
	p = 1 + k
	nr64, k := binary.Uvarint(src[p:])
	if k <= 0 || nr64 > n64 {
		return false, 0, 0, 0, errLine
	}
	return src[0]&1 != 0, int(n64), int(nr64), p + k, nil
}

// checkRuns checks the nr runs that start at p: complete, in order, inside
// the line and never on a right half (right halves are not encoded). It
// returns the start of the text.
func checkRuns(src []byte, p, n, nr int) (int, error) {
	prev := -1
	for i := 0; i < nr; i++ {
		r, next, ok := readRun(src, p)
		if !ok || r.start <= prev || r.start >= n || r.attr&AttrWideCont != 0 {
			return 0, errLine
		}
		prev, p = r.start, next
	}
	return p, nil
}

// checkLine validates an encoded line without decoding it.
func checkLine(src []byte, maxN int) error {
	_, n, nr, p, err := lineHeader(src, maxN)
	if err == nil {
		_, err = checkRuns(src, p, n, nr)
	}
	return err
}

// decodeLine decodes src into dst. The line must fit in maxN cells and the
// result never goes over n cells. A text that is too short (damaged
// snapshot, or an orphan right half from older versions) ends the current
// run without an error.
func decodeLine(src []byte, dst []Cell, maxN int) ([]Cell, bool, error) {
	dst = dst[:0]
	wrapped, n, nr, p, err := lineHeader(src, maxN)
	if err != nil {
		return dst, false, err
	}
	text, err := checkRuns(src, p, n, nr)
	if err != nil {
		return dst, false, err
	}
	if cap(dst) < n {
		dst = make([]Cell, 0, n)
	}
	var r, next run
	if nr > 0 {
		r, p, _ = readRun(src, p)
	}
	for i := 0; i < nr; i++ {
		end := n
		if i+1 < nr {
			next, p, _ = readRun(src, p)
			end = next.start
		}
		// Each run starts again at its own cell, and a wide character does
		// not spill over into the next run: an orphan half encoded by an
		// older version no longer shifts the rest of the line.
		for len(dst) < r.start {
			dst = append(dst, Cell{})
		}
		wide := r.attr&AttrWide != 0
		for x := r.start; x < end && text < len(src); x++ {
			ch, sz := utf8.DecodeRune(src[text:])
			text += sz
			dst = append(dst, MakeCell(ch, r.attr, r.fg, r.bg))
			if wide && x+1 < end {
				dst = append(dst, MakeCell(0, AttrWideCont, r.fg, r.bg))
				x++
			}
		}
		r = next
	}
	return dst, wrapped, nil
}
