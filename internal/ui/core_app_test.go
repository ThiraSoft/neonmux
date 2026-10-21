package ui

import (
	"image"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

// TestLaunchStaysOnTitle: at launch with nothing to restore, the window
// stays on the title screen, without a terminal until mod+enter. A command
// given with -e still opens in a terminal.
func TestLaunchStaysOnTitle(t *testing.T) {
	a, _ := testApp()
	a.openFirstPanes()
	if len(a.panes) != 0 {
		t.Fatalf("%d panes, want the title screen", len(a.panes))
	}
	a, _ = testApp()
	a.opts.Exec = []string{"nvim", "main.go"}
	a.openFirstPanes()
	if len(a.panes) != 1 {
		t.Fatalf("-e: %d panes, want 1", len(a.panes))
	}
	if tp, ok := a.panes[0].(*TermPane); !ok || !slices.Equal(tp.argv, []string{"nvim", "main.go"}) {
		t.Errorf("-e: pane %T, want a terminal running nvim main.go", a.panes[0])
	}
}

// TestSplashHintsFont: the hints of the title screen use the pixel font at
// 14 points, whatever the interface font, and follow the display scale
// when the fonts are loaded again (window moved to another screen).
func TestSplashHintsFont(t *testing.T) {
	a, _ := coreApp(t, 0)
	a.symbols = "-"
	a.cfg.UIFont = "iosevka"
	a.loadFonts()
	if h := a.splashFont(); h.Name != gfx.FontPixel || h.CellH != 52 {
		t.Fatalf("Retina: hints %s %dpx, want the pixel font, 13px × 4", h.Name, h.CellH)
	}
	a.dsf = 1
	a.loadFonts()
	if h := a.splashFont(); h.CellH != 26 {
		t.Errorf("standard screen: hints %dpx, want 13px × 2", h.CellH)
	}
}

// TestClosePaneForgetsPressPane: a pane closed during a click must no
// longer receive the button release.
func TestClosePaneForgetsPressPane(t *testing.T) {
	a, ps := testApp(false, false)
	a.focus = 0
	a.pressPane = ps[1]
	a.closePane(1)
	if a.pressPane != nil {
		t.Error("pressPane still points to the closed pane")
	}
}

// TestRelayoutKeepsFrameCtx: Update holds the frame context by pointer.
// A relayout in the middle (key, click) must not rewrite it, otherwise
// DT drops to a few µs and animations skip a step.
func TestRelayoutKeepsFrameCtx(t *testing.T) {
	a, _ := coreApp(t, 2)
	now := time.Now()
	a.lastFrame = now.Add(-16 * time.Millisecond)
	ctx := a.frameCtx(now)
	a.lastFrame = now
	dt, at := ctx.DT, ctx.Now
	a.relayout()
	if ctx.DT != dt || !ctx.Now.Equal(at) {
		t.Errorf("context rewritten by relayout: DT %v → %v", dt, ctx.DT)
	}
}

// TestDoubleClickNeedsSamePlace: a double click needs two clicks in quick
// succession at the same place (10 pixels) and on the same pane, not just
// in the same column.
func TestDoubleClickNeedsSamePlace(t *testing.T) {
	a, ps := testApp(false, false)
	t0 := time.Now()
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	steps := []struct {
		at   time.Time
		pt   image.Point
		p    Pane
		want int
	}{
		{ms(0), image.Pt(100, 100), ps[0], 1},
		{ms(100), image.Pt(103, 104), ps[0], 2},
		{ms(200), image.Pt(103, 140), ps[0], 1}, // same column, 36 pixels lower
		{ms(300), image.Pt(104, 141), ps[0], 2},
		{ms(400), image.Pt(104, 141), ps[1], 1},  // other pane, same point (shared edge)
		{ms(1000), image.Pt(104, 141), ps[1], 1}, // too late
	}
	for i, s := range steps {
		if got := a.countClick(s.at, s.pt, s.p); got != s.want {
			t.Errorf("click %d: count %d, want %d", i, got, s.want)
		}
	}
}

// refPaneAt and refFloatHit are the lookups from before hitAt, kept as a
// reference: each one sorted the draw order again.
func refPaneAt(a *App, pt image.Point) int {
	order := a.drawOrder()
	for k := len(order) - 1; k >= 0; k-- {
		if pt.In(order[k].Base().target) {
			return slices.Index(a.panes, order[k])
		}
	}
	return -1
}

func refFloatHit(a *App, pt image.Point) (int, edges) {
	grab := int(4 * a.px)
	order := a.drawOrder()
	for k := len(order) - 1; k >= 0; k-- {
		p := order[k]
		if isTile(p) {
			break
		}
		if p == a.zoomFloat {
			return -1, 0
		}
		r := p.Base().target
		if e := floatEdges(r, pt, grab); e != 0 || pt.In(r) {
			return slices.Index(a.panes, p), e
		}
	}
	return -1, 0
}

// TestHitAtMatchesSeparateSearches: the single pass of hitAt finds the
// same pane and the same edges as the two separate lookups.
func TestHitAtMatchesSeparateSearches(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 300; trial++ {
		var kinds []bool
		for range 1 + rng.IntN(6) {
			kinds = append(kinds, rng.IntN(2) == 0)
		}
		a, ps := testApp(kinds...)
		a.px = 2
		for _, p := range ps {
			x, y := rng.IntN(300), rng.IntN(300)
			p.Base().target = image.Rect(x, y, x+20+rng.IntN(200), y+20+rng.IntN(200))
			p.Base().hidden = rng.IntN(8) == 0
			if p.Base().floating && rng.IntN(3) == 0 {
				a.raise(p)
			}
		}
		if rng.IntN(4) == 0 {
			a.zoomFloat = ps[rng.IntN(len(ps))]
		}
		for range 50 {
			pt := image.Pt(rng.IntN(560)-20, rng.IntN(560)-20)
			h := a.hitAt(pt)
			fi, fe := refFloatHit(a, pt)
			if pi := refPaneAt(a, pt); h.pane != pi || h.float != fi || h.edges != fe {
				t.Fatalf("trial %d, point %v: hitAt %+v, want pane %d, floating %d, edges %b", trial, pt, h, pi, fi, fe)
			}
		}
	}
}

func BenchmarkMouseMoveHitTest(b *testing.B) {
	a, ps := testApp(false, false, false, false, true, true)
	a.px = 2
	for i, p := range ps {
		p.Base().target = image.Rect(i*100, 0, i*100+180, 400)
	}
	pt := image.Pt(250, 200)
	b.Run("before", func(b *testing.B) {
		for b.Loop() {
			refPaneAt(a, pt)
			refPaneAt(a, pt)
			refFloatHit(a, pt)
		}
	})
	b.Run("after", func(b *testing.B) {
		for b.Loop() {
			a.hitAt(pt)
		}
	})
}
