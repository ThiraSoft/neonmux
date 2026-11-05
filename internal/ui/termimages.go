package ui

import (
	"bytes"
	"image"
	"image/png"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

type imgKey struct {
	id  uint32
	gen uint64
}

type cachedImg struct {
	eb   *ebiten.Image
	w, h int
}

type phItem struct {
	imgID, pID   uint32
	col, y       int
	phRow, phCol int
}

type vpKey struct {
	imgID, pID uint32
}

type vpSize struct {
	cols, rows int
}

type drawOp struct {
	img     *ebiten.Image
	src     image.Rectangle
	dst     image.Rectangle
	filter  ebiten.Filter
	natural bool
}

type termImages struct {
	cache        map[imgKey]*cachedImg
	failed       map[imgKey]bool
	activeGens   map[uint32]uint64
	needed       map[uint32]bool
	vps          map[vpKey]vpSize
	placements   []vt.Placement
	placeholders []phItem
	toDecode     []vt.Image
	ops          []drawOp
	buf          []vt.Cell
	lastSeq      uint64
	lastGfxSeq   uint64
	lastOffset   int
	prepared     bool
}

func (ti *termImages) dispose() {
	for k, c := range ti.cache {
		if c.eb != nil {
			c.eb.Deallocate()
		}
		delete(ti.cache, k)
	}
	clear(ti.failed)
	clear(ti.activeGens)
	ti.ops = nil
	ti.prepared = false
	ti.lastSeq = 0
	ti.lastGfxSeq = 0
}

func decodeImage(img vt.Image) (image.Image, error) {
	switch img.Format {
	case 100: // PNG
		return png.Decode(bytes.NewReader(img.Data))
	case 24: // RGB
		rgba := image.NewRGBA(image.Rect(0, 0, img.W, img.H))
		src, dst := img.Data, rgba.Pix
		n := min(len(src)/3, img.W*img.H)
		for i := 0; i < n; i++ {
			dst[4*i] = src[3*i]
			dst[4*i+1] = src[3*i+1]
			dst[4*i+2] = src[3*i+2]
			dst[4*i+3] = 255
		}
		return rgba, nil
	case 32: // RGBA
		rgba := image.NewRGBA(image.Rect(0, 0, img.W, img.H))
		copy(rgba.Pix, img.Data)
		return rgba, nil
	default:
		return nil, image.ErrFormat
	}
}

func (ti *termImages) prepare(t *vt.Terminal, offset int) {
	t.Lock()
	defer t.Unlock()

	cols, rows := t.Size()
	ti.placements = t.VisiblePlacements(offset, ti.placements[:0])

	if ti.vps == nil {
		ti.vps = make(map[vpKey]vpSize)
	} else {
		clear(ti.vps)
	}
	if ti.needed == nil {
		ti.needed = make(map[uint32]bool)
	} else {
		clear(ti.needed)
	}
	ti.placeholders = ti.placeholders[:0]

	for _, p := range ti.placements {
		ti.needed[p.ImageID] = true
	}

	for y := 0; y < rows; y++ {
		line, next := t.ViewLineBuf(y, offset, ti.buf)
		ti.buf = next
		n := min(len(line), cols)
		for x := 0; x < n; x++ {
			c := line[x]
			imgID, pID, r, col, ok := c.Placeholder()
			if !ok {
				continue
			}
			ti.needed[imgID] = true
			ti.placeholders = append(ti.placeholders, phItem{
				imgID: imgID, pID: pID,
				col: x, y: y,
				phRow: r, phCol: col,
			})
			k := vpKey{imgID, pID}
			if _, exists := ti.vps[k]; !exists {
				vc, vr, _ := t.VirtualPlacement(imgID, pID)
				ti.vps[k] = vpSize{cols: vc, rows: vr}
			}
		}
	}

	if ti.activeGens == nil {
		ti.activeGens = make(map[uint32]uint64)
	} else {
		clear(ti.activeGens)
	}
	ti.toDecode = ti.toDecode[:0]

	for id := range ti.needed {
		if img, ok := t.Image(id); ok {
			ti.activeGens[id] = img.Gen
			key := imgKey{id: id, gen: img.Gen}
			if (ti.cache == nil || ti.cache[key] == nil) && (ti.failed == nil || !ti.failed[key]) {
				imgCopy := img
				imgCopy.Data = bytes.Clone(img.Data)
				ti.toDecode = append(ti.toDecode, imgCopy)
			}
		}
	}
}

func (ti *termImages) syncCache() {
	if ti.cache == nil {
		ti.cache = make(map[imgKey]*cachedImg)
	}
	if ti.failed == nil {
		ti.failed = make(map[imgKey]bool)
	}
	for _, img := range ti.toDecode {
		key := imgKey{id: img.ID, gen: img.Gen}
		raw, err := decodeImage(img)
		if err != nil {
			ti.failed[key] = true
			continue
		}
		eb := ebiten.NewImageFromImage(raw)
		ti.cache[key] = &cachedImg{eb: eb, w: raw.Bounds().Dx(), h: raw.Bounds().Dy()}
	}

	for k, c := range ti.cache {
		gen, active := ti.activeGens[k.id]
		if !active || k.gen != gen {
			if c.eb != nil {
				c.eb.Deallocate()
			}
			delete(ti.cache, k)
		}
	}
	for k := range ti.failed {
		gen, active := ti.activeGens[k.id]
		if !active || k.gen != gen {
			delete(ti.failed, k)
		}
	}
}

func (ti *termImages) findCached(id uint32) *cachedImg {
	if ti.activeGens != nil {
		if gen, ok := ti.activeGens[id]; ok {
			return ti.cache[imgKey{id, gen}]
		}
	}
	return nil
}

func (ti *termImages) computeOps(origin image.Point, cellW, cellH int) []drawOp {
	ti.ops = ti.ops[:0]
	for _, p := range ti.placements {
		c := ti.findCached(p.ImageID)
		if c == nil || c.eb == nil {
			continue
		}
		src := p.Src
		if src.Empty() {
			src = c.eb.Bounds()
		}
		px := origin.X + p.Col*cellW + p.OffX
		py := origin.Y + p.Row*cellH + p.OffY
		dstW, dstH := p.Cols*cellW, p.Rows*cellH
		if p.Natural {
			dstW = src.Dx()
			dstH = src.Dy()
		}
		filter := ebiten.FilterLinear
		if dstW == src.Dx() && dstH == src.Dy() {
			filter = ebiten.FilterNearest
		}
		ti.ops = append(ti.ops, drawOp{
			img:     c.eb,
			src:     src,
			dst:     image.Rect(px, py, px+dstW, py+dstH),
			filter:  filter,
			natural: p.Natural,
		})
	}

	for _, ph := range ti.placeholders {
		c := ti.findCached(ph.imgID)
		if c == nil || c.eb == nil {
			continue
		}
		b := c.eb.Bounds()
		vp := ti.vps[vpKey{ph.imgID, ph.pID}]
		vpCols, vpRows := vp.cols, vp.rows
		if vpCols <= 0 {
			vpCols = (b.Dx() + cellW - 1) / cellW
		}
		if vpRows <= 0 {
			vpRows = (b.Dy() + cellH - 1) / cellH
		}
		srcRect, dstRect := vt.PlaceholderSlice(b.Dx(), b.Dy(), vpCols, vpRows, cellW, cellH, ph.phRow, ph.phCol)
		if srcRect.Empty() || dstRect.Empty() {
			continue
		}
		cellX := origin.X + ph.col*cellW + dstRect.Min.X
		cellY := origin.Y + ph.y*cellH + dstRect.Min.Y
		filter := ebiten.FilterLinear
		if dstRect.Dx() == srcRect.Dx() && dstRect.Dy() == srcRect.Dy() {
			filter = ebiten.FilterNearest
		}
		ti.ops = append(ti.ops, drawOp{
			img:    c.eb,
			src:    srcRect,
			dst:    image.Rect(cellX, cellY, cellX+dstRect.Dx(), cellY+dstRect.Dy()),
			filter: filter,
		})
	}
	return ti.ops
}

func (ti *termImages) draw(dst *ebiten.Image, t *vt.Terminal, offset int, origin image.Point, cellW, cellH int, syncing bool) {
	if t == nil || cellW <= 0 || cellH <= 0 {
		return
	}
	seq := t.Seq()
	gfxSeq := t.GraphicsSeq()
	if !syncing && (!ti.prepared || ti.lastSeq != seq || ti.lastGfxSeq != gfxSeq || ti.lastOffset != offset) {
		ti.prepare(t, offset)
		ti.syncCache()
		ti.lastSeq = seq
		ti.lastGfxSeq = gfxSeq
		ti.lastOffset = offset
		ti.prepared = true
	}

	ops := ti.computeOps(origin, cellW, cellH)
	for _, op := range ops {
		sub := op.img.SubImage(op.src).(*ebiten.Image)
		opts := &ebiten.DrawImageOptions{}
		opts.GeoM.Scale(float64(op.dst.Dx())/float64(op.src.Dx()), float64(op.dst.Dy())/float64(op.src.Dy()))
		opts.GeoM.Translate(float64(op.dst.Min.X), float64(op.dst.Min.Y))
		opts.Filter = op.filter
		dst.DrawImage(sub, opts)
	}
}
