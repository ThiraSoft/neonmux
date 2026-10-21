package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/shell"
)

// Floating panes live in the same a.panes slice as the tiles: numbers,
// focus, notifications and persistence stay shared. Only the floating flag
// takes them out of the tile layout, and they are drawn on top, in z
// order.

const (
	maxFloats  = 4
	floatAlpha = 0.97 // a floating pane almost fully hides what it covers
)

// floatGeom places a floating pane. X and Y locate the pane in the free
// space of the work area (0: against the left or top, 1: right or bottom),
// so a pane tucked into a corner stays there when the window is resized.
// W and H are in points (logical pixels).
type floatGeom struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func (g floatGeom) empty() bool { return g.W <= 0 || g.H <= 0 }

// floatCorners gives the position of each corner, in the order new
// floating panes fill them.
var floatCorners = []struct {
	name string
	x, y float64
}{{"br", 1, 1}, {"tr", 1, 0}, {"bl", 0, 1}, {"tl", 0, 0}, {"center", 0.5, 0.5}}

// parseCorner accepts tl, tr, bl, br, center and their long forms
// (top-left…).
func parseCorner(s string) (x, y float64, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	long := map[string]string{"top-left": "tl", "top-right": "tr", "bottom-left": "bl", "bottom-right": "br", "c": "center", "centre": "center"}
	if l, found := long[s]; found {
		s = l
	}
	for _, c := range floatCorners {
		if c.name == s {
			return c.x, c.y, true
		}
	}
	return 0, 0, false
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }

// edges names the edges of a floating pane grabbed with the mouse. edgeAll
// moves the whole pane.
type edges uint8

const (
	edgeLeft edges = 1 << iota
	edgeRight
	edgeTop
	edgeBottom
	edgeAll = edgeLeft | edgeRight | edgeTop | edgeBottom
)

// floatPlace computes the rectangle of a floating pane in area, without
// going below the minimum size mn or overflowing the area.
func floatPlace(g floatGeom, area image.Rectangle, mn image.Point, dsf float64) image.Rectangle {
	w := min(max(int(math.Round(g.W*dsf)), mn.X), max(area.Dx(), 1))
	h := min(max(int(math.Round(g.H*dsf)), mn.Y), max(area.Dy(), 1))
	x := area.Min.X + int(math.Round(clamp01(g.X)*float64(area.Dx()-w)))
	y := area.Min.Y + int(math.Round(clamp01(g.Y)*float64(area.Dy()-h)))
	return image.Rect(x, y, x+w, y+h)
}

// floatGeomOf is the inverse of floatPlace. A pane as wide as the area
// keeps the horizontal position of prev (it has nowhere to go), and the
// same goes for height.
func floatGeomOf(r, area image.Rectangle, dsf float64, prev floatGeom) floatGeom {
	frac := func(pos, free int, old float64) float64 {
		if free <= 0 {
			return old
		}
		return clamp01(float64(pos) / float64(free))
	}
	return floatGeom{
		X: frac(r.Min.X-area.Min.X, area.Dx()-r.Dx(), prev.X),
		Y: frac(r.Min.Y-area.Min.Y, area.Dy()-r.Dy(), prev.Y),
		W: float64(r.Dx()) / dsf,
		H: float64(r.Dy()) / dsf,
	}
}

// clampInto brings r back inside area by translating it.
func clampInto(r, area image.Rectangle) image.Rectangle {
	d := image.Point{}
	if r.Max.X > area.Max.X {
		d.X = area.Max.X - r.Max.X
	}
	if r.Min.X+d.X < area.Min.X {
		d.X = area.Min.X - r.Min.X
	}
	if r.Max.Y > area.Max.Y {
		d.Y = area.Max.Y - r.Max.Y
	}
	if r.Min.Y+d.Y < area.Min.Y {
		d.Y = area.Min.Y - r.Min.Y
	}
	return r.Add(d)
}

func absInt(v int) int { return max(v, -v) }

// dragRect applies a drag of d to r: a move if e is edgeAll, otherwise a
// resize of the edges e. An edge that comes within snap pixels of an edge
// of the area snaps to it.
func dragRect(r image.Rectangle, d image.Point, e edges, area image.Rectangle, mn image.Point, snap int) image.Rectangle {
	if e == edgeAll {
		r = clampInto(r.Add(d), area)
		var s image.Point
		switch {
		case absInt(r.Min.X-area.Min.X) <= snap:
			s.X = area.Min.X - r.Min.X
		case absInt(area.Max.X-r.Max.X) <= snap:
			s.X = area.Max.X - r.Max.X
		}
		switch {
		case absInt(r.Min.Y-area.Min.Y) <= snap:
			s.Y = area.Min.Y - r.Min.Y
		case absInt(area.Max.Y-r.Max.Y) <= snap:
			s.Y = area.Max.Y - r.Max.Y
		}
		return r.Add(s)
	}
	near := func(v, edge int) int {
		if absInt(v-edge) <= snap {
			return edge
		}
		return v
	}
	if e&edgeLeft != 0 {
		r.Min.X = min(near(max(r.Min.X+d.X, area.Min.X), area.Min.X), r.Max.X-mn.X)
	}
	if e&edgeRight != 0 {
		r.Max.X = max(near(min(r.Max.X+d.X, area.Max.X), area.Max.X), r.Min.X+mn.X)
	}
	if e&edgeTop != 0 {
		r.Min.Y = min(near(max(r.Min.Y+d.Y, area.Min.Y), area.Min.Y), r.Max.Y-mn.Y)
	}
	if e&edgeBottom != 0 {
		r.Max.Y = max(near(min(r.Max.Y+d.Y, area.Max.Y), area.Max.Y), r.Min.Y+mn.Y)
	}
	return r
}

// floatEdges returns the edges of r grabbed at pt: a band of grab pixels on
// both sides of the outline, wider near the corners so they are easy to
// catch. Zero: pt is inside, away from the edges.
func floatEdges(r image.Rectangle, pt image.Point, grab int) edges {
	if !pt.In(r.Inset(-grab)) {
		return 0
	}
	var e edges
	corner := 4 * grab
	switch {
	case pt.X < r.Min.X+grab:
		e |= edgeLeft
	case pt.X >= r.Max.X-grab:
		e |= edgeRight
	}
	switch {
	case pt.Y < r.Min.Y+grab:
		e |= edgeTop
	case pt.Y >= r.Max.Y-grab:
		e |= edgeBottom
	}
	switch {
	case e == edgeLeft || e == edgeRight:
		if pt.Y < r.Min.Y+corner {
			e |= edgeTop
		} else if pt.Y >= r.Max.Y-corner {
			e |= edgeBottom
		}
	case e == edgeTop || e == edgeBottom:
		if pt.X < r.Min.X+corner {
			e |= edgeLeft
		} else if pt.X >= r.Max.X-corner {
			e |= edgeRight
		}
	}
	return e
}

func edgesCursor(e edges) ebiten.CursorShapeType {
	switch e {
	case edgeAll:
		return ebiten.CursorShapeMove
	case edgeLeft, edgeRight:
		return ebiten.CursorShapeEWResize
	case edgeTop, edgeBottom:
		return ebiten.CursorShapeNSResize
	case edgeLeft | edgeTop, edgeRight | edgeBottom:
		return ebiten.CursorShapeNWSEResize
	case edgeRight | edgeTop, edgeLeft | edgeBottom:
		return ebiten.CursorShapeNESWResize
	}
	return ebiten.CursorShapeDefault
}

// floatDrag is a floating pane being moved or resized with the mouse.
type floatDrag struct {
	p     Pane
	edges edges
	from  image.Point     // mouse position when the drag started
	start image.Rectangle // pane rectangle when the drag started
}

// floatArea returns the area where floating panes go: the tile area, inset
// by a margin so a floating pane tucked into a corner stands out.
func (a *App) floatArea() image.Rectangle {
	gap, _, status, _ := a.metrics()
	return image.Rect(2*gap, 2*gap, a.w-2*gap, a.h-status-2*gap)
}

// minFloatSize: a header and at least 16 columns by 3 rows.
func (a *App) minFloatSize() image.Point {
	_, header, _, pad := a.metrics()
	return image.Pt(16*a.termFont.CellW+2*pad, header+3*a.termFont.CellH+2*pad)
}

func (a *App) floatRect(g floatGeom) image.Rectangle {
	return floatPlace(g, a.floatArea(), a.minFloatSize(), a.dsf)
}

// floatShadow is the offset of the drop shadow of floating panes.
func (a *App) floatShadow() int { return int(6 * a.px) }

// paneExtent returns everything drawing a pane can touch: the border glow,
// and the drop shadow for a floating pane.
func (a *App) paneExtent(p Pane, now time.Time) image.Rectangle {
	b := p.Base()
	r := b.rect(now).Inset(-int(3 * a.px))
	if b.floating {
		r.Max = r.Max.Add(image.Pt(a.floatShadow(), a.floatShadow()))
	}
	return r
}

// cellsToGeom converts a size in terminal cells (columns × rows) into a
// floating pane size.
func (a *App) cellsToGeom(cols, rows int) (w, h float64) {
	_, header, _, pad := a.metrics()
	f := a.termFont
	return float64(cols*f.CellW+2*pad) / a.dsf, float64(header+rows*f.CellH+2*pad) / a.dsf
}

// defaultFloatGeom: a bit more than a third of the area, in the first free
// corner (bottom right first).
func (a *App) defaultFloatGeom() floatGeom {
	area := a.floatArea()
	g := floatGeom{W: float64(area.Dx()) * 0.38 / a.dsf, H: float64(area.Dy()) * 0.4 / a.dsf}
	for _, c := range floatCorners {
		taken := false
		for _, p := range a.panes {
			if b := p.Base(); isFloat(p) && b.geom.X == c.x && b.geom.Y == c.y {
				taken = true
			}
		}
		if !taken {
			g.X, g.Y = c.x, c.y
			return g
		}
	}
	g.X, g.Y = 1, 1
	return g
}

// centerGeom: a large floating pane in the center of the area.
func (a *App) centerGeom() floatGeom {
	area := a.floatArea()
	return floatGeom{X: 0.5, Y: 0.5, W: float64(area.Dx()) * 0.7 / a.dsf, H: float64(area.Dy()) * 0.8 / a.dsf}
}

// roomBeside tells whether a pane can open without taking the place of a
// tile: a free tile, or room for one more floating pane.
func (a *App) roomBeside() bool {
	if a.countPanes(false) < a.cfg.MaxPanes || a.countPanes(true) < maxFloats {
		return true
	}
	a.toast("no room for a new pane: close one first", a.theme.Warn)
	return false
}

// floatIfFull lays p in the center when the tiles are full, instead of
// letting it send the active tile, maybe an agent at work, to the garage.
func (a *App) floatIfFull(p Pane) {
	if a.countPanes(false) >= a.cfg.MaxPanes {
		b := p.Base()
		b.floating, b.geom = true, a.centerGeom()
	}
}

// addBeside adds p without taking the place of a tile: when the tiles are
// full, it floats.
func (a *App) addBeside(p Pane) {
	if !a.roomBeside() {
		p.Close()
		return
	}
	a.floatIfFull(p)
	a.addPane(p)
}

// countPanes counts the tiles or the floating panes on screen. Neither the
// garage nor the visitor counts toward the limits.
func (a *App) countPanes(floating bool) int {
	n := 0
	for _, p := range a.panes {
		if b := p.Base(); !b.parked && p != a.visitor && b.floating == floating {
			n++
		}
	}
	return n
}

func isTile(p Pane) bool  { b := p.Base(); return !b.floating && !b.parked }
func isFloat(p Pane) bool { b := p.Base(); return b.floating && !b.parked }

// raise puts a floating pane above the others.
func (a *App) raise(p Pane) {
	a.zTop++
	p.Base().z = a.zTop
}

// drawOrder returns the visible panes in drawing order: the tiles, then the
// floating panes from lowest to highest.
func (a *App) drawOrder() []Pane {
	a.order = a.order[:0]
	for _, p := range a.panes {
		if !p.Base().hidden && isTile(p) {
			a.order = append(a.order, p)
		}
	}
	n := len(a.order)
	for _, p := range a.panes {
		if !p.Base().hidden && isFloat(p) {
			a.order = append(a.order, p)
		}
	}
	slices.SortStableFunc(a.order[n:], func(x, y Pane) int { return x.Base().z - y.Base().z })
	return a.order
}

// activeTile returns the tile that holds the focus of the tile plane: the
// focused pane if it is a tile, otherwise the last focused tile.
func (a *App) activeTile() Pane {
	if p := a.focused(); p != nil && isTile(p) {
		return p
	}
	if p := a.lastTile; p != nil && isTile(p) && slices.Contains(a.panes, p) {
		return p
	}
	for _, p := range a.panes {
		if isTile(p) {
			return p
		}
	}
	return nil
}

// activeFloat returns the focused floating pane, the last one that had the
// focus, or the one on top.
func (a *App) activeFloat() Pane {
	if p := a.focused(); p != nil && isFloat(p) {
		return p
	}
	if p := a.lastFloat; p != nil && isFloat(p) && slices.Contains(a.panes, p) {
		return p
	}
	var top Pane
	for _, p := range a.panes {
		if isFloat(p) && (top == nil || p.Base().z > top.Base().z) {
			top = p
		}
	}
	return top
}

// noteFocus remembers the pane about to lose the focus, to come back to it
// when switching between planes.
func (a *App) noteFocus() {
	if p := a.focused(); p != nil && !isParked(p) {
		if isFloat(p) {
			a.lastFloat = p
		} else {
			a.lastTile = p
		}
	}
}

// nearestTile returns the index of the tile closest to i (from i onward,
// then before it), or -1.
func (a *App) nearestTile(i int) int {
	for k := i; k < len(a.panes); k++ {
		if isTile(a.panes[k]) {
			return k
		}
	}
	for k := min(i, len(a.panes)) - 1; k >= 0; k-- {
		if isTile(a.panes[k]) {
			return k
		}
	}
	return -1
}

// neighborIn looks for the neighbor of the focused pane in a direction,
// among the panes keep accepts.
func (a *App) neighborIn(dir string, keep func(Pane) bool) int {
	if a.focused() == nil {
		return -1
	}
	rects := make([]image.Rectangle, len(a.panes))
	for i, p := range a.panes {
		if b := p.Base(); i == a.focus || (!b.hidden && keep(p)) {
			rects[i] = b.target
		}
	}
	return neighbor(rects, a.focus, dir)
}

// focusDir moves the focus in a direction. From a tile, only tiles count
// (a floating pane on top would skew the geometry). From a floating pane,
// another floating pane is tried first, then a tile.
func (a *App) focusDir(dir string) {
	p := a.focused()
	if p == nil {
		return
	}
	i := a.neighborIn(dir, isTile)
	if isFloat(p) {
		if j := a.neighborIn(dir, isFloat); j >= 0 {
			i = j
		}
	}
	if i >= 0 {
		a.setFocus(i)
	}
}

// focusLayer switches the focus between the tiles and the floating panes,
// going back to the last focused pane of each plane.
func (a *App) focusLayer() {
	var target Pane
	if p := a.focused(); p != nil && isFloat(p) {
		target = a.activeTile()
	} else if target = a.activeFloat(); target == nil {
		a.toast("no floating pane ("+a.hintFor("pane.float")+" creates one)", a.theme.Warn)
		return
	}
	if target != nil {
		a.setFocus(slices.Index(a.panes, target))
	}
}

// toggleFloat turns a tile into a floating pane, or the other way around. A
// pane that has floated before gets its last spot back.
func (a *App) toggleFloat(p Pane) {
	if p == nil {
		return
	}
	if p == a.visitor {
		a.keepVisitor()
		return
	}
	b := p.Base()
	if b.parked {
		return
	}
	if b.floating {
		if a.countPanes(false) >= a.cfg.MaxPanes {
			a.toast("no room left for a tile ("+itoa(a.cfg.MaxPanes)+" at most)", a.theme.Warn)
			return
		}
		b.floating = false
		if a.drag != nil && a.drag.p == p {
			a.drag = nil
		}
	} else {
		if !a.makeFloating(p) {
			return
		}
	}
	a.zoomed, a.zoomFloat = false, nil
	a.relayout()
}

// makeFloating makes p floating without redoing the layout.
func (a *App) makeFloating(p Pane) bool {
	b := p.Base()
	if b.floating || b.parked {
		return !b.parked
	}
	if a.countPanes(true) >= maxFloats {
		a.toast("already "+itoa(maxFloats)+" floating panes", a.theme.Warn)
		return false
	}
	if b.geom.empty() {
		b.geom = a.defaultFloatGeom()
	}
	// A zoomed tile that starts floating takes the zoom with it: otherwise
	// another tile would go fullscreen without being asked to.
	if a.zoomed && p == a.activeTile() {
		a.zoomed = false
	}
	b.floating = true
	a.raise(p)
	return true
}

// moveFloat tucks a floating pane into a corner (or the center). A tile is
// made floating first.
func (a *App) moveFloat(p Pane, x, y float64) {
	if p == nil || !a.makeFloating(p) {
		return
	}
	g := &p.Base().geom
	g.X, g.Y = x, y
	a.zoomFloat = nil
	a.relayout()
}

// snapFloat snaps a floating pane to the edge of the area in a direction:
// two presses (right then up) tuck it into a corner.
func (a *App) snapFloat(p Pane, dir string) {
	a.zoomFloat = nil
	g := &p.Base().geom
	switch dir {
	case "left":
		g.X = 0
	case "right":
		g.X = 1
	case "up":
		g.Y = 0
	case "down":
		g.Y = 1
	}
	a.relayout()
}

// floatResizeChar is the resize mode of a floating pane: h j k l for the
// size, H J K L for the position, y u b n for the corners (vim's
// diagonals), c for the center, = for the default size.
func (a *App) floatResizeChar(p Pane, r rune) {
	b := p.Base()
	g := b.geom
	area := a.floatArea()
	cw, ch := 2*a.termFont.CellW, a.termFont.CellH
	switch r {
	case 'h':
		g.W -= float64(cw) / a.dsf
	case 'l':
		g.W += float64(cw) / a.dsf
	case 'k':
		g.H -= float64(ch) / a.dsf
	case 'j':
		g.H += float64(ch) / a.dsf
	case 'H', 'J', 'K', 'L':
		d := map[rune]image.Point{'H': {-cw, 0}, 'L': {cw, 0}, 'K': {0, -ch}, 'J': {0, ch}}[r]
		g = floatGeomOf(clampInto(a.floatRect(g).Add(d), area), area, a.dsf, g)
	case 'y', 'u', 'b', 'n', 'c':
		corner := map[rune]string{'y': "tl", 'u': "tr", 'b': "bl", 'n': "br", 'c': "center"}[r]
		g.X, g.Y, _ = parseCorner(corner)
	case '=':
		def := a.defaultFloatGeom()
		g.W, g.H = def.W, def.H
	case 'q':
		a.mode = ""
		return
	default:
		return
	}
	// The size kept is the one that really fits in the area.
	fr := a.floatRect(g)
	g.W, g.H = float64(fr.Dx())/a.dsf, float64(fr.Dy())/a.dsf
	b.geom = g
	a.zoomFloat = nil
	a.relayout()
}

// mouseHit is what lies under the mouse.
type mouseHit struct {
	pane  int   // pane under the point (paneAt), -1 otherwise
	float int   // floating pane hit or grabbed by an edge, -1 otherwise
	edges edges // grabbed edges of that floating pane, zero if hit away from the edges
}

// hitAt looks in a single pass, from top to bottom of the drawing order,
// for the pane under pt and the floating pane whose edge is grabbed. The
// grab band extends past the floating pane: it can be grabbed by an edge
// without being the pane under the point. A maximized floating pane cannot
// be grabbed, and below the first tile there are no more floating panes.
func (a *App) hitAt(pt image.Point) mouseHit {
	h := mouseHit{pane: -1, float: -1}
	grab := int(4 * a.px)
	order := a.drawOrder()
	floats := true // still among the floating panes, with no answer for them yet
	for k := len(order) - 1; k >= 0 && (floats || h.pane < 0); k-- {
		p := order[k]
		r := p.Base().target
		if floats {
			if isTile(p) || p == a.zoomFloat {
				floats = false
			} else if e := floatEdges(r, pt, grab); e != 0 || pt.In(r) {
				h.float, h.edges = slices.Index(a.panes, p), e
				floats = false
			}
		}
		if h.pane < 0 && pt.In(r) {
			h.pane = slices.Index(a.panes, p)
		}
	}
	return h
}

// startDrag starts a drag. A maximized floating pane does not move: it
// already fills the whole area.
func (a *App) startDrag(p Pane, e edges, pt image.Point) {
	if a.zoomFloat == p {
		return
	}
	a.drag = &floatDrag{p: p, edges: e, from: pt, start: p.Base().target}
}

// dragTo follows the mouse during a drag: the pane follows with no animation.
func (a *App) dragTo(ctx *Ctx, pt image.Point) {
	d := a.drag
	if !isFloat(d.p) {
		a.drag = nil
		return
	}
	b := d.p.Base()
	area := a.floatArea()
	r := dragRect(d.start, pt.Sub(d.from), d.edges, area, a.minFloatSize(), int(8*a.px))
	if r == b.target {
		return
	}
	b.geom = floatGeomOf(r, area, a.dsf, b.geom)
	a.place(ctx, d.p, r, false)
	a.needRedraw = true
}

func (a *App) endDrag() {
	a.drag = nil
	a.layoutDirty = true
	a.needRedraw = true
}

// drawShadow draws the drop shadow of a floating pane: three layers offset
// down and to the right, lighter and lighter.
func (a *App) drawShadow(P *gfx.Painter, r image.Rectangle) {
	s := float32(a.floatShadow())
	black := color.RGBA{A: 255}
	for k, alpha := range []float32{0.22, 0.12, 0.07} {
		o := s * float32(k+1) / 3
		P.Rect(float32(r.Min.X)+o, float32(r.Min.Y)+o, float32(r.Dx()), float32(r.Dy()), black, alpha)
	}
}

// FloatOptions are the options of `neonmux float`.
type FloatOptions struct {
	Corner     string
	Cols, Rows int
	Cmd        []string
}

// ParseFloatArgs reads `[-c corner] [-s colsxrows] [--] [command…]`.
func ParseFloatArgs(args []string) (FloatOptions, error) {
	var o FloatOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, val, hasVal := strings.Cut(arg, "=")
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "--":
			o.Cmd = args[i+1:]
			return o, nil
		case "-c", "--corner":
			v, err := next()
			if err != nil {
				return o, err
			}
			if _, _, ok := parseCorner(v); !ok {
				return o, fmt.Errorf("unknown corner %q (tl, tr, bl, br or center)", v)
			}
			o.Corner = v
		case "-s", "--size":
			v, err := next()
			if err != nil {
				return o, err
			}
			c, r, ok := strings.Cut(strings.ToLower(v), "x")
			cols, err1 := strconv.Atoi(c)
			rows, err2 := strconv.Atoi(r)
			if !ok || err1 != nil || err2 != nil || cols < 1 || rows < 1 {
				return o, fmt.Errorf("invalid size %q (columns x rows, for example 60x15)", v)
			}
			o.Cols, o.Rows = cols, rows
		default:
			if strings.HasPrefix(arg, "-") {
				return o, fmt.Errorf("unknown option %q", arg)
			}
			o.Cmd = args[i:]
			return o, nil
		}
	}
	return o, nil
}

// floatRequest serves `neonmux float`. With no command or option, it
// toggles the pane that ran it between tile and floating; with options
// only, it makes it float at that spot; with a command, it opens it in a
// new floating pane.
func (a *App) floatRequest(p Pane, args []string, cwd string) {
	o, err := ParseFloatArgs(args)
	if err != nil {
		a.toast("neonmux float: "+err.Error(), a.theme.Warn)
		return
	}
	apply := func(p Pane) {
		b := p.Base()
		if b.geom.empty() {
			b.geom = a.defaultFloatGeom()
		}
		if x, y, ok := parseCorner(o.Corner); ok {
			b.geom.X, b.geom.Y = x, y
		}
		if o.Cols > 0 {
			b.geom.W, b.geom.H = a.cellsToGeom(o.Cols, o.Rows)
		}
	}
	if len(o.Cmd) == 0 {
		switch {
		case p == nil:
		case isParked(p):
			a.toast("neonmux float: this pane is in the garage ("+a.hintFor("agents.pick")+" to bring it out)", a.theme.Warn)
		case o.Corner == "" && o.Cols == 0:
			a.toggleFloat(p)
		case isFloat(p) || a.makeFloating(p):
			apply(p)
			a.zoomFloat = nil
			a.relayout()
		}
		return
	}
	if a.countPanes(true) >= maxFloats {
		a.toast("already "+itoa(maxFloats)+" floating panes", a.theme.Warn)
		return
	}
	// A single argument goes to the shell as is ("cd x && neonwave");
	// several are quoted one by one.
	line := o.Cmd[0]
	if len(o.Cmd) > 1 {
		quoted := make([]string, len(o.Cmd))
		for i, s := range o.Cmd {
			quoted[i] = shellQuote(s)
		}
		line = strings.Join(quoted, " ")
	}
	// The login shell gets back the terminal's PATH (~/.local/bin…).
	// exec would replace the shell at the first command of a sequence.
	if !strings.ContainsAny(line, ";&|\n") {
		line = "exec " + line
	}
	argv := []string{shell.DefaultShell(), "-l", "-i", "-c", line}
	t := NewTermPane(a.newID(), argv, cwd, a.paneEnv())
	if fields := strings.Fields(o.Cmd[0]); len(fields) > 0 {
		t.cmdName = filepath.Base(fields[0])
	}
	t.floating = true
	apply(t)
	a.addPane(t)
}
