package ui

import (
	"image"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

func TestFloatPlaceCorners(t *testing.T) {
	area := image.Rect(10, 10, 1010, 610)
	mn := image.Pt(50, 40)
	cases := []struct {
		g    floatGeom
		want image.Rectangle
	}{
		{floatGeom{X: 0, Y: 0, W: 200, H: 100}, image.Rect(10, 10, 210, 110)},
		{floatGeom{X: 1, Y: 1, W: 200, H: 100}, image.Rect(810, 510, 1010, 610)},
		{floatGeom{X: 1, Y: 0, W: 200, H: 100}, image.Rect(810, 10, 1010, 110)},
		{floatGeom{X: 0.5, Y: 0.5, W: 200, H: 100}, image.Rect(410, 260, 610, 360)},
		// Too small: minimum size. Too large: the whole area.
		{floatGeom{X: 1, Y: 1, W: 10, H: 10}, image.Rect(960, 570, 1010, 610)},
		{floatGeom{X: 1, Y: 1, W: 5000, H: 5000}, area},
	}
	for _, c := range cases {
		if got := floatPlace(c.g, area, mn, 1); got != c.want {
			t.Errorf("%+v: %v, want %v", c.g, got, c.want)
		}
	}
	// On Retina, the size is in points: twice as many pixels.
	if got := floatPlace(floatGeom{X: 0, Y: 0, W: 200, H: 100}, area, mn, 2); got.Dx() != 400 || got.Dy() != 200 {
		t.Errorf("dsf 2: %v", got)
	}
}

func TestFloatStaysInCornerOnResize(t *testing.T) {
	mn := image.Pt(50, 40)
	small := image.Rect(0, 0, 800, 600)
	r := image.Rect(600, 0, 800, 100) // against the top right
	g := floatGeomOf(r, small, 1, floatGeom{})
	if g.X != 1 || g.Y != 0 {
		t.Fatalf("geometry %+v, want the top right corner", g)
	}
	big := image.Rect(0, 0, 1600, 900)
	if got := floatPlace(g, big, mn, 1); got != image.Rect(1400, 0, 1600, 100) {
		t.Errorf("after growing: %v", got)
	}
}

func TestFloatGeomRoundTrip(t *testing.T) {
	area := image.Rect(12, 12, 1588, 900)
	mn := image.Pt(50, 40)
	for _, r := range []image.Rectangle{
		image.Rect(100, 50, 500, 350),
		image.Rect(12, 600, 412, 900),
		image.Rect(1188, 12, 1588, 300),
	} {
		g := floatGeomOf(r, area, 2, floatGeom{})
		if got := floatPlace(g, area, mn, 2); got != r {
			t.Errorf("%v → %+v → %v", r, g, got)
		}
	}
	// As wide as the area: the previous horizontal position is kept.
	g := floatGeomOf(image.Rect(12, 100, 1588, 300), area, 1, floatGeom{X: 0.25})
	if g.X != 0.25 {
		t.Errorf("X = %v, want 0.25", g.X)
	}
}

func TestDragRect(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	mn := image.Pt(100, 80)
	start := image.Rect(400, 200, 600, 400)
	cases := []struct {
		name string
		d    image.Point
		e    edges
		want image.Rectangle
	}{
		{"move", image.Pt(30, -20), edgeAll, image.Rect(430, 180, 630, 380)},
		{"stopped at the edge", image.Pt(900, 0), edgeAll, image.Rect(800, 200, 1000, 400)},
		{"snapped to the edge", image.Pt(-395, 0), edgeAll, image.Rect(0, 200, 200, 400)},
		{"bottom right corner", image.Pt(50, 40), edgeRight | edgeBottom, image.Rect(400, 200, 650, 440)},
		{"minimum size", image.Pt(-500, 0), edgeRight, image.Rect(400, 200, 500, 400)},
		{"left edge", image.Pt(-100, 0), edgeLeft, image.Rect(300, 200, 600, 400)},
		{"snapped edge", image.Pt(0, 195), edgeBottom, image.Rect(400, 200, 600, 600)},
	}
	for _, c := range cases {
		if got := dragRect(start, c.d, c.e, area, mn, 8); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFloatEdges(t *testing.T) {
	r := image.Rect(100, 100, 500, 400)
	cases := []struct {
		pt   image.Point
		want edges
	}{
		{image.Pt(300, 250), 0},
		{image.Pt(50, 250), 0}, // outside the pane
		{image.Pt(98, 250), edgeLeft},
		{image.Pt(501, 250), edgeRight},
		{image.Pt(300, 399), edgeBottom},
		{image.Pt(499, 399), edgeRight | edgeBottom},
		// Near a corner, the band widens along the edge.
		{image.Pt(490, 399), edgeRight | edgeBottom},
		{image.Pt(101, 110), edgeLeft | edgeTop},
	}
	for _, c := range cases {
		if got := floatEdges(r, c.pt, 4); got != c.want {
			t.Errorf("%v: %b, want %b", c.pt, got, c.want)
		}
	}
	if edgesCursor(edgeRight|edgeTop) != ebiten.CursorShapeNESWResize || edgesCursor(edgeAll) != ebiten.CursorShapeMove {
		t.Error("edge cursors")
	}
}

func TestParseFloatArgs(t *testing.T) {
	cases := []struct {
		args []string
		want FloatOptions
		err  bool
	}{
		{nil, FloatOptions{}, false},
		{[]string{"neonwave"}, FloatOptions{Cmd: []string{"neonwave"}}, false},
		{[]string{"-c", "tr", "-s", "60x15", "neonwave", "-v"}, FloatOptions{Corner: "tr", Cols: 60, Rows: 15, Cmd: []string{"neonwave", "-v"}}, false},
		{[]string{"--corner=bottom-left", "--", "-x"}, FloatOptions{Corner: "bottom-left", Cmd: []string{"-x"}}, false},
		{[]string{"-c", "north"}, FloatOptions{}, true},
		{[]string{"-s", "60"}, FloatOptions{}, true},
		{[]string{"-c"}, FloatOptions{}, true},
		{[]string{"--whatever"}, FloatOptions{}, true},
	}
	for _, c := range cases {
		got, err := ParseFloatArgs(c.args)
		if (err != nil) != c.err {
			t.Errorf("%q: error %v", c.args, err)
			continue
		}
		if !c.err && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %+v, want %+v", c.args, got, c.want)
		}
	}
}

// fakePane is a minimal pane for testing focus and stacking.
type fakePane struct {
	paneBase
	closed bool
}

func (p *fakePane) Kind() string                                    { return "fake" }
func (p *fakePane) Title() string                                   { return "fake" }
func (p *fakePane) Info() string                                    { return "" }
func (p *fakePane) Layout(*Ctx, image.Rectangle)                    {}
func (p *fakePane) Update(*Ctx) bool                                { return false }
func (p *fakePane) Key(*Ctx, KeyEvent) bool                         { return false }
func (p *fakePane) Chars(*Ctx, []rune)                              {}
func (p *fakePane) Mouse(*Ctx, MouseEvent) bool                     { return false }
func (p *fakePane) Draw(*Ctx, *ebiten.Image, image.Rectangle, bool) {}
func (p *fakePane) AnimFPS(*Ctx) int                                { return 0 }
func (p *fakePane) NextWake(*Ctx) time.Time                         { return time.Time{} }
func (p *fakePane) Close()                                          { p.closed = true }
func (p *fakePane) Done() bool                                      { return false }
func (p *fakePane) Mode() string                                    { return "" }

// testApp creates an app without a window: relayout stops for lack of a
// font, which is enough for the focus logic.
func testApp(floating ...bool) (*App, []Pane) {
	a := &App{cfg: DefaultConfig(), theme: theme.Get("neon-tokyo")}
	var ps []Pane
	for i, f := range floating {
		p := &fakePane{}
		p.id = i + 1
		p.floating = f
		if f {
			a.raise(p)
		}
		ps = append(ps, p)
	}
	a.panes = slices.Clone(ps)
	return a, ps
}

func TestDrawOrderFloatsOnTop(t *testing.T) {
	a, ps := testApp(true, false, true, false)
	a.raise(ps[0]) // the first floating pane goes on top
	got := a.drawOrder()
	want := []Pane{ps[1], ps[3], ps[2], ps[0]}
	if !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", ids(got), ids(want))
	}
	// The click goes to the floating pane on top.
	ps[0].Base().target = image.Rect(0, 0, 100, 100)
	ps[2].Base().target = image.Rect(50, 50, 150, 150)
	ps[1].Base().target = image.Rect(0, 0, 300, 300)
	if i := a.paneAt(image.Pt(60, 60)); i != 0 {
		t.Errorf("paneAt: %d, want 0", i)
	}
	if i := a.paneAt(image.Pt(200, 200)); i != 1 {
		t.Errorf("paneAt: %d, want 1 (the tile)", i)
	}
}

func TestFocusLayerRoundTrip(t *testing.T) {
	a, ps := testApp(false, false, true)
	a.setFocus(1)
	a.focusLayer()
	if a.focused() != ps[2] {
		t.Fatalf("focus %d, want the floating pane", a.focus)
	}
	a.focusLayer()
	if a.focused() != ps[1] {
		t.Fatalf("focus %d, want tile 2 (the last focused one)", a.focus)
	}
}

func TestCloseFloatReturnsToTile(t *testing.T) {
	a, ps := testApp(false, false, true)
	a.setFocus(1)
	a.setFocus(2)
	a.closePane(2)
	if a.focused() != ps[1] {
		t.Errorf("after closing the floating pane, focus %d, want tile 2", a.focus)
	}
	// Closing a tile keeps the focus on a tile, not on a floating pane.
	a, ps = testApp(false, true, false)
	a.focus = 0
	a.closePane(0)
	if a.focused() != ps[2] {
		t.Errorf("after closing a tile, focus %d, want the other tile", a.focus)
	}
	// Closing a pane before the focused pane does not move the focus.
	a, ps = testApp(false, false, false)
	a.focus = 2
	a.closePane(0)
	if a.focused() != ps[2] {
		t.Errorf("focus %d, want the same pane", a.focus)
	}
}

func TestAddPaneParksTileNotFloat(t *testing.T) {
	a, ps := testApp(false, false, false, false, true)
	a.setFocus(1)
	a.setFocus(4) // focus on the floating pane, tiles full
	n := &fakePane{}
	a.addPane(n)
	if ps[1].(*fakePane).closed || !isParked(ps[1]) || isParked(ps[4]) {
		t.Error("the last focused tile must go to the garage without being closed, never the floating pane")
	}
	// The new tile takes its place (and its number), the garage is at the end.
	want := []Pane{ps[0], n, ps[2], ps[3], ps[4], ps[1]}
	if !slices.Equal(a.panes, want) || a.focused() != n {
		t.Errorf("panes %v, focus %d", ids(a.panes), a.focus)
	}
}

func ids(ps []Pane) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = p.Base().id
	}
	return out
}

func TestZoomTileSurvivesFloatFocus(t *testing.T) {
	a, ps := testApp(false, false, true)
	a.setFocus(1)
	a.do("pane.zoom")
	a.focusLayer() // to the floating pane: the tile stays zoomed
	if !a.zoomed || a.zoomFloat != nil || a.activeTile() != ps[1] {
		t.Fatalf("zoomed=%v zoomFloat=%v", a.zoomed, a.zoomFloat)
	}
	a.do("pane.zoom") // zoom of the floating pane, separate
	if !a.zoomed || a.zoomFloat != ps[2] {
		t.Fatalf("zoomed=%v zoomFloat=%v", a.zoomed, a.zoomFloat)
	}
	a.focusLayer() // the floating pane loses the focus: it goes back to its spot
	if a.zoomFloat != nil || !a.zoomed || a.focused() != ps[1] {
		t.Errorf("zoomFloat=%v zoomed=%v focus=%d", a.zoomFloat, a.zoomed, a.focus)
	}
}

func TestAddPaneUnmaximizesFloat(t *testing.T) {
	a, ps := testApp(false, true)
	a.setFocus(1)
	a.do("pane.zoom")
	if a.zoomFloat != ps[1] {
		t.Fatal("the floating pane should be maximized")
	}
	a.addPane(&fakePane{}) // otherwise the new pane would have the focus without being visible
	if a.zoomFloat != nil {
		t.Error("a new pane must cancel the maximize")
	}
}

func TestFocusedFloatRaisedAgain(t *testing.T) {
	a, ps := testApp(false, true, true)
	a.setFocus(1)
	a.raise(ps[2]) // the other floating pane goes on top, the focus does not move
	a.setFocus(1)  // click on the visible part
	if ps[1].Base().z != a.zTop {
		t.Error("the clicked focused floating pane must come back up")
	}
}

func TestHintForSkipsShadowedCombo(t *testing.T) {
	// On Linux, mod is ctrl+shift: mod+shift+f is the same as mod+f.
	a := &App{cfg: DefaultConfig(), mod: ModCtrl | ModShift}
	a.buildBindings()
	if got, want := a.hintFor("pane.float"), a.leader.String()+" shift+f"; got != want {
		t.Errorf("hint %q, want %q", got, want)
	}
	// The first combo wins: mod+f stays the zoom, mod+h the focus.
	for combo, want := range map[string]string{"mod+f": "pane.zoom", "mod+h": "focus.left", "mod+g": "layout.next", "mod+n": "notify.next"} {
		c, _ := ParseCombo(combo, a.mod)
		if got := a.bindings[c]; got != want {
			t.Errorf("Linux %s: %q, want %q", combo, got, want)
		}
	}
	a.mod = ModMeta
	a.buildBindings()
	if got := a.hintFor("pane.float"); got != (Combo{Mods: ModMeta | ModShift, Name: "f"}).String() {
		t.Errorf("hint macOS %q", got)
	}
}

func TestFocusBorderAlone(t *testing.T) {
	a, ps := testApp(false, false)
	a.focus = 0
	if !a.focusBorderShown() {
		t.Fatal("two panes on screen: the focus border marks the focused pane")
	}
	a.park(ps[1])
	if a.focusBorderShown() {
		t.Error("alone on screen, the focused pane has no focus border")
	}
	a.cfg.SoloBorder = true
	if !a.focusBorderShown() {
		t.Error("focus_border_alone keeps the focus border")
	}
	// In monocle, the other tiles are hidden: it is alone too.
	a, ps = testApp(false, false)
	a.focus = 0
	ps[1].Base().hidden = true
	if a.focusBorderShown() {
		t.Error("monocle: a single pane shown, no focus border")
	}
}
