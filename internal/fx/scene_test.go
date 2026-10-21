package fx

import (
	"image"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// testFrame draws one frame of the background on a screen of the given size.
func testFrame(t *testing.T, b *Background, size image.Point, dt float64) {
	t.Helper()
	f, err := gfx.LoadFont(gfx.FontPixel, 14, 1)
	if err != nil {
		t.Fatal(err)
	}
	ScreenSize = size
	t.Cleanup(func() { ScreenSize = image.Point{} })
	var P gfx.Painter
	b.Update(dt)
	b.Draw(ebiten.NewImage(size.X, size.Y), &P, f, theme.Get("ice"), 4)
}

// Switching to one of the first four backgrounds (grid, rain, stars, dust)
// frees the previous self-contained background and its video memory.
func TestLegacyKindReleasesDecor(t *testing.T) {
	sh, err := LoadShaders()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"grid", "rain", "stars", "dust"} {
		b := NewBackground(sh)
		b.Kind, b.Animated = "fire", true
		testFrame(t, b, image.Pt(320, 200), 1.0/30)
		f, ok := b.decor.(*fire)
		if !ok || f.px.img == nil {
			t.Fatal("the fire is not set up")
		}
		b.Kind = kind
		testFrame(t, b, image.Pt(320, 200), 1.0/30)
		if b.decor != nil || f.px.img != nil {
			t.Errorf("%s: the fire is still allocated", kind)
		}
	}
}

// decorSize returns the screen size a self-contained background was built
// for.
func decorSize(d decor) image.Point {
	switch d := d.(type) {
	case *fire:
		return d.size
	case *life:
		return image.Pt(d.size[0], d.size[1])
	case *scope:
		return d.size
	case *city:
		return d.size
	case *circuit:
		return d.size
	case *swarm:
		return d.size
	}
	return image.Point{}
}

// During a mouse resize, the size changes on every frame: backgrounds only
// rebuild once the size is stable. The swarm, for its part, follows the new
// size without starting over.
func TestDecorWaitsForStableSize(t *testing.T) {
	sh, err := LoadShaders()
	if err != nil {
		t.Fatal(err)
	}
	start := image.Pt(640, 400)
	for _, kind := range []string{"fire", "life", "scope", "city", "circuit", "swarm"} {
		b := NewBackground(sh)
		b.Kind, b.Animated = kind, true
		for i := 0; i < 5; i++ {
			testFrame(t, b, start, 1.0/60)
		}
		var first *boid
		if sw, ok := b.decor.(*swarm); ok {
			first = &sw.boids[0]
		}
		size := start
		for i := 1; i <= 20; i++ {
			size = start.Add(image.Pt(4*i, 2*i))
			testFrame(t, b, size, 1.0/60)
			if kind == "swarm" {
				continue
			}
			if got := decorSize(b.decor); got != start {
				t.Fatalf("%s: rebuilt during the resize (%v)", kind, got)
			}
		}
		if sw, ok := b.decor.(*swarm); ok {
			if &sw.boids[0] != first {
				t.Fatal("swarm: the flock started over")
			}
			if want := float64(size.X) / float64(size.Y); sw.aspect != want {
				t.Fatalf("swarm: aspect %v instead of %v", sw.aspect, want)
			}
			for _, bd := range sw.boids {
				if bd.x < 0 || bd.x > sw.aspect || bd.y < 0 || bd.y > 1 {
					t.Fatalf("swarm: bird out of view %+v", bd)
				}
			}
		}
		for i := 0; i < 15; i++ {
			testFrame(t, b, size, 1.0/60)
		}
		if got := decorSize(b.decor); got != size {
			t.Errorf("%s: not rebuilt once the size was stable (%v instead of %v)", kind, got, size)
		}
	}
}
