package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Aurora: downhill slalom under the aurora. q/d steer, s tucks for speed,
// space jumps the bumps. Pass between the flags of each gate, a missed
// gate costs three seconds, a pine stops the skier for a moment. Thirty
// gates, against the clock.

const slGates = 30

type slThing struct {
	x, y float64 // world: y grows down the slope
	kind int     // 0 gate (x is its center), 1 pine, 2 bump
	done bool
}

type slalom struct {
	base
	x, y, vx, speed float64
	stun, air       int
	things          []slThing
	gates, missed   int
	time            int // frames
	won             bool
}

func newSlalom(seed uint64) *slalom {
	g := &slalom{base: newBase(seed, 0x736b6965)}
	g.x, g.speed = 240, 1.5
	y := 160.0
	cx := 240.0
	for i := range slGates {
		cx = min(max(cx+(g.rng.Float64()*2-1)*110, 60), 420)
		g.things = append(g.things, slThing{x: cx, y: y, kind: 0})
		for range 2 + i/8 {
			g.things = append(g.things, slThing{x: 10 + g.rng.Float64()*460, y: y + 30 + g.rng.Float64()*60, kind: 1})
		}
		if g.rng.IntN(3) == 0 {
			g.things = append(g.things, slThing{x: cx + (g.rng.Float64()*2-1)*30, y: y + 60, kind: 2})
		}
		y += 110
	}
	return g
}

func (g *slalom) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newSlalom(g.rng.Uint64())
		return
	}
	if !repeat && name == "space" && g.air == 0 && !g.over {
		g.air = 30
	}
}

func (g *slalom) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *slalom) secs() float64 { return float64(g.time)/60 + 3*float64(g.missed) }

func (g *slalom) tick() {
	if g.over {
		return
	}
	g.time++
	if g.air > 0 {
		g.air--
	}
	// The course is laid out for 480 pixels: it is centered on the view.
	off := (g.vw - 480) / 2
	h := g.held
	if g.stun > 0 {
		g.stun--
		g.speed = 0.4
	} else {
		if left(h) {
			g.vx -= 0.18
		}
		if right(h) {
			g.vx += 0.18
		}
		top := 3.2
		if down(h) {
			top = 4.2
		}
		g.speed += (top - math.Abs(g.vx)*0.25 - g.speed) * 0.02
	}
	g.vx *= 0.92
	g.x = min(max(g.x+g.vx, off+6), off+474)
	prev := g.y
	g.y += g.speed
	for i := range g.things {
		t := &g.things[i]
		if t.done {
			continue
		}
		tx := t.x + off
		switch t.kind {
		case 0:
			if prev < t.y && g.y >= t.y {
				t.done = true
				g.gates++
				if math.Abs(g.x-tx) > 18 {
					g.missed++
					g.sparks(g.x, 60, 8, 3, 1)
				} else {
					g.sparks(g.x, 60, 6, 2, 1)
				}
				if g.gates == slGates {
					g.over, g.won = true, true
					if b, ok := bestTime["aurora"]; !ok || g.secs() < b {
						bestTime["aurora"] = g.secs()
					}
				}
			}
		case 1:
			if g.air == 0 && math.Abs(g.x-tx) < 6 && math.Abs(g.y-t.y) < 5 {
				t.done = true
				g.stun = 40
				g.sparks(g.x, 60, 14, 0, 1.4)
			}
		case 2:
			if math.Abs(g.x-tx) < 10 && math.Abs(g.y-t.y) < 3 && g.air == 0 {
				g.air = 24
			}
		}
	}
}

// bestTime keeps the best times of the session, by game.
var bestTime = map[string]float64{}

func fmtSecs(s float64) string {
	return strconv.Itoa(int(s)) + "." + strconv.Itoa(int(s*10)%10) + " s"
}

func (g *slalom) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	snow := theme.Mix(th.BG, th.FG, 0.12)
	P.Rect(0, v.oy+20*v.s, v.W, v.H, snow, 0.55)
	off := (g.vw - 480) / 2
	const heroY = 60.0
	o := theme.Mix(th.BG, black, 0.6)
	for _, t := range g.things {
		y := t.y - g.y + heroY
		if y < 24 || y > ViewH+20 {
			continue
		}
		x := t.x + off
		switch t.kind {
		case 0:
			c := th.Accent
			if t.done {
				c = th.Dim
			}
			for _, fx := range []float64{x - 20, x + 18} {
				v.hud(fx, y-12, 1, 12, o, 1)
				v.hud(fx+1, y-12, 5, 4, c, 1)
			}
		case 1:
			for k := 0.0; k < 4; k++ {
				v.hud(x-1-k*1.5, y-12+k*3, 3+k*3, 3, theme.Mix(th.Accent2, black, 0.4), 1)
			}
			v.hud(x, y, 2, 3, o, 1)
		case 2:
			v.hud(x-10, y-1, 20, 2, th.FG, 0.6)
			v.hud(x-6, y-2, 12, 1, th.FG, 0.6)
		}
	}
	if !(g.over && g.won) {
		x := math.Floor(g.x)
		lift := 0.0
		if g.air > 0 {
			t := float64(g.air) / 30
			lift = 10 * 4 * t * (1 - t)
		}
		lean := math.Max(-3, math.Min(3, g.vx*2))
		y := heroY - lift
		v.hud(x-4, heroY+1, 8, 1, o, 0.5)
		v.hud(x-5+lean, y, 10, 1, th.FG, 1) // skis
		v.hud(x-2, y-8, 4, 7, th.Warn, 1)
		v.hud(x-2, y-11, 4, 3, th.FG, 1)
		v.hud(x-4, y-6, 1, 6, o, 1)
		v.hud(x+3, y-6, 1, 6, o, 1)
	}
	g.drawParts(v, 0, 0)
	v.text(fmtSecs(g.secs()), 8, 4, th.FG, 1)
	v.text("gates "+strconv.Itoa(g.gates)+"/"+strconv.Itoa(slGates)+"  missed "+strconv.Itoa(g.missed), 8, 16, th.Dim, 1)
	line := "time " + fmtSecs(g.secs())
	if b, ok := bestTime["aurora"]; ok {
		line += "   best " + fmtSecs(b)
	}
	g.ending(v, "FINISH", line)
}
