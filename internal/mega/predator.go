package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Swarm: the player is a predator in a flock. zqsd move it; the boids
// flock together and flee it when it comes close. Catch as many as
// possible in sixty seconds. Space dashes, once every two seconds.

const (
	pdBoids = 70
	pdTime  = 60 * 60
)

type boid struct{ x, y, vx, vy float64 }

type predator struct {
	base
	x, y, vx, vy float64
	boids        []boid
	caught       int
	t            int
	dash, cool   int
}

func newPredator(seed uint64) *predator {
	g := &predator{base: newBase(seed, 0x73776172)}
	g.x, g.y = 240, ViewH/2
	for range pdBoids {
		g.boids = append(g.boids, g.newBoid())
	}
	return g
}

func (g *predator) newBoid() boid {
	a := g.rng.Float64() * 2 * math.Pi
	return boid{x: g.rng.Float64() * 480, y: 20 + g.rng.Float64()*(ViewH-40), vx: math.Cos(a), vy: math.Sin(a)}
}

func (g *predator) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newPredator(g.rng.Uint64())
		return
	}
	if !repeat && name == "space" && g.cool == 0 && !g.over {
		g.dash, g.cool = 14, 120
	}
}

func (g *predator) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *predator) tick() {
	if g.over {
		return
	}
	if g.t++; g.t >= pdTime {
		g.over = true
		record("swarm", g.caught)
		return
	}
	if g.cool > 0 {
		g.cool--
	}
	h := g.held
	acc := 0.22
	if g.dash > 0 {
		g.dash--
		acc = 0.7
	}
	if left(h) {
		g.vx -= acc
	}
	if right(h) {
		g.vx += acc
	}
	if up(h) {
		g.vy -= acc
	}
	if down(h) {
		g.vy += acc
	}
	g.vx *= 0.9
	g.vy *= 0.9
	g.x = min(max(g.x+g.vx, 4), g.vw-4)
	g.y = min(max(g.y+g.vy, 16), ViewH-4)
	for i := range g.boids {
		b := &g.boids[i]
		var cx, cy, ax, ay, sx, sy float64
		n := 0
		for k, o := range g.boids {
			if k == i {
				continue
			}
			dx, dy := o.x-b.x, o.y-b.y
			d2 := dx*dx + dy*dy
			if d2 < 40*40 {
				n++
				cx += dx
				cy += dy
				ax += o.vx
				ay += o.vy
				if d2 < 10*10 {
					sx -= dx
					sy -= dy
				}
			}
		}
		if n > 0 {
			b.vx += cx/float64(n)*0.003 + (ax/float64(n)-b.vx)*0.04 + sx*0.03
			b.vy += cy/float64(n)*0.003 + (ay/float64(n)-b.vy)*0.04 + sy*0.03
		}
		// Flee the predator.
		dx, dy := b.x-g.x, b.y-g.y
		if d := math.Hypot(dx, dy); d < 60 && d > 0 {
			b.vx += dx / d * 0.25
			b.vy += dy / d * 0.25
		}
		// Turn back before the edges.
		if b.x < 20 {
			b.vx += 0.1
		}
		if b.x > g.vw-20 {
			b.vx -= 0.1
		}
		if b.y < 30 {
			b.vy += 0.1
		}
		if b.y > ViewH-20 {
			b.vy -= 0.1
		}
		sp := math.Hypot(b.vx, b.vy)
		top := 1.9
		if sp > top {
			b.vx, b.vy = b.vx/sp*top, b.vy/sp*top
		} else if sp < 0.6 {
			b.vx, b.vy = b.vx*1.1, b.vy*1.1
		}
		b.x += b.vx
		b.y += b.vy
		if math.Hypot(b.x-g.x, b.y-g.y) < 6 {
			g.caught++
			g.sparks(b.x, b.y, 8, 1, 1.2)
			*b = g.newBoid()
			b.x = g.vw - g.x // far from the predator
		}
	}
}

func (g *predator) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	P.Rect(0, 0, v.W, v.H, th.BG, 0.35)
	for _, b := range g.boids {
		x, y := math.Floor(b.x), math.Floor(b.y)
		sp := math.Hypot(b.vx, b.vy) + 1e-9
		tx, ty := math.Round(-b.vx/sp*3), math.Round(-b.vy/sp*3)
		v.hud(x+tx, y+ty, 1, 1, th.Accent2, 0.5)
		v.hud(x, y, 2, 2, th.Accent2, 1)
	}
	if !g.over {
		x, y := math.Floor(g.x), math.Floor(g.y)
		c := th.Warn
		if g.dash > 0 {
			c = th.FG
		}
		v.hud(x-5, y-5, 10, 10, c, 0.2)
		v.hud(x-3, y-2, 6, 4, c, 1)
		v.hud(x-2, y-3, 4, 6, c, 1)
		v.hud(x-1, y-1, 1, 1, theme.Mix(th.BG, black, 0.6), 1)
		v.hud(x+1, y-1, 1, 1, theme.Mix(th.BG, black, 0.6), 1)
	}
	g.drawParts(v, 0, 0)
	remain := (pdTime - g.t + 59) / 60
	v.text(strconv.Itoa(g.caught)+" caught", 8, 4, th.FG, 1)
	v.text(strconv.Itoa(remain)+" s", v.vw()-40, 4, th.FG, 1)
	if g.cool == 0 {
		v.hud(8, 16, 20, 2, th.Warn, 1)
	} else {
		v.hud(8, 16, 20*(1-float64(g.cool)/120), 2, th.Dim, 1)
	}
	g.ending(v, "TIME", strconv.Itoa(g.caught)+" caught   best "+strconv.Itoa(best["swarm"]))
}
