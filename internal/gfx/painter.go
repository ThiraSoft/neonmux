package gfx

import (
	"image"
	"image/color"
	"unicode"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
)

var whiteImg = func() *ebiten.Image {
	img := ebiten.NewImage(3, 3)
	img.Fill(color.White)
	return img
}()

// White returns a small white image used as the source for solid fills.
func White() *ebiten.Image { return whiteImg }

// Batch accumulates textured quads for a single DrawTriangles32 call.
// Buffers are kept from one frame to the next: no allocation in steady
// state.
type Batch struct {
	vs []ebiten.Vertex
	is []uint32
}

// Reset empties the batch without freeing the memory.
func (b *Batch) Reset() {
	b.vs = b.vs[:0]
	b.is = b.is[:0]
}

// Premul converts a color into premultiplied components.
func Premul(c color.RGBA, alpha float32) (r, g, bl, a float32) {
	a = float32(c.A) / 255 * alpha
	return float32(c.R) / 255 * a, float32(c.G) / 255 * a, float32(c.B) / 255 * a, a
}

// Quad adds a destination rectangle textured by the given source area.
func (b *Batch) Quad(dx, dy, dw, dh, sx, sy, sw, sh float32, c color.RGBA, alpha float32) {
	r, g, bl, a := Premul(c, alpha)
	i := uint32(len(b.vs))
	b.vs = append(b.vs,
		ebiten.Vertex{DstX: dx, DstY: dy, SrcX: sx, SrcY: sy, ColorR: r, ColorG: g, ColorB: bl, ColorA: a},
		ebiten.Vertex{DstX: dx + dw, DstY: dy, SrcX: sx + sw, SrcY: sy, ColorR: r, ColorG: g, ColorB: bl, ColorA: a},
		ebiten.Vertex{DstX: dx, DstY: dy + dh, SrcX: sx, SrcY: sy + sh, ColorR: r, ColorG: g, ColorB: bl, ColorA: a},
		ebiten.Vertex{DstX: dx + dw, DstY: dy + dh, SrcX: sx + sw, SrcY: sy + sh, ColorR: r, ColorG: g, ColorB: bl, ColorA: a},
	)
	b.is = append(b.is, i, i+1, i+2, i+1, i+3, i+2)
}

// Rect adds a solid fill (source: the white image).
func (b *Batch) Rect(x, y, w, h float32, c color.RGBA, alpha float32) {
	b.Quad(x, y, w, h, 1, 1, 1, 1, c, alpha)
}

// Flush draws the batch, then empties it.
func (b *Batch) Flush(dst, src *ebiten.Image, blend ebiten.Blend) {
	if len(b.vs) == 0 {
		return
	}
	op := &ebiten.DrawTrianglesOptions{
		ColorScaleMode: ebiten.ColorScaleModePremultipliedAlpha,
		Blend:          blend,
	}
	dst.DrawTriangles32(b.vs, b.is, src, op)
	b.Reset()
}

type pageBatch struct {
	page  *ebiten.Image
	batch Batch
}

// Painter draws free-standing (off-grid) fills and text to a destination.
// Fills go before text on each Flush.
type Painter struct {
	solid Batch
	pages []pageBatch
}

// Rect adds a solid fill.
func (p *Painter) Rect(x, y, w, h float32, c color.RGBA, alpha float32) {
	p.solid.Rect(x, y, w, h, c, alpha)
}

// Border draws a frame of thickness t.
func (p *Painter) Border(r image.Rectangle, t float32, c color.RGBA, alpha float32) {
	x, y := float32(r.Min.X), float32(r.Min.Y)
	w, h := float32(r.Dx()), float32(r.Dy())
	p.Rect(x, y, w, t, c, alpha)
	p.Rect(x, y+h-t, w, t, c, alpha)
	p.Rect(x, y+t, t, h-2*t, c, alpha)
	p.Rect(x+w-t, y+t, t, h-2*t, c, alpha)
}

func (p *Painter) batchFor(page *ebiten.Image) *Batch {
	for i := range p.pages {
		if p.pages[i].page == page {
			return &p.pages[i].batch
		}
	}
	p.pages = append(p.pages, pageBatch{page: page})
	return &p.pages[len(p.pages)-1].batch
}

// Glyph places a glyph from the atlas.
func (p *Painter) Glyph(g Glyph, x, y float32, c color.RGBA, alpha float32) {
	if !g.Exists {
		return
	}
	p.batchFor(g.Page).Quad(x, y, float32(g.W), float32(g.H),
		float32(g.X), float32(g.Y), float32(g.W), float32(g.H), c, alpha)
}

// GlyphScaled places a scaled-up glyph (titles, effects).
func (p *Painter) GlyphScaled(g Glyph, x, y, s float32, c color.RGBA, alpha float32) {
	if !g.Exists {
		return
	}
	p.batchFor(g.Page).Quad(x, y, float32(g.W)*s, float32(g.H)*s,
		float32(g.X), float32(g.Y), float32(g.W), float32(g.H), c, alpha)
}

// Text writes a string at a free position and returns the width used.
func (p *Painter) Text(f *Font, x, y float32, s string, c color.RGBA, alpha float32) float32 {
	return p.TextStyled(f, x, y, s, 0, c, alpha)
}

// TextStyled writes a string with a style (bold, italic).
func (p *Painter) TextStyled(f *Font, x, y float32, s string, style int, c color.RGBA, alpha float32) float32 {
	x0, prev := x, x
	for _, r := range s {
		w := runeCells(r)
		if w == 0 {
			// A combining mark (the accent of a decomposed "é") sits on the
			// previous character; other zero-width characters (joiner,
			// variation selector) are not drawn.
			if prev < x && isMark(r) {
				p.Glyph(f.Glyph(r, style, false), prev, y, c, alpha)
			}
			continue
		}
		if r != ' ' {
			p.Glyph(f.Glyph(r, style, w == 2), x, y, c, alpha)
		}
		prev = x
		x += float32(f.CellW * w)
	}
	return x - x0
}

// TextWidth returns the width of a string in pixels.
func TextWidth(f *Font, s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n * f.CellW
}

// Truncate cuts a string to fit in max cells, with "…".
func Truncate(s string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	n := 0
	for i, r := range s {
		w := runeCells(r)
		if n+w > maxCells {
			if i == 0 {
				return ""
			}
			// Leave room for the ellipsis.
			cut := s[:i]
			for TextCells(cut)+1 > maxCells && len(cut) > 0 {
				_, sz := utf8.DecodeLastRuneInString(cut)
				cut = cut[:len(cut)-sz]
			}
			return cut + "…"
		}
		n += w
	}
	return s
}

// TextCells returns the width of a string in cells.
func TextCells(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n
}

// RuneCells is wired to the emulator's width table.
var RuneCells func(rune) int

// runeCells returns the width of a character in cells: 0 for combining
// marks and invisible characters (joiner, variation selector), 2 for wide
// characters.
func runeCells(r rune) int {
	if r < 0x300 {
		return 1
	}
	// The emulator's table counts one cell for U+FE0F: a variation
	// selector is never seen on its own.
	if unicode.Is(unicode.Variation_Selector, r) {
		return 0
	}
	if RuneCells == nil {
		if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
			return 0
		}
		return 1
	}
	return max(0, RuneCells(r))
}

// Flush draws the fills, then the text, and empties the painter.
func (p *Painter) Flush(dst *ebiten.Image) {
	p.solid.Flush(dst, whiteImg, ebiten.BlendSourceOver)
	for i := range p.pages {
		p.pages[i].batch.Flush(dst, p.pages[i].page, ebiten.BlendSourceOver)
	}
}

// Forget drops the batches tied to freed atlas pages (font change) so as
// not to keep dead references.
func (p *Painter) Forget() {
	p.pages = p.pages[:0]
}

// isMark reports a combining mark that is drawn on the previous character.
// Variation selectors are classified among them but draw nothing.
func isMark(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me) && !unicode.Is(unicode.Variation_Selector, r)
}
