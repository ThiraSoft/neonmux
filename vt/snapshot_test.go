package vt

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
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

func TestRestoreAfterRestartKeepsImages(t *testing.T) {
	src := New(40, 10, 50)
	// Main screen: direct placement with a=T, f=32
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\\r\n")
	// Switch to alternate screen
	feed(src, "\x1b[?1049h\x1b[H")
	// Alt screen: direct placement with a=T, f=32
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=2,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\\r\n")

	dst := New(40, 10, 50)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}

	dst.RestoreAfterRestart(time.Date(2026, 11, 5, 12, 0, 0, 0, time.Local))

	dst.Lock()
	defer dst.Unlock()
	// Main screen image 1 placement must be kept
	ps := dst.VisiblePlacements(0, nil)
	if len(ps) != 1 || ps[0].ImageID != 1 {
		t.Fatalf("expected main screen placement for image 1, got %+v", ps)
	}
	if _, ok := dst.Image(1); !ok {
		t.Fatal("image 1 missing after restart")
	}
	// Alternate screen transient image 2 must be cleared/freed
	if _, ok := dst.Image(2); ok {
		t.Fatal("alternate screen transient image 2 still exists after restart")
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
			dst.p.state > stAPCEsc || dst.p.uneed > 3 || dst.p.np < 0 || dst.p.np > maxParams || dst.p.cur < 0 {
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

func TestSnapshotMidAPCResetsToGround(t *testing.T) {
	src := New(10, 2, 0)
	feed(src, "\x1b_Ga=t,i=1;abc") // parser left in stAPC
	if src.p.state != stAPC {
		t.Fatalf("expected src state stAPC, got %d", src.p.state)
	}
	dst := New(10, 2, 0)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if dst.p.state != stGround {
		t.Fatalf("expected dst state stGround after snapshot restore, got %d", dst.p.state)
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

func TestOldLineFormatStillDecodes(t *testing.T) {
	// Frozen hex encoding produced by the pre-change encodeLine for:
	// New(12, 1, 0) with "\x1b[31mré\x1b[44md日\x1b[0mx\x1b[1;4m \x1b[0m  "
	const oldHex = "01070500010000010000000000020100000104000001000301000001040000018002050000000000000000000600000000000000000972c3a964e697a57820"
	raw, err := hex.DecodeString(oldHex)
	if err != nil {
		t.Fatal(err)
	}
	cells, wrapped, err := decodeLine(raw, nil, 12)
	if err != nil {
		t.Fatalf("decodeLine failed: %v", err)
	}
	if !wrapped {
		t.Fatal("expected wrapped to be true")
	}
	if len(cells) != 7 {
		t.Fatalf("expected 7 cells, got %d", len(cells))
	}
	expected := []struct {
		r    rune
		attr Attr
		fg   Color
		bg   Color
		ul   Color
	}{
		{'r', 0, Indexed(1), ColorDefault, ColorDefault},
		{'é', 0, Indexed(1), ColorDefault, ColorDefault},
		{'d', 0, Indexed(1), Indexed(4), ColorDefault},
		{'日', AttrWide, Indexed(1), Indexed(4), ColorDefault},
		{0, AttrWideCont, Indexed(1), Indexed(4), ColorDefault},
		{'x', 0, ColorDefault, ColorDefault, ColorDefault},
		{' ', AttrBold | AttrUnderline, ColorDefault, ColorDefault, ColorDefault},
	}
	for i, exp := range expected {
		c := cells[i]
		if c.Rune() != exp.r || c.Attr() != exp.attr || c.FG != exp.fg || c.BG != exp.bg || c.UL != exp.ul {
			t.Fatalf("cell %d mismatch: got %+v, want %+v", i, c, exp)
		}
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

func TestSnapshotKeepsImages(t *testing.T) {
	src := New(40, 10, 50)
	// 1. Direct placement with a=T, f=32, 2x2 pixels
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	// 2. Virtual placement with a=T, f=32, 2x2 pixels, U=1, p=1
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=2,p=1,U=1,c=3,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	// 3. Unicode placeholder referencing image 2, placement 1
	feed(src, "\x1b[38;2;0;0;2m\x1b[58;2;0;0;1m\U0010EEEE\u0305\u0305\x1b[0m")

	src.Lock()
	srcPlacements := src.VisiblePlacements(0, nil)
	srcImg1, ok1 := src.Image(1)
	srcImg2, ok2 := src.Image(2)
	srcVPCols, srcVPRows, okVP := src.VirtualPlacement(2, 1)
	src.Unlock()

	if !ok1 || !ok2 || !okVP {
		t.Fatalf("setup failed: ok1=%v ok2=%v okVP=%v", ok1, ok2, okVP)
	}
	if len(srcPlacements) != 1 {
		t.Fatalf("expected 1 direct placement, got %d", len(srcPlacements))
	}

	snap := src.Snapshot()
	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(snap); err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}

	dst.Lock()
	dstPlacements := dst.VisiblePlacements(0, nil)
	dstImg1, dstOk1 := dst.Image(1)
	dstImg2, dstOk2 := dst.Image(2)
	dstVPCols, dstVPRows, dstOkVP := dst.VirtualPlacement(2, 1)
	dst.Unlock()

	if !dstOk1 || !bytes.Equal(dstImg1.Data, srcImg1.Data) {
		t.Fatalf("image 1 not preserved: ok=%v", dstOk1)
	}
	if !dstOk2 || !bytes.Equal(dstImg2.Data, srcImg2.Data) {
		t.Fatalf("image 2 not preserved: ok=%v", dstOk2)
	}
	if !dstOkVP || dstVPCols != srcVPCols || dstVPRows != srcVPRows {
		t.Fatalf("virtual placement not preserved: ok=%v cols=%d rows=%d", dstOkVP, dstVPCols, dstVPRows)
	}
	if len(dstPlacements) != len(srcPlacements) {
		t.Fatalf("placements length mismatch: got %d, want %d", len(dstPlacements), len(srcPlacements))
	}
	if dstPlacements[0] != srcPlacements[0] {
		t.Fatalf("placement mismatch: got %+v, want %+v", dstPlacements[0], srcPlacements[0])
	}

	// Verify placeholder cell
	imgID, pid, r, c, ok := dst.ScreenLine(1)[2].Placeholder()
	if !ok || imgID != 2 || pid != 1 || r != 0 || c != 0 {
		t.Fatalf("placeholder cell decoded wrong: ok=%v imgID=%d pid=%d r=%d c=%d", ok, imgID, pid, r, c)
	}
}

func TestSnapshotWithoutGraphicsSection(t *testing.T) {
	data, err := os.ReadFile("testdata/snapshot-pre-graphics.bin")
	if err != nil {
		t.Fatal(err)
	}
	dst := New(10, 5, 0)
	// Pre-populate with an image and placement
	feed(dst, "\x1b_Ga=T,f=32,s=2,v=2,i=9,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	if err := dst.LoadSnapshot(data); err != nil {
		t.Fatalf("snapshot-pre-graphics.bin rejected: %v", err)
	}
	if dst.Title() != "PreGraphics" {
		t.Fatalf("title = %q, want PreGraphics", dst.Title())
	}
	dst.Lock()
	defer dst.Unlock()
	if ps := dst.VisiblePlacements(0, nil); len(ps) != 0 {
		t.Fatalf("expected 0 placements after loading snapshot without graphics, got %d", len(ps))
	}
	if _, ok := dst.Image(9); ok {
		t.Fatalf("expected previous images to be replaced and cleared, but image 9 still exists")
	}
}

func TestSnapshotCursorUL(t *testing.T) {
	src := New(10, 2, 0)
	feed(src, "\x1b[58;2;12;34;56m")
	if src.cur.ul != RGB(12, 34, 56) {
		t.Fatalf("src cursor ul = %v", src.cur.ul)
	}
	dst := New(10, 2, 0)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if dst.cur.ul != RGB(12, 34, 56) {
		t.Fatalf("dst cursor ul = %v, want %v", dst.cur.ul, RGB(12, 34, 56))
	}
}

func TestSnapshotTransientAndAnchors(t *testing.T) {
	src := New(20, 5, 50)
	// Place a transient image a=T
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=5,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	src.Lock()
	if !src.gfx.transient[5] {
		t.Fatal("image 5 not transient")
	}
	lowMainBefore := src.gfx.lowMain
	src.Unlock()

	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	dst.Lock()
	if !dst.gfx.transient[5] {
		t.Fatal("image 5 lost transient flag after snapshot load")
	}
	if dst.gfx.lowMain != lowMainBefore {
		t.Fatalf("lowMain = %d, want %d", dst.gfx.lowMain, lowMainBefore)
	}
	dst.Unlock()

	// Evict by scrolling lines past history limit
	for i := 0; i < 60; i++ {
		feed(dst, "scroll line\r\n")
	}
	dst.Lock()
	defer dst.Unlock()
	if len(dst.VisiblePlacements(0, nil)) != 0 {
		t.Fatal("placement still visible after scrolling past history")
	}
	if _, ok := dst.Image(5); ok {
		t.Fatal("transient image 5 not freed after placement scrolled away")
	}
}

func TestSnapshotGraphicsCountersAcrossLoad(t *testing.T) {
	// Destination terminal starts with image 1
	dst := New(20, 5, 10)
	feed(dst, "\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	dstSeqBefore := dst.GraphicsSeq()
	dstImgBefore, ok := dst.Image(1)
	if !ok {
		t.Fatal("dst image 1 missing")
	}
	dstGenBefore := dstImgBefore.Gen

	// Source terminal with a DIFFERENT image 1
	src := New(20, 5, 10)
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;BBBBBBBBBBBBBBBBBBBBBB==\x1b\\")

	// Load snapshot into dst
	snap := src.Snapshot()
	if err := dst.LoadSnapshot(snap); err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}

	dstSeqAfter := dst.GraphicsSeq()
	if dstSeqAfter <= dstSeqBefore {
		t.Fatalf("GraphicsSeq did not increase: got %d <= %d", dstSeqAfter, dstSeqBefore)
	}

	dstImgAfter, ok := dst.Image(1)
	if !ok {
		t.Fatal("loaded image 1 missing")
	}
	if dstImgAfter.Gen == dstGenBefore {
		t.Fatalf("loaded image 1 gen (%d) must differ from old gen (%d)", dstImgAfter.Gen, dstGenBefore)
	}
	if dstImgAfter.Gen <= dstGenBefore {
		t.Fatalf("loaded image 1 gen (%d) should be greater than old gen (%d)", dstImgAfter.Gen, dstGenBefore)
	}

	// Verify next image allocated continues above loaded gen
	feed(dst, "\x1b_Ga=T,f=32,s=2,v=2,i=2,c=2,r=2,q=2;CCCCCCCCCCCCCCCCCCCCCC==\x1b\\")
	dstImg2, ok := dst.Image(2)
	if !ok {
		t.Fatal("new image 2 missing")
	}
	if dstImg2.Gen <= dstImgAfter.Gen {
		t.Fatalf("new image 2 gen (%d) should be greater than loaded image gen (%d)", dstImg2.Gen, dstImgAfter.Gen)
	}
}

func TestSnapshotImageDataCopy(t *testing.T) {
	src := New(20, 5, 10)
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	snap := src.Snapshot()

	dst := New(20, 5, 10)
	if err := dst.LoadSnapshot(snap); err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}
	img, ok := dst.Image(1)
	if !ok || len(img.Data) == 0 {
		t.Fatal("image 1 missing or empty")
	}
	originalByte := img.Data[0]

	// Mutate the snapshot buffer
	for i := range snap {
		snap[i] = 0xFF
	}

	// Image.Data in dst must be unaffected
	imgAfter, ok := dst.Image(1)
	if !ok || imgAfter.Data[0] != originalByte {
		t.Fatalf("Image.Data was modified by mutating snapshot buffer: got %d, want %d", imgAfter.Data[0], originalByte)
	}
}

func TestSnapshotCorruptedGraphics(t *testing.T) {
	// Base terminal snapshot without graphics
	baseTerm := New(20, 5, 10)
	feed(baseTerm, "hello world\r\n")
	baseSnap := DropGraphics(baseTerm.Snapshot())

	png2x2 := createTestPNG(2, 2)

	tests := []struct {
		name         string
		encodeGfx    func(e *enc)
		wantImgCount int
		wantPlCount  int
		wantImgs     []uint32
		wantMissing  []uint32
	}{
		{
			name: "duplicate image id keeps first and counted once",
			encodeGfx: func(e *enc) {
				e.u(2) // 2 images
				// Image 10: RGB 2x2 = 12 bytes
				e.u(10)
				e.u(0)
				e.byte1(24)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{1}, 12))
				e.flag(false)
				// Duplicate Image 10: RGB 2x2 = 12 bytes with different content
				e.u(10)
				e.u(0)
				e.byte1(24)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{2}, 12))
				e.flag(false)

				e.u(0) // 0 placements
				e.i(0)
				e.i(0)
				e.i(0)
				e.u(1) // altBase, lowMain, lowAlt, nextID
			},
			wantImgCount: 1,
			wantPlCount:  0,
			wantImgs:     []uint32{10},
		},
		{
			name: "placement pointing to missing image is dropped",
			encodeGfx: func(e *enc) {
				e.u(1) // 1 image
				e.u(5)
				e.u(0)
				e.byte1(24)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{1}, 12))
				e.flag(false)

				e.u(2) // 2 placements: one valid (img 5), one missing (img 999)
				// Placement 1: valid for image 5
				e.u(5)
				e.u(1)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(0)
				e.i(0)
				e.flag(false)
				e.flag(false)
				e.flag(false) // natural
				// Placement 2: missing image 999
				e.u(999)
				e.u(2)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(0)
				e.i(0)
				e.flag(false)
				e.flag(false)
				e.flag(false) // natural

				e.i(0)
				e.i(0)
				e.i(0)
				e.u(1)
			},
			wantImgCount: 1,
			wantPlCount:  1,
			wantImgs:     []uint32{5},
			wantMissing:  []uint32{999},
		},
		{
			name: "damaged raw data length dropped",
			encodeGfx: func(e *enc) {
				e.u(3) // 3 images
				// Image 1: RGB 2x2 but only 10 bytes instead of 12 -> dropped
				e.u(1)
				e.u(0)
				e.byte1(24)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{1}, 10))
				e.flag(false)
				// Image 2: RGBA 2x2 but 15 bytes instead of 16 -> dropped
				e.u(2)
				e.u(0)
				e.byte1(32)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{2}, 15))
				e.flag(false)
				// Image 3: RGBA 2x2 with correct 16 bytes -> kept
				e.u(3)
				e.u(0)
				e.byte1(32)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{3}, 16))
				e.flag(false)

				e.u(0) // 0 placements
				e.i(0)
				e.i(0)
				e.i(0)
				e.u(1)
			},
			wantImgCount: 1,
			wantPlCount:  0,
			wantImgs:     []uint32{3},
			wantMissing:  []uint32{1, 2},
		},
		{
			name: "damaged PNG data dropped",
			encodeGfx: func(e *enc) {
				e.u(3) // 3 images
				// Image 1: PNG format but garbage bytes -> dropped
				e.u(1)
				e.u(0)
				e.byte1(100)
				e.u(2)
				e.u(2)
				e.bytes([]byte("not a png"))
				e.flag(false)
				// Image 2: valid PNG but dimension mismatch (PNG is 2x2, declared 3x3) -> dropped
				e.u(2)
				e.u(0)
				e.byte1(100)
				e.u(3)
				e.u(3)
				e.bytes(png2x2)
				e.flag(false)
				// Image 3: valid PNG 2x2 matching declared dimensions -> kept
				e.u(3)
				e.u(0)
				e.byte1(100)
				e.u(2)
				e.u(2)
				e.bytes(png2x2)
				e.flag(false)

				e.u(0) // 0 placements
				e.i(0)
				e.i(0)
				e.i(0)
				e.u(1)
			},
			wantImgCount: 1,
			wantPlCount:  0,
			wantImgs:     []uint32{3},
			wantMissing:  []uint32{1, 2},
		},
		{
			name: "damaged placement dimensions dropped",
			encodeGfx: func(e *enc) {
				e.u(1)
				e.u(1)
				e.u(0)
				e.byte1(32)
				e.u(2)
				e.u(2)
				e.bytes(bytes.Repeat([]byte{1}, 16))
				e.flag(false)

				e.u(3)
				// Placement 1: cols = 0 -> dropped
				e.u(1)
				e.u(1)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(2)
				e.i(2)
				e.i(0)
				e.i(2)
				e.i(0)
				e.i(0)
				e.flag(false)
				e.flag(false)
				e.flag(false) // natural
				// Placement 2: maxX < minX -> dropped
				e.u(1)
				e.u(2)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(5)
				e.i(0)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(0)
				e.i(0)
				e.flag(false)
				e.flag(false)
				e.flag(false) // natural
				// Placement 3: valid -> kept
				e.u(1)
				e.u(3)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(0)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(2)
				e.i(0)
				e.i(0)
				e.flag(false)
				e.flag(false)
				e.flag(false) // natural

				e.i(0)
				e.i(0)
				e.i(0)
				e.u(1)
			},
			wantImgCount: 1,
			wantPlCount:  1,
			wantImgs:     []uint32{1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &enc{}
			tc.encodeGfx(e)
			snap := withGraphics(t, baseSnap, e.b)

			dst := New(20, 5, 10)
			if err := dst.LoadSnapshot(snap); err != nil {
				t.Fatalf("LoadSnapshot returned error: %v", err)
			}
			dst.Lock()
			defer dst.Unlock()

			if len(dst.gfx.images) != tc.wantImgCount {
				t.Fatalf("images count: got %d, want %d", len(dst.gfx.images), tc.wantImgCount)
			}
			if len(dst.gfx.placements) != tc.wantPlCount {
				t.Fatalf("placements count: got %d, want %d", len(dst.gfx.placements), tc.wantPlCount)
			}
			for _, id := range tc.wantImgs {
				if _, ok := dst.Image(id); !ok {
					t.Fatalf("expected image %d to be present", id)
				}
			}
			for _, id := range tc.wantMissing {
				if _, ok := dst.Image(id); ok {
					t.Fatalf("expected image %d to be missing/dropped", id)
				}
			}
		})
	}
}

// TestSnapshotKeepsNatural: a placement made without c or r keeps its
// natural size across a snapshot, so the window draws it unscaled.
func TestSnapshotKeepsNatural(t *testing.T) {
	src := New(40, 10, 50)
	src.SetPixelSize(400, 200)
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=1,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=2,c=3,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(src.Snapshot()); err != nil {
		t.Fatal(err)
	}
	dst.Lock()
	defer dst.Unlock()
	ps := dst.VisiblePlacements(0, nil)
	if len(ps) != 2 {
		t.Fatalf("%d placements, want 2", len(ps))
	}
	for _, p := range ps {
		want := p.ImageID == 1
		if p.Natural != want || dst.PlacementNatural(p.ImageID, p.ID) != want {
			t.Fatalf("image %d: Natural %v / %v, want %v", p.ImageID, p.Natural, dst.PlacementNatural(p.ImageID, p.ID), want)
		}
	}
}

// TestSnapshotGraphicsSectionDelimited: the graphics section carries its
// length, so a section added after it by a later version survives
// DropGraphics and does not get read as graphics.
func TestSnapshotGraphicsSectionDelimited(t *testing.T) {
	future := []byte{0x03, 0xAA, 0xBB, 0xCC}

	src := New(20, 5, 10)
	feed(src, "hello\r\n\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	snap := append(src.Snapshot(), future...)
	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst.Image(1); !ok {
		t.Fatal("image lost with a section after graphics")
	}
	dropped := DropGraphics(snap)
	if !bytes.HasSuffix(dropped, future) {
		t.Fatalf("DropGraphics cut the section after graphics: tail %x", dropped[max(0, len(dropped)-8):])
	}
	if len(dropped) >= len(snap) {
		t.Fatal("DropGraphics kept the graphics")
	}
	if err := dst.LoadSnapshot(dropped); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst.Image(1); ok {
		t.Fatal("image still there after DropGraphics")
	}
	if got := lineText(dst, 0); got != "hello" {
		t.Fatalf("line 0 = %q", got)
	}

	// No images: the section is still written, so what follows is never
	// taken for graphics.
	plain := New(20, 5, 10)
	feed(plain, "hi")
	empty := plain.Snapshot()
	if !bytes.Equal(DropGraphics(empty), empty) {
		t.Fatal("DropGraphics changed a snapshot without images")
	}
	if empty[len(empty)-1] != 0 {
		t.Fatalf("snapshot without images ends with %#x, want an empty graphics section", empty[len(empty)-1])
	}
}

// withGraphics returns base, a snapshot ending with an empty graphics
// section, with gfx as its graphics section instead.
func withGraphics(t testing.TB, base, gfx []byte) []byte {
	t.Helper()
	if len(base) == 0 || base[len(base)-1] != 0 {
		t.Fatal("base snapshot does not end with an empty graphics section")
	}
	// The image records go in a block of their own.
	d := &dec{b: gfx}
	n := d.u()
	for i := uint64(0); i < n && d.err == nil; i++ {
		d.u()
		d.u()
		d.byte1()
		d.u()
		d.u()
		d.bytes()
		d.flag()
	}
	if d.err != nil {
		t.Fatal("bad image records in test graphics")
	}
	at := len(gfx) - len(d.b)
	sec := binary.AppendUvarint(nil, uint64(at))
	sec = append(sec, gfx...)
	out := append([]byte(nil), base[:len(base)-1]...)
	out = binary.AppendUvarint(out, uint64(len(sec)))
	return append(out, sec...)
}

// TestGraphicsSeqDuringLoad: GraphicsSeq is read without the lock (the
// renderer) while LoadSnapshot replaces the graphics (go test -race).
func TestGraphicsSeqDuringLoad(t *testing.T) {
	src := New(10, 4, 10)
	feed(src, "\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	snap := src.Snapshot()
	dst := New(10, 4, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			_ = dst.GraphicsSeq()
		}
	}()
	last := dst.GraphicsSeq()
	for range 50 {
		if err := dst.LoadSnapshot(snap); err != nil {
			t.Fatal(err)
		}
		if s := dst.GraphicsSeq(); s <= last {
			t.Fatalf("GraphicsSeq %d after load, was %d", s, last)
		} else {
			last = s
		}
	}
	<-done
}

// TestSplitJoinImages: the image data can be kept apart from a snapshot
// and put back; placements stay in the snapshot.
func TestSplitJoinImages(t *testing.T) {
	src := New(20, 5, 10)
	feed(src, "hi\r\n\x1b_Ga=T,f=32,s=2,v=2,i=1,c=2,r=2,q=2;AAAAAAAAAAAAAAAAAAAAAA==\x1b\\")
	feed(src, "\x1b_Ga=t,f=24,s=40,v=40,i=2,q=2;"+strings.Repeat("A", 40*40*3*4/3)+"\x1b\\")
	snap := src.Snapshot()
	text, imgs := SplitImages(snap)
	if len(imgs) < 40*40*3 || len(text) >= len(snap)-40*40*3 {
		t.Fatalf("split: text %d imgs %d from %d", len(text), len(imgs), len(snap))
	}
	dst := New(2, 1, 0)
	if err := dst.LoadSnapshot(text); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst.Image(1); ok {
		t.Fatal("image data left in the text part")
	}
	joined := JoinImages(text, imgs)
	if !bytes.Equal(joined, snap) {
		t.Fatal("JoinImages(SplitImages(snap)) differs from snap")
	}
	if err := dst.LoadSnapshot(joined); err != nil {
		t.Fatal(err)
	}
	dst.Lock()
	ps := dst.VisiblePlacements(0, nil)
	_, ok2 := dst.Image(2)
	dst.Unlock()
	if len(ps) != 1 || !ok2 {
		t.Fatalf("after join: %d placements, image 2 %v", len(ps), ok2)
	}
	// A snapshot that kept its images is left alone.
	if !bytes.Equal(JoinImages(snap, imgs), snap) {
		t.Fatal("JoinImages changed a snapshot that has its images")
	}
	plain := New(5, 2, 0).Snapshot()
	if txt, im := SplitImages(plain); !bytes.Equal(txt, plain) || len(im) != 0 {
		t.Fatal("SplitImages changed a snapshot without images")
	}
}
