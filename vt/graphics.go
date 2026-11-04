package vt

import (
	"bytes"
	"compress/zlib"
	"errors"
	"image/png"
	"io"
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
}

func newGraphics() *graphics {
	return &graphics{
		nextID: 1,
		images: make(map[uint32]*Image),
	}
}

func (g *graphics) reset() {
	g.images = make(map[uint32]*Image)
	g.order = nil
	g.totalBytes = 0
	g.active = nil
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

func (g *graphics) hasPlacements(id uint32) bool {
	// Task 3: no placements yet.
	// Task 4 will check if active placements exist for this image ID.
	return false
}

func (g *graphics) removeImageAt(idx int, id uint32) {
	g.order = append(g.order[:idx], g.order[idx+1:]...)
	if img, ok := g.images[id]; ok {
		g.totalBytes -= len(img.Data)
		delete(g.images, id)
		g.seq.Add(1)
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
		decompressed, err := io.ReadAll(zr)
		_ = zr.Close()
		if err != nil {
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
			g.active = &chunkTransfer{
				cmd:  c,
				data: append([]byte(nil), c.payload...),
			}
		} else {
			g.active.data = append(g.active.data, c.payload...)
			if c.q != 0 {
				g.active.cmd.q = c.q
			}
		}
		return
	}

	// Final chunk (m == 0 or single-chunk)
	if g.active != nil {
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
		// TODO(Task 4): delete placements for this image on replacement
		g.seq.Add(1)
		t.gReply(c, "OK")
		if c.a == 'T' {
			// TODO(Task 4): place image at cursor
		}
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
	g.seq.Add(1)

	if hadID || hadNum {
		t.gReply(c, "OK")
	}

	if c.a == 'T' {
		// TODO(Task 4): place image at cursor
	}
}
