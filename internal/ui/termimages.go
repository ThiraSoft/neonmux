package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sync"
	"sync/atomic"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

type imgKey struct {
	id  uint32
	gen uint64
}

// cachedImg is a decoded image on the GPU. w and h are the image's own
// size, which placements are expressed in; the texture may be smaller.
type cachedImg struct {
	eb   *ebiten.Image
	w, h int
}

// texRect brings r, in the image's pixels, to the texture's.
func (c *cachedImg) texRect(r image.Rectangle) image.Rectangle {
	b := c.eb.Bounds()
	if c.w <= 0 || c.h <= 0 || (b.Dx() == c.w && b.Dy() == c.h) {
		return r
	}
	sx := func(v int) int { return v * b.Dx() / c.w }
	sy := func(v int) int { return v * b.Dy() / c.h }
	out := image.Rect(sx(r.Min.X), sy(r.Min.Y), sx(r.Max.X), sy(r.Max.Y))
	if out.Dx() == 0 && r.Dx() > 0 {
		out.Max.X = min(out.Min.X+1, b.Max.X)
	}
	if out.Dy() == 0 && r.Dy() > 0 {
		out.Max.Y = min(out.Min.Y+1, b.Max.Y)
	}
	return out.Intersect(b)
}

// Limits of the window's image decoding. A program can send hundreds of
// small PNGs that announce 10000x10000 pixels each: decoded as they come,
// they would take tens of GB and freeze or crash the window.
const (
	maxTermImagePixels = 40_000_000 // per image, decoded at full size
	maxTermImageSide   = 4096       // long side on the GPU, scaled down beyond
	maxTermImageBytes  = 256 << 20  // textures of one pane, decoding included
)

var errTermImageTooLarge = errors.New("image too large")

// decodeSlots lets one image decode at a time in the whole window: the
// memory peak stays at one image.
var decodeSlots = make(chan struct{}, 1)

// decoded is the result of a decode, handed to the render thread.
type decoded struct {
	key   imgKey
	epoch uint64
	img   image.Image
	w, h  int // the image's own size
	err   error
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
	// Decodes run off the render thread. Their results wait in done until
	// the next draw takes them; ready tells Update to ask for one.
	mu      sync.Mutex
	done    []decoded
	ready   atomic.Bool
	epoch   uint64 // results of an older epoch (before dispose) are dropped
	wake    func()
	wg      sync.WaitGroup
	pending map[imgKey]int64 // decodes in flight, with their texture bytes

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
	clear(ti.pending)
	ti.mu.Lock()
	ti.epoch++
	ti.done = nil
	ti.mu.Unlock()
	ti.ops = nil
	ti.prepared = false
	ti.lastSeq = 0
	ti.lastGfxSeq = 0
}

// texBytes is what an image of w x h takes on the GPU once scaled down to
// maxSide.
func texBytes(w, h, maxSide int) int64 {
	if maxSide > 0 && max(w, h) > maxSide {
		s := float64(maxSide) / float64(max(w, h))
		w, h = max(1, int(float64(w)*s)), max(1, int(float64(h)*s))
	}
	return int64(w) * int64(h) * 4
}

// decodeTermImage decodes a terminal image, refusing from its header an
// image above maxTermImagePixels, and scales it down to maxSide. A decoder
// panic becomes an error.
func decodeTermImage(img vt.Image, maxSide int) (m image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, err = nil, fmt.Errorf("image decoder panic: %v", r)
		}
	}()
	w, h := img.W, img.H
	if img.Format == 100 {
		cfg, err := png.DecodeConfig(bytes.NewReader(img.Data))
		if err != nil {
			return nil, err
		}
		if cfg.Width != w || cfg.Height != h {
			return nil, fmt.Errorf("PNG of %dx%d announced as %dx%d", cfg.Width, cfg.Height, w, h)
		}
	}
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxTermImagePixels {
		return nil, errTermImageTooLarge
	}
	m, err = decodeRaw(img)
	if err != nil {
		return nil, err
	}
	return shrink(m, maxSide), nil
}

func decodeRaw(img vt.Image) (image.Image, error) {
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
	case 32: // RGBA, straight alpha
		m := image.NewNRGBA(image.Rect(0, 0, img.W, img.H))
		copy(m.Pix, img.Data)
		return m, nil
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
			_, pending := ti.pending[key]
			if !pending && ti.cache[key] == nil && !ti.failed[key] {
				imgCopy := img
				imgCopy.Data = bytes.Clone(img.Data)
				ti.toDecode = append(ti.toDecode, imgCopy)
			}
		}
	}
}

// syncCache starts the decodes prepare asked for and drops the textures
// of images no longer shown. An image beyond the pane's budget fails, and
// is not tried again for that generation.
func (ti *termImages) syncCache() {
	ti.init()
	ti.evict()
	maxSide := fitSide(maxTermImageSide)
	used := ti.usedBytes()
	for _, img := range ti.toDecode {
		key := imgKey{id: img.ID, gen: img.Gen}
		if _, ok := ti.pending[key]; ok {
			continue
		}
		n := texBytes(img.W, img.H, maxSide)
		if img.W <= 0 || img.H <= 0 || used+n > maxTermImageBytes {
			ti.failed[key] = true
			continue
		}
		used += n
		ti.pending[key] = n
		ti.startDecode(key, img, maxSide)
	}
	ti.toDecode = ti.toDecode[:0]
}

func (ti *termImages) init() {
	if ti.cache == nil {
		ti.cache = make(map[imgKey]*cachedImg)
	}
	if ti.failed == nil {
		ti.failed = make(map[imgKey]bool)
	}
	if ti.pending == nil {
		ti.pending = make(map[imgKey]int64)
	}
}

// usedBytes counts the textures held and the decodes in flight.
func (ti *termImages) usedBytes() int64 {
	var n int64
	for _, c := range ti.cache {
		b := c.eb.Bounds()
		n += int64(b.Dx()) * int64(b.Dy()) * 4
	}
	for _, p := range ti.pending {
		n += p
	}
	return n
}

// startDecode decodes img in the background, one image at a time in the
// window, and hands the result to the next draw.
func (ti *termImages) startDecode(key imgKey, img vt.Image, maxSide int) {
	ti.mu.Lock()
	epoch := ti.epoch
	ti.mu.Unlock()
	wake := ti.wake
	ti.wg.Add(1)
	go func() {
		defer ti.wg.Done()
		decodeSlots <- struct{}{}
		m, err := decodeTermImage(img, maxSide)
		<-decodeSlots
		ti.mu.Lock()
		if epoch == ti.epoch {
			ti.done = append(ti.done, decoded{key: key, epoch: epoch, img: m, w: img.W, h: img.H, err: err})
		}
		ti.mu.Unlock()
		ti.ready.Store(true)
		if wake != nil {
			wake()
		}
	}()
}

// collect takes the finished decodes and uploads them. A result for an
// image no longer shown is dropped.
func (ti *termImages) collect() {
	ti.mu.Lock()
	done := ti.done
	ti.done = nil
	ti.mu.Unlock()
	if len(done) == 0 {
		return
	}
	ti.init()
	for _, r := range done {
		delete(ti.pending, r.key)
		if gen, ok := ti.activeGens[r.key.id]; !ok || gen != r.key.gen {
			continue
		}
		if r.err != nil {
			ti.failed[r.key] = true
			continue
		}
		ti.cache[r.key] = &cachedImg{eb: ebiten.NewImageFromImage(r.img), w: r.w, h: r.h}
	}
}

// takeReady reports whether decodes finished since the last call.
func (ti *termImages) takeReady() bool { return ti.ready.Swap(false) }

// evict drops the textures and failures of images no longer shown.
func (ti *termImages) evict() {
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
			src = image.Rect(0, 0, c.w, c.h)
		}
		px := origin.X + p.Col*cellW + p.OffX
		py := origin.Y + p.Row*cellH + p.OffY
		dstW, dstH := p.Cols*cellW, p.Rows*cellH
		if p.Natural {
			dstW = src.Dx()
			dstH = src.Dy()
		}
		src = c.texRect(src)
		if src.Empty() {
			continue
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
		b := image.Rect(0, 0, c.w, c.h)
		vp := ti.vps[vpKey{ph.imgID, ph.pID}]
		vpCols, vpRows := vp.cols, vp.rows
		if vpCols <= 0 {
			vpCols = (b.Dx() + cellW - 1) / cellW
		}
		if vpRows <= 0 {
			vpRows = (b.Dy() + cellH - 1) / cellH
		}
		srcRect, dstRect := vt.PlaceholderSlice(b.Dx(), b.Dy(), vpCols, vpRows, cellW, cellH, ph.phRow, ph.phCol)
		srcRect = c.texRect(srcRect)
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

func (ti *termImages) draw(dst *ebiten.Image, t *vt.Terminal, offset int, origin image.Point, cellW, cellH int, syncing bool, wake func()) {
	if t == nil || cellW <= 0 || cellH <= 0 {
		return
	}
	ti.wake = wake
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
	ti.collect()

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
