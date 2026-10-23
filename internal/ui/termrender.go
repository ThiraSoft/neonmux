package ui

import (
	"image"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// termRenderer keeps a terminal's grid in a texture and only redraws the
// lines that changed. The default background stays transparent there: the
// animated background can show through without invalidating the cache.
type termRenderer struct {
	img      *ebiten.Image
	bg       gfx.Batch // background fills, in copy blend mode (clears the line)
	fg       gfx.Painter
	buf      []vt.Cell // buffer for scrollback lines, kept from one render to the next
	offset   int
	braille  bool
	theme    *theme.Theme
	font     *gfx.Font
	rows     int // lines redrawn at the last render (HUD)
	gridCols int // columns of the terminal grid at the last render
	gridRows int // rows of the terminal grid at the last render

	// Terminal state read under the lock of the last render, the one in the
	// texture: the cursor, the selection overlay and the scrollbar use it
	// without taking the lock again on every frame.
	first, screen int64   // scrollback bounds (AbsBase)
	hist          int     // reachable scrollback lines
	cx, cy, shape int     // cursor
	cursorVis     bool    // cursor visible (DECTCEM)
	under         vt.Cell // cell under the cursor
}

var transparent = color.RGBA{}

// subImage keeps dst.SubImage(r) from one frame to the next: SubImage
// allocates on every call, and the destination of a full render does not change.
type subImage struct {
	src, sub *ebiten.Image
	r        image.Rectangle
}

func (c *subImage) of(dst *ebiten.Image, r image.Rectangle) *ebiten.Image {
	if c.src != dst || c.r != r || c.sub == nil {
		c.src, c.r, c.sub = dst, r, dst.SubImage(r).(*ebiten.Image)
	}
	return c.sub
}

func (r *termRenderer) dispose() {
	if r.img != nil {
		r.img.Deallocate()
		r.img = nil
	}
	r.gridCols, r.gridRows = 0, 0
}

func (r *termRenderer) render(t *vt.Terminal, f *gfx.Font, th *theme.Theme, offset int, braille bool) {
	t.Lock()
	cols, rows := t.Size()
	w, h := cols*f.CellW, rows*f.CellH
	first, screen := t.AbsBase()
	// In the scrollback, line y of the view shows line y-offset of the
	// screen. As long as nothing scrolls (the scrollback bounds stay the
	// same), only the screen lines that changed need redoing; a scroll
	// shifts the whole view.
	full := r.theme != th || r.font != f || r.offset != offset || r.braille != braille || t.DirtyAll() ||
		offset > 0 && (first != r.first || screen != r.screen)
	if r.img == nil || r.img.Bounds().Dx() != w || r.img.Bounds().Dy() != h {
		r.dispose()
		r.img = ebiten.NewImageWithOptions(image.Rect(0, 0, w, h), &ebiten.NewImageOptions{Unmanaged: true})
		full = true
	}
	r.theme, r.font, r.offset, r.braille = th, f, offset, braille
	r.first, r.screen = first, screen
	r.gridCols, r.gridRows = cols, rows
	r.rows = 0
	var line []vt.Cell
	for y := 0; y < rows; y++ {
		if !full && (y < offset || !t.RowDirty(y-offset)) {
			continue
		}
		line, r.buf = t.ViewLineBuf(y, offset, r.buf)
		r.row(line, y, cols, f, th)
		r.rows++
	}
	r.hist = t.HistoryLen()
	r.cx, r.cy, r.cursorVis, r.shape = t.Cursor()
	r.under = vt.Cell{}
	if r.cy < rows {
		if l := t.ScreenLine(r.cy); r.cx < len(l) {
			r.under = l[r.cx]
		}
	}
	t.ClearDirty()
	t.Unlock()

	r.bg.Flush(r.img, gfx.White(), ebiten.BlendCopy)
	r.fg.Flush(r.img)
}

// cellColors resolves the effective colors of a cell.
// Keep in step with Capture in internal/remote/screen.go.
func cellColors(c vt.Cell, th *theme.Theme) (fg, bg color.RGBA, hasBG bool) {
	a := c.Attr()
	fg = th.FG
	switch {
	case c.FG.IsIndexed():
		i := c.FG.Index()
		if a&vt.AttrBold != 0 && i < 8 {
			i += 8 // bold = bright color, as in xterm
		}
		fg = th.Palette(i)
	case c.FG.IsRGB():
		r, g, b := c.FG.RGB()
		fg = color.RGBA{r, g, b, 255}
	}
	bg = th.BG
	switch {
	case c.BG.IsIndexed():
		bg, hasBG = th.Palette(c.BG.Index()), true
	case c.BG.IsRGB():
		r, g, b := c.BG.RGB()
		bg, hasBG = color.RGBA{r, g, b, 255}, true
	}
	if a&vt.AttrInverse != 0 {
		fg, bg, hasBG = bg, fg, true
	}
	return
}

func (r *termRenderer) row(line []vt.Cell, y, cols int, f *gfx.Font, th *theme.Theme) {
	cw, ch := float32(f.CellW), float32(f.CellH)
	py := float32(y) * ch
	r.bg.Rect(0, py, float32(cols)*cw, ch, transparent, 1)

	n := min(len(line), cols)
	runStart, runColor, inRun := 0, color.RGBA{}, false
	flushRun := func(end int) {
		if inRun {
			r.bg.Rect(float32(runStart)*cw, py, float32(end-runStart)*cw, ch, runColor, 1)
			inRun = false
		}
	}
	ul := max(1, f.CellH/14)
	for x := 0; x < n; x++ {
		c := line[x]
		a := c.Attr()
		if a&vt.AttrWideCont != 0 {
			continue
		}
		fg, bg, hasBG := cellColors(c, th)
		wide := a&vt.AttrWide != 0
		span := 1
		if wide {
			span = 2
		}
		if hasBG {
			if !inRun || bg != runColor {
				flushRun(x)
				runStart, runColor, inRun = x, bg, true
			}
		} else {
			flushRun(x)
		}
		px := float32(x) * cw
		if rn := c.Rune(); rn > ' ' && a&vt.AttrHidden == 0 {
			if r.braille {
				rn = brailleRune(rn)
			}
			style := 0
			if a&vt.AttrBold != 0 {
				style |= gfx.StyleBold
			}
			if a&vt.AttrItalic != 0 {
				style |= gfx.StyleItalic
			}
			alpha := float32(1)
			if a&vt.AttrFaint != 0 {
				alpha = 0.55
			}
			r.fg.Glyph(f.Glyph(rn, style, wide), px, py, fg, alpha)
		}
		if a&vt.AttrUnderline != 0 {
			r.fg.Rect(px, py+float32(f.Baseline+ul), cw*float32(span), float32(ul), fg, 1)
		}
		if a&vt.AttrStrike != 0 {
			r.fg.Rect(px, py+ch/2, cw*float32(span), float32(ul), fg, 1)
		}
	}
	flushRun(n)
}
