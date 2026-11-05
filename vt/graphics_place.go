package vt

import (
	"cmp"
	"image"
	"math"
	"slices"
)

// Placement is a direct placement of an image, as VisiblePlacements
// returns it.
type Placement struct {
	ImageID, ID uint32
	Row, Col    int // top left cell, in view coordinates: Row may be negative
	OffX, OffY  int // offset in pixels inside the top left cell
	Src         image.Rectangle
	Cols, Rows  int // cells covered
	Z           int32
}

// placement is a stored placement. Its Row is not kept: abs anchors it
// to a line, sb.total+y on the main screen and altBase+y on the alternate
// one, so that it scrolls with the text without being touched.
type placement struct {
	Placement
	abs     int64
	alt     bool
	virtual bool // U=1: shown through Unicode placeholders, not anchored
	natural bool // neither c nor r was specified
}

// freeing says what happens to the images whose placements were removed.
type freeing int

const (
	keepImages    freeing = iota
	freeTransient         // images sent with a=T and no longer placed
	freeUnplaced          // every image no longer placed
)

// VisiblePlacements appends to dst[:0] the direct placements of the
// active screen that cross the view scrolled back by offset lines (as for
// ViewLine), sorted by Z, then by creation order. Lock required.
func (t *Terminal) VisiblePlacements(offset int, dst []Placement) []Placement {
	dst = dst[:0]
	g := t.gfx
	if g == nil {
		return dst
	}
	alt := t.g == t.alt
	if alt {
		offset = 0
	}
	base := t.screenBase(alt)
	for _, p := range g.placements {
		// Virtual placements have no position of their own.
		if p.virtual || p.alt != alt {
			continue
		}
		row := int(p.abs-base) + offset
		if row+p.Rows <= 0 || row >= t.rows {
			continue
		}
		q := p.Placement
		q.Row = row
		dst = append(dst, q)
	}
	slices.SortStableFunc(dst, func(a, b Placement) int { return cmp.Compare(a.Z, b.Z) })
	return dst
}

// PlacementNatural reports whether the direct placement was created without
// explicit c or r dimensions (natural size). Lock required.
func (t *Terminal) PlacementNatural(imageID, placementID uint32) bool {
	if t.gfx == nil {
		return false
	}
	ps := t.gfx.placements
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		if !p.virtual && p.ImageID == imageID && (placementID == 0 || p.ID == placementID) {
			return p.natural
		}
	}
	return false
}

func (t *Terminal) screenBase(alt bool) int64 {
	if alt {
		return t.gfx.altBase
	}
	return t.sb.total
}

// cellSize returns the size of a cell in pixels, 8×16 when unknown.
func (t *Terminal) cellSize() (w, h int) {
	if t.pxW <= 0 || t.pxH <= 0 {
		return 8, 16
	}
	return max(1, t.pxW/t.cols), max(1, t.pxH/t.rows)
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// findImage returns the image a command names, by id or by number (the
// newest image with that number).
func (g *graphics) findImage(c gcmd) *Image {
	if c.i != 0 {
		return g.images[c.i]
	}
	if c.I != 0 {
		for i := len(g.order) - 1; i >= 0; i-- {
			if img := g.images[g.order[i]]; img.Number == c.I {
				return img
			}
		}
	}
	return nil
}

// placeCmd handles a=p.
func (t *Terminal) placeCmd(c gcmd) {
	img := t.gfx.findImage(c)
	if img == nil {
		t.gReply(c, "ENOENT:image not found")
		return
	}
	c.i = img.ID
	if t.place(c, img) != nil {
		t.gReply(c, "EINVAL")
		return
	}
	t.gReply(c, "OK")
}

// place puts img at the cursor with the keys of c, then moves the cursor
// past it unless C=1.
func (t *Terminal) place(c gcmd, img *Image) error {
	x0, y0 := max(c.x, 0), max(c.y, 0)
	w, h := c.w, c.h
	if w <= 0 {
		w = img.W - x0
	}
	if h <= 0 {
		h = img.H - y0
	}
	src := image.Rect(x0, y0, x0+w, y0+h).Intersect(image.Rect(0, 0, img.W, img.H))
	if src.Empty() {
		return errInval
	}
	cw, ch := t.cellSize()
	offX, offY := min(max(c.X, 0), cw-1), min(max(c.Y, 0), ch-1)
	cols, rows := min(max(c.c, 0), maxImageDim), min(max(c.r, 0), maxImageDim)
	nat := c.c == 0 && c.r == 0
	switch {
	case cols == 0 && rows == 0:
		cols, rows = ceilDiv(offX+src.Dx(), cw), ceilDiv(offY+src.Dy(), ch)
	case rows == 0: // keep the aspect ratio
		rows = ceilDiv(cols*cw*src.Dy(), src.Dx()*ch)
	case cols == 0:
		cols = ceilDiv(rows*ch*src.Dx(), src.Dy()*cw)
	}
	cols, rows = min(max(cols, 1), maxImageDim), min(max(rows, 1), maxImageDim)
	alt := t.g == t.alt
	p := &placement{
		Placement: Placement{
			ImageID: img.ID, ID: c.p, Col: t.cur.x,
			OffX: offX, OffY: offY, Src: src, Cols: cols, Rows: rows, Z: c.z,
		},
		abs:     t.screenBase(alt) + int64(t.cur.y),
		alt:     alt,
		virtual: c.U == 1,
		natural: nat,
	}
	t.gfx.addPlacement(p)
	if p.virtual || c.C == 1 {
		return nil
	}
	// The cursor ends on the last row covered, right after the image,
	// scrolling if the image goes past the bottom.
	for i := 1; i < rows; i++ {
		t.index()
	}
	t.cur.x = min(p.Col+cols, t.cols-1)
	t.cur.wrapNext = false
	return nil
}

// addPlacement stores p. It replaces the placement with the same image
// and id, and drops the oldest one past maxPlacements.
func (g *graphics) addPlacement(p *placement) {
	if p.ID != 0 {
		g.removePlacements(func(q *placement) bool { return q.ImageID == p.ImageID && q.ID == p.ID })
	}
	g.placements = append(g.placements, p)
	g.refs[p.ImageID]++
	if !p.virtual {
		if p.alt {
			g.lowAlt = min(g.lowAlt, p.abs)
		} else {
			g.lowMain = min(g.lowMain, p.abs)
		}
	}
	g.seq.Add(1)
	// After p is counted, so that its own image is never freed here.
	if len(g.placements) > maxPlacements {
		oldest := g.placements[0]
		g.dropPlacements(func(q *placement) bool { return q == oldest }, freeTransient)
	}
}

// removePlacements drops the placements match selects and returns the
// images that lost one.
func (g *graphics) removePlacements(match func(*placement) bool) (touched []uint32) {
	kept := g.placements[:0]
	for _, p := range g.placements {
		if !match(p) {
			kept = append(kept, p)
			continue
		}
		if g.refs[p.ImageID]--; g.refs[p.ImageID] <= 0 {
			delete(g.refs, p.ImageID)
		}
		if !slices.Contains(touched, p.ImageID) {
			touched = append(touched, p.ImageID)
		}
	}
	clear(g.placements[len(kept):])
	g.placements = kept
	if touched != nil {
		g.seq.Add(1)
		g.recomputeLow()
	}
	return touched
}

// recomputeLow sets lowMain and lowAlt from the anchors.
func (g *graphics) recomputeLow() {
	g.lowMain, g.lowAlt = math.MaxInt64, math.MaxInt64
	for _, p := range g.placements {
		switch {
		case p.virtual:
		case p.alt:
			g.lowAlt = min(g.lowAlt, p.abs)
		default:
			g.lowMain = min(g.lowMain, p.abs)
		}
	}
}

// dropPlacements removes placements, then the images f says to free.
func (g *graphics) dropPlacements(match func(*placement) bool, f freeing, ids ...uint32) {
	ids = append(g.removePlacements(match), ids...)
	if f == keepImages {
		return
	}
	for _, id := range ids {
		if g.refs[id] == 0 && (f == freeUnplaced || g.transient[id]) {
			g.removeImage(id)
		}
	}
}

// prunePlacements drops the placements whose anchor line no longer
// exists: gone from the history, off the alternate screen, or below the
// screen.
func (t *Terminal) prunePlacements() {
	g := t.gfx
	if g == nil || len(g.placements) == 0 {
		return
	}
	mainLo, mainHi := t.sb.total-int64(t.sb.Len()), t.sb.total+int64(t.rows)
	altLo, altHi := g.altBase, g.altBase+int64(t.rows)
	g.dropPlacements(func(p *placement) bool {
		switch {
		case p.virtual:
			return false
		case p.alt:
			return p.abs < altLo || p.abs >= altHi
		}
		return p.abs < mainLo || p.abs >= mainHi
	}, freeTransient)
}

// scrollPlacements follows a scroll of lines [top, bot] by n lines, up
// when n > 0 and down when n < 0. Only full screen scrolls move the
// placements: on the main screen they move by themselves, since the
// lines that leave go to the history.
func (t *Terminal) scrollPlacements(top, bot, n int) {
	g := t.gfx
	if g == nil || len(g.placements) == 0 {
		return
	}
	alt := t.g == t.alt
	full := top == 0 && bot == t.rows-1
	switch {
	case n > 0 && full && alt:
		g.altBase += int64(n)
	case n > 0 && full:
	case n > 0 && top == 0 && !alt:
		// The region's lines went to the history: keep the screen's
		// placements on their rows.
		screen := t.sb.total - int64(n)
		for _, p := range g.placements {
			if !p.virtual && !p.alt && p.abs >= screen {
				p.abs += int64(n)
			}
		}
	case n < 0 && full:
		base := t.screenBase(alt)
		for _, p := range g.placements {
			if !p.virtual && p.alt == alt && p.abs >= base {
				p.abs -= int64(n)
			}
		}
		t.prunePlacements()
		return
	default:
		return
	}
	if g.lowMain < t.sb.total-int64(t.sb.Len()) || g.lowAlt < g.altBase {
		t.prunePlacements()
	}
}

// erasePlacements drops the placements of the active screen that cross
// the screen (ED 2).
func (t *Terminal) erasePlacements() {
	g := t.gfx
	if g == nil || len(g.placements) == 0 {
		return
	}
	alt := t.g == t.alt
	base := t.screenBase(alt)
	g.dropPlacements(func(p *placement) bool {
		row := int(p.abs - base)
		return !p.virtual && p.alt == alt && row+p.Rows > 0 && row < t.rows
	}, freeTransient)
}

// clearAltPlacements drops the direct placements of the alternate screen,
// when it is cleared.
func (t *Terminal) clearAltPlacements() {
	if g := t.gfx; g != nil && len(g.placements) > 0 {
		g.dropPlacements(func(p *placement) bool { return p.alt && !p.virtual }, freeTransient)
	}
}

// resizePlacements follows a resize. The alternate screen moved up by
// altShift lines; its placements now off the screen are dropped.
func (t *Terminal) resizePlacements(altShift int) {
	g := t.gfx
	if g == nil {
		return
	}
	g.altBase += int64(altShift)
	if len(g.placements) == 0 {
		return
	}
	// The reflow moved the main screen's anchors.
	g.recomputeLow()
	g.dropPlacements(func(p *placement) bool { return p.alt && !p.virtual && p.Col >= t.cols }, freeTransient)
	t.prunePlacements()
}

// deleteCmd handles a=d. An uppercase d also frees the images left
// without a placement.
func (t *Terminal) deleteCmd(c gcmd) {
	g := t.gfx
	d := c.d
	if d == 0 {
		d = 'a'
	}
	f := keepImages
	if d >= 'A' && d <= 'Z' {
		f = freeUnplaced
	}
	alt := t.g == t.alt
	base := t.screenBase(alt)
	direct := func(p *placement) bool { return !p.virtual && p.alt == alt }
	row := func(p *placement) int { return int(p.abs - base) }
	covers := func(p *placement, x, y int) bool {
		r := row(p)
		return direct(p) && x >= p.Col && x < p.Col+p.Cols && y >= r && y < r+p.Rows
	}
	var match func(*placement) bool
	var ids []uint32
	switch d | 0x20 {
	case 'a': // visible, or below the view
		match = func(p *placement) bool { return direct(p) && row(p)+p.Rows > 0 }
	case 'i', 'n':
		img := g.findImage(gcmd{i: c.i, I: c.I})
		if d|0x20 == 'i' && c.i == 0 || d|0x20 == 'n' && c.I == 0 || img == nil {
			return
		}
		ids = []uint32{img.ID}
		match = func(p *placement) bool { return p.ImageID == img.ID && (c.p == 0 || p.ID == c.p) }
	case 'r':
		if c.x > c.y {
			return
		}
		for id := range g.images {
			if int(id) >= c.x && int(id) <= c.y {
				ids = append(ids, id)
			}
		}
		match = func(p *placement) bool { return int(p.ImageID) >= c.x && int(p.ImageID) <= c.y }
	case 'c':
		match = func(p *placement) bool { return covers(p, t.cur.x, t.cur.y) }
	case 'p':
		match = func(p *placement) bool { return covers(p, c.x-1, c.y-1) }
	case 'q':
		match = func(p *placement) bool { return covers(p, c.x-1, c.y-1) && p.Z == c.z }
	case 'x':
		match = func(p *placement) bool { return direct(p) && c.x-1 >= p.Col && c.x-1 < p.Col+p.Cols }
	case 'y':
		match = func(p *placement) bool { return direct(p) && c.y-1 >= row(p) && c.y-1 < row(p)+p.Rows }
	case 'z':
		match = func(p *placement) bool { return direct(p) && p.Z == c.z }
	default: // f/F: animation frames, not supported
		return
	}
	g.dropPlacements(match, f, ids...)
}
