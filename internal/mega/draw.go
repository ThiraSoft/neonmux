package mega

import (
	"image/color"
	"math"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

var black = color.RGBA{0, 0, 0, 255}

// Scale returns the size of a game pixel on a screen h pixels high: the
// level fills the height, in whole pixels.
func Scale(h int) int { return max(1, h/ViewH) }

// view holds what a frame needs to place game pixels on the screen.
type view struct {
	P      *gfx.Painter
	f      *gfx.Font
	th     *theme.Theme
	s      float32 // screen pixels per game pixel
	cam    float64
	oy     float32 // screen y of the top of the level
	W, H   float32
	weapon color.RGBA
}

// rect fills a rectangle given in level coordinates.
func (v *view) rect(x, y, w, h float64, c color.RGBA, a float32) {
	sx := float32(math.Floor(x)-v.cam) * v.s
	sy := v.oy + float32(math.Floor(y))*v.s
	v.P.Rect(sx, sy, float32(w)*v.s, float32(h)*v.s, c, a)
}

// hud fills a rectangle given in view coordinates (game pixels).
func (v *view) hud(x, y, w, h float64, c color.RGBA, a float32) {
	v.P.Rect(float32(x)*v.s, v.oy+float32(y)*v.s, float32(w)*v.s, float32(h)*v.s, c, a)
}

// sprite draws s with its top left corner at x, y,
// mirrored when face < 0. pal maps the letters to colors.
func (v *view) sprite(s sprite, x, y float64, face float64, pal func(byte) color.RGBA, a float32) {
	x, y = math.Floor(x), math.Floor(y)
	for r, row := range s.rows {
		for c := 0; c < len(row); {
			ch := row[c]
			n := 1
			for c+n < len(row) && row[c+n] == ch {
				n++
			}
			if ch != '.' {
				px := x + float64(c)
				if face < 0 {
					px = x + float64(s.w-c-n)
				}
				v.rect(px, y+float64(r), float64(n), 1, pal(ch), a)
			}
			c += n
		}
	}
}

// palette returns the letter mapping of a sprite whose main color is main.
func (v *view) palette(main color.RGBA, flash bool) func(byte) color.RGBA {
	th := v.th
	outline := theme.Mix(th.BG, black, 0.6)
	return func(ch byte) color.RGBA {
		if flash && ch != 'K' {
			return th.FG
		}
		switch ch {
		case 'K':
			return outline
		case 'P':
			return main
		case 'S':
			return theme.Mix(main, black, 0.35)
		case 'W':
			return theme.Mix(main, th.FG, 0.7)
		case 'F':
			return th.FG
		case 'E':
			return th.Warn
		case 'D':
			return theme.Mix(th.Dim, th.BG, 0.2)
		}
		return main
	}
}

func (v *view) weaponColor(w int) color.RGBA {
	th := v.th
	switch w {
	case fan:
		return th.Accent
	case lance:
		return th.Warn
	case mortar:
		return theme.Mix(th.FG, th.Accent, 0.25)
	}
	return th.Accent2
}

func (v *view) partColor(k int) color.RGBA {
	switch k {
	case 1:
		return v.th.Accent
	case 2:
		return v.weapon
	case 3:
		return v.th.Warn
	}
	return v.th.FG
}

// Draw paints the game on a screen W x H, over the background already
// drawn there.
func (g *Game) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	s := Scale(H)
	v := &view{P: P, f: f, th: th, s: float32(s), cam: math.Floor(g.cam),
		oy: float32(H - ViewH*s), W: float32(W), H: float32(H)}
	v.weapon = v.weaponColor(g.weapon)
	if g.shake > 0 {
		v.oy += float32((g.frame%3)-1) * v.s
	}
	// A veil over the city, so that the game stands out.
	P.Rect(0, 0, v.W, v.H, th.BG, 0.35)
	g.drawTiles(v)
	for _, it := range g.items {
		if it.life < 90 && g.frame/4%2 == 0 {
			continue
		}
		c := th.FG
		if it.energy {
			c = v.weapon
		}
		v.rect(it.x, it.y+1, 8, 6, theme.Mix(th.BG, black, 0.6), 1)
		v.rect(it.x+1, it.y, 6, 8, theme.Mix(th.BG, black, 0.6), 1)
		v.rect(it.x+1, it.y+1, 6, 6, c, 1)
		v.rect(it.x+2, it.y+2, 2, 2, th.FG, 1)
	}
	for _, fo := range g.foes {
		g.drawFoe(v, fo)
	}
	if b := g.boss; b != nil {
		g.drawBoss(v, b)
	}
	if g.st != stDead && (g.p.inv == 0 || g.frame/3%2 == 0) {
		g.drawHero(v)
	}
	for _, sh := range g.shots {
		g.drawShot(v, &sh)
	}
	for _, e := range g.ebul {
		switch {
		case e.wave:
			fl := float64(g.frame % 4)
			v.rect(e.x, e.y+2+fl/2, e.w, e.h-2-fl/2, th.Warn, 0.9)
			v.rect(e.x+2, e.y+4+fl, e.w-4, e.h-4-fl, th.FG, 0.9)
		default:
			v.rect(e.x-1, e.y-1, e.w+2, e.h+2, th.Warn, 0.35)
			v.rect(e.x, e.y, e.w, e.h, th.Warn, 1)
			v.rect(e.x+1, e.y+1, max(1, e.w-2), max(1, e.h-2), th.FG, 1)
		}
	}
	for _, pt := range g.parts {
		a := float32(pt.life) / float32(pt.max)
		if pt.size > 3 {
			// The big orbs of an explosion blink.
			if g.frame/4%2 == 0 {
				continue
			}
			a = 1
		}
		c := v.partColor(pt.col)
		v.rect(pt.x-pt.size/2, pt.y-pt.size/2, pt.size, pt.size, c, a)
		if pt.size > 3 {
			v.rect(pt.x-pt.size/4, pt.y-pt.size/4, pt.size/2, pt.size/2, th.FG, a)
		}
	}
	g.drawHUD(v)
}

func (g *Game) drawTiles(v *view) {
	th := v.th
	wall := theme.Mix(theme.Mix(th.BG, black, 0.15), th.Accent2, 0.14)
	edge := theme.Mix(wall, th.Accent2, 0.45)
	dark := theme.Mix(wall, black, 0.3)
	c0 := int(v.cam) / T
	c1 := c0 + int(float64(v.W)/float64(v.s))/T + 2
	for c := c0; c <= c1 && c < g.lv.w; c++ {
		for r := 0; r < Rows; r++ {
			x, y := float64(c*T), float64(r*T)
			switch g.lv.at(c, r) {
			case '#':
				v.rect(x, y, T, T, wall, 1)
				if g.lv.at(c, r-1) != '#' {
					v.rect(x, y, T, 2, edge, 1)
					v.rect(x, y+2, T, 1, dark, 1)
				} else {
					// Two windows per tile, some lit.
					h := uint32(c*7919+r*104729) * 2654435761
					for k := range 2 {
						lit := (h>>(k*7+3))%5 < 2
						col := theme.Mix(wall, black, 0.25)
						if lit {
							col = theme.Mix(wall, th.Warn, 0.55)
							if (h>>(k*5+11))%3 == 0 {
								col = theme.Mix(wall, th.Accent2, 0.6)
							}
						}
						v.rect(x+3+float64(k)*7, y+4, 3, 5, col, 1)
					}
				}
				if g.lv.at(c-1, r) != '#' {
					v.rect(x, y, 1, T, dark, 1)
				}
			case '=':
				v.rect(x, y, T, 6, theme.Mix(th.Dim, th.BG, 0.25), 1)
				v.rect(x, y, T, 1, th.Accent2, 0.8)
				v.rect(x+2, y+2, 2, 2, theme.Mix(th.BG, black, 0.5), 1)
				v.rect(x+10, y+2, 2, 2, theme.Mix(th.BG, black, 0.5), 1)
				v.rect(x, y+5, T, 1, theme.Mix(th.BG, black, 0.5), 1)
			case '^':
				for k := range 4 {
					for i := range 6 {
						w := float64(i/2*2 + 1)
						v.rect(x+float64(k*4)+2-w/2, y+10+float64(i), w, 1, theme.Mix(th.FG, th.Warn, float32(i)/8), 1)
					}
				}
			case 'D':
				if !g.solid(c, r) {
					v.rect(x, y, 1, T, th.Accent, 0.25)
					v.rect(x+T-1, y, 1, T, th.Accent, 0.25)
					continue
				}
				v.rect(x, y, T, T, theme.Mix(th.Dim, th.BG, 0.4), 1)
				for k := 0; k < T; k += 4 {
					v.rect(x, y+float64(k), T, 1, theme.Mix(th.BG, black, 0.4), 1)
				}
				v.rect(x+7, y, 2, T, th.Accent, 0.7)
			}
		}
	}
	// Checkpoints: a beacon on the roof, lit once reached.
	for _, c := range g.lv.cps {
		x, y := float64(c*T)+7, float64(g.lv.top(c)*T)
		v.rect(x, y-10, 1, 10, th.Dim, 1)
		col, a := th.Dim, float32(1)
		if c <= g.cp {
			col = th.Accent2
			if g.frame/20%2 == 0 {
				a = 0.6
			}
		}
		v.rect(x-1, y-13, 3, 3, col, a)
	}
}

func (g *Game) drawHero(v *view) {
	p := &g.p
	s := heroStand
	switch {
	case !p.ground:
		s = heroJump
	case p.vx != 0:
		s = heroRun[p.anim/8%2]
	}
	pal := v.palette(v.weapon, p.stun > 0 && g.frame/2%2 == 0)
	x := p.x - 1
	y := p.y + p.h - float64(s.h)
	v.sprite(s, x, y, p.face, pal, 1)
	if p.shoot > 0 {
		cx := x + 12
		if p.face < 0 {
			cx = x + float64(s.w) - 12 - float64(cannon.w)
		}
		v.sprite(cannon, cx, y+10, p.face, pal, 1)
	}
}

func (g *Game) drawFoe(v *view, f *foe) {
	th := v.th
	pal := v.palette(th.Accent, f.flash > 0)
	var s sprite
	switch f.kind {
	case walker:
		s = walkerSpr[f.t/10%2]
	case flyer:
		s = flyerSpr[f.t/3%2]
	case turret:
		s = turretShut
		if f.open {
			s = turretOpen
		}
	case hopper:
		s = hopperSpr
		if f.ground && f.t%60 > 45 {
			s = hopperSquat
		}
	}
	v.sprite(s, f.cx()-float64(s.w)/2, f.y+f.h-float64(s.h), f.face, pal, 1)
}

func (g *Game) drawBoss(v *view, b *boss) {
	th := v.th
	main := [4]color.RGBA{th.Warn, th.Accent2, th.Accent, theme.Mix(th.Accent, th.FG, 0.35)}[b.kind]
	pal := v.palette(main, b.inv > 8)
	s := guardSpr
	if b.flying {
		s = sirenSpr
	}
	x := b.cx() - float64(s.w)/2
	y := b.y + b.h - float64(s.h)
	v.sprite(s, x, y, b.face, pal, 1)
	o := theme.Mix(th.BG, black, 0.6)
	switch b.kind {
	case 0: // two lightning horns
		for _, hx := range []float64{x + 6, x + 16} {
			v.rect(hx, y-5, 2, 3, o, 1)
			v.rect(hx+1, y-7, 2, 3, o, 1)
			v.rect(hx, y-4, 1, 3, th.FG, 1)
			v.rect(hx+1, y-6, 1, 3, th.FG, 1)
		}
	case 2: // the crane's hook on its back
		v.rect(x+11, y-8, 2, 8, o, 1)
		v.rect(x+8, y-9, 8, 2, o, 1)
		v.rect(x+9, y-8, 6, 1, th.Warn, 1)
	case 3: // a crown
		v.rect(x+6, y-4, 12, 4, o, 1)
		for k := range 3 {
			v.rect(x+7+float64(k)*4, y-6, 2, 3, th.Warn, 1)
		}
		v.rect(x+7, y-3, 10, 2, th.Warn, 1)
	}
}

func (g *Game) drawShot(v *view, s *shot) {
	th := v.th
	c := v.weaponColor(s.kind)
	switch {
	case s.boom:
		k := float64(18-s.life) / 18
		r := 6 + 14*k
		cx, cy := s.cx(), s.cy()
		a := float32(1 - k*0.7)
		v.rect(cx-r, cy-r/2, 2*r, r, c, a*0.6)
		v.rect(cx-r/2, cy-r, r, 2*r, c, a*0.6)
		v.rect(cx-r/2, cy-r/2, r, r, th.FG, a)
	case s.kind == lance:
		v.rect(s.x, s.y-1, s.w, s.h+2, c, 0.4)
		v.rect(s.x, s.y, s.w, s.h, c, 1)
		v.rect(s.x+2, s.y+1, s.w-4, 1, th.FG, 1)
	case s.kind == mortar:
		v.rect(s.x+1, s.y, 4, 6, c, 1)
		v.rect(s.x, s.y+1, 6, 4, c, 1)
		v.rect(s.x+2, s.y+1, 2, 2, th.FG, 1)
	default:
		v.rect(s.x, s.y+1, s.w, s.h-2, c, 1)
		v.rect(s.x+1, s.y, s.w-2, s.h, c, 1)
		v.rect(s.x+1, s.y+1, s.w-2, s.h-2, th.FG, 1)
	}
}

// bar draws a vertical gauge of maxHP notches, filled to n.
func (v *view) bar(x, y float64, n int, c color.RGBA) {
	th := v.th
	v.hud(x-1, y-1, 8, maxHP*2+2, theme.Mix(th.BG, black, 0.6), 1)
	for i := range maxHP {
		col, a := theme.Mix(th.BG, black, 0.3), float32(1)
		if maxHP-i <= n {
			col = c
		}
		v.hud(x, y+float64(i*2), 6, 1, col, a)
		if maxHP-i <= n {
			v.hud(x+2, y+float64(i*2), 2, 1, th.FG, 0.8)
		}
	}
}

// text writes s at view coordinates (game pixels), in the UI font scaled
// to about 8 game pixels high.
func (v *view) text(s string, x, y float64, c color.RGBA, a float32) float64 {
	sc := v.textScale()
	sx, sy := float32(x)*v.s, v.oy+float32(y)*v.s
	for _, r := range s {
		if r != ' ' {
			v.P.GlyphScaled(v.f.Glyph(r, 0, false), sx, sy, sc, c, a)
		}
		sx += float32(v.f.CellW) * sc
	}
	return float64(v.textW(s))
}

func (v *view) textScale() float32 {
	return max(1, float32(math.Round(float64(9*v.s)/float64(v.f.CellH))))
}

// textW returns the width of s in game pixels.
func (v *view) textW(s string) float32 {
	n := 0
	for range s {
		n++
	}
	return float32(n*v.f.CellW) * v.textScale() / v.s
}

// center writes s centered on the view at height y.
func (v *view) center(s string, y float64, c color.RGBA, a float32) {
	vw := float64(v.W / v.s)
	x := (vw - float64(v.textW(s))) / 2
	v.text(s, x+1, y+1, theme.Mix(v.th.BG, black, 0.6), a)
	v.text(s, x, y, c, a)
}

func (g *Game) drawHUD(v *view) {
	th := v.th
	vw := float64(v.W / v.s)
	v.bar(10, 12, g.p.hp, th.FG)
	if g.weapon != pulse {
		v.bar(20, 12, g.energy[g.weapon], v.weapon)
	}
	v.text(weaponNames[g.weapon], 32, 12, v.weapon, 1)
	if b := g.boss; b != nil {
		v.bar(vw-18, 12, max(0, b.hp), th.Warn)
		name := bossNames[b.kind]
		v.text(name, vw-24-float64(v.textW(name)), 12, th.Warn, 1)
	}
	switch g.st {
	case stReady:
		if g.stT/8%2 == 0 {
			v.center("READY", ViewH/2-20, th.FG, 1)
		}
	case stIntro:
		v.center(bossNames[g.boss.kind], ViewH/2-20, th.Warn, 1)
	case stWin:
		v.center("CITY CLEAR", ViewH/2-40, th.Accent2, 1)
		sec := g.timeF / 60
		v.center("time "+strconv.Itoa(sec/60)+":"+pad2(sec%60), ViewH/2-24, th.FG, 1)
		if g.stT > 60 {
			v.center("space: again    esc: leave", ViewH/2-8, th.Dim, 1)
		}
	}
	if g.bannerT > 0 && g.st != stWin && g.frame/10%4 != 0 {
		v.center(g.banner, ViewH/2-56, v.weapon, 1)
	}
	if g.Paused && !g.inv {
		v.center("PAUSE", ViewH/2-20, th.FG, 1)
	}
	if g.inv {
		g.drawInventory(v)
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func (g *Game) drawInventory(v *view) {
	th := v.th
	vw := float64(v.W / v.s)
	const pw, ph = 200, 120
	x0, y0 := math.Floor((vw-pw)/2), math.Floor((ViewH-ph)/2)-10
	v.hud(0, -float64(v.oy/v.s), vw, float64(v.H/v.s), th.BG, 0.5)
	v.hud(x0-2, y0-2, pw+4, ph+4, theme.Mix(th.BG, black, 0.6), 1)
	v.hud(x0, y0, pw, ph, theme.Mix(th.BG, th.Accent2, 0.08), 1)
	v.hud(x0, y0, pw, 1, th.Accent2, 1)
	v.hud(x0, y0+ph-1, pw, 1, th.Accent2, 1)
	v.text("WEAPONS", x0+8, y0+6, th.Accent2, 1)
	for w := range nWeapons {
		cx := x0 + 8 + float64(w%2)*96
		cy := y0 + 24 + float64(w/2)*38
		c := v.weaponColor(w)
		if w == g.invCur && g.frame/12%3 != 0 {
			v.hud(cx-3, cy-3, 92, 34, th.Accent, 1)
			v.hud(cx-2, cy-2, 90, 32, theme.Mix(th.BG, th.Accent2, 0.08), 1)
		}
		if !g.unlocked[w] {
			v.text("???", cx+16, cy+4, th.Dim, 1)
			continue
		}
		v.hud(cx, cy+4, 10, 10, theme.Mix(th.BG, black, 0.6), 1)
		v.hud(cx+1, cy+5, 8, 8, c, 1)
		v.hud(cx+3, cy+7, 3, 3, th.FG, 1)
		v.text(weaponNames[w], cx+16, cy+4, c, 1)
		if w == g.weapon {
			v.hud(cx+16, cy+14, float64(v.textW(weaponNames[w])), 1, c, 1)
		}
		if w != pulse {
			for i := range maxEn / 2 {
				col := theme.Mix(th.BG, black, 0.3)
				if i*2 < g.energy[w] {
					col = c
				}
				v.hud(cx+16+float64(i)*4, cy+20, 3, 3, col, 1)
			}
		}
	}
	v.text("zqsd/hjkl  move    i  close", x0+8, y0+ph-14, th.Dim, 1)
}
