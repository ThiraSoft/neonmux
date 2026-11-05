package ui

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"slices"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

type imgKey struct {
	id  uint32
	gen uint64
}

type cachedImg struct {
	eb  *ebiten.Image
	raw image.Image
}

type termImages struct {
	cache        map[imgKey]*cachedImg
	placements   []vt.Placement
	placeholders []phItem
	vps          map[vpKey]vpSize
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

func (ti *termImages) prepare(t *vt.Terminal, offset int) (
	placements []vt.Placement,
	placeholders []phItem,
	toDecode []vt.Image,
	vps map[vpKey]vpSize,
	activeIDs []uint32,
) {
	t.Lock()
	defer t.Unlock()

	cols, rows := t.Size()
	ti.placements = t.VisiblePlacements(offset, ti.placements[:0])
	placements = append([]vt.Placement(nil), ti.placements...)

	vps = make(map[vpKey]vpSize)
	needed := make(map[uint32]bool)

	for _, p := range placements {
		needed[p.ImageID] = true
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
			needed[imgID] = true
			placeholders = append(placeholders, phItem{
				imgID: imgID, pID: pID,
				col: x, y: y,
				phRow: r, phCol: col,
			})
			k := vpKey{imgID, pID}
			if _, exists := vps[k]; !exists {
				vc, vr, _ := t.VirtualPlacement(imgID, pID)
				vps[k] = vpSize{cols: vc, rows: vr}
			}
		}
	}

	for id := range needed {
		activeIDs = append(activeIDs, id)
		if img, ok := t.Image(id); ok {
			key := imgKey{id: id, gen: img.Gen}
			if ti.cache == nil || ti.cache[key] == nil {
				imgCopy := img
				imgCopy.Data = bytes.Clone(img.Data)
				toDecode = append(toDecode, imgCopy)
			}
		}
	}

	return
}

func (ti *termImages) syncCache(toDecode []vt.Image, activeIDs []uint32) {
	if ti.cache == nil {
		ti.cache = make(map[imgKey]*cachedImg)
	}
	for _, img := range toDecode {
		raw, err := decodeImage(img)
		if err != nil {
			continue
		}
		eb := ebiten.NewImageFromImage(raw)
		ti.cache[imgKey{id: img.ID, gen: img.Gen}] = &cachedImg{eb: eb, raw: raw}
	}

	for k, c := range ti.cache {
		if !slices.Contains(activeIDs, k.id) {
			if c.eb != nil {
				c.eb.Deallocate()
			}
			delete(ti.cache, k)
		}
	}
}

func (ti *termImages) findCached(id uint32) *cachedImg {
	var bestGen uint64
	var best *cachedImg
	for k, c := range ti.cache {
		if k.id == id && (best == nil || k.gen > bestGen) {
			bestGen = k.gen
			best = c
		}
	}
	return best
}

func (ti *termImages) draw(dst *ebiten.Image, t *vt.Terminal, offset int, origin image.Point, cellW, cellH int) {
	if t == nil || cellW <= 0 || cellH <= 0 {
		return
	}
	seq := t.Seq()
	gfxSeq := t.GraphicsSeq()
	if !ti.prepared || ti.lastSeq != seq || ti.lastGfxSeq != gfxSeq || ti.lastOffset != offset {
		placements, placeholders, toDecode, vps, activeIDs := ti.prepare(t, offset)
		ti.syncCache(toDecode, activeIDs)
		ti.placements = placements
		ti.placeholders = placeholders
		ti.vps = vps
		ti.lastSeq = seq
		ti.lastGfxSeq = gfxSeq
		ti.lastOffset = offset
		ti.prepared = true
	}

	for _, p := range ti.placements {
		c := ti.findCached(p.ImageID)
		if c == nil || c.eb == nil {
			continue
		}
		src := p.Src
		if src.Empty() {
			src = c.raw.Bounds()
		}
		px := origin.X + p.Col*cellW + p.OffX
		py := origin.Y + p.Row*cellH + p.OffY
		dstW, dstH := p.Cols*cellW, p.Rows*cellH
		if p.Cols == (p.OffX+src.Dx()+cellW-1)/cellW && p.Rows == (p.OffY+src.Dy()+cellH-1)/cellH {
			dstW = src.Dx()
			dstH = src.Dy()
		}
		sub := c.eb.SubImage(src).(*ebiten.Image)
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Scale(float64(dstW)/float64(src.Dx()), float64(dstH)/float64(src.Dy()))
		op.GeoM.Translate(float64(px), float64(py))
		if dstW == src.Dx() && dstH == src.Dy() {
			op.Filter = ebiten.FilterNearest
		} else {
			op.Filter = ebiten.FilterLinear
		}
		dst.DrawImage(sub, op)
	}

	for _, ph := range ti.placeholders {
		c := ti.findCached(ph.imgID)
		if c == nil || c.eb == nil {
			continue
		}
		b := c.raw.Bounds()
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
		sub := c.eb.SubImage(srcRect).(*ebiten.Image)
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Scale(float64(dstRect.Dx())/float64(srcRect.Dx()), float64(dstRect.Dy())/float64(srcRect.Dy()))
		op.GeoM.Translate(float64(cellX), float64(cellY))
		if dstRect.Dx() == srcRect.Dx() && dstRect.Dy() == srcRect.Dy() {
			op.Filter = ebiten.FilterNearest
		} else {
			op.Filter = ebiten.FilterLinear
		}
		dst.DrawImage(sub, op)
	}
}

func (ti *termImages) drawRGBA(dst *image.RGBA, t *vt.Terminal, offset int, origin image.Point, cellW, cellH int) {
	if t == nil || cellW <= 0 || cellH <= 0 {
		return
	}
	seq := t.Seq()
	gfxSeq := t.GraphicsSeq()
	if !ti.prepared || ti.lastSeq != seq || ti.lastGfxSeq != gfxSeq || ti.lastOffset != offset {
		placements, placeholders, toDecode, vps, activeIDs := ti.prepare(t, offset)
		ti.syncCache(toDecode, activeIDs)
		ti.placements = placements
		ti.placeholders = placeholders
		ti.vps = vps
		ti.lastSeq = seq
		ti.lastGfxSeq = gfxSeq
		ti.lastOffset = offset
		ti.prepared = true
	}

	for _, p := range ti.placements {
		c := ti.findCached(p.ImageID)
		if c == nil || c.raw == nil {
			continue
		}
		src := p.Src
		if src.Empty() {
			src = c.raw.Bounds()
		}
		px := origin.X + p.Col*cellW + p.OffX
		py := origin.Y + p.Row*cellH + p.OffY
		dstW, dstH := p.Cols*cellW, p.Rows*cellH
		if p.Cols == (p.OffX+src.Dx()+cellW-1)/cellW && p.Rows == (p.OffY+src.Dy()+cellH-1)/cellH {
			dstW = src.Dx()
			dstH = src.Dy()
		}
		drawScaled(dst, image.Rect(px, py, px+dstW, py+dstH), c.raw, src)
	}

	for _, ph := range ti.placeholders {
		c := ti.findCached(ph.imgID)
		if c == nil || c.raw == nil {
			continue
		}
		b := c.raw.Bounds()
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
		drawScaled(dst, image.Rect(cellX, cellY, cellX+dstRect.Dx(), cellY+dstRect.Dy()), c.raw, srcRect)
	}
}

func drawScaled(dst *image.RGBA, dstRect image.Rectangle, src image.Image, srcRect image.Rectangle) {
	dx, dy := dstRect.Dx(), dstRect.Dy()
	sx, sy := srcRect.Dx(), srcRect.Dy()
	if dx <= 0 || dy <= 0 || sx <= 0 || sy <= 0 {
		return
	}
	clip := dstRect.Intersect(dst.Bounds())
	if clip.Empty() {
		return
	}
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		syPos := srcRect.Min.Y + (y-dstRect.Min.Y)*sy/dy
		for x := clip.Min.X; x < clip.Max.X; x++ {
			sxPos := srcRect.Min.X + (x-dstRect.Min.X)*sx/dx
			c := src.At(sxPos, syPos)
			_, _, _, a := c.RGBA()
			if a == 0xffff {
				dst.Set(x, y, c)
			} else if a > 0 {
				draw.Draw(dst, image.Rect(x, y, x+1, y+1), image.NewUniform(c), image.Point{}, draw.Over)
			}
		}
	}
}
