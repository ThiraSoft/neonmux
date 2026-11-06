package mega

import (
	"math"
	"math/rand/v2"
)

// Physics, in game pixels and frames (60 per second).
const (
	step     = 1.0 / 60
	grav     = 0.25
	maxFall  = 7
	runSpeed = 1.5
	jumpV    = -5.4
	maxHP    = 28
	maxEn    = 28
)

// Weapons, in the order of the inventory. Each guardian beaten gives the
// next one.
const (
	pulse = iota
	fan
	lance
	mortar
	nWeapons
)

var weaponNames = [nWeapons]string{"PULSE", "FAN", "LANCE", "MORTAR"}
var weaponCost = [nWeapons]int{0, 1, 2, 3}

// Guardians: the weapon each one fears, and their names.
var bossNames = [4]string{"VOLT", "SIREN", "CRANE", "OVERLORD"}
var bossWeak = [4]int{-1, fan, lance, mortar}

type state int

const (
	stReady state = iota // "READY" before playing
	stPlay
	stIntro // a guardian's life bar fills up
	stDead
	stWin
)

// body is anything that moves and collides with the tiles.
type body struct {
	x, y, w, h float64
	vx, vy     float64
	ground     bool
}

func (b *body) cx() float64 { return b.x + b.w/2 }
func (b *body) cy() float64 { return b.y + b.h/2 }

func overlap(a, b *body) bool {
	return a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h
}

type player struct {
	body
	face   float64 // 1 right, -1 left
	hp     int
	inv    int // invincibility frames
	stun   int // knockback frames
	shoot  int // arm held out
	anim   int
	charge int // frames the pulse has been held
}

// Pulse charge, in frames held: a half shot, then a full one.
const (
	chargeHalf = 30
	chargeFull = 80
)

type shot struct {
	body
	kind int
	dmg  int
	life int
	boom bool    // mortar explosion
	big  int     // charged pulse: 1 half, 2 full
	hit  []any   // what a piercing shot already hit
	grav float64 // mortar shells fall
}

type foe struct {
	body
	kind  foeKind
	hp    int
	face  float64
	t     int
	flash int
	sp    int // index of the spawn point
	baseY float64
	open  bool
}

type boss struct {
	body
	kind     int
	hp       int
	face     float64
	t, act   int
	inv      int
	flying   bool
	ax0, ax1 float64 // arena bounds
	airborne bool
	acts     int
}

type ebullet struct {
	body
	grav bool
	wave bool // shockwave on the ground
	life int
}

type item struct {
	body
	energy bool
	life   int
}

type part struct {
	x, y, vx, vy float64
	life, max    int
	size         float64
	col          int // 0 FG, 1 accent, 2 accent2, 3 warn
}

// Game is a running game. Its methods are called from the UI thread.
type Game struct {
	lv   *level
	rng  *rand.Rand
	st   state
	stT  int
	p    player
	cam  float64
	panX float64 // camera moves not yet reported to the background

	shots []shot
	foes  []*foe
	boss  *boss
	ebul  []ebullet
	items []item
	parts []part

	active   []bool // spawn point that has a living foe
	inView   []bool
	cp       int // current checkpoint column
	arena    int // arena the player is in, -1 outside
	entered  []bool
	cleared  []bool
	unlocked [nWeapons]bool
	energy   [nWeapons]int
	weapon   int

	inv    bool // inventory open
	invCur int

	banner  string
	bannerT int
	winIn   int
	shake   int
	frame   int
	timeF   int
	acc     float64
	jumpBuf int
	fireReq bool

	held   func(string) bool
	viewW  float64
	Paused bool
	scale  int
}

// New starts a game at the first checkpoint.
func New(seed uint64) *Game {
	g := &Game{lv: buildLevel(), rng: rand.New(rand.NewPCG(seed, 0x6d656761)), viewW: 480}
	g.active = make([]bool, len(g.lv.spawns))
	g.inView = make([]bool, len(g.lv.spawns))
	g.entered = make([]bool, len(g.lv.arenas))
	g.cleared = make([]bool, len(g.lv.arenas))
	g.unlocked[pulse] = true
	for i := range g.energy {
		g.energy[i] = maxEn
	}
	g.cp = g.lv.cps[0]
	g.held = func(string) bool { return false }
	g.respawn()
	return g
}

// SetHeld gives the game the keys being held, by name.
func (g *Game) SetHeld(h func(string) bool) { g.held = h }

// SetView gives the width of the view, in game pixels.
func (g *Game) SetView(w float64) { g.viewW = max(64, w) }

// PanX returns how far the camera moved since the last call, in game pixels.
func (g *Game) PanX() float64 {
	d := g.panX
	g.panX = 0
	return d
}

// Won reports that the last guardian is beaten.
func (g *Game) Won() bool { return g.st == stWin }

// Key handles a key press (repeat: auto-repeat of a held key).
func (g *Game) Key(name string, repeat bool) {
	if g.inv {
		g.menuKey(name)
		return
	}
	if repeat {
		return
	}
	switch g.st {
	case stWin:
		if g.stT > 60 && (name == "space" || name == "enter") {
			*g = *New(g.rng.Uint64())
		}
		return
	case stDead:
		return
	}
	switch name {
	case "i":
		g.inv, g.invCur = true, g.weapon
	case "j":
		g.fireReq = true
	case "space":
		g.jumpBuf = 6
	}
}

// Back handles escape: it closes the inventory if open, and reports it.
func (g *Game) Back() bool {
	if g.inv {
		g.inv = false
		return true
	}
	return false
}

func (g *Game) menuKey(name string) {
	c := g.invCur
	switch name {
	case "z", "k", "up":
		c -= 2
	case "s", "j", "down":
		c += 2
	case "q", "h", "left":
		c--
	case "d", "l", "right":
		c++
	case "i", "enter", "space":
		g.inv = false
		return
	}
	if c >= 0 && c < nWeapons && (c/2 == g.invCur/2 || c%2 == g.invCur%2) {
		g.invCur = c
	}
	if g.unlocked[g.invCur] {
		g.weapon = g.invCur
	}
}

// Update moves the game forward by dt seconds, in fixed steps.
func (g *Game) Update(dt float64) {
	g.frame++
	if g.Paused || g.inv {
		g.acc = 0
		return
	}
	g.acc += min(dt, 0.1)
	for n := 0; g.acc >= step && n < 6; n++ {
		g.acc -= step
		g.tick()
	}
}

func (g *Game) tick() {
	g.stT++
	if g.bannerT > 0 {
		g.bannerT--
	}
	if g.shake > 0 {
		g.shake--
	}
	g.updateParts()
	switch g.st {
	case stReady:
		g.follow(false)
		if g.stT > 75 {
			g.setState(stPlay)
		}
		return
	case stDead:
		if g.stT > 120 {
			g.respawn()
		}
		return
	case stWin:
		return
	case stIntro:
		g.updatePlayer(false)
		if b := g.boss; b != nil {
			b.hp = min(maxHP, g.stT/2)
			g.moveBody(&b.body, !b.flying)
		}
		g.follow(true)
		if g.stT > 2*maxHP+20 {
			g.setState(stPlay)
		}
		return
	}
	g.timeF++
	g.updatePlayer(true)
	g.updateShots()
	g.spawnFoes()
	g.updateFoes()
	g.updateBoss()
	g.updateBullets()
	g.updateItems()
	g.checkArena()
	g.follow(g.arena >= 0)
	if g.winIn > 0 {
		if g.winIn--; g.winIn == 0 {
			g.setState(stWin)
		}
	}
}

func (g *Game) setState(s state) { g.st, g.stT = s, 0 }

// respawn puts the hero back at the checkpoint, with full health. The foes
// come back, the guardians not yet beaten too.
func (g *Game) respawn() {
	c := g.cp
	g.p = player{body: body{w: 12, h: 20}, face: 1, hp: maxHP}
	g.p.x = float64(c*T) + 2
	g.p.y = float64(g.lv.top(c)*T) - g.p.h
	g.shots, g.foes, g.ebul, g.items = g.shots[:0], g.foes[:0], g.ebul[:0], g.items[:0]
	g.boss = nil
	clear(g.active)
	clear(g.inView)
	for i := range g.entered {
		g.entered[i] = g.cleared[i]
	}
	g.arena = -1
	g.cam = g.camTarget(false)
	g.setState(stReady)
}

// solid reports whether the tile at column c, row r blocks.
func (g *Game) solid(c, r int) bool {
	switch g.lv.at(c, r) {
	case '#', '=':
		return true
	case 'D':
		for i, a := range g.lv.arenas {
			if c == a.in {
				return g.entered[i]
			}
			if c == a.out {
				return !g.cleared[i]
			}
		}
	}
	return false
}

func cell(v float64) int { return int(math.Floor(v / T)) }

// moveBody moves b by its speed, stops it against the tiles and applies
// gravity if fall. It reports a collision on the x axis.
func (g *Game) moveBody(b *body, fall bool) (hitX bool) {
	if fall {
		b.vy = min(b.vy+grav, maxFall)
	}
	b.x += b.vx
	r0, r1 := cell(b.y), cell(b.y+b.h-0.01)
	if b.vx > 0 {
		c := cell(b.x + b.w - 0.01)
		for r := r0; r <= r1; r++ {
			if g.solid(c, r) {
				b.x, hitX = float64(c*T)-b.w, true
				break
			}
		}
	} else if b.vx < 0 {
		c := cell(b.x)
		for r := r0; r <= r1; r++ {
			if g.solid(c, r) {
				b.x, hitX = float64((c+1)*T), true
				break
			}
		}
	}
	b.y += b.vy
	c0, c1 := cell(b.x), cell(b.x+b.w-0.01)
	b.ground = false
	if b.vy > 0 {
		r := cell(b.y + b.h - 0.01)
		for c := c0; c <= c1; c++ {
			if g.solid(c, r) {
				b.y, b.vy, b.ground = float64(r*T)-b.h, 0, true
				break
			}
		}
	} else if b.vy < 0 {
		r := cell(b.y)
		for c := c0; c <= c1; c++ {
			if g.solid(c, r) {
				b.y, b.vy = float64((r+1)*T), 0
				break
			}
		}
	}
	// Standing still on the ground: probe just below.
	if !b.ground && b.vy == 0 {
		r := cell(b.y + b.h + 0.5)
		for c := c0; c <= c1; c++ {
			if g.solid(c, r) {
				b.ground = true
			}
		}
	}
	return hitX
}

func (g *Game) updatePlayer(control bool) {
	p := &g.p
	left := control && (g.held("q") || g.held("left"))
	right := control && (g.held("d") || g.held("right"))
	if p.stun > 0 {
		p.stun--
		p.vx = -p.face * 0.6
	} else {
		p.vx = 0
		if left != right {
			p.face = 1
			if left {
				p.face = -1
			}
			p.vx = p.face * runSpeed
		}
	}
	if p.inv > 0 {
		p.inv--
	}
	if p.shoot > 0 {
		p.shoot--
	}
	if g.jumpBuf > 0 {
		g.jumpBuf--
		if control && p.ground && p.stun == 0 {
			p.vy, p.ground, g.jumpBuf = jumpV, false, 0
		}
	}
	if p.vy < 0 && !(control && g.held("space")) {
		p.vy = max(p.vy, -1)
	}
	g.moveBody(&p.body, true)
	if p.ground && p.vx != 0 {
		p.anim++
	} else {
		p.anim = 0
	}
	if g.fireReq && control && p.stun == 0 {
		g.fire()
	}
	g.fireReq = false
	// Holding the trigger charges the pulse, letting go releases it.
	if control && g.weapon == pulse && g.held("j") {
		p.charge++
	} else {
		if p.charge >= chargeHalf && control && p.stun == 0 {
			g.fireCharged(p.charge >= chargeFull)
		}
		p.charge = 0
	}
	if !control {
		return
	}
	if p.y > ViewH+24 {
		g.die()
		return
	}
	// Spikes: the lower half of their tile.
	spk := body{x: p.x + 2, y: p.y, w: p.w - 4, h: p.h}
	for c := cell(spk.x); c <= cell(spk.x+spk.w); c++ {
		for r := cell(spk.y); r <= cell(spk.y+spk.h); r++ {
			if g.lv.at(c, r) == '^' && overlap(&spk, &body{x: float64(c * T), y: float64(r*T) + 8, w: T, h: 8}) {
				g.die()
				return
			}
		}
	}
	for _, c := range g.lv.cps {
		if c > g.cp && p.x > float64(c*T) {
			g.cp = c
		}
	}
}

// hurt takes dmg points from the hero, unless it just got hit.
func (g *Game) hurt(dmg int, fromX float64) {
	p := &g.p
	if p.inv > 0 || g.st != stPlay {
		return
	}
	if p.hp -= dmg; p.hp <= 0 {
		g.die()
		return
	}
	p.inv, p.stun = 70, 18
	p.face = 1
	if fromX < p.cx() {
		p.face = -1
	}
	p.vy = min(p.vy, -1.5)
	g.burst(p.cx(), p.cy(), 6, 3)
}

func (g *Game) die() {
	if g.st == stDead {
		return
	}
	p := &g.p
	p.hp = 0
	x, y := p.cx(), min(p.cy(), ViewH-8)
	for k := range 8 {
		a := float64(k) * math.Pi / 4
		for _, s := range []float64{1.1, 2.2} {
			g.parts = append(g.parts, part{x: x, y: y, vx: math.Cos(a) * s, vy: math.Sin(a) * s, life: 110, max: 110, size: 6, col: 2})
		}
	}
	g.setState(stDead)
}

// fire shoots the current weapon if there is room and energy.
func (g *Game) fire() {
	p := &g.p
	w := g.weapon
	limit := [nWeapons]int{3, 6, 2, 1}[w]
	n := 0
	for _, s := range g.shots {
		if s.kind == w && !s.boom && s.big == 0 {
			n++
		}
	}
	if n >= limit || g.energy[w] < weaponCost[w] {
		return
	}
	g.energy[w] -= weaponCost[w]
	p.shoot = 14
	x := p.cx() + p.face*12
	y := p.y + 11
	add := func(s shot) {
		s.kind = w
		s.x -= s.w / 2
		s.y -= s.h / 2
		g.shots = append(g.shots, s)
	}
	switch w {
	case pulse:
		add(shot{body: body{x: x, y: y, w: 6, h: 4, vx: p.face * 4.5}, dmg: 1, life: 90})
	case fan:
		for _, a := range []float64{-0.32, 0, 0.32} {
			add(shot{body: body{x: x, y: y, w: 4, h: 4, vx: p.face * 4 * math.Cos(a), vy: 4 * math.Sin(a)}, dmg: 1, life: 60})
		}
	case lance:
		add(shot{body: body{x: x + p.face*6, y: y, w: 20, h: 3, vx: p.face * 6}, dmg: 2, life: 70})
	case mortar:
		add(shot{body: body{x: x, y: y - 2, w: 6, h: 6, vx: p.face * 2.6, vy: -3.6}, dmg: 2, life: 200, grav: 0.18})
	}
}

// fireCharged releases a charged pulse, which ignores the shot limit.
func (g *Game) fireCharged(full bool) {
	p := &g.p
	p.shoot = 14
	s := shot{kind: pulse, big: 1, dmg: 2, life: 90}
	s.w, s.h, s.vx = 10, 8, p.face*5
	if full {
		s.big, s.dmg = 2, 4
		s.w, s.h, s.vx = 16, 12, p.face*5.5
		g.shake = 4
	}
	s.x = p.cx() + p.face*14 - s.w/2
	s.y = p.y + 11 - s.h/2
	g.shots = append(g.shots, s)
}

// explode turns a mortar shell into its blast.
func (g *Game) explode(s *shot) {
	cx, cy := s.cx(), s.cy()
	s.boom, s.life, s.hit = true, 18, nil
	s.w, s.h, s.vx, s.vy, s.grav = 40, 40, 0, 0, 0
	s.x, s.y = cx-20, cy-20
	s.dmg = 3
	g.burst(cx, cy, 14, 3)
	g.shake = 8
}

func (g *Game) updateShots() {
	j := 0
	for i := range g.shots {
		s := &g.shots[i]
		s.life--
		if !s.boom {
			s.vy += s.grav
			s.x += s.vx
			s.y += s.vy
			if s.kind == mortar && g.solid(cell(s.cx()), cell(s.cy())) {
				g.explode(s)
			}
		}
		if !s.boom && s.kind != mortar && s.big < 2 && g.solid(cell(s.cx()), cell(s.cy())) {
			s.life = 0
		}
		if s.x < g.cam-32 || s.x > g.cam+g.viewW+32 || s.y > ViewH+16 {
			s.life = 0
		}
		g.hitFoes(s)
		if s.life > 0 {
			g.shots[j] = *s
			j++
		}
	}
	g.shots = g.shots[:j]
}

// hitFoes applies a shot to the foes and the guardian it touches.
func (g *Game) hitFoes(s *shot) {
	if s.life <= 0 {
		return
	}
	pierce := s.kind == lance || s.boom || s.big == 2
	for _, f := range g.foes {
		if f.hp <= 0 || !overlap(&s.body, &f.body) || hitBefore(s, f) {
			continue
		}
		if f.kind == turret && !f.open {
			// The shut dome deflects the shot.
			if !s.boom {
				g.burst(s.cx(), s.cy(), 3, 0)
				s.life = 0
				return
			}
			continue
		}
		s.hit = append(s.hit, f)
		f.hp -= s.dmg
		f.flash = 6
		if f.hp <= 0 {
			g.killFoe(f)
		}
		if s.kind == mortar && !s.boom {
			g.explode(s)
			return
		}
		if !pierce {
			s.life = 0
			return
		}
	}
	b := g.boss
	if b == nil || b.hp <= 0 || !overlap(&s.body, &b.body) || hitBefore(s, b) {
		return
	}
	if s.kind == mortar && !s.boom {
		g.explode(s)
		return
	}
	s.hit = append(s.hit, b)
	if !pierce {
		s.life = 0
	}
	if b.inv > 0 {
		return
	}
	dmg := s.dmg
	if bossWeak[b.kind] == s.kind {
		dmg = 4
	}
	b.hp -= dmg
	b.inv = 12
	if b.hp <= 0 {
		g.killBoss()
	}
}

func hitBefore(s *shot, v any) bool {
	for _, h := range s.hit {
		if h == v {
			return true
		}
	}
	return false
}

func (g *Game) killFoe(f *foe) {
	g.burst(f.cx(), f.cy(), 12, 1)
	if r := g.rng.Float64(); r < 0.3 {
		g.items = append(g.items, item{body: body{x: f.cx() - 4, y: f.cy() - 4, w: 8, h: 8, vy: -2}, energy: r < 0.12, life: 420})
	}
}

// spawnFoes brings to life the foes whose spawn point comes into view.
func (g *Game) spawnFoes() {
	for i, sp := range g.lv.spawns {
		x := float64(sp.col * T)
		in := x > g.cam-T && x < g.cam+g.viewW+T
		if in && !g.inView[i] && !g.active[i] {
			g.active[i] = true
			f := &foe{kind: sp.kind, sp: i, face: -1}
			switch sp.kind {
			case walker:
				f.w, f.h, f.hp = 14, 11, 2
			case flyer:
				f.w, f.h, f.hp = 14, 8, 1
			case turret:
				f.w, f.h, f.hp = 14, 12, 3
			case hopper:
				f.w, f.h, f.hp = 10, 14, 2
			}
			f.x = x + (T-f.w)/2
			f.y = float64((sp.row+1)*T) - f.h
			f.baseY = f.y
			f.t = g.rng.IntN(40)
			g.foes = append(g.foes, f)
		}
		g.inView[i] = in
	}
}

func (g *Game) updateFoes() {
	p := &g.p
	j := 0
	for _, f := range g.foes {
		f.t++
		if f.flash > 0 {
			f.flash--
		}
		toward := 1.0
		if p.cx() < f.cx() {
			toward = -1
		}
		switch f.kind {
		case walker:
			if f.vx == 0 {
				f.vx = toward * 0.5
			}
			// Turn back at walls and at the edge of the roof.
			ahead := f.x - 1
			if f.vx > 0 {
				ahead = f.x + f.w + 1
			}
			if g.moveBody(&f.body, true) || f.ground && !g.solid(cell(ahead), cell(f.y+f.h+2)) {
				f.vx = -f.vx
			}
			f.face = math.Copysign(1, f.vx)
		case flyer:
			f.vx = toward * 0.7
			f.face = toward
			f.x += f.vx
			f.y = f.baseY + 12*math.Sin(float64(f.t)*0.06)
		case turret:
			g.moveBody(&f.body, true)
			ph := f.t % 150
			f.open = ph >= 90
			if ph == 100 {
				g.aimed(f.cx(), f.y+4, 2, 3, 0.3)
			}
		case hopper:
			f.face = toward
			if f.ground {
				f.vx = 0
				if f.t%60 == 0 {
					f.vy, f.vx = -4.6, toward*1.2
				}
			}
			g.moveBody(&f.body, true)
		}
		if f.hp > 0 && overlap(&f.body, &p.body) {
			g.hurt(3, f.cx())
		}
		gone := f.x < g.cam-3*T || f.x > g.cam+g.viewW+3*T || f.y > ViewH+T
		if f.hp <= 0 || gone {
			g.active[f.sp] = false
			continue
		}
		g.foes[j] = f
		j++
	}
	clear(g.foes[j:])
	g.foes = g.foes[:j]
}

// aimed fires n bullets at the hero, spread by sp radians.
func (g *Game) aimed(x, y float64, speed float64, n int, sp float64) {
	a := math.Atan2(g.p.cy()-y, g.p.cx()-x)
	for k := range n {
		da := (float64(k) - float64(n-1)/2) * sp
		g.ebul = append(g.ebul, ebullet{body: body{x: x - 2, y: y - 2, w: 4, h: 4,
			vx: speed * math.Cos(a+da), vy: speed * math.Sin(a+da)}, life: 300})
	}
}

func (g *Game) updateBullets() {
	j := 0
	for i := range g.ebul {
		e := &g.ebul[i]
		e.life--
		switch {
		case e.wave:
			if g.moveBody(&e.body, true) {
				e.life = 0
			}
		case e.grav:
			e.vy += 0.15
			e.x += e.vx
			e.y += e.vy
			if g.solid(cell(e.cx()), cell(e.y+e.h)) {
				g.burst(e.cx(), e.cy(), 8, 3)
				e.life = 0
			}
		default:
			e.x += e.vx
			e.y += e.vy
		}
		if overlap(&e.body, &g.p.body) {
			g.hurt(3, e.cx())
			if !e.wave {
				e.life = 0
			}
		}
		if e.life > 0 && e.x > g.cam-32 && e.x < g.cam+g.viewW+32 && e.y < ViewH+16 && e.y > -64 {
			g.ebul[j] = *e
			j++
		}
	}
	g.ebul = g.ebul[:j]
}

func (g *Game) updateItems() {
	j := 0
	for i := range g.items {
		it := &g.items[i]
		it.life--
		g.moveBody(&it.body, true)
		if overlap(&it.body, &g.p.body) {
			if it.energy {
				w := g.weapon
				if w == pulse {
					// The pulse needs no energy: fill the emptiest weapon.
					for k := fan; k < nWeapons; k++ {
						if g.unlocked[k] && (w == pulse || g.energy[k] < g.energy[w]) {
							w = k
						}
					}
				}
				g.energy[w] = min(maxEn, g.energy[w]+8)
			} else {
				g.p.hp = min(maxHP, g.p.hp+8)
			}
			continue
		}
		if it.life > 0 && it.y < ViewH+16 {
			g.items[j] = *it
			j++
		}
	}
	g.items = g.items[:j]
}

// checkArena closes the gate behind the hero entering a guardian's room.
func (g *Game) checkArena() {
	g.arena = -1
	p := &g.p
	for i, a := range g.lv.arenas {
		if p.x < float64(a.in*T) || p.x > float64((a.out+1)*T) {
			continue
		}
		g.arena = i
		if !g.entered[i] && p.x >= float64((a.in+1)*T)+2 {
			g.entered[i] = true
			g.foes = g.foes[:0]
			clear(g.active)
			g.startBoss(i)
		}
	}
}

func (g *Game) startBoss(i int) {
	a := g.lv.arenas[i]
	b := &boss{kind: a.boss, face: -1, ax0: float64((a.in + 1) * T), ax1: float64(a.out * T)}
	b.w, b.h = 20, 26
	b.x = b.ax1 - 3*T
	b.y = float64(a.floor*T) - b.h - 4*T
	if a.boss == 1 {
		b.w, b.h, b.flying = 22, 12, true
		b.y = 3 * T
	}
	g.boss = b
	g.setState(stIntro)
}

func (g *Game) killBoss() {
	b := g.boss
	g.boss = nil
	g.ebul = g.ebul[:0]
	for k := range 16 {
		a := float64(k) * math.Pi / 8
		g.parts = append(g.parts, part{x: b.cx(), y: b.cy(), vx: math.Cos(a) * 1.6, vy: math.Sin(a) * 1.6, life: 100, max: 100, size: 8, col: 1})
	}
	g.burst(b.cx(), b.cy(), 30, 3)
	g.shake = 20
	g.cleared[g.arena] = true
	g.p.hp = maxHP
	if b.kind+1 < nWeapons {
		w := b.kind + 1
		g.unlocked[w] = true
		g.energy[w] = maxEn
		g.banner, g.bannerT = "GOT "+weaponNames[w]+"  press i", 240
	}
	if b.kind == 3 {
		g.winIn = 150
	}
}

func (g *Game) updateBoss() {
	b := g.boss
	if b == nil {
		return
	}
	p := &g.p
	b.t++
	if b.inv > 0 {
		b.inv--
	}
	toward := 1.0
	if p.cx() < b.cx() {
		toward = -1
	}
	fast := 1.0
	if b.hp < maxHP/2 {
		fast = 1.3
	}
	switch b.kind {
	case 0:
		g.volt(b, toward, fast)
	case 1:
		g.siren(b, toward, fast)
	case 2:
		g.crane(b, toward, fast)
	case 3:
		// The last guardian chains the moves of the others.
		switch b.acts % 3 {
		case 0:
			g.volt(b, toward, fast*1.15)
		case 1:
			g.crane(b, toward, fast*1.15)
		case 2:
			g.burstFan(b, toward)
		}
	}
	b.x = min(max(b.x, b.ax0), b.ax1-b.w)
	if overlap(&b.body, &p.body) {
		g.hurt(4, b.cx())
	}
}

// next ends a boss move: back to waiting, one more move done.
func (b *boss) next() { b.act, b.t, b.acts = 0, 0, b.acts+1 }

func (g *Game) volt(b *boss, toward, fast float64) {
	switch b.act {
	case 0: // wait, facing the hero
		b.vx = 0
		b.face = toward
		if b.t > int(40/fast) && b.ground {
			b.t = 0
			if b.hp < maxHP/2 && g.rng.IntN(2) == 0 {
				b.act = 3
			} else {
				b.act, b.vy, b.airborne = 1, -6, true
				b.vx = min(max((g.p.cx()-b.cx())/48, -2.6), 2.6)
			}
		}
	case 1: // jump toward the hero
		if b.ground && b.t > 4 {
			b.act, b.t, b.vx = 2, 0, 0
		}
	case 2: // three shots
		b.face = toward
		if b.t == 8 || b.t == 20 || b.t == 32 {
			g.ebul = append(g.ebul, ebullet{body: body{x: b.cx() + b.face*12, y: b.y + 12, w: 5, h: 4, vx: b.face * 3}, life: 200})
		}
		if b.t > 44 {
			b.next()
		}
	case 3: // dash
		b.vx = b.face * 3.2 * fast
		if b.t > 90 {
			b.next()
		}
	}
	if g.moveBody(&b.body, true) && b.act == 3 {
		b.next()
	}
}

func (g *Game) siren(b *boss, toward, fast float64) {
	hover := 3*T + 10*math.Sin(float64(b.t)*0.05)
	switch b.act {
	case 0:
		if b.vx == 0 {
			b.vx = -1.2
		}
		b.vx = math.Copysign(1.2*fast, b.vx)
		if b.x <= b.ax0+T || b.x+b.w >= b.ax1-T {
			b.vx = -b.vx
			b.x = min(max(b.x, b.ax0+T+1), b.ax1-T-b.w-1)
		}
		b.y += (hover - b.y) * 0.1
		b.face = toward
		if b.t%int(70/fast) == 35 {
			g.aimed(b.cx(), b.cy()+4, 2.2, 3, 0.35)
		}
		if b.t > 280 {
			b.act, b.t = 1, 0
		}
	case 1: // dive on the hero
		dx, dy := g.p.cx()-b.cx(), g.p.y-b.y
		d := max(1, math.Hypot(dx, dy))
		if b.t == 1 {
			b.vx, b.vy = dx/d*3*fast, dy/d*3*fast
		}
		if b.y > g.p.y || b.t > 70 {
			b.act, b.t = 2, 0
		}
	case 2: // back up
		b.vx, b.vy = 0, -2
		if b.y <= hover {
			b.vy = 0
			b.act, b.t = 0, 0
			b.acts++
		}
	}
	if b.act != 0 {
		b.x += b.vx
		b.y += b.vy
	} else {
		b.x += b.vx
	}
}

func (g *Game) crane(b *boss, toward, fast float64) {
	switch b.act {
	case 0:
		b.vx = 0
		b.face = toward
		if b.t > int(50/fast) && b.ground {
			b.act, b.t, b.vy, b.airborne = 1, 0, -7, true
			b.vx = min(max((g.p.cx()-b.cx())/56, -2), 2)
		}
	case 1:
		if b.ground && b.t > 4 {
			// The landing sends a shockwave both ways.
			for _, d := range []float64{-1, 1} {
				g.ebul = append(g.ebul, ebullet{body: body{x: b.cx() - 5 + d*10, y: b.y + b.h - 10, w: 10, h: 10, vx: d * 2.6}, wave: true, life: 400})
			}
			g.shake = 12
			b.act, b.t, b.vx = 2, 0, 0
		}
	case 2: // throw bombs
		b.face = toward
		if b.t == 12 || b.t == 34 || b.t == 56 {
			g.ebul = append(g.ebul, ebullet{body: body{x: b.cx() - 3, y: b.y, w: 6, h: 6,
				vx: (g.p.cx() - b.cx()) / 55, vy: -4}, grav: true, life: 300})
		}
		if b.t > 70 {
			b.next()
		}
	}
	g.moveBody(&b.body, true)
}

// burstFan is the last guardian's volley: five shots, twice.
func (g *Game) burstFan(b *boss, toward float64) {
	b.vx = 0
	b.face = toward
	if b.t == 15 || b.t == 45 {
		g.aimed(b.cx(), b.y+10, 2.4, 5, 0.22)
	}
	if b.t > 70 {
		b.next()
	}
	g.moveBody(&b.body, true)
}

// camTarget returns where the camera should be.
func (g *Game) camTarget(inArena bool) float64 {
	maxCam := float64(g.lv.w*T) - g.viewW
	if inArena && g.arena >= 0 {
		a := g.lv.arenas[g.arena]
		x0, x1 := float64(a.in*T), float64((a.out+1)*T)
		if x1-x0 <= g.viewW {
			return math.Max(0, (x0+x1)/2-g.viewW/2)
		}
		return min(max(g.p.cx()-g.viewW/2, x0), x1-g.viewW)
	}
	return min(max(g.p.cx()-g.viewW/2, 0), max(0, maxCam))
}

func (g *Game) follow(inArena bool) {
	t := g.camTarget(inArena)
	old := g.cam
	if inArena {
		g.cam += (t - g.cam) * 0.12
	} else {
		g.cam = t
	}
	g.panX += g.cam - old
}

// burst throws n sparks of color col.
func (g *Game) burst(x, y float64, n, col int) {
	for range n {
		a := g.rng.Float64() * 2 * math.Pi
		s := 0.5 + g.rng.Float64()*2
		l := 14 + g.rng.IntN(16)
		g.parts = append(g.parts, part{x: x, y: y, vx: math.Cos(a) * s, vy: math.Sin(a)*s - 0.5, life: l, max: l, size: 2, col: col})
	}
}

func (g *Game) updateParts() {
	j := 0
	for _, pt := range g.parts {
		pt.life--
		pt.x += pt.vx
		pt.y += pt.vy
		if pt.size <= 3 {
			pt.vy += 0.08
		}
		if pt.life > 0 {
			g.parts[j] = pt
			j++
		}
	}
	g.parts = g.parts[:j]
}
