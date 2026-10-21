package gfx

import (
	"bufio"
	"compress/gzip"
	"embed"
	"fmt"
	"image"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
)

// Terminus Font 4.49.1: the original BDF files, compressed with gzip (see
// terminus/README.md). The interface draws its text with it.
//
//go:embed terminus/*.bdf.gz
var terminusFS embed.FS

// terminusSizes are the pixel heights Terminus is drawn at.
var terminusSizes = []int{12, 14, 16, 18, 20, 22, 24, 28, 32}

// bdfFont is a bitmap font read from a BDF file, with a single cell size.
type bdfFont struct {
	w, h   int // cell size in pixels
	ascent int // rows above the baseline
	glyphs map[rune]bdfGlyph
}

// bdfGlyph is a 1-bit bitmap, most significant bit on the left. Its
// bounding box is placed relative to the origin, on the baseline.
type bdfGlyph struct {
	w, h, x, y int
	stride     int
	bits       []byte
}

func (g bdfGlyph) on(col, row int) bool {
	return g.bits[row*g.stride+col/8]&(0x80>>(col%8)) != 0
}

// parseBDF reads the parts of a BDF file that a character-cell font needs:
// the bounding box, the ascent and the encoded glyphs.
func parseBDF(r io.Reader) (*bdfFont, error) {
	f := &bdfFont{glyphs: map[rune]bdfGlyph{}}
	sc := bufio.NewScanner(r)
	var g bdfGlyph
	enc, row, inBitmap := -1, 0, false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if inBitmap {
			if line == "ENDCHAR" {
				inBitmap = false
				if enc >= 0 && row == g.h {
					f.glyphs[rune(enc)] = g
				}
				continue
			}
			if row < g.h {
				for i := 0; i < g.stride && 2*i+2 <= len(line); i++ {
					b, err := strconv.ParseUint(line[2*i:2*i+2], 16, 8)
					if err != nil {
						return nil, fmt.Errorf("bdf: bitmap row %q: %w", line, err)
					}
					g.bits[row*g.stride+i] = byte(b)
				}
				row++
			}
			continue
		}
		key, rest, _ := strings.Cut(line, " ")
		n := bdfInts(rest)
		switch key {
		case "FONTBOUNDINGBOX":
			if len(n) >= 2 {
				f.w, f.h = n[0], n[1]
			}
		case "FONT_ASCENT":
			if len(n) >= 1 {
				f.ascent = n[0]
			}
		case "ENCODING":
			enc = -1
			if len(n) >= 1 {
				enc = n[0]
			}
		case "BBX":
			if len(n) < 4 || n[0] < 0 || n[1] < 0 {
				return nil, fmt.Errorf("bdf: bad BBX %q", rest)
			}
			g = bdfGlyph{w: n[0], h: n[1], x: n[2], y: n[3], stride: (n[0] + 7) / 8}
		case "BITMAP":
			g.bits = make([]byte, g.stride*g.h)
			inBitmap, row = true, 0
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if f.w <= 0 || f.h <= 0 || len(f.glyphs) == 0 {
		return nil, fmt.Errorf("bdf: no glyphs")
	}
	return f, nil
}

func bdfInts(s string) []int {
	var n []int
	for field := range strings.FieldsSeq(s) {
		v, err := strconv.Atoi(field)
		if err != nil {
			break
		}
		n = append(n, v)
	}
	return n
}

var terminusCache struct {
	sync.Mutex
	fonts map[string]*bdfFont
}

// loadTerminus returns Terminus at a pixel height from terminusSizes.
func loadTerminus(px int, bold bool) (*bdfFont, error) {
	weight := "n"
	if bold {
		weight = "b"
	}
	name := fmt.Sprintf("terminus/ter-u%d%s.bdf.gz", px, weight)
	terminusCache.Lock()
	defer terminusCache.Unlock()
	if f := terminusCache.fonts[name]; f != nil {
		return f, nil
	}
	file, err := terminusFS.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	f, err := parseBDF(zr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if terminusCache.fonts == nil {
		terminusCache.fonts = map[string]*bdfFont{}
	}
	terminusCache.fonts[name] = f
	return f, nil
}

// pickTerminus chooses the Terminus size and the integer scale that come
// closest to a cell height in physical pixels. Scaling starts only past the
// largest size, so that text stays as fine as the screen allows.
func pickTerminus(target float64) (px, scale int) {
	scale = max(1, int(math.Ceil((target-1)/float64(terminusSizes[len(terminusSizes)-1]))))
	px = terminusSizes[0]
	for _, s := range terminusSizes {
		if math.Abs(float64(s)-target/float64(scale)) < math.Abs(float64(px)-target/float64(scale)) {
			px = s
		}
	}
	return px, scale
}

// TerminusHeight returns the cell height in physical pixels of Terminus at
// a size in points, for a display scale factor.
func TerminusHeight(size, dsf float64) int {
	px, scale := pickTerminus(size * dsf * 4 / 3)
	return px * scale
}

// drawBDFGlyph draws g into m, in a cell whose top left corner is at the
// origin of m, each font pixel becoming a scale×scale block. italic shifts
// the upper half of the letters one font pixel to the right, like the
// pixel font does.
func drawBDFGlyph(m *image.Alpha, f *bdfFont, g bdfGlyph, scale int, italic bool) {
	top := f.ascent - g.y - g.h
	b := m.Bounds()
	for row := 0; row < g.h; row++ {
		shift := 0
		if italic && top+row < f.ascent/2+1 {
			shift = 1
		}
		for col := 0; col < g.w; col++ {
			if !g.on(col, row) {
				continue
			}
			x0, y0 := b.Min.X+(g.x+col+shift)*scale, b.Min.Y+(top+row)*scale
			for y := y0; y < y0+scale; y++ {
				for x := x0; x < x0+scale; x++ {
					if (image.Point{x, y}).In(b) {
						m.Pix[m.PixOffset(x, y)] = 0xff
					}
				}
			}
		}
	}
}
