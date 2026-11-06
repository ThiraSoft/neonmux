package mega

import (
	"math"
	"math/rand/v2"
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/sfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Stars: a side shooter in front of the star field. zqsd moves the ship,
// j (held) or space shoots. Waves of drifters, divers and gunners, a
// mothership every third wave. Power-ups widen the shot or give a shield.

var shipSpr = spr(
	"KKKK............",
	"KPPPKK..........",
	".KSPPPKKKK......",
	".KPPPPPPFFKKK...",
	"KPPWPPPPPPPPPKK.",
	".KPPPPPPFFKKK...",
	".KSPPPKKKK......",
	"KPPPKK..........",
	"KKKK............",
)

var drifterSpr = spr(
	"....KKKK....",
	"..KKPPPPKK..",
	".KPPWPPPPPK.",
	"KPPKEEEEKPPK",
	"KPPKEEEEKPPK",
	".KPPPPPPPPK.",
	"..KKSSSSKK..",
	"....KKKK....",
)

var diverSpr = spr(
	"........KKKK",
	"......KKPPSK",
	"....KKPPPPK.",
	"..KKPPWPPK..",
	"KKEEPPPPPK..",
	"KKEEPPPPPK..",
	"..KKPPPPPK..",
	"....KKPPPPK.",
	"......KKPPSK",
	"........KKKK",
)

var gunnerSpr = spr(
	"....KKKKKK....",
	"..KKDDDDDDKK..",
	".KDDKKKKKKDDK.",
	"KPPKEEEEEEKPPK",
	"KPPPKKKKKKPPPK",
	"KSPPPPWPPPPPSK",
	"KSPPPPPPPPPPSK",
	"KPPPKKKKKKPPPK",
	"KPPKEEEEEEKPPK",
	".KDDKKKKKKDDK.",
	"..KKDDDDDDKK..",
	"....KKKKKK....",
)

const (
	sDrifter = iota
	sDiver
	sGunner
	sBoss
)

type sFoe struct {
	body
	kind, hp, maxHP int
	t               int
	baseY, phase    float64
	flash           int
	aimed           bool
}

type sSpawn struct {
	at   int
	kind int
	y    float64
}

type sBonus struct {
	body
	shield bool
}

type stars struct {
	rng    *rand.Rand
	held   func(string) bool
	vw     float64
	frame  int
	acc    float64
	paused bool

	p        body
	lives    int
	power    int
	shield   bool
	inv      int
	cool     int
	dead     int // frames before the respawn
	score    int
	over     bool
	overT    int
	wave     int
	waveT    int
	queue    []sSpawn
	banner   string
	bannerT  int
	shots    []shot
	foes     []*sFoe
	bullets  []ebullet
	bonus    []sBonus
	parts    []part
	fireHeld bool
}

func newStars(seed uint64) *stars {
	g := &stars{rng: rand.New(rand.NewPCG(seed, 0x73746172)), vw: 480, held: func(string) bool { return false }}
	g.lives, g.power = 3, 1
	g.p = body{x: 40, y: ViewH / 2, w: 10, h: 5}
	g.nextWave()
	return g
}

func (g *stars) SetHeld(h func(string) bool) { g.held = h }
func (g *stars) Back() bool                  { return false }

func (g *stars) Key(name string, repeat bool) {
	if g.over && !repeat && g.overT > 45 && (name == "space" || name == "enter") {
		*g = *newStars(g.rng.Uint64())
	}
}

func (g *stars) Step(dt float64, W, H int, paused bool) {
	g.vw = float64(W) / float64(Scale(H))
	g.paused = paused
	g.frame++
	if paused {
		g.acc = 0
		return
	}
	g.acc += min(dt, 0.1)
	for n := 0; g.acc >= step && n < 6; n++ {
		g.acc -= step
		g.tick()
	}
}

func (g *stars) nextWave() {
	g.wave++
	g.waveT = 0
	g.queue = g.queue[:0]
	w := g.wave
	if w%3 == 0 {
		g.queue = append(g.queue, sSpawn{at: 90, kind: sBoss, y: ViewH / 2})
		g.banner, g.bannerT = "WARNING", 150
		return
	}
	g.banner, g.bannerT = "WAVE "+strconv.Itoa(w), 120
	at := 60
	y := 40 + g.rng.Float64()*(ViewH-80)
	for i := range 5 + w/2 {
		g.queue = append(g.queue, sSpawn{at: at + i*18, kind: sDrifter, y: y})
	}
	at += 160
	for i := range min(2+w, 9) {
		g.queue = append(g.queue, sSpawn{at: at + i*30, kind: sDiver, y: 24 + g.rng.Float64()*(ViewH-48)})
	}
	at += 120
	for i := range 1 + w/3 {
		g.queue = append(g.queue, sSpawn{at: at + i*90, kind: sGunner, y: 40 + g.rng.Float64()*(ViewH-80)})
	}
}

func (g *stars) tick() {
	if g.bannerT > 0 {
		g.bannerT--
	}
	g.updateParts()
	if g.over {
		g.overT++
		return
	}
	g.waveT++
	g.player()
	g.spawn()
	g.moveShots()
	g.moveFoes()
	g.moveBullets()
	g.moveBonus()
	if len(g.queue) == 0 && len(g.foes) == 0 {
		g.nextWave()
	}
}

func (g *stars) player() {
	p := &g.p
	if g.dead > 0 {
		if g.dead--; g.dead == 0 {
			if g.lives == 0 {
				g.over = true
				record("stars", g.score)
				sfx.Play(sfx.Die)
				return
			}
			*p = body{x: 30, y: ViewH / 2, w: 10, h: 5}
			g.inv = 120
		}
		return
	}
	if g.inv > 0 {
		g.inv--
	}
	h := g.held
	const sp = 2.2
	p.vx, p.vy = 0, 0
	if left(h) {
		p.vx = -sp
	}
	if right(h) {
		p.vx = sp
	}
	if up(h) {
		p.vy = -sp
	}
	if down(h) {
		p.vy = sp
	}
	p.x = min(max(p.x+p.vx, 4), g.vw-20)
	p.y = min(max(p.y+p.vy, 18), ViewH-10)
	if g.cool > 0 {
		g.cool--
	}
	if (h("j") || h("space")) && g.cool == 0 {
		g.cool = 7
		sfx.Play(sfx.Shoot)
		g.fire()
	}
}

func (g *stars) fire() {
	x, y := g.p.x+14, g.p.y+2
	add := func(dy, vy float64) {
		g.shots = append(g.shots, shot{body: body{x: x, y: y + dy - 1, w: 8, h: 2, vx: 6, vy: vy}, dmg: 1, life: 120})
	}
	switch g.power {
	case 1:
		add(0, 0)
	case 2:
		add(-3, 0)
		add(3, 0)
	default:
		add(-3, 0)
		add(3, 0)
		add(0, -1.2)
		add(0, 1.2)
		if g.power >= 4 {
			add(0, -2.4)
			add(0, 2.4)
		}
	}
}

func (g *stars) spawn() {
	j := 0
	for _, s := range g.queue {
		if s.at > g.waveT {
			g.queue[j] = s
			j++
			continue
		}
		f := &sFoe{kind: s.kind, baseY: s.y, phase: g.rng.Float64() * 6}
		f.x = g.vw + 8
		switch s.kind {
		case sDrifter:
			f.w, f.h, f.hp = 12, 8, 1
		case sDiver:
			f.w, f.h, f.hp = 12, 10, 1
		case sGunner:
			f.w, f.h, f.hp = 14, 12, 6
		case sBoss:
			f.w, f.h, f.hp = 48, 36, 40+15*(g.wave/3)
		}
		f.maxHP = f.hp
		f.y = s.y - f.h/2
		g.foes = append(g.foes, f)
	}
	g.queue = g.queue[:j]
}

func (g *stars) aim(x, y, speed float64, n int, spread float64) {
	a := math.Atan2(g.p.cy()-y, g.p.cx()-x)
	for k := range n {
		da := (float64(k) - float64(n-1)/2) * spread
		g.bullets = append(g.bullets, ebullet{body: body{x: x - 2, y: y - 2, w: 4, h: 4,
			vx: speed * math.Cos(a+da), vy: speed * math.Sin(a+da)}, life: 400})
	}
}

func (g *stars) moveFoes() {
	j := 0
	for _, f := range g.foes {
		f.t++
		if f.flash > 0 {
			f.flash--
		}
		switch f.kind {
		case sDrifter:
			f.x -= 1.3
			f.y = f.baseY - f.h/2 + 22*math.Sin(float64(f.t)*0.05+f.phase)
		case sDiver:
			if !f.aimed && f.x < g.vw*0.72 {
				f.aimed = true
				a := math.Atan2(g.p.cy()-f.cy(), g.p.cx()-f.cx())
				f.vx, f.vy = 2.6*math.Cos(a), 2.6*math.Sin(a)
			}
			if !f.aimed {
				f.vx, f.vy = -1.8, 0
			}
			f.x += f.vx
			f.y += f.vy
		case sGunner:
			if f.t < 400 {
				f.x += (g.vw - 60 - f.x) * 0.03
			} else {
				f.x -= 1.5
			}
			f.y = f.baseY - f.h/2 + 30*math.Sin(float64(f.t)*0.02+f.phase)
			if f.t%80 == 40 {
				g.aim(f.x, f.cy(), 2, 1, 0)
			}
		case sBoss:
			f.x += (g.vw - 70 - f.x) * 0.02
			f.y = ViewH/2 - f.h/2 + 60*math.Sin(float64(f.t)*0.012)
			rage := f.hp < f.maxHP/2
			if f.t%60 == 30 {
				g.aim(f.x+6, f.cy(), 2.2, 5, 0.25)
			}
			if f.t%150 == 100 || rage && f.t%90 == 45 {
				for k := range 12 {
					a := float64(k)*math.Pi/6 + float64(f.t)*0.01
					g.bullets = append(g.bullets, ebullet{body: body{x: f.cx() - 2, y: f.cy() - 2, w: 4, h: 4,
						vx: 1.6 * math.Cos(a), vy: 1.6 * math.Sin(a)}, life: 400})
				}
			}
		}
		if g.dead == 0 && g.inv == 0 && overlap(&f.body, &g.p) {
			g.hit()
			if f.kind != sBoss {
				f.hp = 0
				g.boom(f.cx(), f.cy(), 10)
			}
		}
		out := f.x < -40 || f.y < -60 || f.y > ViewH+60
		if f.hp > 0 && !out {
			g.foes[j] = f
			j++
		}
	}
	clear(g.foes[j:])
	g.foes = g.foes[:j]
}

func (g *stars) moveShots() {
	j := 0
	for _, s := range g.shots {
		s.x += s.vx
		s.y += s.vy
		s.life--
		for _, f := range g.foes {
			if f.hp > 0 && overlap(&s.body, &f.body) {
				s.life = 0
				f.hp -= s.dmg
				f.flash = 4
				if f.hp <= 0 {
					g.kill(f)
				}
				break
			}
		}
		if s.life > 0 && s.x < g.vw+10 && s.y > -10 && s.y < ViewH+10 {
			g.shots[j] = s
			j++
		}
	}
	g.shots = g.shots[:j]
}

func (g *stars) kill(f *sFoe) {
	pts := [4]int{100, 150, 300, 2000}[f.kind]
	g.score += pts
	sfx.Play(sfx.Boom)
	n := 10
	if f.kind == sBoss {
		n = 60
		g.bullets = g.bullets[:0]
	}
	g.boom(f.cx(), f.cy(), n)
	chance := [4]float64{0.08, 0.1, 0.5, 1}[f.kind]
	if g.rng.Float64() < chance {
		g.bonus = append(g.bonus, sBonus{body: body{x: f.cx() - 4, y: f.cy() - 4, w: 8, h: 8}, shield: g.rng.IntN(3) == 0})
	}
}

func (g *stars) hit() {
	if g.shield {
		g.shield = false
		sfx.Play(sfx.Hit)
		g.inv = 60
		g.boom(g.p.cx(), g.p.cy(), 6)
		return
	}
	g.lives--
	g.power = max(1, g.power-1)
	sfx.Play(sfx.Hurt)
	g.dead = 90
	g.boom(g.p.cx(), g.p.cy(), 24)
}

func (g *stars) moveBullets() {
	j := 0
	for _, b := range g.bullets {
		b.x += b.vx
		b.y += b.vy
		b.life--
		if g.dead == 0 && g.inv == 0 && overlap(&b.body, &g.p) {
			g.hit()
			continue
		}
		if b.life > 0 && b.x > -8 && b.x < g.vw+8 && b.y > -8 && b.y < ViewH+8 {
			g.bullets[j] = b
			j++
		}
	}
	g.bullets = g.bullets[:j]
}

func (g *stars) moveBonus() {
	j := 0
	for _, b := range g.bonus {
		b.x -= 0.8
		pick := b.body
		pick.x, pick.y, pick.w, pick.h = b.x-4, b.y-4, 16, 16
		if g.dead == 0 && overlap(&pick, &g.p) {
			sfx.Play(sfx.Pickup)
			switch {
			case b.shield:
				g.shield = true
			case g.power < 4:
				g.power++
			default:
				g.score += 500
			}
			continue
		}
		if b.x > -10 {
			g.bonus[j] = b
			j++
		}
	}
	g.bonus = g.bonus[:j]
}

func (g *stars) boom(x, y float64, n int) {
	for range n {
		a := g.rng.Float64() * 2 * math.Pi
		s := 0.4 + g.rng.Float64()*2.2
		l := 16 + g.rng.IntN(20)
		g.parts = append(g.parts, part{x: x, y: y, vx: math.Cos(a) * s, vy: math.Sin(a) * s, life: l, max: l, size: 2, col: 1 + g.rng.IntN(3)})
	}
}

func (g *stars) updateParts() {
	j := 0
	for _, p := range g.parts {
		p.x += p.vx
		p.y += p.vy
		p.vx *= 0.96
		p.vy *= 0.96
		if p.life--; p.life > 0 {
			g.parts[j] = p
			j++
		}
	}
	g.parts = g.parts[:j]
}

func (g *stars) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	P.Rect(0, 0, v.W, v.H, th.BG, 0.2)
	for _, b := range g.bonus {
		c := th.Accent
		if b.shield {
			c = th.Accent2
		}
		if g.frame/6%2 == 0 {
			v.rect(b.x-1, b.y-1, 10, 10, c, 0.3)
		}
		v.rect(b.x, b.y, 8, 8, theme.Mix(th.BG, black, 0.6), 1)
		v.rect(b.x+1, b.y+1, 6, 6, c, 1)
		v.rect(b.x+3, b.y+2, 2, 4, th.FG, 1)
	}
	for _, fo := range g.foes {
		pal := v.palette(th.Accent, fo.flash > 0)
		switch fo.kind {
		case sDrifter:
			v.sprite(drifterSpr, fo.x, fo.y, 1, pal, 1)
		case sDiver:
			v.sprite(diverSpr, fo.x, fo.y, 1, pal, 1)
		case sGunner:
			v.sprite(gunnerSpr, fo.x+fo.w/2-7, fo.y, 1, v.palette(th.Warn, fo.flash > 0), 1)
		case sBoss:
			g.drawMother(v, fo)
		}
	}
	if g.dead == 0 && !g.over && (g.inv == 0 || g.frame/3%2 == 0) {
		x, y := g.p.x-3, g.p.y-2
		v.sprite(shipSpr, x, y, 1, v.palette(th.Accent2, false), 1)
		fl := float64(2 + g.frame%3)
		v.rect(x-fl, y+4, fl, 1, th.Warn, 1)
		if g.shield {
			v.rect(x-3, y-3, 22, 1, th.Accent2, 0.7)
			v.rect(x-3, y+11, 22, 1, th.Accent2, 0.7)
			v.rect(x+19, y-2, 1, 13, th.Accent2, 0.7)
		}
	}
	for _, s := range g.shots {
		v.rect(s.x, s.y, s.w, s.h, th.Accent2, 1)
		v.rect(s.x+2, s.y, s.w-2, 1, th.FG, 1)
	}
	for _, b := range g.bullets {
		v.rect(b.x-1, b.y-1, 6, 6, th.Warn, 0.35)
		v.rect(b.x, b.y, 4, 4, th.Warn, 1)
		v.rect(b.x+1, b.y+1, 2, 2, th.FG, 1)
	}
	for _, p := range g.parts {
		v.rect(p.x, p.y, p.size, p.size, v.partColor(p.col), float32(p.life)/float32(p.max))
	}
	g.hud(v)
}

func (g *stars) drawMother(v *view, f *sFoe) {
	th := v.th
	o := theme.Mix(th.BG, black, 0.6)
	hull := theme.Mix(th.Accent, th.BG, 0.25)
	if f.flash > 0 {
		hull = th.FG
	}
	x, y := f.x, f.y
	v.rect(x+8, y, 32, 36, o, 1)
	v.rect(x, y+8, 48, 20, o, 1)
	v.rect(x+9, y+1, 30, 34, hull, 1)
	v.rect(x+1, y+9, 46, 18, hull, 1)
	v.rect(x+4, y+4, 10, 4, theme.Mix(hull, black, 0.35), 1)
	v.rect(x+4, y+28, 10, 4, theme.Mix(hull, black, 0.35), 1)
	v.rect(x+16, y+12, 14, 12, o, 1)
	pulse := float32(0.6 + 0.4*math.Sin(float64(g.frame)*0.2))
	v.rect(x+18, y+14, 10, 8, th.Warn, pulse)
	v.rect(x+21, y+16, 4, 4, th.FG, 1)
	for k := range 3 {
		v.rect(x+40, y+6+float64(k)*11, 6, 3, th.Accent2, 1)
	}
	vw := v.vw()
	w := vw * 0.4
	v.hud((vw-w)/2, 10, w, 3, o, 1)
	v.hud((vw-w)/2, 10, w*float64(f.hp)/float64(f.maxHP), 3, th.Warn, 1)
}

func (g *stars) hud(v *view) {
	th := v.th
	v.text(strconv.Itoa(g.score), 8, 4, th.FG, 1)
	for i := range g.lives {
		x := v.vw() - 14 - float64(i)*12
		v.hud(x, 6, 8, 3, th.Accent2, 1)
		v.hud(x, 5, 3, 5, th.Accent2, 1)
	}
	for i := range 4 {
		c := theme.Mix(th.BG, black, 0.3)
		if i < g.power {
			c = th.Accent
		}
		v.hud(8+float64(i)*5, 16, 4, 2, c, 1)
	}
	if g.bannerT > 0 && g.frame/10%3 != 0 {
		c := th.FG
		if g.banner == "WARNING" {
			c = th.Warn
		}
		v.center(g.banner, ViewH/2-30, c, 1)
	}
	if g.over {
		v.center("GAME OVER", ViewH/2-30, th.Warn, 1)
		v.center("score "+strconv.Itoa(g.score)+"   best "+strconv.Itoa(best["stars"]), ViewH/2-14, th.FG, 1)
		if g.overT > 45 {
			v.center("space: again    esc: leave", ViewH/2+2, th.Dim, 1)
		}
	}
	if g.paused {
		v.center("PAUSE", ViewH/2-30, th.FG, 1)
	}
}
