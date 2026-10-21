package ui

import (
	"encoding/base64"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
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
