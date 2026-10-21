package fx

import (
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

func TestDecorShadersCompile(t *testing.T) {
	for name, src := range map[string]string{"aurora": auroraSrc, "topo": topoSrc, "lava": lavaSrc, "life": lifeSrc, "scope": scopeSrc} {
		if _, err := ebiten.NewShader([]byte(src)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBackgroundsKnown(t *testing.T) {
	legacy := []string{"grid", "rain", "stars", "dust", "none"}
	for _, b := range Backgrounds {
		if !slices.Contains(legacy, b) && decors[b] == nil {
			t.Errorf("background %q has no implementation", b)
		}
	}
	for k := range decors {
		if !slices.Contains(Backgrounds, k) {
			t.Errorf("background %q missing from the cycle", k)
		}
	}
	for _, th := range theme.All {
		if !slices.Contains(Backgrounds, th.Background) {
			t.Errorf("theme %s: unknown background %q", th.Name, th.Background)
		}
	}
}

func TestLifeBlinker(t *testing.T) {
	l := &life{rng: rand.New(rand.NewPCG(1, 2)), w: 6, h: 6}
	n := l.w * l.h
	l.cur, l.nxt = make([]bool, n), make([]bool, n)
	l.heat, l.born, l.age = make([]float32, n), make([]float32, n), make([]uint16, n)
	for x := 1; x <= 3; x++ {
		l.set(x, 2)
	}
	start := slices.Clone(l.cur)
	l.generation()
	if !l.cur[1*6+2] || !l.cur[3*6+2] || l.cur[2*6+1] {
		t.Fatal("the blinker did not rotate")
	}
	l.generation()
	if !slices.Equal(l.cur, start) {
		t.Fatal("the blinker did not return to its position")
	}
}

func TestCircuitTracesDontCross(t *testing.T) {
	c := &circuit{rng: rand.New(rand.NewPCG(3, 4))}
	c.generate(&stage{size: image.Pt(2560, 1600), pixel: 6})
	if len(c.traces) < 50 || len(c.chips) < 3 {
		t.Fatalf("board too sparse: %d traces, %d chips", len(c.traces), len(c.chips))
	}
	seen := map[[2]int]bool{}
	diag := map[[3]int]bool{}
	for _, tr := range c.traces {
		for k := 1; k < len(tr.pts); k++ {
			a, b := tr.pts[k-1], tr.pts[k]
			dx, dy := sign(b[0]-a[0]), sign(b[1]-a[1])
			n := max(abs(b[0]-a[0]), abs(b[1]-a[1])) / c.pitch
			for s := 0; s <= n; s++ {
				p := [2]int{a[0] + dx*s*c.pitch, a[1] + dy*s*c.pitch}
				if p[0] < 0 || p[1] < 0 || p[0] >= c.lw || p[1] >= c.lh {
					t.Fatalf("node off the board: %v", p)
				}
				if s > 0 || k == 1 {
					if seen[p] {
						t.Fatalf("two traces go through %v", p)
					}
					seen[p] = true
				}
				if s > 0 && dx != 0 && dy != 0 {
					q := [3]int{min(p[0], p[0]-dx*c.pitch), min(p[1], p[1]-dy*c.pitch)}
					if diag[q] {
						t.Fatalf("two diagonals cross at %v", q)
					}
					diag[q] = true
				}
			}
		}
	}
}

func TestFirePalette(t *testing.T) {
	f := &fire{}
	f.palette(theme.Get("blood-moon"))
	if f.top != theme.Get("blood-moon").Warn {
		t.Error("Blood Moon: the flame should rise toward gold")
	}
	f.palette(theme.Get("ice"))
	if f.top != theme.Get("ice").FG {
		t.Error("Ice: the flame should turn white rather than amber")
	}
	if f.pal[0][3] != 0 || f.pal[fireLevels][3] == 0 {
		t.Error("the palette should start from transparent")
	}
}

func TestScopeGlide(t *testing.T) {
	o := &scope{mode: 0}
	o.t = 1
	if a, b := o.ratio(); a != scopeRatios[0][0] || b != scopeRatios[0][1] {
		t.Fatalf("start of figure: %v:%v", a, b)
	}
	o.t = scopeHold - 1e-6
	a, b := o.ratio()
	if math.Abs(a-scopeRatios[1][0]) > 1e-3 || math.Abs(b-scopeRatios[1][1]) > 1e-3 {
		t.Fatalf("end of glide: %v:%v", a, b)
	}
}

func TestSwarmStaysFinite(t *testing.T) {
	s := &swarm{rng: rand.New(rand.NewPCG(5, 6))}
	s.init(&stage{size: image.Pt(1600, 1000), pixel: 4})
	for i := 0; i < 3000; i++ {
		s.advance()
	}
	for _, b := range s.boids {
		if math.IsNaN(b.x+b.y+b.vx+b.vy) || b.x < 0 || b.y < 0 || b.x > s.aspect || b.y > 1 {
			t.Fatalf("lost bird: %+v", b)
		}
	}
}

func TestCityStripLoops(t *testing.T) {
	c := &city{rng: rand.New(rand.NewPCG(7, 8)), lw: 400, lh: 250}
	for d := 0; d < 3; d++ {
		l := &cityLayer{w: 600}
		c.buildings(l, d, 5+2*d, 14+4*d, 0.2, 0.5, 0.2, 0.3)
		right := 0
		for _, k := range l.blocks {
			right = max(right, k.x+k.w)
		}
		if right != l.w {
			t.Errorf("layer %d: the strip stops at %d instead of %d", d, right, l.w)
		}
		for _, w := range l.wins {
			if int(w.x) < 0 || int(w.x)+int(w.w) > l.w || int(w.y) >= c.lh {
				t.Fatalf("layer %d: window outside the strip %+v", d, w)
			}
		}
	}
}

// shaderDecor gives access to the clock and the shrunk image of a shader
// background.
func shaderDecor(d decor) (*float64, *lowres) {
	switch d := d.(type) {
	case *aurora:
		return &d.t, &d.lr
	case *topo:
		return &d.t, &d.lr
	case *lava:
		return &d.t, &d.lr
	}
	return nil, nil
}

// A shader background only recomputes at its own rate: an app frame that
// lands between two background steps (keystroke, terminal output) reuses
// the previous render.
func TestShaderDecorFollowsItsCadence(t *testing.T) {
	s := &stage{size: image.Pt(320, 200), th: theme.Get("ice"), pixel: 4}
	dst := ebiten.NewImage(320, 200)
	for _, kind := range []string{"aurora", "topo", "lava"} {
		d := decors[kind](rand.New(rand.NewPCG(1, 2)))
		clock, lr := shaderDecor(d)
		step := 1 / float64(d.fps())
		*clock = 10 + 0.1*step
		d.draw(dst, s)
		first := lr.key
		d.step(0.25 * step)
		d.draw(dst, s)
		if lr.key != first {
			t.Errorf("%s: recomputed between two steps", kind)
		}
		d.step(step)
		d.draw(dst, s)
		if lr.key == first {
			t.Errorf("%s: not recomputed at the next step", kind)
		}
	}
}

// After ten days of animation, what the shaders receive stays small: in
// float32, a raw clock of 864,000 s no longer has enough precision and the
// background noise freezes.
func TestShaderTimeStaysSmall(t *testing.T) {
	th := theme.Get("ice")
	const tenDays = 10*86400 + 0.123
	check := func(name string, u map[string]any) {
		for k, v := range u {
			var vs []float32
			switch v := v.(type) {
			case float32:
				vs = []float32{v}
			case []float32:
				if k == "Size" || len(v) == 3 {
					continue // image size and colors
				}
				vs = v
			}
			for _, x := range vs {
				if math.Abs(float64(x)) > 1024 {
					t.Errorf("%s: %s is %v", name, k, x)
				}
			}
		}
	}
	for _, kind := range []string{"aurora", "topo", "lava"} {
		d := decors[kind](rand.New(rand.NewPCG(1, 2)))
		check(kind, d.(interface {
			vars(float64, *theme.Theme) map[string]any
		}).vars(tenDays, th))
	}
	check("grid", gridVars(tenDays, image.Pt(320, 200), 4, th))
	if !strings.Contains(kageNoise, fmt.Sprintf("mod(i, %d.0)", noisePeriod)) {
		t.Error("the shader noise does not loop on noisePeriod")
	}
}

// The circuit spends most of its time at 15 fps: its flashes are spaced
// out, and its rate only goes up while they run.
func TestCircuitMostlyAtRest(t *testing.T) {
	st := &stage{size: image.Pt(2880, 1800), pixel: 8}
	for seed := uint64(1); seed <= 3; seed++ {
		c := &circuit{rng: rand.New(rand.NewPCG(seed, 4))}
		c.generate(st)
		rest, total := 0.0, 0.0
		for total < 600 {
			f := c.fps()
			if f > 30 {
				t.Fatalf("seed %d: %d fps", seed, f)
			}
			dt := 1 / float64(f)
			if f == 15 {
				rest += dt
			}
			total += dt
			c.step(dt)
		}
		if rest/total < 0.6 {
			t.Errorf("seed %d: only %.0f%% of the time at 15 fps", seed, 100*rest/total)
		}
	}
}

// A flash starts every 2.4 s on average and travels at 190 to 360 board
// pixels per second.
func TestCircuitFlashPace(t *testing.T) {
	st := &stage{size: image.Pt(2880, 1800), pixel: 8}
	c := &circuit{rng: rand.New(rand.NewPCG(5, 4))}
	c.generate(st)
	const dt, total = 1.0 / 30, 1200.0
	flashes := 0
	for s := 0.0; s < total; s += dt {
		before := c.spawn
		c.step(dt)
		if c.spawn > before-dt+1e-9 {
			flashes++
		}
		for _, q := range c.pulses {
			if q.v < 190 || q.v > 360 {
				t.Fatalf("flash at %.0f board pixels per second", q.v)
			}
		}
	}
	if every := total / float64(flashes); every < 2.2 || every > 2.6 {
		t.Errorf("one flash every %.2f s, expected about 2.4 s", every)
	}
}
