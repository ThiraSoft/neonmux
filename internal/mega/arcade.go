package mega

import (
	"math"
	"math/rand/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Arcade is a game hidden in a background. Typing "game" over the
// background starts it (internal/ui/mega.go).
type Arcade interface {
	// Key handles a key press, by its name in the layout.
	Key(name string, repeat bool)
	// Back handles escape: false means leave the game.
	Back() bool
	// SetHeld gives the keys being held.
	SetHeld(func(string) bool)
	// Step moves the game forward by dt seconds on a W x H screen.
	Step(dt float64, W, H int, paused bool)
	// Draw paints the game over the background.
	Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int)
}

// Panner is a game whose camera moves the background, in screen pixels.
type Panner interface{ PanScreen() float64 }

var arcades = map[string]func(seed uint64) Arcade{
	"city":    func(s uint64) Arcade { return New(s) },
	"stars":   func(s uint64) Arcade { return newStars(s) },
	"lava":    func(s uint64) Arcade { return newLava(s) },
	"circuit": func(s uint64) Arcade { return newSnake(s) },
	"grid":    func(s uint64) Arcade { return newRunner(s) },
	"rain":    func(s uint64) Arcade { return newGlyphs(s) },
	"dust":    func(s uint64) Arcade { return newFirefly(s) },
	"aurora":  func(s uint64) Arcade { return newSlalom(s) },
	"topo":    func(s uint64) Arcade { return newGolf(s) },
	"fire":    func(s uint64) Arcade { return newBreakout(s) },
	"life":    func(s uint64) Arcade { return newLifePuzzle(s) },
	"swarm":   func(s uint64) Arcade { return newPredator(s) },
	"scope":   func(s uint64) Arcade { return newTennis(s) },
}

// Has reports whether the background has a game.
func Has(kind string) bool { return arcades[kind] != nil }

// ForBackground starts the game of a background, nil if it has none.
func ForBackground(kind string, seed uint64) Arcade {
	if mk := arcades[kind]; mk != nil {
		return mk(seed)
	}
	return nil
}

// Step implements Arcade for the run and gun.
func (g *Game) Step(dt float64, W, H int, paused bool) {
	g.scale = Scale(H)
	g.SetView(float64(W) / float64(g.scale))
	g.Paused = paused
	g.Update(dt)
}

// PanScreen implements Panner: the city follows the camera.
func (g *Game) PanScreen() float64 { return g.PanX() * float64(max(1, g.scale)) }

// held keys shared by the games.
func left(h func(string) bool) bool  { return h("q") || h("left") || h("h") }
func right(h func(string) bool) bool { return h("d") || h("right") || h("l") }
func up(h func(string) bool) bool    { return h("z") || h("up") || h("k") }
func down(h func(string) bool) bool  { return h("s") || h("down") }

// best keeps the session's best scores, by game.
var best = map[string]int{}

func record(game string, score int) int {
	best[game] = max(best[game], score)
	return best[game]
}

// base is what the small games share: the fixed step, the end screen and
// the sparks.
type base struct {
	rng    *rand.Rand
	held   func(string) bool
	vw     float64
	frame  int
	acc    float64
	paused bool
	over   bool
	overT  int
	parts  []part
}

func newBase(seed, salt uint64) base {
	return base{rng: rand.New(rand.NewPCG(seed, salt)), vw: 480, held: func(string) bool { return false }}
}

func (b *base) SetHeld(h func(string) bool) { b.held = h }
func (b *base) Back() bool                  { return false }

// again reports a request to play again from the end screen.
func (b *base) again(name string, repeat bool) bool {
	return b.over && !repeat && b.overT > 45 && (name == "space" || name == "enter")
}

// loop runs tick at 60 Hz for dt seconds.
func (b *base) loop(dt float64, W, H int, paused bool, tick func()) {
	b.vw = float64(W) / float64(Scale(H))
	b.paused = paused
	b.frame++
	if paused {
		b.acc = 0
		return
	}
	b.acc += min(dt, 0.1)
	for n := 0; b.acc >= step && n < 6; n++ {
		b.acc -= step
		if b.over {
			b.overT++
		}
		b.stepParts()
		tick()
	}
}

func (b *base) sparks(x, y float64, n, col int, speed float64) {
	for range n {
		a := b.rng.Float64() * 2 * math.Pi
		s := (0.3 + b.rng.Float64()) * speed
		l := 14 + b.rng.IntN(18)
		b.parts = append(b.parts, part{x: x, y: y, vx: math.Cos(a) * s, vy: math.Sin(a) * s, life: l, max: l, size: 2, col: col})
	}
}

func (b *base) stepParts() {
	j := 0
	for _, p := range b.parts {
		p.x += p.vx
		p.y += p.vy
		p.vx *= 0.97
		p.vy *= 0.97
		if p.life--; p.life > 0 {
			b.parts[j] = p
			j++
		}
	}
	b.parts = b.parts[:j]
}

// drawParts draws the sparks, offset by ox, oy in view coordinates.
func (b *base) drawParts(v *view, ox, oy float64) {
	for _, p := range b.parts {
		v.hud(math.Floor(ox+p.x), math.Floor(oy+p.y), p.size, p.size, v.partColor(p.col), float32(p.life)/float32(p.max))
	}
}

// ending draws the end screen, the pause, and nothing while playing.
func (b *base) ending(v *view, title, line string) {
	th := v.th
	if b.over {
		v.center(title, ViewH/2-30, th.Warn, 1)
		v.center(line, ViewH/2-14, th.FG, 1)
		if b.overT > 45 {
			v.center("space: again    esc: leave", ViewH/2+2, th.Dim, 1)
		}
	}
	if b.paused {
		v.center("PAUSE", ViewH/2-30, th.FG, 1)
	}
}
