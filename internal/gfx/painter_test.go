package gfx

import (
	"image/color"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/mattn/go-runewidth"
)

// quads returns the first vertex of each quad accumulated in the painter.
func quads(p *Painter) (vs []ebiten.Vertex) {
	return p.Quads()
}

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
	xs := func(p *Painter) (xs []float32) {
		for _, q := range quads(p) {
			xs = append(xs, q.DstX)
		}
		return xs
	}
	var p Painter
	w := p.Text(f, 10, 0, "e\u0301x", color.RGBA{255, 255, 255, 255}, 1)
	if w != float32(2*f.CellW) {
		t.Fatalf("width %v instead of %d", w, 2*f.CellW)
	}
	if xs := xs(&p); len(xs) != 3 || xs[0] != 10 || xs[1] != 10 || xs[2] != float32(10+f.CellW) {
		t.Fatalf("glyphs placed at %v", xs)
	}
	p = Painter{}
	p.Text(f, 0, 0, "a\u200db\ufe0f", color.RGBA{255, 255, 255, 255}, 1)
	if xs := xs(&p); len(xs) != 2 || xs[1] != float32(f.CellW) {
		t.Fatalf("glyphs placed at %v", xs)
	}
}

// With Runes set, Text draws the mapped character in the same cell, and a
// combining mark is composed with its letter before the mapping.
func TestRunesMapping(t *testing.T) {
	f, err := LoadFont(FontPixel, 14, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Dispose()

	var p Painter
	p.Runes = func(r rune) rune {
		if r == 'a' {
			return '⠁'
		}
		return r
	}
	white := color.RGBA{255, 255, 255, 255}
	w := p.Text(f, 0, 0, "ae\u0301", white, 1)
	if want := float32(2 * f.CellW); w != want {
		t.Fatalf("width %v, want %v", w, want)
	}

	qs := quads(&p)
	if len(qs) != 2 {
		t.Fatalf("got %d quads, want 2", len(qs))
	}

	g1 := f.Glyph('⠁', 0, false)
	if qs[0].SrcX != float32(g1.X) || qs[0].SrcY != float32(g1.Y) {
		t.Errorf("quad 0 Src = (%v, %v), want (%v, %v)", qs[0].SrcX, qs[0].SrcY, g1.X, g1.Y)
	}
	if qs[0].DstX != 0 {
		t.Errorf("quad 0 DstX = %v, want 0", qs[0].DstX)
	}

	g2 := f.Glyph('é', 0, false)
	if qs[1].SrcX != float32(g2.X) || qs[1].SrcY != float32(g2.Y) {
		t.Errorf("quad 1 Src = (%v, %v), want (%v, %v)", qs[1].SrcX, qs[1].SrcY, g2.X, g2.Y)
	}
	if wantX := float32(f.CellW); qs[1].DstX != wantX {
		t.Errorf("quad 1 DstX = %v, want %v", qs[1].DstX, wantX)
	}

	for i := range p.pages {
		p.pages[i].batch.Reset()
	}
	p.Text(f, 0, 0, "e\u0301", white, 1)
	if n := testing.AllocsPerRun(20, func() {
		for i := range p.pages {
			p.pages[i].batch.Reset()
		}
		p.Text(f, 0, 0, "e\u0301", white, 1)
	}); n != 0 {
		t.Fatalf("Text with Runes on NFD string: %.0f allocations, want 0", n)
	}
}
