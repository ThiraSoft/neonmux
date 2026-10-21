package vt

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func lineText(t *Terminal, y int) string {
	var b strings.Builder
	for _, c := range t.ScreenLine(y) {
		if c.Attr()&AttrWideCont != 0 {
			continue
		}
		r := c.Rune()
		if r == 0 {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), " ")
}

func feed(t *Terminal, s string) { t.Write([]byte(s)) }

func TestPrintAndWrap(t *testing.T) {
	term := New(5, 3, 100)
	feed(term, "hello world")
	if got := lineText(term, 0); got != "hello" {
		t.Fatalf("line 0 = %q", got)
	}
	if got := lineText(term, 1); got != " worl" {
		t.Fatalf("line 1 = %q", got)
	}
	if got := lineText(term, 2); got != "d" {
		t.Fatalf("line 2 = %q", got)
	}
}

func TestScrollIntoHistory(t *testing.T) {
	term := New(10, 2, 100)
	feed(term, "one\r\ntwo\r\nthree\r\nfour")
	if term.HistoryLen() != 2 {
		t.Fatalf("history = %d lines", term.HistoryLen())
	}
	cells, _ := term.sb.line(0, nil)
	if got := cellsText(cells); got != "one" {
		t.Fatalf("oldest line = %q", got)
	}
	if lineText(term, 0) != "three" || lineText(term, 1) != "four" {
		t.Fatalf("screen = %q / %q", lineText(term, 0), lineText(term, 1))
	}
}

func cellsText(cells []Cell) string {
	var b strings.Builder
	for _, c := range cells {
		if c.Attr()&AttrWideCont != 0 {
			continue
		}
		r := c.Rune()
		if r == 0 {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.TrimRight(b.String(), " ")
}

func TestScrollRegion(t *testing.T) {
	term := New(10, 5, 100)
	feed(term, "0\r\n1\r\n2\r\n3\r\n4")
	feed(term, "\x1b[2;4r")   // region on rows 2 to 4
	feed(term, "\x1b[4;1H\n") // LF at the bottom of the region
	want := []string{"0", "2", "3", "", "4"}
	for y, w := range want {
		if got := lineText(term, y); got != w {
			t.Fatalf("line %d = %q, want %q", y, got, w)
		}
	}
	if term.HistoryLen() != 0 {
		t.Fatalf("an inner region should not feed the history")
	}
}

func TestAltScreen(t *testing.T) {
	term := New(10, 3, 100)
	feed(term, "shell$")
	feed(term, "\x1b[?1049h\x1b[Hvim")
	if lineText(term, 0) != "vim" {
		t.Fatalf("alt = %q", lineText(term, 0))
	}
	feed(term, "\x1b[?1049l")
	if lineText(term, 0) != "shell$" {
		t.Fatalf("back = %q", lineText(term, 0))
	}
	x, y, _, _ := term.Cursor()
	if x != 6 || y != 0 {
		t.Fatalf("cursor restored at %d,%d", x, y)
	}
}

func TestSGR(t *testing.T) {
	term := New(10, 1, 0)
	feed(term, "\x1b[1;38;2;255;0;128mA\x1b[38:2::1:2:3;48;5;200mB\x1b[0mC")
	l := term.ScreenLine(0)
	if l[0].Attr()&AttrBold == 0 || l[0].FG != RGB(255, 0, 128) {
		t.Fatalf("A: attr=%v fg=%x", l[0].Attr(), l[0].FG)
	}
	if l[1].FG != RGB(1, 2, 3) || l[1].BG != Indexed(200) {
		t.Fatalf("B: fg=%x bg=%x", l[1].FG, l[1].BG)
	}
	if l[2].Attr() != 0 || l[2].FG != 0 {
		t.Fatalf("C is not reset")
	}
}

func TestPrivateSGRIgnored(t *testing.T) {
	term := New(10, 1, 0)
	feed(term, "\x1b[>4;2mX") // XTMODKEYS sent by vim, not an SGR
	if term.ScreenLine(0)[0].Attr() != 0 {
		t.Fatalf("CSI > m should not change the style")
	}
}

func TestWideChars(t *testing.T) {
	term := New(6, 2, 0)
	feed(term, "a日本b")
	if got := lineText(term, 0); got != "a日本b" {
		t.Fatalf("line = %q", got)
	}
	x, _, _, _ := term.Cursor()
	if x != 5 {
		t.Fatalf("cursor at %d", x)
	}
	// Overwriting the right half erases the left half.
	feed(term, "\x1b[1;3HZ")
	if got := lineText(term, 0); got != "a Z本b" {
		t.Fatalf("after overwrite = %q", got)
	}
}

func TestScrollbackRoundTrip(t *testing.T) {
	term := New(8, 1, 10)
	feed(term, "\x1b[31mré\x1b[44md日\x1b[0m\r\n")
	cells, _ := term.sb.line(0, nil)
	if got := cellsText(cells); got != "réd日" {
		t.Fatalf("text = %q", got)
	}
	if cells[0].FG != Indexed(1) || cells[2].BG != Indexed(4) || cells[3].Attr()&AttrWide == 0 {
		t.Fatalf("styles lost: %+v", cells[:4])
	}
}

func TestScrollbackReuse(t *testing.T) {
	term := New(20, 1, 3)
	for i := 0; i < 50; i++ {
		feed(term, "test line\r\n")
	}
	if term.HistoryLen() != 3 {
		t.Fatalf("history = %d", term.HistoryLen())
	}
	allocs := testing.AllocsPerRun(100, func() { feed(term, "test line\r\n") })
	if allocs > 0 {
		t.Fatalf("%v allocations per line in steady state", allocs)
	}
}

func TestReplies(t *testing.T) {
	term := New(10, 5, 0)
	var got []string
	term.SetReply(func(b []byte) { got = append(got, string(b)) })
	feed(term, "\x1b[3;4H\x1b[6n\x1b[?2026$p")
	if len(got) != 1 || got[0] != "\x1b[3;4R\x1b[?2026;2$y" {
		t.Fatalf("replies = %q", got)
	}
}

func TestResizeKeepsCursorLine(t *testing.T) {
	term := New(10, 4, 100)
	feed(term, "a\r\nb\r\nc\r\nprompt")
	term.Resize(10, 2)
	if lineText(term, 1) != "prompt" {
		t.Fatalf("after shrinking: %q / %q", lineText(term, 0), lineText(term, 1))
	}
	if term.HistoryLen() != 2 {
		t.Fatalf("history = %d", term.HistoryLen())
	}
}

func TestDECGraphics(t *testing.T) {
	term := New(10, 1, 0)
	feed(term, "\x1b(0lqk\x1b(Bx")
	if got := lineText(term, 0); got != "┌─┐x" {
		t.Fatalf("line = %q", got)
	}
}

func BenchmarkFlood(b *testing.B) {
	term := New(200, 50, 10000)
	line := []byte("\x1b[32m2026-10-19T10:00:00Z\x1b[0m INFO  request handled path=/api/v1/items status=200 dur=12ms\r\n")
	data := []byte{}
	for len(data) < 64*1024 {
		data = append(data, line...)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.Write(data)
	}
}

// BenchmarkFloodColor: colored build log, one color per line.
func BenchmarkFloodColor(b *testing.B) {
	var sb strings.Builder
	for i := 0; sb.Len() < 64*1024; i++ {
		fmt.Fprintf(&sb, "\x1b[3%dm[%05d] building package neonmux/internal/something ... ok\x1b[0m\r\n", i%8, i)
	}
	data := []byte(sb.String()[:64*1024])
	term := New(200, 50, 10000)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.Write(data)
	}
}

// BenchmarkEncodeLine: a log line, non-ASCII text with styles, an empty
// line and a line with one color per cell.
func BenchmarkEncodeLine(b *testing.B) {
	term := New(200, 4, 0)
	feed(term, "\x1b[32m2026-10-19T10:00:00Z\x1b[0m INFO  request handled path=/api/v1/items status=200 dur=12ms\r\n")
	feed(term, "café 日本語 \x1b[1;31merror\x1b[0m: file not found\r\n\r\n")
	for x := 0; x < 200; x++ {
		feed(term, fmt.Sprintf("\x1b[38;5;%dm#", x))
	}
	var lines [][]Cell
	for y := 0; y < 4; y++ {
		lines = append(lines, term.ScreenLine(y))
	}
	var buf []byte
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, l := range lines {
			buf = encodeLine(buf[:0], l, false)
		}
	}
}

// BenchmarkREP: 64 KB of "x ESC[9999b", all of it once handled under the lock.
func BenchmarkREP(b *testing.B) {
	term := New(200, 50, 10000)
	data := []byte("x" + strings.Repeat("\x1b[9999b", 65536/7))
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.Write(data)
	}
}

// BenchmarkDECALN: 64 KB of "ESC # 8", the screen filled with E every time.
func BenchmarkDECALN(b *testing.B) {
	term := New(200, 50, 10000)
	data := []byte(strings.Repeat("\x1b#8", 65536/3))
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.Write(data)
	}
}

// BenchmarkViewHistory: one frame scrolled back into the history, reusing the buffer.
func BenchmarkViewHistory(b *testing.B) {
	term := New(200, 50, 1000)
	for i := 0; i < 1000; i++ {
		feed(term, fmt.Sprintf("\x1b[32m2026-10-19T10:00:00Z\x1b[0m INFO  request %d handled path=/api/v1/items\r\n", i))
	}
	var buf []Cell
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for y := 0; y < 50; y++ {
			buf = term.ViewLine(y, 500, buf)
		}
	}
}

func TestScreenText(t *testing.T) {
	term := New(20, 4, 0)
	feed(term, "hello  \r\n日本 ok\r\n")
	got := term.ScreenText()
	want := []string{"hello", "日本 ok", "", ""}
	if len(got) != len(want) {
		t.Fatalf("%d lines: %q", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// checkWide checks that no half of a wide character is orphaned.
func checkWide(t *testing.T, term *Terminal, ctx string) {
	t.Helper()
	_, rows := term.Size()
	for y := 0; y < rows; y++ {
		l := term.ScreenLine(y)
		for x, c := range l {
			a := c.Attr()
			if a&AttrWide != 0 && (x+1 >= len(l) || l[x+1].Attr()&AttrWideCont == 0) {
				t.Fatalf("%s: orphan left half at %d,%d (%q)", ctx, x, y, lineText(term, y))
			}
			if a&AttrWideCont != 0 && (x == 0 || l[x-1].Attr()&AttrWide == 0) {
				t.Fatalf("%s: orphan right half at %d,%d (%q)", ctx, x, y, lineText(term, y))
			}
		}
	}
}

func TestRepliesCapped(t *testing.T) {
	term := New(80, 24, 0)
	var got []byte
	calls := 0
	term.SetReply(func(b []byte) { got = append(got, b...); calls++ })
	feed(term, strings.Repeat("\x1b[c", 65536/3))
	if calls != 1 || len(got) > 4096 {
		t.Fatalf("DA1: %d bytes of replies in %d call(s)", len(got), calls)
	}
	// Only whole replies go out, never a truncated sequence.
	if len(got) == 0 || len(got)%len("\x1b[?62;22c") != 0 {
		t.Fatalf("truncated reply: %d bytes", len(got))
	}
	got = nil
	term.SetReportColors([3]uint8{}, [3]uint8{}, [3]uint8{}, func(int) (uint8, uint8, uint8) { return 1, 2, 3 })
	feed(term, "\x1b]4"+strings.Repeat(";1;?", 16000)+"\x07")
	if len(got) > 4096 {
		t.Fatalf("OSC 4: %d bytes of replies", len(got))
	}
	// The cap applies per Write: the next one replies again.
	got = nil
	feed(term, "\x1b[5n")
	if string(got) != "\x1b[0n" {
		t.Fatalf("next reply = %q", got)
	}
}

func TestREPMatchesPrint(t *testing.T) {
	for _, c := range []struct {
		name, pre, ch string
		n             int
	}{
		{"simple", "", "x", 7},
		{"wrap", "\x1b[1;8H", "x", 30},
		{"scroll", "\x1b[4;1H", "y", 35}, // REP stops at one screen
		{"wide", "\x1b[1;2H", "日", 9},
		{"wide in the last column", "\x1b[1;10H", "日", 3},
		{"over wide characters", "日本語日本\x1b[1;2H", "x", 5},
		{"insert", "abcdefghij\x1b[1;3H\x1b[4h", "z", 4},
		{"insert over wide", "日本語\x1b[1;2H\x1b[4h", "x", 3},
		{"insert that overflows", "abc日本語\x1b[1;2H\x1b[4h", "日", 7},
		{"no autowrap", "\x1b[?7l\x1b[1;7H", "w", 9},
		{"no autowrap, wide", "\x1b[?7l\x1b[1;8H", "日", 4},
		{"graphics set", "\x1b(0", "q", 12},
		{"pending wrap", "abcdefghij", "k", 3},
		{"colors", "\x1b[1;31;44m", "c", 15},
	} {
		rep, lit := New(10, 4, 10), New(10, 4, 10)
		feed(rep, c.pre+c.ch+fmt.Sprintf("\x1b[%db", c.n))
		feed(lit, c.pre+strings.Repeat(c.ch, c.n+1))
		sameScreen(t, rep, lit, c.name)
		if rep.cur != lit.cur {
			t.Fatalf("%s: cursor %+v / %+v", c.name, rep.cur, lit.cur)
		}
		for i := 0; i < rep.HistoryLen(); i++ {
			a, _ := rep.sb.line(i, nil)
			b, _ := lit.sb.line(i, nil)
			if cellsText(a) != cellsText(b) {
				t.Fatalf("%s: history %d %q / %q", c.name, i, cellsText(a), cellsText(b))
			}
		}
		checkWide(t, rep, c.name)
	}
}

func TestWriteInChunks(t *testing.T) {
	term := New(10, 2, 0)
	before := term.Seq()
	// The lock is released between two chunks: Seq moves on each time.
	term.Write([]byte(strings.Repeat("x\x1b[9999b", 100<<10/8)))
	if n := term.Seq() - before; n < 6 {
		t.Fatalf("100 KB processed in %d chunk(s)", n)
	}
}

func TestWideInsertMode(t *testing.T) {
	term := New(6, 1, 0)
	feed(term, "日b\r\x1b[4hz")
	if got := lineText(term, 0); got != "z日b" {
		t.Fatalf("insert before a wide character = %q", got)
	}
	checkWide(t, term, "insert")
	// The wide character pushed out on the right leaves nothing behind.
	feed(term, "yxw")
	if got := lineText(term, 0); got != "zyxw日" {
		t.Fatalf("overflow = %q", got)
	}
	feed(term, "v")
	if got := lineText(term, 0); got != "zyxwv" {
		t.Fatalf("half overflow = %q", got)
	}
	checkWide(t, term, "overflow")
}

func TestWideICH(t *testing.T) {
	term := New(6, 1, 10)
	feed(term, "日b\x1b[1G\x1b[@")
	if got := lineText(term, 0); got != " 日b" {
		t.Fatalf("ICH on a left half = %q", got)
	}
	checkWide(t, term, "ICH left")
	// The history reads the line back as is.
	feed(term, "\r\n")
	cells, _ := term.sb.line(0, nil)
	if got := cellsText(cells); got != " 日b" || len(cells) != 4 {
		t.Fatalf("history = %q (%d cells)", got, len(cells))
	}

	term = New(6, 1, 0)
	feed(term, "日b\x1b[2G\x1b[@")
	if got := lineText(term, 0); got != "   b" {
		t.Fatalf("ICH on a right half = %q", got)
	}
	checkWide(t, term, "ICH right")

	term = New(4, 1, 0)
	feed(term, "ab日\x1b[1G\x1b[@")
	if got := lineText(term, 0); got != " ab" {
		t.Fatalf("ICH pushing a half out = %q", got)
	}
	checkWide(t, term, "ICH overflow")
}

func TestWideLastColumn(t *testing.T) {
	term := New(4, 2, 0)
	feed(term, "ab日\x1b[1;4H本")
	if lineText(term, 0) != "ab" || lineText(term, 1) != "本" {
		t.Fatalf("lines %q / %q", lineText(term, 0), lineText(term, 1))
	}
	checkWide(t, term, "last column")
}

func TestCombiningMarks(t *testing.T) {
	term := New(20, 3, 0)
	feed(term, "Résumé") // NFD, like file names on macOS
	if got := lineText(term, 0); got != "Résumé" {
		t.Fatalf("NFD = %q", got)
	}
	if x, _, _, _ := term.Cursor(); x != 6 {
		t.Fatalf("cursor at %d", x)
	}
	// Without a composed form, the mark is ignored as before.
	feed(term, "\r\nq\u0301x\u200d")
	if got := lineText(term, 1); got != "qx" {
		t.Fatalf("no composition = %q", got)
	}
	// After a wide character, and at the end of a line (pending wrap).
	feed(term, "\r\nが\x1b[1;20Hé")
	if got := lineText(term, 2); got != "が" {
		t.Fatalf("wide = %q", got)
	}
	if r := term.ScreenLine(0)[19].Rune(); r != 'é' {
		t.Fatalf("last column = %q", r)
	}
	// At the start of a line, nothing to compose.
	feed(term, "\x1b[3;1H́")
	if got := lineText(term, 2); got != "が" {
		t.Fatalf("lone mark = %q", got)
	}
}

func TestLineFeedCancelsPendingWrap(t *testing.T) {
	for _, c := range []struct {
		seq  string
		want []string
	}{
		{"\nX", []string{"", "abcde", "    X", ""}},
		{"\x0bX", []string{"", "abcde", "    X", ""}},
		{"\x0cX", []string{"", "abcde", "    X", ""}},
		{"\x1bDX", []string{"", "abcde", "    X", ""}},
		{"\x1bEX", []string{"", "abcde", "X", ""}},
		{"\x1bMX", []string{"    X", "abcde", "", ""}},
	} {
		term := New(5, 4, 0)
		feed(term, "\x1b[2;1Habcde"+c.seq)
		for y, w := range c.want {
			if got := lineText(term, y); got != w {
				t.Fatalf("%q: line %d = %q, want %q", c.seq, y, got, w)
			}
		}
		for y, w := range term.g.wrapped {
			if w {
				t.Fatalf("%q: line %d marked as wrapped", c.seq, y)
			}
		}
	}
}

func TestCNLCPLOutsideRegion(t *testing.T) {
	term := New(10, 10, 0)
	for _, c := range []struct {
		seq string
		y   int
	}{
		{"\x1b[2;5r\x1b[8;4H\x1b[E", 8}, // below the region: down to the bottom of the screen
		{"\x1b[10;4H\x1b[5E", 9},        // without leaving it
		{"\x1b[1;4H\x1b[F", 0},          // above the region
		{"\x1b[3;4H\x1b[9E", 4},         // inside the region: stops at the bottom of the region
		{"\x1b[4;4H\x1b[9F", 1},         // and at the top of the region
		{"\x1b[9;4H\x1b[9F", 1},         // from below: stops at the top of the region
	} {
		feed(term, c.seq)
		x, y, _, _ := term.Cursor()
		if x != 0 || y != c.y {
			t.Fatalf("%q: cursor at %d,%d, want 0,%d", c.seq, x, y, c.y)
		}
	}
}

func TestBellsCoalesced(t *testing.T) {
	term := New(10, 2, 0)
	feed(term, strings.Repeat("\a", 65536))
	if ev := term.TakeEvents(); len(ev) != 1 || ev[0].Kind != EvBell {
		t.Fatalf("%d events", len(ev))
	}
	// A bell after another event counts again.
	feed(term, "\a\x1b]9;hello\x07\a\a")
	ev := term.TakeEvents()
	if len(ev) != 3 || ev[0].Kind != EvBell || ev[1].Kind != EvNotify || ev[2].Kind != EvBell {
		t.Fatalf("events = %+v", ev)
	}
}

func TestEventsCapped(t *testing.T) {
	term := New(10, 2, 0)
	feed(term, strings.Repeat("\x1b]9;notification\x07", 10000))
	if n := len(term.TakeEvents()); n == 0 || n > 1000 {
		t.Fatalf("%d notifications queued", n)
	}
	// A few huge clipboard payloads do not pile up either.
	big := "\x1b]52;c;" + strings.Repeat("QUFB", 200<<10) + "\x07"
	feed(term, strings.Repeat(big, 40))
	size := 0
	for _, ev := range term.TakeEvents() {
		size += len(ev.Body)
	}
	if size > 16<<20 {
		t.Fatalf("%d MB of events queued", size>>20)
	}
	// Once the queue is drained, events come back.
	feed(term, "\x1b]9;again\x07")
	if ev := term.TakeEvents(); len(ev) != 1 || ev[0].Body != "again" {
		t.Fatalf("after draining: %+v", ev)
	}
}

func TestOSCBufferReleased(t *testing.T) {
	term := New(10, 2, 0)
	feed(term, "\x1b]0;"+strings.Repeat("a", 1<<20)+"\x07")
	if c := cap(term.p.osc); c > 16<<10 {
		t.Fatalf("OSC buffer kept: %d bytes", c)
	}
	feed(term, "\x1b]52;c;"+strings.Repeat("QUFB", 200<<10)+"\x1b\\")
	if c := cap(term.p.osc); c > 16<<10 {
		t.Fatalf("OSC buffer kept after ST: %d bytes", c)
	}
	if ev := term.TakeEvents(); len(ev) != 1 || len(ev[0].Body) != 600<<10 {
		t.Fatalf("OSC 52 lost")
	}
}

func TestScrollbackShrinksAfterPeak(t *testing.T) {
	term := New(250, 2, 200)
	var b strings.Builder
	for x := 0; x < 250; x++ {
		fmt.Fprintf(&b, "\x1b[38;2;%d;0;0m#", x)
	}
	b.WriteString("\x1b[0m\r\n")
	for i := 0; i < 210; i++ {
		feed(term, b.String())
	}
	peak := term.HistoryBytes()
	for i := 0; i < 210; i++ {
		feed(term, "x\r\n")
	}
	if got := term.HistoryBytes(); got > 200*64 {
		t.Fatalf("history of %d KB after the peak (%d KB at the peak)", got>>10, peak>>10)
	}
}

func TestNarrowTerminal(t *testing.T) {
	term := New(1, 3, 10)
	if cols, _ := term.Size(); cols != 2 {
		t.Fatalf("%d column(s)", cols)
	}
	feed(term, "日本a語")
	term.Resize(1, 2)
	if cols, _ := term.Size(); cols != 2 {
		t.Fatalf("%d column(s) after Resize", cols)
	}
	feed(term, "語\x1b[1;2H本")
	checkWide(t, term, "two columns")
}

func TestViewLineBuf(t *testing.T) {
	term := New(10, 2, 10)
	feed(term, "one\r\ntwo\r\nthree")
	term.Lock()
	defer term.Unlock()
	var buf []Cell
	// History line: decoded into the buffer, which is meant to be kept.
	line, buf := term.ViewLineBuf(0, 1, buf)
	if cellsText(line) != "one" || cap(buf) < len(line) || &buf[:1][0] != &line[0] {
		t.Fatalf("history = %q", cellsText(line))
	}
	// Screen line: returned without a copy, the buffer comes back untouched.
	line, next := term.ViewLineBuf(1, 1, buf)
	if cellsText(line) != "two" || &line[0] != &term.ScreenLine(0)[0] || &next[:1][0] != &buf[:1][0] {
		t.Fatalf("screen = %q", cellsText(line))
	}
	// Writing into the kept buffer never damages the screen.
	next = next[:cap(next)]
	for i := range next {
		next[i] = MakeCell('#', 0, 0, 0)
	}
	if lineText(term, 0) != "two" || lineText(term, 1) != "three" {
		t.Fatalf("screen modified: %q / %q", lineText(term, 0), lineText(term, 1))
	}
	// In steady state, no allocation per line.
	allocs := testing.AllocsPerRun(100, func() {
		_, buf = term.ViewLineBuf(0, 1, buf)
	})
	if allocs > 0 {
		t.Fatalf("%v allocations per line", allocs)
	}
}

func TestTitleCutOnRuneBoundary(t *testing.T) {
	term := New(10, 1, 0)
	for _, pre := range []string{"", "a"} {
		feed(term, "\x1b]2;"+pre+strings.Repeat("é", 200)+"\x07")
		title := term.Title()
		if !utf8.ValidString(title) || len(title) > 256 || len(title) < 250 {
			t.Fatalf("title of %d bytes, valid=%v", len(title), utf8.ValidString(title))
		}
	}
}
