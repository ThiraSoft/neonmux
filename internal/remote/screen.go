package remote

import (
	"hash/fnv"
	"image"
	"io"
	"strings"

	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

type Run struct {
	Text   string
	FG, BG int32 // 0xRRGGBB; BG -1 = theme background
	Attr   uint8 // 1 bold, 2 italic, 4 underline, 8 strike, 16 faint, 32 wide
}

type Line []Run

type Screen struct {
	Cols, Rows, Offset, Hist int
	CX, CY                   int
	CursorVis                bool
	Alt, Mouse               bool   // alternate screen, mouse tracking
	Lines                    []Line // len == Rows
	Imgs                     []ImgRect
}

// themeList is every theme the phone may pick, in the window's order.
func themeList() []ThemeInfo {
	out := make([]ThemeInfo, len(theme.All))
	for i, th := range theme.All {
		out[i] = ThemeInfo{Name: th.Name, Label: th.Label}
	}
	return out
}

// themeEntry is one theme of /themes.json: the phone's picker and its
// interface colors work without a connection.
type themeEntry struct {
	ThemeInfo
	ThemeMsg
}

func themeFile() []themeEntry {
	out := make([]themeEntry, len(theme.All))
	for i, th := range theme.All {
		p, _ := themePalette(th.Name)
		out[i] = themeEntry{ThemeInfo{Name: th.Name, Label: th.Label}, themeMsg(p)}
	}
	return out
}

// themePalette is the palette of the theme called name, if there is one.
func themePalette(name string) (Palette, bool) {
	for _, th := range theme.All {
		if th.Name != name {
			continue
		}
		p := Palette{
			FG: theme.RGB(th.FG), BG: theme.RGB(th.BG), Cursor: theme.RGB(th.Cursor),
			Accent: theme.RGB(th.Accent), Accent2: theme.RGB(th.Accent2),
		}
		for i := range p.P {
			p.P[i] = theme.RGB(th.Palette(uint8(i)))
		}
		return p, true
	}
	return Palette{}, false
}

func colorToInt(c [3]uint8) int32 {
	return int32(c[0])<<16 | int32(c[1])<<8 | int32(c[2])
}

// cellColors resolves the effective colors of a cell.
// Keep in step with termrender.go cellColors in internal/ui.
func cellColors(c vt.Cell, a vt.Attr, pal Palette) (fg, bg [3]uint8, hasBG bool) {
	fg = pal.FG
	if _, _, _, _, isPH := c.Placeholder(); !isPH {
		switch {
		case c.FG.IsIndexed():
			i := c.FG.Index()
			if a&vt.AttrBold != 0 && i < 8 {
				i += 8 // bold = bright color, as in xterm
			}
			fg = pal.P[i]
		case c.FG.IsRGB():
			r, g, b := c.FG.RGB()
			fg = [3]uint8{r, g, b}
		}
	}
	bg = pal.BG
	switch {
	case c.BG.IsIndexed():
		bg, hasBG = pal.P[c.BG.Index()], true
	case c.BG.IsRGB():
		r, g, b := c.BG.RGB()
		bg, hasBG = [3]uint8{r, g, b}, true
	}
	if a&vt.AttrInverse != 0 {
		fg, bg, hasBG = bg, fg, true
	}
	return
}

func cellAttr(a vt.Attr) uint8 {
	var attr uint8
	if a&vt.AttrBold != 0 {
		attr |= 1
	}
	if a&vt.AttrItalic != 0 {
		attr |= 2
	}
	if a&vt.AttrUnderline != 0 {
		attr |= 4
	}
	if a&vt.AttrStrike != 0 {
		attr |= 8
	}
	if a&vt.AttrFaint != 0 {
		attr |= 16
	}
	if a&vt.AttrWide != 0 {
		attr |= 32
	}
	return attr
}

func lineFromCells(cells []vt.Cell, pal Palette) Line {
	last := len(cells) - 1
	for last >= 0 {
		c := cells[last]
		a := c.Attr()
		if a&vt.AttrWideCont != 0 {
			last--
			continue
		}
		_, _, _, _, isPH := c.Placeholder()
		if isPH {
			a &^= vt.AttrUnderline
		}
		_, _, hasBG := cellColors(c, a, pal)
		attr := cellAttr(a)
		rn := c.Rune()
		isBlank := (isPH || rn == 0 || rn == ' ' || a&vt.AttrHidden != 0) && !hasBG && attr == 0
		if !isBlank {
			break
		}
		last--
	}
	if last < 0 {
		return Line(nil)
	}

	var runs Line
	var curText strings.Builder
	var curFG, curBG int32
	var curAttr uint8
	inRun := false

	flushRun := func() {
		if inRun {
			runs = append(runs, Run{
				Text: curText.String(),
				FG:   curFG,
				BG:   curBG,
				Attr: curAttr,
			})
			curText.Reset()
			inRun = false
		}
	}

	for x := 0; x <= last; x++ {
		c := cells[x]
		a := c.Attr()
		if a&vt.AttrWideCont != 0 {
			continue
		}
		_, _, _, _, isPH := c.Placeholder()
		if isPH {
			a &^= vt.AttrUnderline
		}
		fg, bg, hasBG := cellColors(c, a, pal)
		fgRGB := colorToInt(fg)
		bgRGB := int32(-1)
		if hasBG {
			bgRGB = colorToInt(bg)
		}
		attr := cellAttr(a)

		rn := c.Rune()
		if isPH || rn == 0 || a&vt.AttrHidden != 0 {
			rn = ' '
		}

		if inRun && curFG == fgRGB && curBG == bgRGB && curAttr == attr {
			curText.WriteRune(rn)
		} else {
			flushRun()
			curFG = fgRGB
			curBG = bgRGB
			curAttr = attr
			curText.WriteRune(rn)
			inRun = true
		}
	}
	flushRun()
	return runs
}

// Capture extracts the screen state, styled lines and visible images from
// the terminal. cellW and cellH are the cell size in pixels images are laid
// out with (the phone's); 0 takes the terminal's own. It takes t.Lock
// itself, and only reads image sizes: it never decodes or encodes one.
func Capture(t *vt.Terminal, offset int, pal Palette, cellW, cellH int) Screen {
	var pxW, pxH int
	if cellW <= 0 || cellH <= 0 {
		pxW, pxH = t.PixelSize() // before t.Lock: the same mutex
	}

	t.Lock()
	defer t.Unlock()

	cols, rows := t.Size()
	cx, cy, vis, _ := t.Cursor()
	hist := t.HistoryLen()
	modes := t.Modes()

	if offset < 0 {
		offset = 0
	} else if offset > hist {
		offset = hist
	}

	if cellW <= 0 || cellH <= 0 {
		if pxW > 0 && pxH > 0 && cols > 0 && rows > 0 {
			cellW, cellH = max(1, pxW/cols), max(1, pxH/rows)
		} else {
			cellW, cellH = 8, 16
		}
	}

	ir := imgRects{t: t, cellW: cellW, cellH: cellH}
	ir.direct(offset)

	lines := make([]Line, rows)
	var buf []vt.Cell
	for y := 0; y < rows; y++ {
		var cells []vt.Cell
		cells, buf = t.ViewLineBuf(y, offset, buf)
		lines[y] = lineFromCells(cells, pal)
		ir.placeholders(y, cells[:min(len(cells), cols)])
	}

	return Screen{
		Cols:      cols,
		Rows:      rows,
		Offset:    offset,
		Hist:      hist,
		CX:        cx,
		CY:        cy,
		CursorVis: vis,
		Alt:       modes&vt.ModeAltScreen != 0,
		Mouse:     modes&vt.ModeMouseMask != 0,
		Lines:     lines,
		Imgs:      ir.out,
	}
}

// imgRects collects the ImgRects of a view, at most maxImgRects. Sources
// are in the image's reference size (imageRefSize). Lock held.
type imgRects struct {
	t            *vt.Terminal
	cellW, cellH int
	out          []ImgRect
	imgs         map[uint32]vt.Image // sizes and gens, Data unused
	vps          map[[2]uint32][2]int
	seg          phSeg
}

// phSeg is a run of placeholder cells on one row showing consecutive
// columns of the same placement: one rect.
type phSeg struct {
	on               bool
	id, pid          uint32
	phRow, col, x, y int // col, x: last cell's
	x0               int
	src0, dst0       image.Rectangle // first cell's
	src1, dst1       image.Rectangle // last cell's
}

func (r *imgRects) full() bool { return len(r.out) >= maxImgRects }

func (r *imgRects) image(id uint32) (vt.Image, bool) {
	if img, ok := r.imgs[id]; ok {
		return img, img.W > 0 && img.H > 0
	}
	if r.imgs == nil {
		r.imgs = make(map[uint32]vt.Image)
	}
	img, _ := r.t.Image(id)
	img.Data = nil
	r.imgs[id] = img
	return img, img.W > 0 && img.H > 0
}

func (r *imgRects) add(img vt.Image, x, y, w, h float64, src image.Rectangle) {
	if r.full() || w <= 0 || h <= 0 {
		return
	}
	src = src.Intersect(image.Rect(0, 0, img.W, img.H))
	if src.Empty() {
		return
	}
	rw, rh := imageRefSize(img.W, img.H)
	sx0, sy0 := src.Min.X*rw/img.W, src.Min.Y*rh/img.H
	sx1, sy1 := src.Max.X*rw/img.W, src.Max.Y*rh/img.H
	if sx1 <= sx0 {
		sx0, sx1 = min(sx0, rw-1), min(sx0, rw-1)+1
	}
	if sy1 <= sy0 {
		sy0, sy1 = min(sy0, rh-1), min(sy0, rh-1)+1
	}
	r.out = append(r.out, ImgRect{ID: img.ID, Gen: img.Gen, X: x, Y: y, W: w, H: h,
		SX: sx0, SY: sy0, SW: sx1 - sx0, SH: sy1 - sy0})
}

// direct adds the direct placements, as the window draws them: stretched
// over Cols x Rows cells, or at their size in pixels when natural.
func (r *imgRects) direct(offset int) {
	for _, p := range r.t.VisiblePlacements(offset, nil) {
		if r.full() {
			return
		}
		img, ok := r.image(p.ImageID)
		if !ok {
			continue
		}
		src := p.Src
		if src.Empty() {
			src = image.Rect(0, 0, img.W, img.H)
		}
		cw, ch := float64(r.cellW), float64(r.cellH)
		w, h := float64(p.Cols), float64(p.Rows)
		if p.Natural {
			w, h = float64(src.Dx())/cw, float64(src.Dy())/ch
		}
		r.add(img, float64(p.Col)+float64(p.OffX)/cw, float64(p.Row)+float64(p.OffY)/ch, w, h, src)
	}
}

// placeholders adds the placeholder cells of view row y, merged by phSeg.
func (r *imgRects) placeholders(y int, cells []vt.Cell) {
	for x, c := range cells {
		id, pid, phRow, phCol, ok := c.Placeholder()
		if !ok {
			r.flush()
			continue
		}
		img, ok := r.image(id)
		if !ok {
			r.flush()
			continue
		}
		vk := [2]uint32{id, pid}
		vp, seen := r.vps[vk]
		if !seen {
			vc, vr, _ := r.t.VirtualPlacement(id, pid)
			if vc <= 0 {
				vc = (img.W + r.cellW - 1) / r.cellW
			}
			if vr <= 0 {
				vr = (img.H + r.cellH - 1) / r.cellH
			}
			vp = [2]int{vc, vr}
			if r.vps == nil {
				r.vps = make(map[[2]uint32][2]int)
			}
			r.vps[vk] = vp
		}
		src, dst := vt.PlaceholderSlice(img.W, img.H, vp[0], vp[1], r.cellW, r.cellH, phRow, phCol)
		if src.Empty() || dst.Empty() {
			r.flush()
			continue
		}
		s := &r.seg
		if s.on && s.id == id && s.pid == pid && s.phRow == phRow && s.col+1 == phCol && s.x+1 == x && s.y == y {
			s.col, s.x, s.src1, s.dst1 = phCol, x, src, dst
			continue
		}
		r.flush()
		*s = phSeg{on: true, id: id, pid: pid, phRow: phRow, col: phCol, x: x, y: y, x0: x,
			src0: src, dst0: dst, src1: src, dst1: dst}
	}
	r.flush()
}

func (r *imgRects) flush() {
	s := r.seg
	if !s.on {
		return
	}
	r.seg.on = false
	img, _ := r.image(s.id)
	cw, ch := float64(r.cellW), float64(r.cellH)
	left := float64(s.x0) + float64(s.dst0.Min.X)/cw
	right := float64(s.x) + float64(s.dst1.Max.X)/cw
	src := image.Rect(s.src0.Min.X, s.src0.Min.Y, s.src1.Max.X, s.src0.Max.Y)
	r.add(img, left, float64(s.y)+float64(s.dst0.Min.Y)/ch, right-left, float64(s.dst0.Dy())/ch, src)
}

// LineHash computes the 64-bit FNV-1a hash over the encoded runs of a line.
func LineHash(l Line) uint64 {
	h := fnv.New64a()
	var b [9]byte
	for _, r := range l {
		_, _ = io.WriteString(h, r.Text)
		b[0] = byte(r.FG >> 24)
		b[1] = byte(r.FG >> 16)
		b[2] = byte(r.FG >> 8)
		b[3] = byte(r.FG)
		b[4] = byte(r.BG >> 24)
		b[5] = byte(r.BG >> 16)
		b[6] = byte(r.BG >> 8)
		b[7] = byte(r.BG)
		b[8] = r.Attr
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}
