package vt

import (
	"strings"
	"testing"
)

// allLines returns the history followed by the screen, as text.
func allLines(term *Terminal) []string {
	first, screen := term.AbsBase()
	var out []string
	for a := first; a < screen+int64(term.rows); a++ {
		cells, _ := term.AbsLine(a, nil)
		out = append(out, strings.TrimRight(cellsText(cells), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func checkLines(t *testing.T, term *Terminal, want ...string) {
	t.Helper()
	got := allLines(term)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func checkCursor(t *testing.T, term *Terminal, x, y int) {
	t.Helper()
	if cx, cy, _, _ := term.Cursor(); cx != x || cy != y {
		t.Fatalf("cursor = (%d, %d), want (%d, %d)", cx, cy, x, y)
	}
}

func TestReflowNarrow(t *testing.T) {
	term := New(10, 4, 100)
	feed(term, "abcdefghijklm")
	term.Resize(5, 4)
	checkLines(t, term, "abcde", "fghij", "klm")
	checkCursor(t, term, 3, 2)
}

func TestReflowWiden(t *testing.T) {
	term := New(5, 4, 100)
	feed(term, "hello world")
	term.Resize(20, 4)
	checkLines(t, term, "hello world")
	checkCursor(t, term, 11, 0)
	feed(term, "!")
	checkLines(t, term, "hello world!")
}

func TestReflowKeepsHardNewlines(t *testing.T) {
	term := New(5, 4, 100)
	feed(term, "ab\r\ncd")
	term.Resize(10, 4)
	checkLines(t, term, "ab", "cd")
	term.Resize(2, 4)
	checkLines(t, term, "ab", "cd")
}

func TestReflowRoundTrip(t *testing.T) {
	term := New(20, 3, 100)
	text := []string{"the quick brown fox", "jumps", "over the lazy dog!!", "x"}
	feed(term, strings.Join(text, "\r\n"))
	for _, w := range []int{7, 3, 13, 2, 40, 20} {
		term.Resize(w, 3)
	}
	checkLines(t, term, text...)
	checkCursor(t, term, 1, 2)
}

func TestReflowScrollback(t *testing.T) {
	term := New(6, 2, 100)
	feed(term, "hello world\r\nnext\r\n$ ")
	// "hello " and "world" are in the history: the space at the end of the
	// wrapped line must survive.
	term.Resize(20, 2)
	checkLines(t, term, "hello world", "next", "$")
	checkCursor(t, term, 2, 1)
	if term.HistoryLen() != 1 {
		t.Fatalf("history = %d", term.HistoryLen())
	}
}

func TestReflowLineStraddlingHistory(t *testing.T) {
	term := New(4, 2, 100)
	feed(term, "abcdefghij")
	// "abcd" in the history, "efgh" and "ij" on the screen.
	term.Resize(10, 2)
	checkLines(t, term, "abcdefghij")
	checkCursor(t, term, 10-1, 0)
	feed(term, "k")
	checkLines(t, term, "abcdefghij", "k")
}

func TestReflowPendingWrap(t *testing.T) {
	term := New(5, 3, 100)
	feed(term, "hello")
	term.Resize(10, 3)
	checkCursor(t, term, 5, 0)
	feed(term, "!")
	checkLines(t, term, "hello!")

	term = New(10, 3, 100)
	feed(term, "abcde")
	term.Resize(5, 3)
	feed(term, "f")
	checkLines(t, term, "abcde", "f")
}

func TestReflowWideChars(t *testing.T) {
	term := New(10, 3, 100)
	feed(term, "ab日本")
	term.Resize(3, 3)
	checkLines(t, term, "ab", "日", "本")

	// A wide character that did not fit at the end of a line leaves a
	// blank cell behind, which must not turn into a space when widening.
	term = New(6, 3, 100)
	feed(term, "abcde日")
	term.Resize(10, 3)
	checkLines(t, term, "abcde日")
}

func TestReflowCursorMidLine(t *testing.T) {
	term := New(20, 3, 100)
	feed(term, "$ git commit -m")
	feed(term, "\x1b[9D") // back on the 'c' of "commit"
	term.Resize(5, 3)
	checkCursor(t, term, 1, 1)
	if got := term.ScreenLine(1)[1].Rune(); got != 'c' {
		t.Fatalf("cursor on %q", got)
	}
}

func TestReflowPushesOverflowToHistory(t *testing.T) {
	term := New(10, 2, 100)
	feed(term, "0123456789\r\n$ ")
	term.Resize(5, 2)
	checkLines(t, term, "01234", "56789", "$")
	checkCursor(t, term, 2, 1)
	if term.HistoryLen() != 1 {
		t.Fatalf("history = %d", term.HistoryLen())
	}
}

func TestReflowHistoryFirstIndexStable(t *testing.T) {
	term := New(10, 2, 100)
	for i := 0; i < 5; i++ {
		feed(term, "0123456789\r\n")
	}
	first, _ := term.AbsBase()
	term.Resize(5, 2)
	if f, _ := term.AbsBase(); f != first {
		t.Fatalf("first = %d, was %d", f, first)
	}
}

func TestReflowAltScreen(t *testing.T) {
	term := New(5, 3, 100)
	feed(term, "hello world\x1b[?1049h")
	feed(term, "\x1b[Halt screen")
	term.Resize(20, 3)
	// The alternate screen is left to the program, which redraws.
	if got := lineText(term, 0); got != "alt s" {
		t.Fatalf("alt line 0 = %q", got)
	}
	feed(term, "\x1b[?1049l")
	checkLines(t, term, "hello world")
	checkCursor(t, term, 11, 0)
}

func TestReflowClearedScreenStaysClear(t *testing.T) {
	term := New(10, 3, 100)
	feed(term, "old 1\r\nold 2\r\nold 3\r\nold 4\r\n\x1b[2J\x1b[H$ ")
	term.Resize(20, 3)
	if got := lineText(term, 0); got != "$" {
		t.Fatalf("line 0 = %q", got)
	}
	checkCursor(t, term, 2, 0)
}

func TestReflowRowsOnlyKeepsOldBehavior(t *testing.T) {
	term := New(5, 4, 100)
	feed(term, "hello world")
	term.Resize(5, 2)
	checkLines(t, term, "hello", " worl", "d")
}

func BenchmarkReflowFullHistory(b *testing.B) {
	term := New(120, 40, 10000)
	line := strings.Repeat("lorem ipsum dolor sit amet ", 7)
	for i := 0; i < 10000; i++ {
		feed(term, "\x1b[3"+string(rune('1'+i%6))+"m"+line[:i%len(line)]+"\x1b[0m\r\n")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			term.Resize(97, 40)
		} else {
			term.Resize(120, 40)
		}
	}
}
