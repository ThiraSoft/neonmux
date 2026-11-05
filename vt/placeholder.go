package vt

import (
	"image"
	"slices"
)

// Unicode placeholders of the kitty graphics protocol: a program places
// an image virtually (U=1), then prints U+10EEEE where it should show.
// The foreground color gives the image id, the underline color the
// placement id, and up to three diacritics the row, the column and the
// high byte of the image id.
//
// A placeholder cell keeps in its rune phBase + row<<9 | col, which is
// out of Unicode but fits in the 21 bits of the rune, in FG the color it
// was printed with, and in UL idHigh<<24 | placementID.
const (
	placeholderRune = 0x10EEEE
	phBase          = 0x110000
	phColBits       = 9
	phMaxCol        = 1<<phColBits - 1
	phIDMask        = 1<<24 - 1
)

// Placeholder decodes a Unicode placeholder cell: the image and placement
// it shows and the cell of the image it holds. ok is false for any other
// cell. An image id of 0 matches no image.
func (c Cell) Placeholder() (imageID, placementID uint32, row, col int, ok bool) {
	r := c.Rune()
	if r < phBase {
		return 0, 0, 0, 0, false
	}
	v := int(r - phBase)
	imageID = colorID(c.FG) | uint32(c.UL)&^phIDMask
	return imageID, uint32(c.UL) & phIDMask, v >> phColBits, v & phMaxCol, true
}

// colorID reads an id out of a color: the 24 bits of an RGB color, the
// index of a palette color, 0 for the default color.
func colorID(c Color) uint32 {
	switch {
	case c.IsRGB():
		return uint32(c) & phIDMask
	case c.IsIndexed():
		return uint32(c.Index())
	}
	return 0
}

func placeholderValue(row, col int) rune {
	return phBase + rune(row<<phColBits|min(col, phMaxCol))
}

// printPlaceholder prints U+10EEEE. Its row and column follow the cell on
// its left when that one shows the same image and placement (column + 1),
// and are (0, 0) otherwise, until diacritics say otherwise.
func (t *Terminal) printPlaceholder() {
	if t.cur.wrapNext {
		t.wrap()
	}
	fg, pid := t.cur.fg, colorID(t.cur.ul)
	row, col, high := 0, 0, uint32(0)
	x := t.cur.x
	if x > 0 {
		left := t.g.lines[t.cur.y][x-1]
		if _, lp, lr, lc, ok := left.Placeholder(); ok && left.FG == fg && lp == pid {
			row, col, high = lr, lc+1, uint32(left.UL)&^phIDMask
		}
	}
	t.put(placeholderValue(row, col), 1, 1)
	t.g.lines[t.cur.y][x].UL = Color(high | pid)
	t.lastRune = placeholderRune
	t.phMarks = 0
}

// placeholderDiacritic reports a diacritic of the table that follows a
// placeholder still open to diacritics. go-runewidth gives some of them a
// width of 1: print sends them to combine anyway.
func (t *Terminal) placeholderDiacritic(r rune) bool {
	if t.phMarks >= 3 || r < rowColumnDiacritics[0] || r > rowColumnDiacritics[len(rowColumnDiacritics)-1] {
		return false
	}
	line, x := t.prevCell()
	if x < 0 || line[x].Rune() < phBase {
		return false
	}
	_, found := slices.BinarySearch(rowColumnDiacritics[:], r)
	return found
}

// placeholderMark applies a diacritic to the placeholder at line[x]: the
// first gives the row, the second the column, the third the high byte of
// the image id. Diacritics out of the table, and past the third, are
// ignored.
func (t *Terminal) placeholderMark(line []Cell, x int, r rune) {
	n, found := slices.BinarySearch(rowColumnDiacritics[:], r)
	if !found || t.phMarks >= 3 {
		return
	}
	c := &line[x]
	v := int(c.Rune() - phBase)
	row, col := v>>phColBits, v&phMaxCol
	switch t.phMarks {
	case 0:
		if n != row {
			col = 0 // the column inferred from the left is for another row
		}
		row = n
	case 1:
		col = n
	default:
		if n > 0xff {
			return
		}
		c.UL = Color(uint32(n)<<24 | uint32(c.UL)&phIDMask)
	}
	t.phMarks++
	c.ra = uint32(placeholderValue(row, col)) | c.ra&^runeMask
	t.markDirty(t.cur.y)
}

// VirtualPlacement returns the size in cells of a virtual placement of an
// image, the one with id placementID, or the newest one when placementID
// is 0. Lock required.
func (t *Terminal) VirtualPlacement(imageID, placementID uint32) (cols, rows int, ok bool) {
	if t.gfx == nil {
		return 0, 0, false
	}
	ps := t.gfx.placements
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		if p.virtual && p.ImageID == imageID && (placementID == 0 || p.ID == placementID) {
			return p.Cols, p.Rows, true
		}
	}
	return 0, 0, false
}

// PlaceholderSlice computes what a placeholder cell shows. The image,
// imgW×imgH pixels, fits a box of cols×rows cells of cellW×cellH pixels,
// keeping its aspect ratio, centered (as kitty does). src is the part of
// the image seen by the cell at (row, col), dst where it goes inside the
// cell, in pixels. Both are empty when the cell is outside the image.
//
// Pixel edges are rounded down, so that neighboring cells share no pixel;
// a cell that sees less than one pixel of an enlarged image still gets
// one.
func PlaceholderSlice(imgW, imgH, cols, rows, cellW, cellH, row, col int) (src, dst image.Rectangle) {
	if imgW <= 0 || imgH <= 0 || cols <= 0 || rows <= 0 || cellW <= 0 || cellH <= 0 {
		return
	}
	boxW, boxH := cols*cellW, rows*cellH
	w, h := boxW, boxH
	if imgW*boxH >= imgH*boxW {
		h = max(1, imgH*boxW/imgW)
	} else {
		w = max(1, imgW*boxH/imgH)
	}
	pic := image.Rect(0, 0, w, h).Add(image.Pt((boxW-w)/2, (boxH-h)/2))
	cell := image.Rect(0, 0, cellW, cellH).Add(image.Pt(col*cellW, row*cellH))
	in := cell.Intersect(pic)
	if in.Empty() {
		return
	}
	in = in.Sub(pic.Min)
	src = image.Rect(in.Min.X*imgW/w, in.Min.Y*imgH/h, in.Max.X*imgW/w, in.Max.Y*imgH/h)
	if src.Dx() == 0 {
		src.Max.X = min(src.Min.X+1, imgW)
		src.Min.X = src.Max.X - 1
	}
	if src.Dy() == 0 {
		src.Max.Y = min(src.Min.Y+1, imgH)
		src.Min.Y = src.Max.Y - 1
	}
	return src, cell.Intersect(pic).Sub(cell.Min)
}
