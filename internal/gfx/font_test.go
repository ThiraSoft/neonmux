package gfx

import (
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
)

// fontDir writes copies of Go Mono under the given names.
func fontDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A TTF font finds its italic and bold italic files next to the regular
// file, with the two common naming conventions.
func TestSiblingItalics(t *testing.T) {
	for _, names := range [][4]string{
		{"Mono-Regular.ttf", "Mono-Bold.ttf", "Mono-Italic.ttf", "Mono-BoldItalic.ttf"},
		{"Mono.ttf", "Mono-Bold.ttf", "Mono-Oblique.ttf", "Mono-BoldOblique.ttf"},
	} {
		dir := fontDir(t, map[string][]byte{names[0]: gomono.TTF, names[1]: gomonobold.TTF,
			names[2]: gomonoitalic.TTF, names[3]: gomonobolditalic.TTF})
		f, err := LoadFont(filepath.Join(dir, names[0]), 14, 2)
		if err != nil {
			t.Fatal(err)
		}
		for s := 1; s < 4; s++ {
			for o := 0; o < s; o++ {
				if f.faces[s] == f.faces[o] {
					t.Errorf("%s: styles %d and %d share the same face", names[0], s, o)
				}
			}
		}
		f.Dispose()
	}
}

// slant measures the slant of a vertical stroke: the offset between the
// middle of its top row and the middle of its bottom row.
func slant(m *image.Alpha) float64 {
	mid := func(y int) (float64, bool) {
		sum, n := 0.0, 0.0
		for x := 0; x < m.Rect.Dx(); x++ {
			if v := float64(m.Pix[y*m.Stride+x]); v > 0 {
				sum += float64(x) * v
				n += v
			}
		}
		return sum / max(n, 1), n > 0
	}
	top, bot := -1.0, -1.0
	for y := 0; y < m.Rect.Dy(); y++ {
		if x, ok := mid(y); ok {
			if top < 0 {
				top = x
			}
			bot = x
		}
	}
	return top - bot
}

// Without an italic file, the glyph is slanted: the top of a vertical bar
// leans to the right.
func TestFakeItalicSlants(t *testing.T) {
	dir := fontDir(t, map[string][]byte{"Mono-Regular.ttf": gomono.TTF})
	f, err := LoadFont(filepath.Join(dir, "Mono-Regular.ttf"), 14, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Dispose()
	for _, style := range []int{0, StyleItalic, StyleBold | StyleItalic} {
		m := image.NewAlpha(image.Rect(0, 0, f.CellW, f.CellH))
		f.drawGlyph(m, '|', style)
		s := slant(m)
		if style == 0 && math.Abs(s) > 0.5 || style != 0 && s < 2 {
			t.Errorf("style %d: slant %.1f px", style, s)
		}
	}
}
