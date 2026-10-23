package ui

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// TestRenameNarrowPaneNoPanic: the rename field of a pane narrower than
// its header (4 columns on Retina, a floating pane at its minimum size with
// a larger UI font) must not go out of the slice bounds.
func TestRenameNarrowPaneNoPanic(t *testing.T) {
	a, ts := coreApp(t, 1)
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	cw := a.uiFont.CellW
	for _, buf := range []string{"", "abc", "a name much longer than the space available"} {
		for cells := 0; cells <= 40; cells++ {
			p.target, p.from = image.Rect(100, 100, 100+cells*cw, 400), image.Rectangle{}
			a.renaming, a.renameBuf = p, []rune(buf)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("width %d cells, field %q: panic %v", cells, buf, r)
					}
				}()
				a.drawPane(ctx, dst, p, 0, true)
			}()
		}
	}
}

// TestContentRectStaysInPane: the content area of a pane that is too small
// is empty, never flipped above its header.
func TestContentRectStaysInPane(t *testing.T) {
	r := image.Rect(100, 100, 104, 110)
	if c := contentRect(r, 20, 3); !c.Empty() || c.Min.Y < r.Min.Y+20 {
		t.Errorf("area %v for pane %v", c, r)
	}
	if got := contentRect(image.Rect(0, 0, 200, 100), 20, 3); got != image.Rect(3, 23, 197, 97) {
		t.Errorf("regular area %v", got)
	}
}

// TestTermDrawClippedToContent: a terminal smaller than one line has a
// texture taller than its area. It is drawn clipped to the area, without
// covering the header.
func TestTermDrawClippedToContent(t *testing.T) {
	a, ts := coreApp(t, 1)
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	content := image.Rect(100, 200, 900, 200+a.termFont.CellH/2)
	p.Layout(ctx, content)
	p.Draw(ctx, dst, content, true)
	if p.r.img.Bounds().Dy() <= content.Dy() {
		t.Fatalf("texture of %d pixels for an area of %d", p.r.img.Bounds().Dy(), content.Dy())
	}
	if got := p.clip.sub.Bounds(); got != content {
		t.Errorf("drawing into %v, want the content area %v", got, content)
	}
}

// TestSelectionClampedToGrid: a selection made at 150 columns does not
// spill out of a terminal shrunk back to 80.
func TestSelectionClampedToGrid(t *testing.T) {
	p := &TermPane{cols: 80, rows: 24}
	p.sel = selection{active: true, a: selPos{row: 5, col: 10}, b: selPos{row: 7, col: 150}}
	for row := int64(5); row <= 7; row++ {
		x0, x1 := p.selSpan(row)
		if x0 < 0 || x1 > p.cols || x0 > x1 {
			t.Errorf("line %d: columns %d to %d for %d columns", row, x0, x1, p.cols)
		}
	}
	p.sel.a.col = 200 // the anchor itself outside the grid
	if x0, x1 := p.selSpan(5); x0 > x1 || x1 > p.cols {
		t.Errorf("anchor outside the grid: %d to %d", x0, x1)
	}
}

// TestHiddenPaneNotDamaged: a hidden pane is never drawn, so its dirty
// flag never clears. It must therefore not be added to the damage.
func TestHiddenPaneNotDamaged(t *testing.T) {
	a, ps := testApp(false, false)
	ps[1].Base().hidden = true
	a.damagePane(ps[1])
	if len(a.damage) != 0 {
		t.Fatal("a hidden pane must not be redrawn")
	}
	a.damagePane(ps[0])
	if len(a.damage) != 1 {
		t.Fatal("a visible pane must be")
	}
}

// TestHiddenNotifyNoFrames: an unread notification makes the border pulse
// at 15 frames per second, but only if the pane is shown.
func TestHiddenNotifyNoFrames(t *testing.T) {
	a, ts := coreApp(t, 2)
	ctx := a.frameCtx(time.Now())
	a.wasAnimating = false
	ts[1].notify = "bell"
	ts[1].hidden = true
	ts[1].animAt, ts[1].glitchAt = time.Time{}, time.Time{}
	ts[0].animAt, ts[0].glitchAt = time.Time{}, time.Time{}
	a.bg.Kind = "none"
	a.cfg.Animate = false
	a.lit, a.litAt = ts[0], time.Time{} // focus border at rest
	a.schedule(ctx)
	if fps := a.pacer.FPS(); fps != 0 {
		t.Errorf("hidden notified pane: %d frames/s, want 0", fps)
	}
	ts[1].hidden = false
	a.schedule(ctx)
	if fps := a.pacer.FPS(); fps != 15 {
		t.Errorf("visible notified pane: %d frames/s, want 15", fps)
	}
}

// TestFocusGlowFrames: the focus border lights up again on every pane
// change, not when coming back to the window. It only asks for frames
// while it dims.
func TestFocusGlowFrames(t *testing.T) {
	a, ts := coreApp(t, 2)
	for _, p := range ts {
		p.animAt, p.glitchAt = time.Time{}, time.Time{}
	}
	a.wasAnimating = false
	a.cfg.Animate = false
	now := time.Now()
	frames := func() int {
		a.damage = a.damage[:0]
		a.schedule(a.frameCtx(now))
		return a.pacer.FPS()
	}
	if fps := frames(); fps != 30 || !slices.Contains(a.damage, Pane(ts[0])) {
		t.Errorf("focus gained: %d frames/s, damage %d", fps, len(a.damage))
	}
	now = now.Add(2 * time.Second)
	if fps := frames(); fps != 0 {
		t.Errorf("border at rest: %d frames/s, want 0", fps)
	}
	a.focus = 1
	if fps := frames(); fps != 30 || !slices.Contains(a.damage, Pane(ts[1])) {
		t.Errorf("pane change: %d frames/s, damage %d", fps, len(a.damage))
	}
	now = now.Add(2 * time.Second)
	a.windowFocus(false)
	if fps := frames(); fps != 0 {
		t.Errorf("window left: %d frames/s, want 0", fps)
	}
	a.windowFocus(true)
	if fps := frames(); fps != 0 {
		t.Errorf("back on the window: %d frames/s, want 0 (no flash)", fps)
	}
	now = now.Add(2 * time.Second)
	a.cfg.FocusRest = 100
	a.focus = 0
	if fps := frames(); fps != 0 {
		t.Errorf("focus_border_rest at 100: %d frames/s, want 0", fps)
	}
}

// TestGridLargerThanPaneIsClipped: when the terminal grid (60x15) is larger
// than the pane content (40x10), drawing is clipped to the content rectangle,
// the grid is anchored at the content's top-left, and mouse mapping outside
// the content or grid is ignored.
func TestGridLargerThanPaneIsClipped(t *testing.T) {
	a, ts := coreApp(t, 1)
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+40*cw, 100+10*ch)
	p.Layout(ctx, content)

	// Terminal resized to 60x15 (e.g. owned by phone).
	p.t.Resize(60, 15)
	p.dirty = true

	p.Draw(ctx, dst, content, true)

	// Grid texture is rendered to full terminal size.
	if got := p.r.img.Bounds().Size(); got != image.Pt(60*cw, 15*ch) {
		t.Fatalf("grid texture size = %v, want 60x15 (%dx%d)", got, 60*cw, 15*ch)
	}

	// Anchored at content's top-left.
	if p.origin != content.Min {
		t.Errorf("grid origin = %v, want content top-left %v", p.origin, content.Min)
	}

	// Destination is clipped to the content rectangle: nothing is drawn outside it.
	if got := p.clip.sub.Bounds(); got != content {
		t.Errorf("clipped destination = %v, want content area %v", got, content)
	}

	// Mouse mapping stays relative to origin and ignores cells outside content or grid.
	if pt, ok := p.cellAt(content.Min.X+cw/2, content.Min.Y+ch/2); !ok || pt != image.Pt(0, 0) {
		t.Errorf("cellAt top-left = (%v, %v), want (0, 0), true", pt, ok)
	}
	if pt, ok := p.cellAt(content.Min.X+39*cw+cw/2, content.Min.Y+9*ch+ch/2); !ok || pt != image.Pt(39, 9) {
		t.Errorf("cellAt bottom-right of content = (%v, %v), want (39, 9), true", pt, ok)
	}
	if pt, ok := p.cellAt(content.Max.X+5, content.Min.Y+5); ok {
		t.Errorf("cellAt outside content = (%v, %v), want false", pt, ok)
	}
	if pt, ok := p.cellAt(content.Min.X+5, content.Max.Y+5); ok {
		t.Errorf("cellAt below content = (%v, %v), want false", pt, ok)
	}
	if pt, ok := p.cellAt(content.Min.X-5, content.Min.Y+5); ok {
		t.Errorf("cellAt before origin = (%v, %v), want false", pt, ok)
	}
}

// TestGridSmallerThanPaneLeavesBackground: when the terminal grid (20x5) is smaller
// than the pane content (40x10), the grid is anchored at the content's top-left,
// nothing is drawn outside the pane content rectangle, the rest of the pane shows
// the pane background, and mouse mapping ignores cells outside the 20x5 grid.
func TestGridSmallerThanPaneLeavesBackground(t *testing.T) {
	a, ts := coreApp(t, 1)
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	p := ts[0]
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+40*cw, 100+10*ch)
	p.Layout(ctx, content)

	// Resized to 60x15 first, then to 20x5.
	p.t.Resize(60, 15)
	p.dirty = true
	p.Draw(ctx, dst, content, true)

	p.t.Resize(20, 5)
	p.dirty = true

	// Active selection extending past 20 columns.
	p.sel = selection{
		active: true,
		a:      selPos{row: p.absRow(2), col: 0},
		b:      selPos{row: p.absRow(2), col: 35},
	}

	p.Draw(ctx, dst, content, true)

	// Grid texture only covers 20x5 cells; rest of 40x10 pane shows background.
	if got := p.r.img.Bounds().Size(); got != image.Pt(20*cw, 5*ch) {
		t.Fatalf("grid texture size = %v, want 20x5 (%dx%d)", got, 20*cw, 5*ch)
	}

	// Anchored at content's top-left.
	if p.origin != content.Min {
		t.Errorf("grid origin = %v, want content top-left %v", p.origin, content.Min)
	}

	// Destination is clipped to the content rectangle.
	if got := p.clip.sub.Bounds(); got != content {
		t.Errorf("clipped destination = %v, want content area %v", got, content)
	}

	// Selection overlay must be clamped to the 20-column grid, not pane's 40 cols.
	if x0, x1 := p.selSpan(p.absRow(2)); x0 != 0 || x1 != 20 {
		t.Errorf("selection span on row 2 = [%d, %d), want [0, 20)", x0, x1)
	}

	// Mouse mapping stays relative to origin and ignores cells outside the 20x5 grid.
	if pt, ok := p.cellAt(content.Min.X+5*cw, content.Min.Y+2*ch); !ok || pt != image.Pt(5, 2) {
		t.Errorf("cellAt inside grid = (%v, %v), want (5, 2), true", pt, ok)
	}
	if pt, ok := p.cellAt(content.Min.X+25*cw, content.Min.Y+2*ch); ok {
		t.Errorf("cellAt col 25 (outside 20-col grid) = (%v, %v), want false", pt, ok)
	}
	if pt, ok := p.cellAt(content.Min.X+5*cw, content.Min.Y+7*ch); ok {
		t.Errorf("cellAt row 7 (outside 5-row grid) = (%v, %v), want false", pt, ok)
	}
	if pt, ok := p.cellAt(content.Min.X-5, content.Min.Y+2*ch); ok {
		t.Errorf("cellAt before origin = (%v, %v), want false", pt, ok)
	}

	// Mouse click outside the 20x5 grid is ignored (no selection started).
	p.Mouse(ctx, MouseEvent{Kind: MousePress, X: content.Min.X + 25*cw, Y: content.Min.Y + 2*ch, Button: ebiten.MouseButtonLeft})
	if p.sel.a.col == 25 {
		t.Errorf("selection started outside 20-col grid at col 25")
	}
}

