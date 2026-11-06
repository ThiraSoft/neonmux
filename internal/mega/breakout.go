package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/sfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Fire: breakout on a wall of embers. q/d move the paddle, space
// launches. A broken ember lights up again after a while: clear the wall
// fast. Three balls.

const (
	brCols, brRows = 12, 6
	brW, brH       = 28.0, 10.0
	brTop          = 30.0
	brRelight      = 60 * 14
)

type breakout struct {
	base
	ash    [brRows][brCols]int // 0 lit, >0 frames since broken
	px     float64
	bx, by float64
	vx, vy float64
	stuck  bool
	balls  int
	score  int
	level  int
}

func newBreakout(seed uint64) *breakout {
	g := &breakout{base: newBase(seed, 0x66697265)}
	g.balls, g.level = 3, 1
	g.serve()
	return g
}

func (g *breakout) fieldX() float64 { return math.Floor((g.vw - brCols*brW) / 2) }

func (g *breakout) serve() {
	g.stuck = true
	g.px = brCols * brW / 2
}

func (g *breakout) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newBreakout(g.rng.Uint64())
		return
	}
	if !repeat && name == "space" && g.stuck && !g.over {
		g.stuck = false
		sp := 2.6 + 0.3*float64(g.level)
		g.vx, g.vy = sp*0.6, -sp*0.8
	}
}

func (g *breakout) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *breakout) tick() {
	if g.over {
		return
	}
	const fw = brCols * brW
	h := g.held
	if left(h) {
		g.px -= 3.4
	}
	if right(h) {
		g.px += 3.4
	}
	g.px = min(max(g.px, 18), fw-18)
	lit := 0
	for r := range brRows {
		for c := range brCols {
			if g.ash[r][c] > 0 {
				if g.ash[r][c]++; g.ash[r][c] > brRelight {
					g.ash[r][c] = 0
				}
			} else {
				lit++
			}
		}
	}
	if lit == 0 {
		g.level++
		g.score += 1000
		sfx.Play(sfx.Win)
		g.ash = [brRows][brCols]int{}
		g.serve()
		return
	}
	if g.stuck {
		g.bx, g.by = g.px, ViewH-24
		return
	}
	for range 2 { // two half steps: no tunneling through a brick
		g.bx += g.vx / 2
		g.by += g.vy / 2
		if g.bx < 2 || g.bx > fw-2 {
			g.vx = -g.vx
			g.bx = min(max(g.bx, 2), fw-2)
			sfx.Play(sfx.Bounce)
		}
		if g.by < brTop-20 {
			g.vy = math.Abs(g.vy)
			sfx.Play(sfx.Bounce)
		}
		c, r := int(g.bx/brW), int((g.by-brTop)/brH)
		if g.by >= brTop && r < brRows && c >= 0 && c < brCols && g.ash[r][c] == 0 {
			g.ash[r][c] = 1
			g.score += 10 * (brRows - r)
			sfx.Play(sfx.Hit)
			g.sparks(float64(c)*brW+brW/2, brTop+float64(r)*brH+5, 8, 3, 1)
			// Bounce on the side that was crossed.
			cx := float64(c)*brW + brW/2
			cy := brTop + float64(r)*brH + brH/2
			if math.Abs(g.bx-cx)/brW > math.Abs(g.by-cy)/brH {
				g.vx = -g.vx
			} else {
				g.vy = -g.vy
			}
		}
		// The paddle: where it is hit sets the angle.
		if g.vy > 0 && g.by >= ViewH-22 && g.by <= ViewH-16 && math.Abs(g.bx-g.px) < 20 {
			sp := math.Hypot(g.vx, g.vy)
			a := (g.bx - g.px) / 20 * 1.1
			g.vx, g.vy = sp*math.Sin(a), -sp*math.Cos(a)
			sfx.Play(sfx.Bounce)
		}
	}
	if g.by > ViewH+4 {
		sfx.Play(sfx.Miss)
		if g.balls--; g.balls == 0 {
			sfx.Play(sfx.Die)
			g.over = true
			record("fire", g.score)
			return
		}
		g.serve()
	}
}

func (g *breakout) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	x0 := g.fieldX()
	const fw = brCols * brW
	o := theme.Mix(th.BG, black, 0.6)
	v.hud(x0-3, brTop-24, 2, ViewH, th.Warn, 0.4)
	v.hud(x0+fw+1, brTop-24, 2, ViewH, th.Warn, 0.4)
	for r := range brRows {
		for c := range brCols {
			x, y := x0+float64(c)*brW, brTop+float64(r)*brH
			a := g.ash[r][c]
			if a > 0 {
				// Ash, that glows again as it is about to relight.
				k := float32(a) / brRelight
				v.hud(x+1, y+1, brW-2, brH-2, theme.Mix(th.Dim, th.Warn, k*k), 0.35+0.4*k*k)
				continue
			}
			flick := float32(0.8 + 0.2*math.Sin(float64(g.frame)*0.1+float64(r*7+c*3)))
			hot := theme.Mix(th.Warn, th.Accent, float32(r)/brRows)
			v.hud(x+1, y+1, brW-2, brH-2, o, 1)
			v.hud(x+2, y+2, brW-4, brH-4, hot, flick)
			v.hud(x+3, y+2, brW-8, 1, th.FG, 0.7*flick)
		}
	}
	px := x0 + g.px
	v.hud(px-20, ViewH-17, 40, 5, o, 1)
	v.hud(px-19, ViewH-16, 38, 3, th.Accent2, 1)
	v.hud(px-19, ViewH-16, 38, 1, th.FG, 0.8)
	if !g.over {
		bx, by := x0+math.Floor(g.bx), math.Floor(g.by)
		v.hud(bx-2, by-2, 5, 5, th.Warn, 0.4)
		v.hud(bx-1, by-1, 3, 3, th.FG, 1)
	}
	g.drawParts(v, x0, 0)
	v.text(strconv.Itoa(g.score), x0, 4, th.FG, 1)
	v.text("level "+strconv.Itoa(g.level), x0+fw/2-20, 4, th.Dim, 1)
	for i := range g.balls {
		v.hud(x0+fw-6-float64(i)*7, 7, 4, 4, th.FG, 1)
	}
	if g.stuck && !g.over && g.frame/20%2 == 0 {
		v.center("space", ViewH/2+30, th.Dim, 1)
	}
	g.ending(v, "BURNT OUT", "score "+strconv.Itoa(g.score)+"   best "+strconv.Itoa(best["fire"]))
}
