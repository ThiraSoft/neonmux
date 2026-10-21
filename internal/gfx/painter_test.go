package gfx

import (
	"image/color"
	"testing"

	"github.com/mattn/go-runewidth"
)

// Zero-width characters take no cell: a decomposed "Résumé" (e + combining
// accent) counts six, a joiner (U+200D) or a variation selector (U+FE0F)
// none.
func TestZeroWidthRunes(t *testing.T) {
	cond := runewidth.NewCondition()
	cond.EastAsianWidth = false
	for _, cells := range []func(rune) int{nil, cond.RuneWidth} {
		RuneCells = cells
		for s, want := range map[string]int{
			"Re\u0301sume\u0301": 6,
			"❤\ufe0f":            1,
			"a\u200db":           2,
		} {
			if got := TextCells(s); got != want {
				t.Errorf("TextCells(%q) = %d, want %d (table wired: %v)", s, got, want, cells != nil)
			}
		}
	}
	if got := TextCells("日本"); got != 4 {
		t.Errorf("TextCells(日本) = %d, want 4", got)
	}
	RuneCells = nil
}

// When drawing, the combining accent sits on the previous letter, and the
// other zero-width characters leave neither a hole nor a replacement box.
func TestZeroWidthDrawing(t *testing.T) {
	f, err := LoadFont(FontPixel, 14, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Dispose()
	quads := func(p *Painter) (xs []float32) {
		for _, pb := range p.pages {
			for i := 0; i < len(pb.batch.vs); i += 4 {
				xs = append(xs, pb.batch.vs[i].DstX)
			}
		}
		return xs
	}
	var p Painter
	w := p.Text(f, 10, 0, "e\u0301x", color.RGBA{255, 255, 255, 255}, 1)
	if w != float32(2*f.CellW) {
		t.Fatalf("width %v instead of %d", w, 2*f.CellW)
	}
	if xs := quads(&p); len(xs) != 3 || xs[0] != 10 || xs[1] != 10 || xs[2] != float32(10+f.CellW) {
		t.Fatalf("glyphs placed at %v", xs)
	}
	p = Painter{}
	p.Text(f, 0, 0, "a\u200db\ufe0f", color.RGBA{255, 255, 255, 255}, 1)
	if xs := quads(&p); len(xs) != 2 || xs[1] != float32(f.CellW) {
		t.Fatalf("glyphs placed at %v", xs)
	}
}
