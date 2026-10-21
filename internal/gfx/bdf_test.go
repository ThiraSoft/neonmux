package gfx

import (
	"image"
	"strings"
	"testing"
)

// Every embedded Terminus size parses, with the cell size its name promises
// and the glyphs the interface relies on.
func TestTerminusSizes(t *testing.T) {
	widths := map[int]int{12: 6, 14: 8, 16: 8, 18: 10, 20: 10, 22: 11, 24: 12, 28: 14, 32: 16}
	for _, px := range terminusSizes {
		for _, bold := range []bool{false, true} {
			f, err := loadTerminus(px, bold)
			if err != nil {
				t.Fatal(err)
			}
			if f.h != px || f.w != widths[px] {
				t.Errorf("ter-u%d (bold %v): cell %dx%d", px, bold, f.w, f.h)
			}
			for _, r := range "Aé€─█▶" {
				if _, ok := f.glyphs[r]; !ok {
					t.Errorf("ter-u%d (bold %v): no glyph for %q", px, bold, r)
				}
			}
		}
	}
}

func TestPickTerminus(t *testing.T) {
	for _, c := range []struct {
		target    float64
		px, scale int
	}{
		{10, 12, 1},
		{16, 16, 1},
		{17.3, 18, 1},
		{21.3, 22, 1},
		{32, 32, 1},
		{37.3, 18, 2},
		{48, 24, 2},
		{64, 32, 2},
		{96, 32, 3},
	} {
		if px, scale := pickTerminus(c.target); px != c.px || scale != c.scale {
			t.Errorf("pickTerminus(%v) = %d×%d, want %d×%d", c.target, px, scale, c.px, c.scale)
		}
	}
}

func TestParseBDF(t *testing.T) {
	const src = `STARTFONT 2.1
FONTBOUNDINGBOX 4 4 0 -1
FONT_ASCENT 3
FONT_DESCENT 1
CHARS 2
STARTCHAR bar
ENCODING 124
BBX 1 3 1 0
BITMAP
80
80
80
ENDCHAR
STARTCHAR unencoded
ENCODING -1
BBX 4 4 0 -1
BITMAP
F0
F0
F0
F0
ENDCHAR
ENDFONT
`
	f, err := parseBDF(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if f.w != 4 || f.h != 4 || f.ascent != 3 || len(f.glyphs) != 1 {
		t.Fatalf("font %dx%d ascent %d, %d glyphs", f.w, f.h, f.ascent, len(f.glyphs))
	}
	m := image.NewAlpha(image.Rect(0, 0, 8, 8))
	drawBDFGlyph(m, f, f.glyphs['|'], 2, false)
	var got strings.Builder
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if m.AlphaAt(x, y).A != 0 {
				got.WriteByte('#')
			} else {
				got.WriteByte('.')
			}
		}
		got.WriteByte('\n')
	}
	want := "..##....\n..##....\n..##....\n..##....\n..##....\n..##....\n........\n........\n"
	if got.String() != want {
		t.Errorf("glyph drawn as\n%swant\n%s", got.String(), want)
	}
}
