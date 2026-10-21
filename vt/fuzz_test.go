package vt

import (
	"os"
	"testing"
)

// sameCells compares two lines the way the history sees them: trailing
// empty cells do not count, and an empty cell equals a space.
func sameCells(a, b []Cell) bool {
	n := max(len(a), len(b))
	for x := 0; x < n; x++ {
		var ca, cb Cell
		if x < len(a) {
			ca = a[x]
		}
		if x < len(b) {
			cb = b[x]
		}
		if ca.IsBlank() && cb.IsBlank() {
			continue
		}
		ra, rb := ca.Rune(), cb.Rune()
		if ra == 0 {
			ra = ' '
		}
		if rb == 0 {
			rb = ' '
		}
		if ra != rb || ca.FG != cb.FG || ca.BG != cb.BG || ca.Attr() != cb.Attr() {
			return false
		}
	}
	return true
}

func FuzzFeed(f *testing.F) {
	f.Add(uint8(5), uint8(3), []byte("hello\x1b[2;3r\x1b[5L日本\x1b[?1049h\x1b[3@x\x1b[4hé"))
	f.Add(uint8(2), uint8(1), []byte("\x1b[38:2::1:2:3m\x1b[4:3m\x1b[99999b日"))
	f.Add(uint8(3), uint8(2), []byte("\x1b]0;t\x07\x1b[6n\x1b[?6h\x1b[H\x1b[10A\x1bM\x1b[r"))
	f.Add(uint8(6), uint8(2), []byte("日b\r\x1b[4hz\x1b[1G\x1b[@ab日\x1b[1;6H本Re\xcc\x81sume\xcc\x81\x1b[?7l日本語\x1b[9b"))
	f.Fuzz(func(t *testing.T, c, r uint8, data []byte) {
		cols := int(c%12) + 1
		rows := int(r%8) + 1
		term := New(cols, rows, 5)
		term.SetReply(func([]byte) {})
		half := len(data) / 2
		term.Write(data[:half])
		term.Resize(cols+int(r%3)-1, rows+int(c%3)-1)
		term.Write(data[half:])
		term.Lock()
		cc, rr := term.Size()
		var buf []Cell
		for y := 0; y < rr; y++ {
			_ = term.ViewLine(y, 0, nil)
			_ = term.ViewLine(y, 3, nil)
			if len(term.ScreenLine(y)) != cc {
				t.Fatalf("line %d has %d cells for %d columns", y, len(term.ScreenLine(y)), cc)
			}
			// A line encoded then read back does not change.
			var err error
			buf, _, err = decodeLine(encodeLine(nil, term.ScreenLine(y), false), buf, cc)
			if err != nil || !sameCells(buf, term.ScreenLine(y)) {
				t.Fatalf("line %d read back differently (%v)", y, err)
			}
		}
		x, y, _, _ := term.Cursor()
		if x < 0 || x >= cc || y < 0 || y >= rr {
			t.Fatalf("cursor off screen %d,%d (%dx%d)", x, y, cc, rr)
		}
		first, screen := term.AbsBase()
		for a := first; a < screen; a++ {
			term.AbsLine(a, nil)
		}
		term.Unlock()
		checkWide(t, term, "stream")
		snap := term.Snapshot()
		dst := New(2, 1, 0)
		if err := dst.LoadSnapshot(snap); err != nil {
			t.Fatalf("snapshot rejected: %v", err)
		}
		for y := 0; y < rr; y++ {
			if !sameCells(term.ScreenLine(y), dst.ScreenLine(y)) {
				t.Fatalf("snapshot: line %d %q / %q", y, lineText(term, y), lineText(dst, y))
			}
		}
		if term.cur != dst.cur || term.Modes() != dst.Modes() || term.HistoryLen() != dst.HistoryLen() {
			t.Fatalf("snapshot: cursor, modes or history differ")
		}
	})
}

func FuzzLoadSnapshot(f *testing.F) {
	if data, err := os.ReadFile("testdata/snap_nmx1.bin"); err == nil {
		f.Add(data) // format of the previous version
	}
	src := New(6, 3, 4)
	src.SetPixelSize(60, 36)
	feed(src, "\x1b[31mab日\r\nc\r\nd\r\ne\r\nf\x1b]7501;state=working\x07\x1b[2;3r")
	f.Add(src.Snapshot())
	src = New(4, 2, 2)
	feed(src, "x\x1b[?1049hy\x1b]0;tt")
	f.Add(src.Snapshot())
	src = New(8, 2, 3)
	feed(src, "日b\x1b[1G\x1b[@\r\n\x1b[4hz\x1b[44m \x1b[0m\r\ne\xcc")
	f.Add(src.Snapshot())
	f.Fuzz(func(t *testing.T, data []byte) {
		term := New(2, 1, 0)
		if err := term.LoadSnapshot(data); err != nil {
			return
		}
		exercise(t, term)
	})
}
