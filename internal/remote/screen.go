package remote

import (
	"hash/fnv"
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
		p := Palette{FG: theme.RGB(th.FG), BG: theme.RGB(th.BG), Cursor: theme.RGB(th.Cursor)}
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
		_, _, hasBG := cellColors(c, a, pal)
		attr := cellAttr(a)
		rn := c.Rune()
		isBlank := (rn == 0 || rn == ' ' || a&vt.AttrHidden != 0) && !hasBG && attr == 0
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
		fg, bg, hasBG := cellColors(c, a, pal)
		fgRGB := colorToInt(fg)
		bgRGB := int32(-1)
		if hasBG {
			bgRGB = colorToInt(bg)
		}
		attr := cellAttr(a)

		rn := c.Rune()
		if rn == 0 || a&vt.AttrHidden != 0 {
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

// Capture extracts the screen state and styled lines from the terminal.
// It takes t.Lock itself.
func Capture(t *vt.Terminal, offset int, pal Palette) Screen {
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

	lines := make([]Line, rows)
	var buf []vt.Cell
	for y := 0; y < rows; y++ {
		var cells []vt.Cell
		cells, buf = t.ViewLineBuf(y, offset, buf)
		lines[y] = lineFromCells(cells, pal)
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
	}
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
