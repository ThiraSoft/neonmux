package ui

import (
	"encoding/base64"
	"fmt"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// Test bench for the core of the interface: a complete app without a
// window (fonts, background, pacer), with terminals plugged into a fake
// session. Ebiten accepts drawing before the main loop: the commands
// simply wait for a GPU, which is enough to exercise the renderer and
// count its allocations (ReadPixels, on the other hand, is refused).

// fakeBackend is a session with no process: the test writes into the
// terminal itself and reads back what the pane sends to the program.
type fakeBackend struct {
	term *vt.Terminal
	done chan struct{}
	mu   sync.Mutex
	in   []byte
	fg   string // foreground program, the shell when empty
}

func newFakeBackend(cols, rows int) *fakeBackend {
	return &fakeBackend{term: vt.New(cols, rows, 10000), done: make(chan struct{})}
}

func (b *fakeBackend) Terminal() *vt.Terminal { return b.term }
func (b *fakeBackend) Key() string            { return "fake" }
func (b *fakeBackend) Write(p []byte) {
	b.mu.Lock()
	b.in = append(b.in, p...)
	b.mu.Unlock()
}
func (b *fakeBackend) Resize(cols, rows, pxW, pxH int) { b.term.Resize(cols, rows) }
func (b *fakeBackend) Done() <-chan struct{}           { return b.done }
func (b *fakeBackend) ForegroundName() string {
	if b.fg != "" {
		return b.fg
	}
	return "zsh"
}
func (b *fakeBackend) ForegroundCwd() string { return "" }
func (b *fakeBackend) Cwd() string           { return "/tmp/project" }
func (b *fakeBackend) Close()                {}
func (b *fakeBackend) Detach()               {}

func (b *fakeBackend) sent() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.in)
}

// coreApp sets up an app of w×h physical pixels (Retina) with n
// terminals as tiles, each filled with a few hundred lines.
func coreApp(t testing.TB, n int) (*App, []*TermPane) {
	t.Helper()
	sh, err := fx.LoadShaders()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Font = gfx.FontPixel
	a := &App{cfg: cfg, theme: theme.Get(cfg.Theme), layout: "tiles", ratio: 0.5, ratio2: 0.5,
		pacer: fx.NewPacer(60), shaders: sh, parts: fx.NewParticles(), posted: make(chan func(), 64),
		start: time.Now()}
	a.bg = fx.NewBackground(sh)
	a.bg.Kind = "none"
	a.dsf, a.px = 2, 2
	a.w, a.h = 2880, 1800
	a.termFont, _ = gfx.LoadFont(gfx.FontPixel, cfg.FontSize, a.dsf)
	a.uiFont = a.termFont
	a.inited = true
	var ts []*TermPane
	for i := 0; i < n; i++ {
		p := NewTermPane(a.newID(), nil, "", nil)
		be := newFakeBackend(80, 24)
		p.s, p.t, p.cols, p.rows = be, be.term, 80, 24
		for k := 0; k < 300; k++ {
			fmt.Fprintf(be.term, "\x1b[3%dmline %d\x1b[0m of terminal %d, with a bit of text\r\n", k%8, k, i)
		}
		// An OSC title on every other terminal, an agent at work on another one.
		if i%2 == 0 {
			fmt.Fprintf(be.term, "\x1b]0;vim main.go\x07")
		}
		if i == 1 {
			fmt.Fprintf(be.term, "\x1b]7501;state=working:msg=%s\x07", base64.StdEncoding.EncodeToString([]byte("Reading app.go")))
		}
		a.panes = append(a.panes, p)
		ts = append(ts, p)
	}
	a.focus = 0
	a.relayout()
	return a, ts
}

// settle brings the app to rest: appearing animations done, one full
// frame, then nothing left to redraw.
func settle(a *App, screen *ebiten.Image) {
	for _, p := range a.panes {
		b := p.Base()
		b.from, b.glitchAt = image.Rectangle{}, time.Time{}
	}
	ctx := a.frameCtx(time.Now())
	for _, p := range a.panes {
		p.Update(ctx)
	}
	a.needRedraw = true
	a.Draw(screen)
	a.damage = a.damage[:0]
	a.needRedraw = false
}

// TestIdleFrameNoAlloc: when idle, a frame builds nothing. Draw computes
// the status bar summary before returning right away, and Update goes
// over every terminal.
func TestIdleFrameNoAlloc(t *testing.T) {
	a, _ := coreApp(t, 4)
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	if n := testing.AllocsPerRun(50, func() { _ = a.statusSignature(time.Now()) }); n != 0 {
		t.Errorf("statusSignature: %.0f allocations", n)
	}
	if n := testing.AllocsPerRun(50, func() { a.Draw(screen) }); n != 0 {
		t.Errorf("idle frame: %.0f allocations", n)
	}
	ctx := a.frameCtx(time.Now())
	if n := testing.AllocsPerRun(50, func() {
		for _, q := range a.panes {
			q.Update(ctx)
		}
	}); n != 0 {
		t.Errorf("Update of 4 idle terminals: %.0f allocations", n)
	}
}

func BenchmarkCoreFrame(b *testing.B) {
	a, _ := coreApp(b, 4)
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	b.ReportAllocs()
	for b.Loop() {
		ctx := a.frameCtx(time.Now())
		for _, p := range a.panes {
			if p.Update(ctx) {
				a.damagePane(p)
			}
		}
		a.Draw(screen)
	}
}

// BenchmarkCoreFullDraw measures a full frame (animated background, layout
// animation). Without a game loop, Ebiten queues every draw command and
// allocates to do so: the allocations of neonmux's own code show up in a
// memory profile, once atlas.(*Image).DrawTriangles is set aside.
func BenchmarkCoreFullDraw(b *testing.B) {
	a, _ := coreApp(b, 4)
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	b.ReportAllocs()
	for b.Loop() {
		a.needRedraw = true
		a.Draw(screen)
	}
}

// BenchmarkCoreHistoryRender measures the full render of a terminal
// scrolled back into the history: every line there is decoded.
func BenchmarkCoreHistoryRender(b *testing.B) {
	a, ts := coreApp(b, 1)
	p := ts[0]
	b.ReportAllocs()
	for b.Loop() {
		p.r.theme = nil // force a full render
		p.r.render(p.t, a.termFont, a.theme, 100)
	}
}
