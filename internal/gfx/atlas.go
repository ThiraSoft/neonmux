// Package gfx gathers the low-level rendering: glyph atlas, fonts,
// procedural drawing and batching of quads into as few GPU calls as possible.
package gfx

import (
	"image"
	"slices"

	"github.com/hajimehoshi/ebiten/v2"
)

const atlasSize = 1024

// maxAtlasPages caps the atlas: 8 pages of 4 MB, that is a few thousand
// ideographs. Without a cap, scrolling through Chinese text would fill
// more than 200 MB of video memory.
const maxAtlasPages = 8

// The first page is only 512×512 (1 MB): enough for ASCII and the box
// drawing characters of a font at a common size.
const firstPageSize = 512

// frameTick numbers the frames (ebiten.Tick); replaced in the tests.
var frameTick = ebiten.Tick

// Glyph refers to a slot in the atlas.
type Glyph struct {
	Page   *ebiten.Image
	X, Y   int
	W, H   int
	Exists bool

	pg *atlasPage
}

// atlasPage is a page of the atlas, with the last frame in which one of
// its glyphs was used.
type atlasPage struct {
	img  *ebiten.Image
	used int64
}

// atlas packs fixed-height glyphs on shelves across 1024×1024 pages
// created on demand. One 4 MB page is enough for ASCII, accented letters
// and the box drawing characters of a font at a common size.
//
// At the cap, the least recently used page is recycled: evict forgets the
// glyphs it held, which will be drawn again on demand. A page used during
// the current frame or the previous one is never recycled: a draw batch
// not yet sent to the GPU may read it (Update and Draw of the same frame
// do not carry the same number). The atlas then goes over the cap for a
// while, and comes back under it on the next page.
type atlas struct {
	pages  []*atlasPage
	x, y   int
	rowH   int
	pixBuf []byte
	evict  func(p *atlasPage)
}

// touch records that the glyph is used during the current frame.
func (g *Glyph) touch() {
	if g.pg != nil {
		g.pg.used = frameTick()
	}
}

func (a *atlas) alloc(w, h int) (*atlasPage, int, int) {
	size := atlasSize
	if len(a.pages) > 0 {
		size = a.pages[len(a.pages)-1].img.Bounds().Dx()
	}
	if len(a.pages) == 0 || a.x+w > size {
		a.x = 0
		a.y += a.rowH
		a.rowH = 0
	}
	if len(a.pages) == 0 || a.y+h > size {
		a.turnPage(w, h)
	}
	page := a.pages[len(a.pages)-1]
	x, y := a.x, a.y
	a.x += w
	a.rowH = max(a.rowH, h)
	return page, x, y
}

// turnPage moves to an empty page: a new one, or at the cap the oldest one
// that can be safely recycled.
func (a *atlas) turnPage(w, h int) {
	now := frameTick()
	a.x, a.y, a.rowH = 0, 0, 0
	if len(a.pages) >= maxAtlasPages {
		if i := a.oldest(now); i >= 0 {
			p := a.pages[i]
			a.forget(p)
			a.pages = append(slices.Delete(a.pages, i, i+1), p)
			p.used = now
			// After an overflow, the extra pages are freed.
			for len(a.pages) > maxAtlasPages {
				i := a.oldest(now)
				if i < 0 {
					break
				}
				a.forget(a.pages[i])
				a.pages[i].img.Deallocate()
				a.pages = slices.Delete(a.pages, i, i+1)
			}
			return
		}
	}
	size := atlasSize
	if len(a.pages) == 0 && w*16 <= firstPageSize && h*8 <= firstPageSize {
		size = firstPageSize
	}
	a.pages = append(a.pages, &atlasPage{used: now, img: ebiten.NewImageWithOptions(
		image.Rect(0, 0, size, size), &ebiten.NewImageOptions{Unmanaged: true})})
}

// oldest returns the index of the least recently used page among those
// used neither during frame now nor during the previous one, or -1. The
// page being filled is not a candidate.
func (a *atlas) oldest(now int64) int {
	best := -1
	for i, p := range a.pages[:len(a.pages)-1] {
		if p.used+1 < now && (best < 0 || p.used < a.pages[best].used) {
			best = i
		}
	}
	return best
}

func (a *atlas) forget(p *atlasPage) {
	if a.evict != nil {
		a.evict(p)
	}
}

// upload copies a coverage mask into the atlas as premultiplied white.
func (a *atlas) upload(mask *image.Alpha) Glyph {
	w, h := mask.Rect.Dx(), mask.Rect.Dy()
	page, x, y := a.alloc(w, h)
	n := w * h * 4
	if cap(a.pixBuf) < n {
		a.pixBuf = make([]byte, n)
	}
	buf := a.pixBuf[:n]
	for i, v := range mask.Pix[:w*h] {
		buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3] = v, v, v, v
	}
	page.img.SubImage(image.Rect(x, y, x+w, y+h)).(*ebiten.Image).WritePixels(buf)
	return Glyph{Page: page.img, X: x, Y: y, W: w, H: h, Exists: true, pg: page}
}

func (a *atlas) dispose() {
	for _, p := range a.pages {
		p.img.Deallocate()
	}
	a.pages = nil
	a.x, a.y, a.rowH = 0, 0, 0
}
