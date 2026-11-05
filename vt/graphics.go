package vt

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"errors"
	"image"
	"image/png"
	"io"
	"math"
	"slices"
	"sync/atomic"
)

const (
	maxImageBytes = 32 << 20
	maxImages     = 512
	maxImageDim   = 10000
	maxPlacements = 4096
)

var (
	errBadF   = errors.New("bad file or medium")
	errBadPNG = errors.New("bad png")
	errInval  = errors.New("invalid argument")
)

// Image holds an image's metadata and raw pixel or PNG data.
type Image struct {
	ID, Number uint32
	Format     uint8 // 24, 32 or 100
	W, H       int
	Data       []byte // after inflate zlib
	Gen        uint64
}

type chunkTransfer struct {
	cmd  gcmd
	data []byte
}

type graphics struct {
	seq        atomic.Uint64
	gen        uint64
	nextID     uint32
	images     map[uint32]*Image
	order      []uint32 // insertion order for eviction
	totalBytes int

	active *chunkTransfer

	placements []*placement    // in creation order
	refs       map[uint32]int  // placements per image
	transient  map[uint32]bool // images sent with a=T
	altBase    int64           // anchor of the alternate screen's first row
	lowMain    int64           // no main screen anchor is lower (may be stale low)
	lowAlt     int64           // same for the alternate screen
}

func newGraphics() *graphics {
	g := &graphics{nextID: 1}
	g.reset()
	return g
}

func (g *graphics) reset() {
	g.images = make(map[uint32]*Image)
	g.order = nil
	g.totalBytes = 0
	g.active = nil
	g.placements = nil
	g.refs = make(map[uint32]int)
	g.transient = make(map[uint32]bool)
	g.altBase = 0
	g.lowMain, g.lowAlt = math.MaxInt64, math.MaxInt64
	g.seq.Add(1)
}

func (g *graphics) allocID() uint32 {
	for {
		id := g.nextID
		g.nextID++
		if id == 0 {
			continue
		}
		if _, exists := g.images[id]; !exists {
			return id
		}
	}
}

func (g *graphics) touchOrder(id uint32) {
	for i, v := range g.order {
		if v == id {
			g.order = append(g.order[:i], g.order[i+1:]...)
			g.order = append(g.order, id)
			break
		}
	}
}

func (g *graphics) hasPlacements(id uint32) bool { return g.refs[id] > 0 }

// removeImageAt frees an image and drops its placements.
func (g *graphics) removeImageAt(idx int, id uint32) {
	g.order = append(g.order[:idx], g.order[idx+1:]...)
	if img, ok := g.images[id]; ok {
		g.totalBytes -= len(img.Data)
		delete(g.images, id)
		g.seq.Add(1)
	}
	delete(g.transient, id)
	if g.refs[id] > 0 {
		g.removePlacements(func(p *placement) bool { return p.ImageID == id })
	}
}

func (g *graphics) removeImage(id uint32) {
	if i := slices.Index(g.order, id); i >= 0 {
		g.removeImageAt(i, id)
	}
}

func (g *graphics) evictOldest() bool {
	// First pass: evict oldest image without placements.
	for i, id := range g.order {
		if !g.hasPlacements(id) {
			g.removeImageAt(i, id)
			return true
		}
	}
	// Second pass: evict oldest image even if it has placements.
	if len(g.order) > 0 {
		g.removeImageAt(0, g.order[0])
		return true
	}
	return false
}

func (g *graphics) evictOldestExcluding(excludeID uint32) bool {
	for i, id := range g.order {
		if id != excludeID && !g.hasPlacements(id) {
			g.removeImageAt(i, id)
			return true
		}
	}
	for i, id := range g.order {
		if id != excludeID {
			g.removeImageAt(i, id)
			return true
		}
	}
	return false
}

func (g *graphics) processPayload(c gcmd) ([]byte, int, int, uint8, error) {
	if c.t != 'd' {
		return nil, 0, 0, 0, errBadF
	}

	data := c.payload
	if c.o == 'z' {
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, 0, 0, 0, errInval
		}
		decompressed, err := io.ReadAll(io.LimitReader(zr, maxImageBytes+1))
		_ = zr.Close()
		if err != nil || len(decompressed) > maxImageBytes {
			return nil, 0, 0, 0, errInval
		}
		data = decompressed
	} else if c.o != 0 {
		return nil, 0, 0, 0, errInval
	}

	switch c.f {
	case 100: // PNG
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, 0, 0, 0, errBadPNG
		}
		w, h := cfg.Width, cfg.Height
		if w <= 0 || h <= 0 || w > maxImageDim || h > maxImageDim {
			return nil, 0, 0, 0, errInval
		}
		return data, w, h, 100, nil

	case 24: // RGB
		w, h := c.s, c.v
		if w <= 0 || h <= 0 || w > maxImageDim || h > maxImageDim {
			return nil, 0, 0, 0, errInval
		}
		if len(data) != w*h*3 {
			return nil, 0, 0, 0, errInval
		}
		return data, w, h, 24, nil

	case 32: // RGBA
		w, h := c.s, c.v
		if w <= 0 || h <= 0 || w > maxImageDim || h > maxImageDim {
			return nil, 0, 0, 0, errInval
		}
		if len(data) != w*h*4 {
			return nil, 0, 0, 0, errInval
		}
		return data, w, h, 32, nil

	default:
		return nil, 0, 0, 0, errInval
	}
}

func (g *graphics) query(t *Terminal, c gcmd) {
	_, _, _, _, err := g.processPayload(c)
	if err == errBadF {
		t.gReply(c, "EBADF")
		return
	}
	if err == errBadPNG {
		t.gReply(c, "EBADPNG")
		return
	}
	if err != nil {
		t.gReply(c, "EINVAL")
		return
	}
	t.gReply(c, "OK")
}

func (g *graphics) transmit(t *Terminal, c gcmd) {
	if c.m == 1 {
		if g.active == nil {
			if len(c.payload) > maxImageBytes {
				t.gReply(c, "EINVAL")
				return
			}
			g.active = &chunkTransfer{
				cmd:  c,
				data: append([]byte(nil), c.payload...),
			}
		} else {
			if len(g.active.data)+len(c.payload) > maxImageBytes {
				cmd := g.active.cmd
				if c.q != 0 {
					cmd.q = c.q
				}
				g.active = nil
				t.gReply(cmd, "EINVAL")
				return
			}
			g.active.data = append(g.active.data, c.payload...)
			if c.q != 0 {
				g.active.cmd.q = c.q
			}
		}
		return
	}

	// Final chunk (m == 0 or single-chunk)
	if g.active != nil {
		if len(g.active.data)+len(c.payload) > maxImageBytes {
			cmd := g.active.cmd
			if c.q != 0 {
				cmd.q = c.q
			}
			g.active = nil
			t.gReply(cmd, "EINVAL")
			return
		}
		g.active.data = append(g.active.data, c.payload...)
		if c.q != 0 {
			g.active.cmd.q = c.q
		}
		c = g.active.cmd
		c.payload = g.active.data
		g.active = nil
	}

	data, w, h, format, err := g.processPayload(c)
	if err == errBadF {
		t.gReply(c, "EBADF")
		return
	}
	if err == errBadPNG {
		t.gReply(c, "EBADPNG")
		return
	}
	if err != nil {
		t.gReply(c, "EINVAL")
		return
	}

	if len(data) > maxImageBytes {
		t.gReply(c, "EINVAL")
		return
	}

	hadID := c.i != 0
	hadNum := c.I != 0
	if !hadID && hadNum {
		c.i = g.allocID()
	}

	// Replacement of an existing image ID
	if existing, ok := g.images[c.i]; ok && hadID {
		g.totalBytes -= len(existing.Data)
		g.totalBytes += len(data)
		existing.Data = data
		existing.Format = format
		existing.W = w
		existing.H = h
		existing.Number = c.I
		g.gen++
		existing.Gen = g.gen
		g.touchOrder(c.i)
		for (g.totalBytes > maxImageBytes || len(g.images) > maxImages) && len(g.order) > 1 {
			if !g.evictOldestExcluding(c.i) {
				break
			}
		}
		// The new data replaces the image everywhere it was shown.
		g.removePlacements(func(p *placement) bool { return p.ImageID == c.i })
		g.setTransient(c)
		g.seq.Add(1)
		reply := "OK"
		if c.a == 'T' && t.place(c, existing) != nil {
			reply = "EINVAL"
		}
		t.gReply(c, reply)
		return
	}

	// Enforce quota before adding new image
	for (g.totalBytes+len(data) > maxImageBytes || len(g.images) >= maxImages) && len(g.order) > 0 {
		if !g.evictOldest() {
			break
		}
	}

	if c.i == 0 && !hadNum {
		c.i = g.allocID()
	}

	g.gen++
	img := &Image{
		ID:     c.i,
		Number: c.I,
		Format: format,
		W:      w,
		H:      h,
		Data:   data,
		Gen:    g.gen,
	}
	g.images[c.i] = img
	g.order = append(g.order, c.i)
	g.totalBytes += len(data)
	g.setTransient(c)
	g.seq.Add(1)

	reply := "OK"
	if c.a == 'T' && t.place(c, img) != nil {
		reply = "EINVAL"
	}
	if hadID || hadNum {
		t.gReply(c, reply)
	}
}

// setTransient records whether the image came with a=T: such an image is
// freed with its last placement when that one scrolls out of the history,
// is erased or leaves the screen.
func (g *graphics) setTransient(c gcmd) {
	if c.a == 'T' {
		g.transient[c.i] = true
	} else {
		delete(g.transient, c.i)
	}
}

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
	if len(g.placements) >= maxPlacements {
		oldest := g.placements[0]
		g.removePlacements(func(q *placement) bool { return q == oldest })
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
		g.lowMain, g.lowAlt = math.MaxInt64, math.MaxInt64
		for _, p := range kept {
			switch {
			case p.virtual:
			case p.alt:
				g.lowAlt = min(g.lowAlt, p.abs)
			default:
				g.lowMain = min(g.lowMain, p.abs)
			}
		}
	}
	return touched
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
