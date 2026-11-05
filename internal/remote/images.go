package remote

import (
	"bytes"
	"container/list"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"sync"
	"weak"

	"golang.org/x/image/draw"

	"github.com/ThiraSoft/neonmux/vt"
)

const (
	// maxImageSide is the long side of an image sent to the phone.
	maxImageSide = 1280
	// maxImagePayload is the most encoded bytes an ImageMsg carries: in
	// base64 with its JSON it stays under maxPayloadSize.
	maxImagePayload = 700 << 10
	// imageCacheBytes bounds the prepared images kept for all phones.
	imageCacheBytes = 32 << 20
	// imageCacheEntries bounds their count (failures weigh almost nothing).
	imageCacheEntries = 1024
	// maxImgRects is the most image rects a frame carries.
	maxImgRects = 256
	// maxCellPx is the largest cell size in pixels a phone may announce.
	maxCellPx = 200
	// maxImagePixels refuses to decode more than vt's 10000x10000.
	maxImagePixels = 10000 * 10000
)

var errImageData = errors.New("image data does not match its size")

// prepareImageFunc is prepareImage; tests replace it.
var prepareImageFunc = prepareImage

// imageRefSize is the size the sources of an ImgRect refer to: the image
// with its long side brought down to maxImageSide. It only depends on the
// image's size, so Capture computes it without encoding anything. The
// bitmap sent may be smaller still (halved to fit maxImagePayload): the
// phone scales the sources by its real size.
func imageRefSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w >= h && w > maxImageSide {
		return maxImageSide, max(1, h*maxImageSide/w)
	}
	if h > w && h > maxImageSide {
		return max(1, w*maxImageSide/h), maxImageSide
	}
	return w, h
}

// imgCacheKey names an image of one terminal: ids and generations are per
// terminal. The weak pointer does not keep a closed terminal alive and is
// never equal to the pointer of a later terminal.
type imgCacheKey struct {
	term weak.Pointer[vt.Terminal]
	id   uint32
	gen  uint64
}

type preparedImage struct {
	key  imgCacheKey
	data []byte
	err  error
	done chan struct{} // closed once data/err are set
	elem *list.Element // in the LRU once done
}

func (p *preparedImage) weight() int { return len(p.data) + 64 }

// imageCache holds the images prepared for the phones, shared by all
// connections: one preparation per image whoever watches it. Bounded by
// bytes and count, least recently used first out.
type imageCache struct {
	mu         sync.Mutex
	maxBytes   int
	maxEntries int
	bytes      int
	m          map[imgCacheKey]*preparedImage
	lru        *list.List // of *preparedImage, front = most recent
	sem        chan struct{}
}

func newImageCache(maxBytes, maxEntries int) *imageCache {
	return &imageCache{
		maxBytes:   maxBytes,
		maxEntries: maxEntries,
		m:          make(map[imgCacheKey]*preparedImage),
		lru:        list.New(),
		// One decode at a time: a 10000x10000 PNG is 400 MB of pixels.
		sem: make(chan struct{}, 1),
	}
}

var phoneImages = newImageCache(imageCacheBytes, imageCacheEntries)

// get returns img, of terminal t, prepared for the phone. It may decode
// and encode: never call it with t's lock held or on a connection's loop.
// A failure is remembered as well, so it is never tried again.
func (c *imageCache) get(t *vt.Terminal, img vt.Image) ([]byte, error) {
	k := imgCacheKey{term: weak.Make(t), id: img.ID, gen: img.Gen}
	c.mu.Lock()
	if p, ok := c.m[k]; ok {
		if p.elem != nil {
			c.lru.MoveToFront(p.elem)
		}
		c.mu.Unlock()
		<-p.done
		return p.data, p.err
	}
	p := &preparedImage{key: k, done: make(chan struct{})}
	c.m[k] = p
	c.mu.Unlock()

	data, err := c.prepare(img)
	if err == nil && len(data) == 0 {
		err = errImageData
	}
	if err != nil {
		data = nil
	}

	c.mu.Lock()
	p.data, p.err = data, err
	close(p.done)
	p.elem = c.lru.PushFront(p)
	c.bytes += p.weight()
	c.pruneLocked(k)
	c.mu.Unlock()
	return data, err
}

// prepare runs prepareImageFunc one at a time; a panic on odd data is a
// failure, not a waiter stuck forever.
func (c *imageCache) prepare(img vt.Image) (data []byte, err error) {
	c.sem <- struct{}{}
	defer func() {
		<-c.sem
		if recover() != nil {
			data, err = nil, errImageData
		}
	}()
	data, _, _, err = prepareImageFunc(img)
	return data, err
}

// pruneLocked drops what can no longer be shown (older generations of the
// image just added, images of closed terminals), then the least recently
// used entries over the bounds.
func (c *imageCache) pruneLocked(added imgCacheKey) {
	for k, p := range c.m {
		if p.elem == nil || k == added {
			continue
		}
		if k.term.Value() == nil || (k.term == added.term && k.id == added.id && k.gen < added.gen) {
			c.removeLocked(p)
		}
	}
	for e := c.lru.Back(); e != nil && (c.bytes > c.maxBytes || len(c.m) > c.maxEntries); {
		prev := e.Prev()
		c.removeLocked(e.Value.(*preparedImage))
		e = prev
	}
}

func (c *imageCache) removeLocked(p *preparedImage) {
	delete(c.m, p.key)
	c.lru.Remove(p.elem)
	p.elem = nil
	c.bytes -= p.weight()
}

func decodeRawImage(img vt.Image) (image.Image, error) {
	if img.W <= 0 || img.H <= 0 || img.W*img.H > maxImagePixels {
		return nil, errImageData
	}
	switch img.Format {
	case 100: // PNG
		cfg, err := png.DecodeConfig(bytes.NewReader(img.Data))
		if err != nil {
			return nil, err
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxImagePixels {
			return nil, errImageData
		}
		return png.Decode(bytes.NewReader(img.Data))
	case 24: // RGB
		if len(img.Data) < img.W*img.H*3 {
			return nil, errImageData
		}
		rgba := image.NewRGBA(image.Rect(0, 0, img.W, img.H))
		src, dst := img.Data, rgba.Pix
		for i := 0; i < img.W*img.H; i++ {
			dst[4*i] = src[3*i]
			dst[4*i+1] = src[3*i+1]
			dst[4*i+2] = src[3*i+2]
			dst[4*i+3] = 255
		}
		return rgba, nil
	case 32: // RGBA, not premultiplied
		if len(img.Data) < img.W*img.H*4 {
			return nil, errImageData
		}
		m := image.NewNRGBA(image.Rect(0, 0, img.W, img.H))
		copy(m.Pix, img.Data)
		return m, nil
	default:
		return nil, image.ErrFormat
	}
}

// isImageOpaque uses the Opaque method every image png.Decode and
// decodeRawImage return has (a scan of the Pix slice).
func isImageOpaque(m image.Image, format uint8) bool {
	if format == 24 {
		return true
	}
	if op, ok := m.(interface{ Opaque() bool }); ok {
		return op.Opaque()
	}
	return false
}

// prepareImage prepares an image to be sent over the wire to the phone:
// JPEG quality 80 if opaque, PNG otherwise, longest side <= maxImageSide,
// halved until it fits in maxImagePayload. w and h are the size encoded.
func prepareImage(img vt.Image) (data []byte, w, h int, err error) {
	raw, err := decodeRawImage(img)
	if err != nil {
		return nil, 0, 0, err
	}
	w, h = imageRefSize(raw.Bounds().Dx(), raw.Bounds().Dy())
	if w <= 0 || h <= 0 {
		return nil, 0, 0, errImageData
	}
	opaque := isImageOpaque(raw, img.Format)

	for {
		var m image.Image = raw
		if raw.Bounds().Dx() != w || raw.Bounds().Dy() != h {
			dst := image.NewRGBA(image.Rect(0, 0, w, h))
			draw.BiLinear.Scale(dst, dst.Bounds(), raw, raw.Bounds(), draw.Src, nil)
			m = dst
		}
		var buf bytes.Buffer
		if opaque {
			err = jpeg.Encode(&buf, m, &jpeg.Options{Quality: 80})
		} else {
			err = png.Encode(&buf, m)
		}
		if err != nil {
			return nil, 0, 0, err
		}
		if buf.Len() <= maxImagePayload {
			return buf.Bytes(), w, h, nil
		}
		if w == 1 && h == 1 {
			return nil, 0, 0, errImageData
		}
		w, h = max(1, w/2), max(1, h/2)
	}
}
