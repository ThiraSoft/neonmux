package vt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// sameScreen compares the text, styles and cursor of two terminals.
func sameScreen(t *testing.T, a, b *Terminal, ctx string) {
	t.Helper()
	ac, ar := a.Size()
	bc, br := b.Size()
	if ac != bc || ar != br {
		t.Fatalf("%s: sizes %dx%d / %dx%d", ctx, ac, ar, bc, br)
	}
	for y := 0; y < ar; y++ {
		la, lb := a.ScreenLine(y), b.ScreenLine(y)
		for x := range la {
			ra, rb := la[x].Rune(), lb[x].Rune()
			if ra == 0 {
				ra = ' '
			}
			if rb == 0 {
				rb = ' '
			}
			if ra != rb || la[x].FG != lb[x].FG || la[x].BG != lb[x].BG || la[x].Attr() != lb[x].Attr() {
				t.Fatalf("%s: cell %d,%d differs (%q/%q)", ctx, x, y, lineText(a, y), lineText(b, y))
			}
		}
	}
	ax, ay, av, as := a.Cursor()
	bx, by, bv, bs := b.Cursor()
	if ax != bx || ay != by || av != bv || as != bs || a.Modes() != b.Modes() {
		t.Fatalf("%s: cursor or modes differ", ctx)
	}
	if a.HistoryLen() != b.HistoryLen() {
		t.Fatalf("%s: history %d / %d", ctx, a.HistoryLen(), b.HistoryLen())
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	src := New(20, 5, 50)
	feed(src, "\x1b[1;31mred\x1b[0m\r\n")
	for i := 0; i < 12; i++ {
		feed(src, "line café 日本\r\n")
	}
	feed(src, "\x1b[2;4r\x1b[?1h\x1b[5 q\x1b]0;title\x07\x1b[44mbg")
	// Cut in the middle of a CSI sequence and of a UTF-8 character.
	feed(src, "\x1b[3")
	feed(src, "\xe6\x97")

	dst := New(5, 2, 10)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	sameScreen(t, src, dst, "after loading")

	// The rest of the stream must give the same result on both sides.
	rest := "2mgreen\x1b[0m\xa5 end\x1b[?1049h\x1b[Halt"
	feed(src, rest)
	feed(dst, rest)
	sameScreen(t, src, dst, "after the rest of the stream")
	if src.Title() != dst.Title() {
		t.Fatalf("title %q / %q", src.Title(), dst.Title())
	}
}

func TestRestoreAfterRestart(t *testing.T) {
	src := New(40, 6, 50)
	feed(src, "one\r\ntwo\r\n$ \x1b[?1049h\x1b[Hnvim")
	dst := New(40, 6, 50)
	_ = dst.LoadSnapshot(src.Snapshot())
	dst.RestoreAfterRestart(time.Date(2026, 10, 19, 12, 44, 0, 0, time.Local))
	// The old screen stays visible, nvim's alternate screen goes away.
	if lineText(dst, 0) != "one" || lineText(dst, 2) != "$" {
		t.Fatalf("screen: %q / %q / %q", lineText(dst, 0), lineText(dst, 1), lineText(dst, 2))
	}
	if !strings.Contains(lineText(dst, 3), "session restored on Oct 19 at 12:44") {
		t.Fatalf("separator = %q", lineText(dst, 3))
	}
	x, y, _, _ := dst.Cursor()
	if x != 0 || y != 4 || dst.Modes()&ModeAltScreen != 0 {
		t.Fatalf("cursor at %d,%d, modes %b", x, y, dst.Modes())
	}
	// Full screen: the top goes to the history to make room.
	full := New(20, 3, 50)
	feed(full, "a\r\nb\r\nc")
	full.RestoreAfterRestart(time.Now())
	if full.HistoryLen() != 2 || lineText(full, 0) != "c" {
		t.Fatalf("full screen: history %d, first line %q", full.HistoryLen(), lineText(full, 0))
	}
}

// testdata/snap_nmx1.bin holds what the version before the fixes wrote
// (NMX1 format, without the pixel size) for a 30×6 terminal with 20 lines
// of history that received this stream. The two "日b" lines leave orphan
// wide character halves, as the old code did after ICH and in insert mode.
// The file was rebuilt with the current encoder, with the old version's
// leftovers put back by hand (orphan halves, OSC buffer kept after the
// sequence, no pixel size), and checked byte for byte against a file the
// old version wrote.
//
//	"\x1b]0;title NMX1\x07\x1b]7;file://host/home/user/project\x07" +
//	"\x1b[1;31mred\x1b[0m line 1\r\n" +
//	"\x1b[44mblue bg\x1b[0m café 日本 end\r\n" +
//	"0123456789012345678901234567890123456789\r\n" +
//	"日b\x1b[1G\x1b[@\r\n" +
//	"日b\r\x1b[4hz\x1b[4l\r\n" +
//	"line 5\r\nline 6\r\nline 7\r\n" +
//	"日b\x1b[1G\x1b[@\r\n" +
//	"\x1b[?1049h\x1b[Heditor\x1b[?1049l" +
//	"\x1b]7501;id=a:state=working:app=claude\x07" +
//	"\x1b[38;2;1;2;3mrgb\x1b[0m 世界\r\n$ \x1b[2;5r\x1b[6;3H\x1b[3"
func TestLoadSnapshotNMX1(t *testing.T) {
	data, err := os.ReadFile("testdata/snap_nmx1.bin")
	if err != nil {
		t.Fatal(err)
	}
	term := New(2, 1, 0)
	term.SetPixelSize(7, 9)
	if err := term.LoadSnapshot(data); err != nil {
		t.Fatalf("old snapshot rejected: %v", err)
	}
	if c, r := term.Size(); c != 30 || r != 6 {
		t.Fatalf("size %dx%d", c, r)
	}
	hist := []string{"red line 1", "blue bg café 日本 end", "012345678901234567890123456789", "0123456789", " 日 b"}
	if term.HistoryLen() != 6 {
		t.Fatalf("history of %d lines", term.HistoryLen())
	}
	for i, w := range hist {
		cells, wrapped := term.sb.line(i, nil)
		if got := cellsText(cells); got != w || wrapped != (i == 2) {
			t.Fatalf("history %d = %q (wrapped=%v), want %q", i, got, wrapped, w)
		}
	}
	// The orphan left half no longer shifts the rest of the line.
	if cells, _ := term.sb.line(4, nil); len(cells) != 4 || cells[3].Rune() != 'b' {
		t.Fatalf("orphan line shifted: %d cells", len(cells))
	}
	// The orphan right half lost its slot in the encoding: a readable,
	// bounded line is good enough.
	if cells, _ := term.sb.line(5, nil); len(cells) > 4 || cells[0].Rune() != 'z' {
		t.Fatalf("orphan line = %q", cellsText(cells))
	}
	screen := []string{"line 5", "line 6", "line 7", " 日 b", "rgb 世界", "$"}
	for y, w := range screen {
		if got := lineText(term, y); got != w {
			t.Fatalf("line %d = %q, want %q", y, got, w)
		}
	}
	if term.ScreenLine(3)[3].Rune() != 'b' {
		t.Fatalf("screen: orphan line shifted")
	}
	if term.ScreenLine(4)[0].FG != RGB(1, 2, 3) {
		t.Fatalf("color lost")
	}
	if term.Title() != "title NMX1" || term.Cwd() != "/home/user/project" {
		t.Fatalf("title %q, directory %q", term.Title(), term.Cwd())
	}
	if st, ok := term.ProgramStatus(); !ok || st.State != PSWorking || st.App != "claude" {
		t.Fatalf("status %+v", st)
	}
	if x, y, _, _ := term.Cursor(); x != 2 || y != 5 || term.top != 1 || term.bot != 4 {
		t.Fatalf("cursor %d,%d, region %d-%d", x, y, term.top, term.bot)
	}
	// Without a pixel size in the snapshot, the terminal keeps its own.
	if term.pxW != 7 || term.pxH != 9 {
		t.Fatalf("pixel size %dx%d", term.pxW, term.pxH)
	}
	// The stream picks up in the middle of the CSI sequence.
	feed(term, "1mX")
	if lineText(term, 5) != "$ X" || term.ScreenLine(5)[2].FG != Indexed(1) {
		t.Fatalf("rest of the stream = %q", lineText(term, 5))
	}
	// An even older snapshot, without the status section.
	old := New(2, 1, 0)
	if err := old.LoadSnapshot(data[:len(data)-len(progressTail(t, data))]); err != nil {
		t.Fatalf("snapshot without statuses rejected: %v", err)
	}
	if _, ok := old.ProgramStatus(); ok || lineText(old, 5) != "$" {
		t.Fatalf("snapshot without statuses read back wrong")
	}
}

// progressTail returns the status section at the end of data.
func progressTail(t *testing.T, data []byte) []byte {
	t.Helper()
	term := New(2, 1, 0)
	if err := term.LoadSnapshot(data); err != nil {
		t.Fatal(err)
	}
	e := &enc{}
	e.progress(term)
	if !bytes.HasSuffix(data, e.b) {
		t.Fatalf("status section not found")
	}
	return e.b
}

func TestSnapshotPixelSize(t *testing.T) {
	src := New(10, 4, 0)
	src.SetPixelSize(100, 80)
	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	var got []byte
	dst.SetReply(func(b []byte) { got = append(got, b...) })
	feed(dst, "\x1b[14t\x1b[16t")
	if string(got) != "\x1b[4;80;100t\x1b[6;20;10t" {
		t.Fatalf("replies = %q", got)
	}
}

// exercise puts a loaded terminal to work: nothing may panic.
func exercise(t *testing.T, term *Terminal) {
	t.Helper()
	term.Lock()
	cols, rows := term.Size()
	if cols*rows > 1<<16 {
		term.Unlock()
		return // too big for thousands of resizes
	}
	var buf []Cell
	for off := 0; off <= term.HistoryLen()+1; off++ {
		for y := 0; y < rows; y++ {
			line := term.ViewLine(y, off, nil)
			if len(line) > max(cols, maxCols) {
				t.Fatalf("line of %d cells", len(line))
			}
			_, buf = term.ViewLineBuf(y, off, buf)
		}
	}
	first, screen := term.AbsBase()
	for a := first; a < screen+int64(rows) && a < first+64; a++ {
		term.AbsLine(a, nil)
	}
	term.Unlock()
	term.SetReply(func([]byte) {})
	feed(term, "ab\r\n日́\x1b[S\x1b[T\x1b[3L\x1b[2M\x1bM\x1b[@\x1b[P\x1b[A\x1b[B\x1b[H\tx\x1b[J\x1b[4h本\x1b[9b\x1b[4l\x1b[6n")
	term.Resize(cols+1, rows+1)
	feed(term, "zz\r\n\x1b[3E\x1b[2F")
	term.Resize(2, 1)
	feed(term, "日本\r\n")
	term.Resize(cols, rows)
	_ = term.ScreenText()
	if err := New(2, 1, 0).LoadSnapshot(term.Snapshot()); err != nil {
		t.Fatalf("snapshot of the loaded terminal rejected: %v", err)
	}
}

func TestLoadSnapshotBadValues(t *testing.T) {
	huge := []byte{0}
	huge = binary.AppendUvarint(huge, 10_000_000)
	huge = binary.AppendUvarint(huge, 1)
	huge = binary.AppendUvarint(huge, 0)
	huge = append(huge, make([]byte, 9)...)
	for _, c := range []struct {
		name string
		mut  func(*Terminal)
		ok   bool
	}{
		{"empty history line", func(s *Terminal) { s.sb.ring[0] = nil }, false},
		{"truncated history line", func(s *Terminal) { s.sb.ring[1] = s.sb.ring[1][:5] }, false},
		{"huge history line", func(s *Terminal) { s.sb.ring[0] = huge }, false},
		{"run outside the line", func(s *Terminal) { s.sb.ring[0] = []byte{0, 2, 1, 7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'a', 'b'} }, false},
		{"negative top", func(s *Terminal) { s.top, s.bot = -5, 2 }, true},
		{"bot past the screen", func(s *Terminal) { s.top, s.bot = 1, 1<<40 }, true},
		{"unknown parser state", func(s *Terminal) { s.p.state = 42 }, true},
		{"uneed too large", func(s *Terminal) { s.p.uneed, s.p.uacc = 200, -1 }, true},
		{"negative parameters", func(s *Terminal) {
			s.p.state, s.p.np, s.p.params[0], s.p.cur, s.p.hasCur = stCSI, 1, -7, -9, true
		}, true},
		{"cursor off screen", func(s *Terminal) { s.cur.x, s.cur.y, s.cur.attr = -4, 1<<40, AttrWide|AttrWideCont }, true},
		{"invalid last character", func(s *Terminal) { s.lastRune = -2 }, true},
		{"history longer than its maximum", func(s *Terminal) { s.sb.max = 1 }, true},
		{"negative history maximum", func(s *Terminal) { s.sb.max = -1 }, true},
		{"single column", func(s *Terminal) {
			s.cols, s.main, s.alt, s.tabs = 1, newGrid(1, 3), newGrid(1, 3), make([]bool, 1)
		}, true},
	} {
		src := New(10, 3, 5)
		feed(src, "\x1b[31mone\r\ntwo 日本\r\nthree\r\nfour\r\nfive")
		c.mut(src)
		dst := New(2, 1, 0)
		err := dst.LoadSnapshot(src.Snapshot())
		if (err == nil) != c.ok {
			t.Fatalf("%s: error %v", c.name, err)
		}
		if err != nil {
			continue
		}
		cols, rows := dst.Size()
		if cols < 2 || dst.top < 0 || dst.bot >= rows || dst.top >= dst.bot || dst.sb.n > max(dst.sb.max, 0) ||
			dst.p.state > stIgnoreSTEsc || dst.p.uneed > 3 || dst.p.np < 0 || dst.p.np > maxParams || dst.p.cur < 0 {
			t.Fatalf("%s: inconsistent loaded state (region %d-%d, parser %d/%d/%d)", c.name, dst.top, dst.bot, dst.p.state, dst.p.uneed, dst.p.np)
		}
		exercise(t, dst)
	}
	// After an unknown parser state, output shows up again.
	src := New(10, 2, 0)
	src.p.state = 42
	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	feed(dst, "hello")
	if lineText(dst, 0) != "hello" {
		t.Fatalf("output swallowed: %q", lineText(dst, 0))
	}
}

func TestDecodeLineBounds(t *testing.T) {
	term := New(8, 1, 0)
	feed(term, "\x1b[31mré\x1b[44md日\x1b[0mx")
	raw := encodeLine(nil, term.ScreenLine(0), true)
	cells, wrapped, err := decodeLine(raw, nil, 8)
	if err != nil || !wrapped || cellsText(cells) != "réd日x" || len(cells) != 6 {
		t.Fatalf("valid line: %q, %v", cellsText(cells), err)
	}
	if _, _, err := decodeLine(raw, nil, 5); err == nil {
		t.Fatalf("line wider than the screen accepted")
	}
	// Each truncation is rejected or read back without going past the line.
	for i := 0; i < len(raw); i++ {
		cells, _, err := decodeLine(raw[:i], nil, 8)
		if err == nil && len(cells) > 6 {
			t.Fatalf("truncation at %d: %d cells", i, len(cells))
		}
		if i < 4 && err == nil {
			t.Fatalf("header truncated at %d accepted", i)
		}
	}
}

// TestEncodeLineFormat pins the encoding of a line, byte for byte as the
// previous version wrote it: that way a window not yet updated can read the
// new daemon's snapshots.
func TestEncodeLineFormat(t *testing.T) {
	term := New(12, 1, 0)
	feed(term, "\x1b[31mré\x1b[44md日\x1b[0mx\x1b[1;4m \x1b[0m  ")
	want := "\x01\a\x05\x00\x01\x00\x00\x01\x00\x00\x00\x00\x00\x02\x01\x00\x00\x01\x04\x00\x00\x01\x00\x03\x01\x00\x00\x01\x04\x00\x00\x01\x80\x02\x05\x00\x00\x00\x00\x00\x00\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00\x00\tréd日x "
	if got := string(encodeLine(nil, term.ScreenLine(0), true)); got != want {
		t.Fatalf("encoding = %q", got)
	}
}

// TestLoadSnapshotCorrupted damages a snapshot byte by byte: no load may
// panic, and neither may the terminal afterwards.
func TestLoadSnapshotCorrupted(t *testing.T) {
	src := New(12, 4, 6)
	feed(src, "\x1b[1;32mgreen\x1b[0m 日本\r\nab\r\ncd\x1b]7501;state=blocked:kind=permission\x07\r\nef\r\n\x1b[2;3r\x1b[?1049hx\x1b]0;ti")
	snap := src.Snapshot()
	for i := 0; i <= len(snap); i++ {
		term := New(2, 1, 0)
		if term.LoadSnapshot(snap[:i]) == nil {
			exercise(t, term)
		}
	}
	for i := len(snapMagic); i < len(snap); i++ {
		for _, v := range []byte{0, 0x7f, 0xff} {
			bad := bytes.Clone(snap)
			bad[i] = v
			term := New(2, 1, 0)
			if term.LoadSnapshot(bad) == nil {
				exercise(t, term)
			}
		}
	}
}

// snapTerm: 200×50 with 10,000 lines of colored log in the history.
func snapTerm() *Terminal {
	term := New(200, 50, 10000)
	var b strings.Builder
	for i := 0; i < 12000; i++ {
		fmt.Fprintf(&b, "\x1b[32m2026-10-19T10:00:00Z\x1b[0m INFO  request %d handled path=/api/v1/items status=200\r\n", i)
	}
	feed(term, b.String())
	return term
}

func BenchmarkSnapshot(b *testing.B) {
	term := snapTerm()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = term.Snapshot()
	}
}

// BenchmarkLoadSnapshot: every line is validated on load.
func BenchmarkLoadSnapshot(b *testing.B) {
	snap := snapTerm().Snapshot()
	dst := New(2, 1, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dst.LoadSnapshot(snap); err != nil {
			b.Fatal(err)
		}
	}
}
