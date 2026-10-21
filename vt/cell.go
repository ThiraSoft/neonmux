package vt

// Color encodes a terminal color on 32 bits.
// The 8 high bits give the kind, the other 24 the value.
type Color uint32

const (
	ColorDefault Color = 0
	kindIndexed  Color = 1 << 24
	kindRGB      Color = 2 << 24
	kindMask     Color = 0xff << 24
)

// Indexed returns a color from the 256-color palette.
func Indexed(i uint8) Color { return kindIndexed | Color(i) }

// RGB returns a truecolor color.
func RGB(r, g, b uint8) Color {
	return kindRGB | Color(r)<<16 | Color(g)<<8 | Color(b)
}

func (c Color) IsDefault() bool { return c == ColorDefault }
func (c Color) IsIndexed() bool { return c&kindMask == kindIndexed }
func (c Color) IsRGB() bool     { return c&kindMask == kindRGB }
func (c Color) Index() uint8    { return uint8(c) }
func (c Color) RGB() (r, g, b uint8) {
	return uint8(c >> 16), uint8(c >> 8), uint8(c)
}

// Attr holds the SGR attributes of a cell (11 bits used).
type Attr uint16

const (
	AttrBold Attr = 1 << iota
	AttrFaint
	AttrItalic
	AttrUnderline
	AttrBlink
	AttrInverse
	AttrHidden
	AttrStrike
	AttrWide     // first half of a double-width character
	AttrWideCont // second half, holds no rune
)

const (
	runeBits = 21
	runeMask = 1<<runeBits - 1
)

// Cell is one cell of the grid. 12 bytes with no padding.
type Cell struct {
	ra uint32 // rune on 21 bits, attributes on the remaining 11 bits
	FG Color
	BG Color
}

// MakeCell builds a cell.
func MakeCell(r rune, a Attr, fg, bg Color) Cell {
	return Cell{ra: uint32(r)&runeMask | uint32(a)<<runeBits, FG: fg, BG: bg}
}

// Rune returns the character (0 for an empty cell).
func (c Cell) Rune() rune { return rune(c.ra & runeMask) }

// Attr returns the attributes.
func (c Cell) Attr() Attr { return Attr(c.ra >> runeBits) }

// IsBlank reports an empty cell with no style and no background.
func (c Cell) IsBlank() bool {
	r := c.Rune()
	return (r == 0 || r == ' ') && c.BG == ColorDefault &&
		c.Attr()&(AttrInverse|AttrUnderline|AttrStrike|AttrWide|AttrWideCont) == 0
}

// blankCell returns an erased cell with the current background (BCE).
func blankCell(bg Color) Cell { return Cell{BG: bg} }
