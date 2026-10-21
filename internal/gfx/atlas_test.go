package gfx

import (
	"testing"
)

// fakeTicks replaces the frame clock with a manual counter.
func fakeTicks(t *testing.T) *int64 {
	t.Helper()
	var tick int64 = 1
	old := frameTick
	frameTick = func() int64 { return tick }
	t.Cleanup(func() { frameTick = old })
	return &tick
}

type slot struct {
	page any
	x, y int
}

// Missing glyphs share a single replacement box: thousands of unknown
// characters add no page.
func TestTofuShared(t *testing.T) {
	for _, name := range []string{FontPixel, FontGoMono, FontIosevka} {
		f, err := LoadFont(name, 14, 2)
		if err != nil {
			t.Fatal(err)
		}
		first := f.Glyph(0xf0000, 0, false)
		pages := f.AtlasPages()
		for r := rune(0xf0001); r < 0xf0000+5000; r++ {
			g := f.Glyph(r, 0, false)
			if g.Page != first.Page || g.X != first.X || g.Y != first.Y {
				t.Fatalf("%s: U+%X has its own tofu", name, r)
			}
		}
		if n := f.AtlasPages(); n != pages {
			t.Errorf("%s: %d extra pages of tofu", name, n-pages)
		}
		f.Dispose()
	}
}

// liveSlots checks that no atlas slot is claimed by two cached glyphs: an
// entry pointing to a recycled page would show another glyph.
func liveSlots(t *testing.T, f *Font) {
	t.Helper()
	seen := map[slot]rune{}
	check := func(r rune, g Glyph) {
		if g.W == 0 || g == f.tofu[0] || g == f.tofu[1] {
			return
		}
		k := slot{g.Page, g.X, g.Y}
		if o, ok := seen[k]; ok {
			t.Fatalf("U+%X and U+%X share the same slot", o, r)
		}
		seen[k] = r
	}
	for s := range f.ascii {
		for r, g := range f.ascii[s] {
			check(rune(r), g)
		}
	}
	for k, g := range f.glyph {
		check(rune(k&0x1fffff), g)
	}
}

// Ideographs scroll by (Chinese text being browsed): the atlas stays under
// its cap and recycles the pages that were not used recently.
func TestAtlasCapped(t *testing.T) {
	tick := fakeTicks(t)
	f, err := LoadFont(FontPixel, 14, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Dispose()
	// 6000 ideographs: twice enough to reach the cap.
	for r := rune(0x4e00); r < 0x4e00+6000; r++ {
		if r%100 == 0 {
			*tick++
		}
		f.Glyph('a', 0, false) // ASCII is used on every frame
		f.Glyph(r, 0, true)
	}
	if n := f.AtlasPages(); n > maxAtlasPages {
		t.Fatalf("%d pages, cap %d", n, maxAtlasPages)
	}
	liveSlots(t, f)
	// A recycled glyph comes back on demand, in a slot of its own.
	f.Glyph(0x4e00, 0, true)
	liveSlots(t, f)
}

// Within a single frame, no page can be recycled: a draw batch not yet
// sent may be reading it. The atlas then goes over the cap, and comes back
// under it in the following frames.
func TestAtlasNeverRecyclesPagesInUse(t *testing.T) {
	tick := fakeTicks(t)
	f, err := LoadFont(FontPixel, 14, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Dispose()
	var got []Glyph
	for r := rune(0x4e00); r < 0x4e00+5000; r++ {
		got = append(got, f.Glyph(r, 0, true))
	}
	if f.AtlasPages() <= maxAtlasPages {
		t.Fatalf("the test should go over the cap (%d pages)", f.AtlasPages())
	}
	for i, g := range got {
		if f.Glyph(0x4e00+rune(i), 0, true) != g {
			t.Fatalf("U+%X moved during the frame", 0x4e00+i)
		}
	}
	liveSlots(t, f)
	*tick += 2
	for r := rune(0x8000); r < 0x8000+500; r++ {
		f.Glyph(r, 0, true)
	}
	if n := f.AtlasPages(); n > maxAtlasPages {
		t.Fatalf("still %d pages, cap %d", n, maxAtlasPages)
	}
	liveSlots(t, f)
}
