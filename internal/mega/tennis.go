package mega

import (
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Scope: vector tennis on the oscilloscope. z/s (or k/j, the arrows) move
// the left paddle; the right one is played by the machine, which gets
// better at each point it loses. First to seven.

const (
	tnW     = 360.0
	tnH     = 200.0
	tnTop   = 26.0
	tnPad   = 30.0
	tnWin   = 7
	tnTrail = 14
)

type tennis struct {
	base
	py, ay  float64 // paddles' centers
	bx, by  float64
	vx, vy  float64
	trail   [][2]float64
	me, cpu int
	serve   int
	skill   float64
}

func newTennis(seed uint64) *tennis {
	g := &tennis{base: newBase(seed, 0x73636f70)}
	g.py, g.ay, g.skill = tnH/2, tnH/2, 0.6
	g.reset(1)
	return g
}

func (g *tennis) reset(dir float64) {
	g.bx, g.by = tnW/2, tnH/2
	a := (g.rng.Float64()*2 - 1) * 0.5
	g.vx, g.vy = dir*2.4*math.Cos(a), 2.4*math.Sin(a)
	g.serve = 50
	g.trail = g.trail[:0]
}

func (g *tennis) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newTennis(g.rng.Uint64())
	}
}

func (g *tennis) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *tennis) tick() {
	if g.over {
		return
	}
	h := g.held
	if up(h) || h("k") {
		g.py -= 3.2
	}
	if down(h) || h("j") {
		g.py += 3.2
	}
	g.py = min(max(g.py, tnPad/2), tnH-tnPad/2)
	// The machine follows the ball, late and capped by its skill.
	target := tnH / 2
	if g.vx > 0 {
		target = g.by
	}
	g.ay += max(-3*g.skill, min(3*g.skill, (target-g.ay)*0.12*g.skill))
	g.ay = min(max(g.ay, tnPad/2), tnH-tnPad/2)
	if g.serve > 0 {
		g.serve--
		return
	}
	g.bx += g.vx
	g.by += g.vy
	if g.by < 2 || g.by > tnH-2 {
		g.vy = -g.vy
		g.by = min(max(g.by, 2), tnH-2)
	}
	hit := func(py float64) {
		off := (g.by - py) / (tnPad / 2)
		sp := min(6, math.Hypot(g.vx, g.vy)*1.06)
		a := off * 0.9
		dir := -math.Copysign(1, g.vx)
		g.vx, g.vy = dir*sp*math.Cos(a), sp*math.Sin(a)
		g.sparks(g.bx, g.by, 6, 2, 1)
	}
	if g.vx < 0 && g.bx <= 10 && g.bx >= 4 && math.Abs(g.by-g.py) < tnPad/2+2 {
		hit(g.py)
	}
	if g.vx > 0 && g.bx >= tnW-10 && g.bx <= tnW-4 && math.Abs(g.by-g.ay) < tnPad/2+2 {
		hit(g.ay)
	}
	g.trail = append(g.trail, [2]float64{g.bx, g.by})
	if len(g.trail) > tnTrail {
		g.trail = g.trail[1:]
	}
	switch {
	case g.bx < -6:
		g.cpu++
		g.reset(1)
	case g.bx > tnW+6:
		g.me++
		g.skill = min(1.6, g.skill+0.15)
		g.reset(-1)
	}
	if g.me == tnWin || g.cpu == tnWin {
		g.over = true
		record("scope", g.me)
	}
}

func (g *tennis) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	x0 := math.Floor((v.vw() - tnW) / 2)
	ph := th.Accent2 // the phosphor
	v.hud(x0, tnTop, tnW, tnH, theme.Mix(th.BG, black, 0.4), 0.6)
	v.hud(x0, tnTop, tnW, 1, ph, 0.6)
	v.hud(x0, tnTop+tnH-1, tnW, 1, ph, 0.6)
	for y := 0.0; y < tnH; y += 8 {
		v.hud(x0+tnW/2, tnTop+y, 1, 4, ph, 0.3)
	}
	paddle := func(x, y float64) {
		v.hud(x-1, tnTop+y-tnPad/2-1, 5, tnPad+2, ph, 0.25)
		v.hud(x, tnTop+y-tnPad/2, 3, tnPad, ph, 1)
	}
	paddle(x0+4, g.py)
	paddle(x0+tnW-7, g.ay)
	for i, t := range g.trail {
		a := float32(i+1) / float32(len(g.trail)+1)
		v.hud(x0+math.Floor(t[0])-1, tnTop+math.Floor(t[1])-1, 3, 3, ph, a*0.5)
	}
	if !g.over {
		bx, by := x0+math.Floor(g.bx), tnTop+math.Floor(g.by)
		v.hud(bx-3, by-3, 7, 7, ph, 0.25)
		v.hud(bx-1, by-1, 3, 3, th.FG, 1)
	}
	g.drawParts(v, x0, tnTop)
	v.text(strconv.Itoa(g.me), x0+tnW/2-30, 6, ph, 1)
	v.text(strconv.Itoa(g.cpu), x0+tnW/2+24, 6, ph, 1)
	title := "YOU WIN"
	if g.cpu == tnWin {
		title = "YOU LOSE"
	}
	g.ending(v, title, strconv.Itoa(g.me)+" - "+strconv.Itoa(g.cpu))
}
