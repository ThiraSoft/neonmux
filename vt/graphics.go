package vt

import (
	"bytes"
	"compress/zlib"
	"errors"
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

// adopt takes over the state of src, a graphics decoded from a snapshot,
// and moves the sequence on. g keeps its own counter.
func (g *graphics) adopt(src *graphics) {
	g.gen = src.gen
	g.nextID = src.nextID
	g.images = src.images
	g.order = src.order
	g.totalBytes = src.totalBytes
	g.active = src.active
	g.placements = src.placements
	g.refs = src.refs
	g.transient = src.transient
	g.altBase = src.altBase
	g.lowMain = src.lowMain
	g.lowAlt = src.lowAlt
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
			g.removeImage(c.i)
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
		g.removeImage(c.i)
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
