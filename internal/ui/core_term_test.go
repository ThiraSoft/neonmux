package ui

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

// TestLocalCloseStopsWakeups: a closed local pane no longer wakes the
// renderer, even if its process still writes or the read finishes
// afterwards.
func TestLocalCloseStopsWakeups(t *testing.T) {
	a, _ := coreApp(t, 0)
	p := NewTermPane(a.newID(), []string{"/bin/sh", "-c", "while :; do echo x; sleep 0.01; done"}, t.TempDir(), nil)
	s, err := a.startSession(p, 80, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	p.s, p.t = s, s.Terminal()
	deadline := time.Now().Add(3 * time.Second)
	for a.pacer.Kicks() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("no output from the shell")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.Close()
	k := a.pacer.Kicks()
	select {
	case <-p.s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the session does not end")
	}
	time.Sleep(100 * time.Millisecond)
	if n := a.pacer.Kicks() - k; n != 0 {
		t.Errorf("%d renderer wakeups after closing", n)
	}
}

// waitsForLock runs fn while the test holds the terminal lock and reports
// whether fn had to wait for it.
func waitsForLock(p *TermPane, fn func()) bool {
	p.t.Lock()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		p.t.Unlock()
		return false
	case <-time.After(300 * time.Millisecond):
		p.t.Unlock()
		<-done
		return true
	}
}

// TestIdleFrameTakesNoLock: when idle, neither Update, nor the title
// (header, status bar), nor the next wakeup, nor drawing an up-to-date
// pane takes the terminal lock, which the program's output competes for
// with the renderer.
func TestIdleFrameTakesNoLock(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	p.Update(ctx)
	p.Draw(ctx, dst, p.content, true) // texture and cursor state up to date
	if waitsForLock(p, func() { p.Update(ctx) }) {
		t.Error("idle Update waits for the lock")
	}
	if waitsForLock(p, func() { _ = displayName(p) }) {
		t.Error("the title waits for the lock")
	}
	if waitsForLock(p, func() { p.NextWake(ctx) }) {
		t.Error("NextWake waits for the lock")
	}
	if waitsForLock(p, func() { p.Draw(ctx, dst, p.content, true) }) {
		t.Error("drawing an up-to-date pane waits for the lock")
	}
	// The OSC title still follows the program's output.
	fmt.Fprintf(p.t, "\x1b]0;htop\x07")
	p.Update(ctx)
	if got := displayName(p); got != "zsh · htop" {
		t.Errorf("title %q after OSC 0", got)
	}
}

// TestHistoryRenderNoAlloc: scrolling back decodes the lines into a
// buffer kept from one frame to the next, with no allocation per line.
func TestHistoryRenderNoAlloc(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	f, th := a.termFont, a.theme
	full := func(offset int) func() {
		return func() {
			p.r.theme = nil // force a full render
			p.r.render(p.t, f, th, offset)
		}
	}
	full(100)()
	screen := testing.AllocsPerRun(50, full(0))
	hist := testing.AllocsPerRun(50, full(100))
	// Without a game loop, Ebiten keeps its commands in a slice that grows
	// now and then: a difference of one allocation does not count. One per
	// scrollback line does.
	if hist > screen+1 {
		t.Errorf("scrollback: %.0f allocations, screen: %.0f (%d lines)", hist, screen, p.rows)
	}
}

// TestHistoryRenderOnlyDirtyRows: while reading the scrollback, a screen
// line that changes (clock, progress bar) only gets itself redrawn. A
// scroll, which shifts the whole view, redraws everything.
func TestHistoryRenderOnlyDirtyRows(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	f, th := a.termFont, a.theme
	p.offset = 5
	p.r.render(p.t, f, th, p.offset)
	fmt.Fprintf(p.t, "\x1b[1;1Hhello") // screen line 0, seen as line 5
	p.r.render(p.t, f, th, p.offset)
	if p.r.rows != 1 {
		t.Errorf("%d lines redrawn for one changed line", p.r.rows)
	}
	fmt.Fprintf(p.t, "\x1b[%d;1H\r\nmore", p.rows) // scroll by one line
	p.r.render(p.t, f, th, p.offset)
	if p.r.rows != p.rows {
		t.Errorf("scroll: %d lines redrawn out of %d", p.r.rows, p.rows)
	}
	p.r.render(p.t, f, th, p.offset)
	if p.r.rows != 0 {
		t.Errorf("nothing changed: %d lines redrawn", p.r.rows)
	}
}

// TestOSC52OnlyFocusedVisible: only the active, visible pane can write to
// the clipboard. A pane without focus, hidden, in the garage or masked by
// zen mode is ignored.
func TestOSC52OnlyFocusedVisible(t *testing.T) {
	var got []string
	old := copyOSC52
	copyOSC52 = func(s string) { got = append(got, s) }
	defer func() { copyOSC52 = old }()

	a, ts := coreApp(t, 2)
	ctx := a.frameCtx(time.Now())
	osc := func(p *TermPane, text string) {
		fmt.Fprintf(p.t, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
		p.Update(ctx)
	}
	a.focus = 0
	osc(ts[1], "no focus")
	ts[0].hidden = true
	osc(ts[0], "hidden")
	ts[0].hidden = false
	a.zen = true
	osc(ts[0], "zen")
	a.zen = false
	osc(ts[0], "active")
	a.park(ts[1])
	a.focus = slices.Index(a.panes, Pane(ts[1]))
	osc(ts[1], "garage")
	if !slices.Equal(got, []string{"active"}) {
		t.Errorf("clipboard written by %q, want only the active pane", got)
	}
}

// TestTextBufNeverAScreenLine: copying text reuses its buffer for
// scrollback lines, but a screen line (returned without a copy) must
// never become it: the next decode would overwrite it.
func TestTextBufNeverAScreenLine(t *testing.T) {
	_, ts := coreApp(t, 1)
	p := ts[0]
	p.t.Lock()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	p.sel = selection{active: true, a: selPos{row: screen - 5}, b: selPos{row: screen - 2, col: 200}, lines: true}
	want := p.SelectedText()
	if cap(p.textBuf) == 0 {
		t.Fatal("scrollback lines are decoded into a new buffer every time")
	}
	if got := p.SelectedText(); got != want {
		t.Fatalf("text changed from one copy to the next:\n%q\n%q", want, got)
	}
	// Down into the screen: the buffer never points to one of its lines.
	p.sel.b.row = screen + 3
	p.SelectedText()
	if p.cm == nil {
		p.EnterCopyMode()
	}
	p.copyLine(screen + 1)
	p.selectWord(selPos{row: screen + 2, col: 3})
	p.t.Lock()
	defer p.t.Unlock()
	_, rows := p.t.Size()
	for y := 0; y < rows; y++ {
		if l := p.t.ScreenLine(y); cap(p.textBuf) > 0 && len(l) > 0 && &p.textBuf[:1][0] == &l[0] {
			t.Fatalf("the text buffer is screen line %d", y)
		}
	}
}

// TestCopySearch: / and ? find the pattern in the scrollback, n and N
// repeat it, the search wraps around and a missing pattern says so.
func TestCopySearch(t *testing.T) {
	a, ts := coreApp(t, 1)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	p.EnterCopyMode()
	typeIn := func(s string) {
		for _, r := range s {
			p.copyChar(ctx, r)
		}
	}
	line := func() string {
		var sb strings.Builder
		for _, c := range p.copyLine(p.cm.pos.row) {
			sb.WriteRune(c.Rune())
		}
		return sb.String()
	}
	search := func(s string) {
		typeIn(s)
		p.copyKey(ctx, KeyEvent{Name: "enter"})
	}

	start := p.cm.pos
	search("?LINE 120 ")
	if p.cm.pos != start {
		t.Fatalf("capital pattern matched %q at %d, want no match", line(), p.cm.pos.col)
	}
	if p.Mode() != "NOT FOUND" {
		t.Fatalf("mode %q after a missing pattern", p.Mode())
	}
	search("?line 120 ")
	if !strings.HasPrefix(line(), "line 120 ") || p.cm.pos.col != 0 {
		t.Fatalf("?line 120 landed on %q col %d", line(), p.cm.pos.col)
	}
	p.searchHits(p.absRow(0))
	if p.Mode() != "COPY" || len(p.cm.hits) == 0 {
		t.Fatalf("mode %q, %d highlighted matches", p.Mode(), len(p.cm.hits))
	}
	search("/terminal")
	row := p.cm.pos.row
	if !strings.HasPrefix(line(), "line 120 ") || p.cm.pos.col != 12 {
		t.Fatalf("/terminal landed on %q col %d", line(), p.cm.pos.col)
	}
	typeIn("n")
	if p.cm.pos.row != row+1 {
		t.Fatalf("n went to row %d, want %d", p.cm.pos.row, row+1)
	}
	typeIn("N")
	if p.cm.pos.row != row {
		t.Fatalf("N went to row %d, want %d", p.cm.pos.row, row)
	}
	// Wrapping: from the top, searching up lands on the last match.
	typeIn("gg")
	search("?line 299 ")
	if !strings.HasPrefix(line(), "line 299 ") {
		t.Fatalf("?line 299 from the top landed on %q", line())
	}
	// Typing, backspace, escape: the prompt shows in the mode, q is typed.
	typeIn("/xq")
	p.copyKey(ctx, KeyEvent{Name: "backspace"})
	if p.Mode() != "/x" {
		t.Fatalf("prompt %q, want /x", p.Mode())
	}
	p.copyKey(ctx, KeyEvent{Name: "escape"})
	if p.cm == nil || p.Mode() != "COPY" {
		t.Fatal("escape must leave the prompt, not copy mode")
	}
}

func TestMatchLine(t *testing.T) {
	cells := func(s string) []vt.Cell {
		var out []vt.Cell
		for _, r := range s {
			out = append(out, vt.MakeCell(r, 0, 0, 0))
		}
		return out
	}
	got := (&copyMode{pat: []rune("foo")}).match(cells("Foo foo FOO"))
	if !slices.Equal(got, [][2]int{{0, 3}, {4, 7}, {8, 11}}) {
		t.Errorf("smartcase lower: %v", got)
	}
	got = (&copyMode{pat: []rune("Foo")}).match(cells("Foo foo FOO"))
	if !slices.Equal(got, [][2]int{{0, 3}}) {
		t.Errorf("smartcase capital: %v", got)
	}
}

// TestCopyParagraph: { and } stop on blank lines, vip and vap select the
// paragraph, vap with the blank lines after it.
func TestCopyParagraph(t *testing.T) {
	a, ts := coreApp(t, 1)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	fmt.Fprint(p.t, "\x1b[2J\x1b[Hone\r\ntwo\r\n\r\n\r\nthree\r\nfour\r\nfive\r\n\r\nsix")
	p.EnterCopyMode()
	p.t.Lock()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	keys := func(s string) {
		for _, r := range s {
			p.copyChar(ctx, r)
		}
	}
	at := func(row int64) {
		p.cm.pos = selPos{screen + row, 1}
		p.cm.visual = 0
	}
	at(5) // four
	keys("}")
	if p.cm.pos.row != screen+7 {
		t.Fatalf("} went to row %d, want 7", p.cm.pos.row-screen)
	}
	keys("{")
	if p.cm.pos.row != screen+3 {
		t.Fatalf("{ went to row %d, want 3", p.cm.pos.row-screen)
	}
	at(4) // first line of a paragraph: { stops on the blank line just above
	keys("{")
	if p.cm.pos.row != screen+3 {
		t.Fatalf("{ from three went to row %d, want 3", p.cm.pos.row-screen)
	}
	at(1) // last line of a paragraph
	keys("}")
	if p.cm.pos.row != screen+2 {
		t.Fatalf("} from two went to row %d, want 2", p.cm.pos.row-screen)
	}
	// Escape cancels a pending object: q then quits.
	at(5)
	keys("vi")
	p.copyKey(ctx, KeyEvent{Name: "escape"})
	keys("q")
	if p.cm != nil {
		t.Fatal("q swallowed by an object cancelled with escape")
	}
	p.EnterCopyMode()
	at(5)
	keys("vip")
	if p.SelectedText() != "three\nfour\nfive" {
		t.Fatalf("vip selected %q", p.SelectedText())
	}
	at(4)
	keys("vap")
	if p.SelectedText() != "three\nfour\nfive\n" {
		t.Fatalf("vap selected %q", p.SelectedText())
	}
	if p.Mode() != "V-LINE" {
		t.Fatalf("mode %q after vap", p.Mode())
	}
}

// TestCopyWordObject: viw selects the word under the cursor, a path
// included, vaw adds the blanks after it, or before it at the end of the
// line.
func TestCopyWordObject(t *testing.T) {
	a, ts := coreApp(t, 1)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	fmt.Fprint(p.t, "\x1b[2J\x1b[Hopen  ./cmd/main.go, (ok) end")
	p.EnterCopyMode()
	p.t.Lock()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	// "open  ./cmd/main.go, (ok) end"
	for _, tc := range []struct {
		col  int
		keys string
		a, b int // selected columns, inclusive
	}{
		{9, "viw", 6, 18},   // ./cmd/main.go
		{1, "viw", 0, 3},    // open
		{1, "vaw", 0, 5},    // open and the two blanks
		{5, "viw", 4, 5},    // the blanks
		{5, "vaw", 4, 18},   // the blanks and the path
		{19, "viw", 19, 19}, // ,
		{22, "viw", 22, 23}, // ok
		{27, "vaw", 25, 28}, // end, with the blank before it
		{2, "Viw", 0, 3},    // from V-LINE back to characters
	} {
		p.cm.pos, p.cm.visual = selPos{screen, tc.col}, 0
		for _, r := range tc.keys {
			p.copyChar(ctx, r)
		}
		if p.cm.anchor.col != tc.a || p.cm.pos.col != tc.b {
			t.Errorf("%s at %d: [%d, %d], want [%d, %d]", tc.keys, tc.col, p.cm.anchor.col, p.cm.pos.col, tc.a, tc.b)
		}
	}
	if got := p.SelectedText(); got != "open" {
		t.Errorf("Viw copies %q", got)
	}
	if p.Mode() != "VISUAL" {
		t.Errorf("mode %q after Viw, want VISUAL", p.Mode())
	}
}

// TestCopyObjectsGrow: counts, objects that grow a selection, brackets
// across lines and quotes, the way vim does them.
func TestCopyObjectsGrow(t *testing.T) {
	a, ts := coreApp(t, 1)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	fmt.Fprint(p.t, "\x1b[2J\x1b[H"+
		"one two three four\r\n"+ // 0
		"f(a, (b + c), [d]) x\r\n"+ // 1
		"func main() {\r\n"+ // 2
		"\tfmt.Println(\"hi\")\r\n"+ // 3
		"}\r\n"+ // 4
		"say \"hello world\" and 'it' now\r\n"+ // 5
		"\r\n"+ // 6
		"p1\r\n\r\np2\r\n\r\np3") // 7..11
	p.EnterCopyMode()
	p.t.Lock()
	_, screen := p.t.AbsBase()
	p.t.Unlock()
	run := func(row, col int, keys string) string {
		p.cm.pos, p.cm.visual, p.cm.count = selPos{screen + int64(row), col}, 0, 0
		for _, r := range keys {
			p.copyChar(ctx, r)
		}
		return p.SelectedText()
	}
	for _, tc := range []struct {
		row, col int
		keys     string
		want     string
	}{
		{0, 1, "v2iw", "one"}, // the blank after it is cut from the copy
		{0, 1, "v3iw", "one two"},
		{0, 1, "v2aw", "one two"},
		{0, 1, "viwiwiw", "one two"},
		{0, 9, "vbiw", " th"}, // backwards: the cursor went left
		{1, 7, "vi(", "b + c"},
		{1, 7, "va(", "(b + c)"},
		{1, 7, "vi(i(", "a, (b + c), [d]"},
		{1, 7, "v2i(", "a, (b + c), [d]"},
		{1, 7, "vibab", "(b + c)"},
		{1, 7, "vabab", "(a, (b + c), [d])"},
		{1, 1, "vi)", "a, (b + c), [d]"}, // on the bracket itself
		{1, 15, "vi[", "d"},
		{3, 3, "vi{", "        fmt.Println(\"hi\")"}, // the tab is on screen as blanks
		{3, 3, "va{", "{\n        fmt.Println(\"hi\")\n}"},
		{3, 14, "vi\"", "hi"},
		{5, 8, "vi\"", "hello world"},
		{5, 8, "va\"", "\"hello world\""},
		{5, 8, "vi\"i\"", "\"hello world\""},
		{5, 0, "vi\"", "hello world"}, // before the first quote
		{5, 23, "vi'", "it"},
		{7, 0, "vipip", "p1\n"},
		{7, 0, "vapap", "p1\n\np2\n"},
		{7, 0, "v2ap", "p1\n\np2\n"},
		{0, 0, "3l", ""},
	} {
		if got := run(tc.row, tc.col, tc.keys); got != tc.want {
			t.Errorf("%q at %d,%d: %q, want %q", tc.keys, tc.row, tc.col, got, tc.want)
		}
	}
	if p.cm.pos.col != 3 {
		t.Errorf("3l went to column %d", p.cm.pos.col)
	}
	// The blank after the quotes, cut from the copy, is in the selection.
	run(5, 8, "va\"")
	if p.cm.anchor.col != 4 || p.cm.pos.col != 17 {
		t.Errorf("va\" selects [%d, %d], want [4, 17]", p.cm.anchor.col, p.cm.pos.col)
	}
	run(0, 1, "v2iw")
	if p.cm.pos.col != 3 {
		t.Errorf("v2iw ends at %d, want 3", p.cm.pos.col)
	}
	// Nothing to select: the selection stays as it was.
	if got := run(0, 2, "vi("); got != "e" {
		t.Errorf("vi( outside brackets: %q", got)
	}
}

// TestCopyHugeCount: a huge count on a search or an object stops once
// nothing changes anymore, instead of walking the scrollback each time.
func TestCopyHugeCount(t *testing.T) {
	a, ts := coreApp(t, 1)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	fmt.Fprint(p.t, "{ foo\r\n  }")
	p.EnterCopyMode()
	keys := func(s string) {
		for _, r := range s {
			p.copyChar(ctx, r)
		}
	}
	start := time.Now()
	keys("/nowhere")
	p.copyKey(ctx, KeyEvent{Name: "enter"})
	keys("99999n")
	keys("k0v99999i(")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("huge counts took %v", d)
	}
	p.exitCopyMode()
	p.EnterCopyMode()
	p.t.Lock()
	_, screen := p.t.AbsBase()
	_, y, _, _ := p.t.Cursor()
	p.t.Unlock()
	p.cm.pos = selPos{screen + int64(y) - 1, 3}
	keys("vi{")
	if got := p.SelectedText(); got != " foo" {
		t.Errorf("vi{ on a closing line with blanks only: %q, want \" foo\"", got)
	}
}
