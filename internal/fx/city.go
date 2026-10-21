package fx

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// City at night: three layers of buildings scrolling slowly at different
// speeds, windows that turn on and off, vertical katakana signs that
// flicker, flying cars between the towers, two searchlights sweeping the
// sky and a moon veiled by clouds. Everything is drawn in big pixels; each
// layer is painted once into a strip that loops on itself.

var black = color.RGBA{0, 0, 0, 255}

type cityWin struct {
	x, y     int16
	w        int16
	on, cool bool
	lit, off color.RGBA
}

type citySign struct {
	x, y   int // in strip pixels, top of the sign
	glyphs []rune
	c      color.RGBA
	phase  float64
}

type cityLayer struct {
	img     *ebiten.Image
	w       int
	speed   float64 // strip pixels per second
	off     float64
	occ     float64 // share of windows that are lit
	blocks  []cityBlock
	wins    []cityWin
	flip    []int // windows to repaint
	beacons [][2]int
	signs   []citySign
}

type flyer struct {
	x, y, vx float64
	blink    float64
}

type city struct {
	rng      *rand.Rand
	t        float64
	size     image.Point
	g        int
	lw, lh   int
	sky      *ebiten.Image
	beams    *ebiten.Image
	layers   [3]*cityLayer
	paintFor *theme.Theme
	cars     []flyer
	nextCar  float64
	noise    valueNoise
	batch    gfx.Batch
	verts    []ebiten.Vertex
}

func newCity(r *rand.Rand) decor {
	return &city{rng: r, noise: newValueNoise(r), t: r.Float64() * 100}
}

func (c *city) fps() int { return 24 }

func (c *city) release() {
	for _, img := range []*ebiten.Image{c.sky, c.beams} {
		if img != nil {
			img.Deallocate()
		}
	}
	c.sky, c.beams = nil, nil
	for i, l := range c.layers {
		if l != nil && l.img != nil {
			l.img.Deallocate()
		}
		c.layers[i] = nil
	}
}

func (c *city) step(dt float64) {
	if c.layers[0] == nil {
		return
	}
	c.t += dt
	for _, l := range c.layers {
		l.off = math.Mod(l.off+l.speed*dt, float64(l.w))
		// A few windows change state every second, without changing the
		// share of those that are lit.
		n := float64(len(l.wins)) / 1200 * dt
		count := int(n)
		if c.rng.Float64() < n-float64(count) {
			count++
		}
		for ; count > 0 && len(l.wins) > 0; count-- {
			i := c.rng.IntN(len(l.wins))
			if on := c.rng.Float64() < l.occ; on != l.wins[i].on {
				l.wins[i].on = on
				l.flip = append(l.flip, i)
			}
		}
	}
	j := 0
	for _, f := range c.cars {
		f.x += f.vx * dt
		if f.x > -30 && f.x < float64(c.lw)+30 {
			c.cars[j] = f
			j++
		}
	}
	c.cars = c.cars[:j]
	c.nextCar -= dt
	if c.nextCar <= 0 {
		c.nextCar = 1 + c.rng.Float64()*3
		f := flyer{y: float64(c.lh) * (0.32 + c.rng.Float64()*0.4), vx: 12 + c.rng.Float64()*30, blink: c.rng.Float64()}
		f.x = -20
		if c.rng.IntN(2) == 0 {
			f.x, f.vx = float64(c.lw)+20, -f.vx
		}
		c.cars = append(c.cars, f)
	}
}

func (c *city) init(s *stage) {
	c.release()
	c.size, c.g = s.size, s.grain()
	c.lw, c.lh = (s.size.X+c.g-1)/c.g, (s.size.Y+c.g-1)/c.g
	opts := &ebiten.NewImageOptions{Unmanaged: true}
	c.sky = ebiten.NewImageWithOptions(image.Rect(0, 0, c.lw, c.lh), opts)
	c.beams = ebiten.NewImageWithOptions(image.Rect(0, 0, c.lw, c.lh), opts)
	w := c.lw * 3 / 2
	specs := [3]struct {
		speed      float64
		bw0, bw1   int
		h0, h1     float64
		occ, signs float64
	}{
		{0.7, 5, 14, 0.3, 0.62, 0.16, 0},
		{1.8, 7, 18, 0.2, 0.46, 0.2, 0.25},
		{3.6, 9, 24, 0.08, 0.3, 0.17, 0.5},
	}
	for d, sp := range specs {
		l := &cityLayer{w: w, speed: sp.speed, off: c.rng.Float64() * float64(w), occ: sp.occ}
		l.img = ebiten.NewImageWithOptions(image.Rect(0, 0, w, c.lh), opts)
		c.layers[d] = l
		c.buildings(l, d, sp.bw0, sp.bw1, sp.h0, sp.h1, sp.occ, sp.signs)
	}
	c.paintFor = nil
	for i := 0; i < 60; i++ {
		c.step(1.0 / 8)
	}
}

// cityBlock is a block of a building: main body, setback floor or antenna.
type cityBlock struct{ x, y, w, h int }

// buildings picks the buildings of a layer and records their windows,
// beacons and signs. Painting comes later (paint), to follow the theme.
func (c *city) buildings(l *cityLayer, d, bw0, bw1 int, h0, h1, occ, signs float64) {
	l.wins, l.beacons, l.signs = l.wins[:0], l.beacons[:0], l.signs[:0]
	l.blocks = l.blocks[:0]
	x := 0
	for x < l.w {
		bw := bw0 + c.rng.IntN(bw1-bw0+1)
		if l.w-x-bw < bw0 {
			bw = l.w - x
		}
		h := int(float64(c.lh) * (h0 + (h1-h0)*math.Pow(c.rng.Float64(), 1.6)))
		top := c.lh - h
		l.blocks = append(l.blocks, cityBlock{x, top, bw, h})
		// A setback floor on some roofs.
		if bw > 7 && c.rng.IntN(5) < 2 {
			in := 1 + c.rng.IntN(bw/4+1)
			th := 3 + c.rng.IntN(max(1, h/6))
			l.blocks = append(l.blocks, cityBlock{x + in, top - th, bw - 2*in, th})
			top -= th
		}
		if c.rng.IntN(10) < 3 {
			ah := 3 + c.rng.IntN(6)
			ax := x + bw/2
			l.blocks = append(l.blocks, cityBlock{ax, top - ah, 1, ah})
			l.beacons = append(l.beacons, [2]int{ax, top - ah - 1})
		}
		// The windows: every other column (every third in the foreground).
		step, ww := 2, int16(1)
		if d == 2 {
			step, ww = 3, 2
		}
		bocc := occ * (0.4 + c.rng.Float64()*1.2)
		cool := c.rng.IntN(4) == 0
		for wy := c.lh - h + 2; wy < c.lh-1; wy += 2 {
			for wx := x + 1; wx+int(ww) < x+bw; wx += step {
				l.wins = append(l.wins, cityWin{x: int16(wx), y: int16(wy), w: ww, on: c.rng.Float64() < bocc, cool: cool})
			}
		}
		if signs > 0 && bw >= 6 && c.rng.Float64() < signs {
			n := 3 + c.rng.IntN(3)
			sg := citySign{x: x + bw/2, y: c.lh - h + 3 + c.rng.IntN(max(1, h/3)), phase: c.rng.Float64() * 100}
			for k := 0; k < n; k++ {
				sg.glyphs = append(sg.glyphs, 0xff66+rune(c.rng.IntN(56)))
			}
			l.signs = append(l.signs, sg)
		}
		x += bw
		if d < 2 && c.rng.IntN(3) == 0 {
			x++
		}
	}
}

// paint paints the sky and the three layers in the theme colors.
func (c *city) paint(th *theme.Theme) {
	c.paintFor = th
	b := &c.batch
	// The sky: night at the top, city glow on the horizon, in five flat bands.
	top := theme.Mix(th.BG, black, 0.25)
	hz := theme.Mix(th.BG, th.Accent, 0.2)
	for y := 0; y < c.lh; y++ {
		u := float64(y) / float64(c.lh)
		k := math.Floor(u*u*u*5+0.5) / 5
		b.Rect(0, float32(y), float32(c.lw), 1, theme.Mix(top, hz, float32(k)), 1)
	}
	for i := 0; i < c.lw*c.lh/900; i++ {
		x, y := c.rng.IntN(c.lw), c.rng.IntN(c.lh/2)
		b.Rect(float32(x), float32(y), 1, 1, theme.Mix(th.BG, th.FG, 0.25+0.4*c.rng.Float32()), 1)
	}
	// The moon and its halo.
	mx, my, r := float64(c.lw)*0.76, float64(c.lh)*0.2, float64(c.lh)*0.085
	moon := theme.Mix(th.BG, th.FG, 0.62)
	for k, rr := range []float64{2.4, 1.8, 1.35} {
		c.disc(mx, my, r*rr, theme.Mix(th.BG, th.Accent2, 0.3), 0.05+0.03*float32(k))
	}
	c.disc(mx, my, r, moon, 1)
	c.disc(mx-r*0.3, my-r*0.2, r*0.22, theme.Mix(moon, th.BG, 0.12), 1)
	c.disc(mx+r*0.35, my+r*0.3, r*0.15, theme.Mix(moon, th.BG, 0.12), 1)
	c.disc(mx+r*0.1, my-r*0.45, r*0.1, theme.Mix(moon, th.BG, 0.1), 1)
	b.Flush(c.sky, gfx.White(), ebiten.BlendSourceOver)

	walls := [3]color.RGBA{
		theme.Mix(th.BG, th.Accent2, 0.12),
		theme.Mix(theme.Mix(th.BG, black, 0.2), th.Accent2, 0.05),
		theme.Mix(th.BG, black, 0.45),
	}
	glow := [3]float32{0.3, 0.5, 0.62}
	for d, l := range c.layers {
		l.img.Clear()
		wall := walls[d]
		for _, k := range l.blocks {
			b.Rect(float32(k.x), float32(k.y), float32(k.w), float32(k.h), wall, 1)
			if k.w > 1 {
				b.Rect(float32(k.x), float32(k.y), float32(k.w), 1, theme.Mix(wall, th.Accent2, 0.25), 1)
			}
		}
		for i := range l.wins {
			w := &l.wins[i]
			hue := th.Warn
			if w.cool {
				hue = th.Accent2
			}
			w.lit = theme.Mix(wall, hue, glow[d])
			w.off = theme.Mix(wall, black, 0.18)
			cl := w.off
			if w.on {
				cl = w.lit
			}
			b.Rect(float32(w.x), float32(w.y), float32(w.w), 1, cl, 1)
		}
		l.flip = l.flip[:0]
		b.Flush(l.img, gfx.White(), ebiten.BlendSourceOver)
		for i := range l.signs {
			l.signs[i].c = []color.RGBA{th.Accent, th.Accent2, th.Warn}[c.rng.IntN(3)]
		}
	}
}

func (c *city) disc(cx, cy, r float64, col color.RGBA, a float32) {
	for y := -r; y <= r; y++ {
		hw := math.Floor(math.Sqrt(max(0, r*r-y*y)))
		c.batch.Rect(float32(math.Floor(cx-hw)), float32(math.Floor(cy+y)), float32(2*hw+1), 1, col, a)
	}
}

// strip draws a layer at its offset, twice so it loops.
func (c *city) strip(dst *ebiten.Image, l *cityLayer) {
	g := float64(c.g)
	for k := 0; k < 2; k++ {
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Scale(g, g)
		op.GeoM.Translate(math.Floor((float64(k*l.w)-l.off)*g), 0)
		dst.DrawImage(l.img, op)
	}
}

// screenX converts an x coordinate in the strip into a screen x, or -1 if
// it is out of view.
func (c *city) screenX(l *cityLayer, x int) float32 {
	sx := math.Mod(float64(x)-l.off+float64(l.w), float64(l.w))
	if sx > float64(c.lw)+4 {
		return -1e9
	}
	return float32(math.Floor(sx * float64(c.g)))
}

func (c *city) draw(dst *ebiten.Image, s *stage) {
	if c.layers[0] == nil || s.settled && (s.size != c.size || s.grain() != c.g) {
		c.init(s)
	}
	if c.paintFor != s.th {
		c.paint(s.th)
	}
	for _, l := range c.layers {
		for _, i := range l.flip {
			w := l.wins[i]
			cl := w.off
			if w.on {
				cl = w.lit
			}
			c.batch.Rect(float32(w.x), float32(w.y), float32(w.w), 1, cl, 1)
		}
		if len(l.flip) > 0 {
			c.batch.Flush(l.img, gfx.White(), ebiten.BlendCopy)
			l.flip = l.flip[:0]
		}
	}
	p, th := s.p, s.th
	g := float32(c.g)
	W, H := float32(s.size.X), float32(s.size.Y)

	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(g), float64(g))
	dst.DrawImage(c.sky, op)

	// Thin streaks of cloud drift in front of the moon.
	for k := 0; k < 4; k++ {
		y := float32(math.Floor(float64(c.lh)*(0.15+0.035*float64(k)))) * g
		x := float32(math.Mod(c.t*(1.5+float64(k)*0.7)*float64(g)+float64(k)*float64(W)*0.37, float64(W)*1.6)) - W*0.3
		p.Rect(float32(math.Floor(float64(x/g)))*g, y, W*(0.18+0.06*float32(k)), g*float32(1+k%2), th.BG, 0.75)
	}
	p.Flush(dst)

	c.searchlights(dst, th)
	c.strip(dst, c.layers[0])
	// The glowing haze between layers, in bands aligned on the big pixels.
	for k := 0; k < 12; k++ {
		y0 := float32(math.Floor(float64(H*(0.5+0.5*float32(k)/12)/g))) * g
		y1 := float32(math.Floor(float64(H*(0.5+0.5*float32(k+1)/12)/g))) * g
		p.Rect(0, y0, W, y1-y0, th.Accent, 0.006*float32(k))
	}
	p.Flush(dst)
	c.strip(dst, c.layers[1])
	c.drawSigns(p, s.f, c.layers[1], 0.55)
	p.Flush(dst)

	for _, f := range c.cars {
		x, y := float32(math.Floor(f.x))*g, float32(math.Floor(f.y))*g
		dir := float32(1)
		if f.vx < 0 {
			dir = -1
		}
		for k := 1; k <= 10; k++ {
			p.Rect(x-dir*float32(k)*g, y, g, g, th.Accent, 0.5*(1-float32(k)/11))
		}
		p.Rect(x, y, g, g, theme.Mix(th.Warn, th.FG, 0.5), 1)
		if math.Mod(c.t+f.blink, 0.8) < 0.12 {
			p.Rect(x-dir*g, y-g, g, g, th.Accent, 1)
		}
	}
	p.Flush(dst)
	c.strip(dst, c.layers[2])
	c.drawSigns(p, s.f, c.layers[2], 0.9)
	for d, l := range c.layers {
		for i, bc := range l.beacons {
			if math.Mod(c.t*0.9+float64(i)*0.37, 1.6) > 0.35 {
				continue
			}
			x := c.screenX(l, bc[0])
			if x < -g {
				continue
			}
			a := float32(0.5 + 0.25*float32(d))
			p.Rect(x-g, float32(bc[1]-1)*g, 3*g, 3*g, th.Accent, 0.15*a)
			p.Rect(x, float32(bc[1])*g, g, g, th.Accent, a)
		}
	}
}

// drawSigns draws the vertical signs of a layer. They flicker: now and
// then a sign dims or goes out for a moment.
func (c *city) drawSigns(p *gfx.Painter, f *gfx.Font, l *cityLayer, alpha float32) {
	g := float32(c.g)
	for _, sg := range l.signs {
		x := c.screenX(l, sg.x)
		if x < -float32(f.CellW)*2 {
			continue
		}
		x -= float32(f.CellW) / 2
		y := float32(sg.y) * g
		n := c.noise.at(c.t*4 + sg.phase)
		on := float32(1)
		if n < 0.18 {
			on = 0.25
		}
		h := float32(len(sg.glyphs) * f.CellH)
		p.Rect(x-g, y-g, float32(f.CellW)+2*g, h+2*g, black, 0.55*alpha)
		p.Rect(x-2*g, y-2*g, float32(f.CellW)+4*g, h+4*g, sg.c, 0.07*alpha*on)
		for k, r := range sg.glyphs {
			p.Glyph(f.Glyph(r, 0, false), x, y+float32(k*f.CellH), sg.c, alpha*on)
		}
	}
}

// searchlights draws two beams sweeping the sky, in additive light, from
// the foot of the city upward. They are computed in big pixels: their edges
// come out as stair steps like the rest of the background.
func (c *city) searchlights(dst *ebiten.Image, th *theme.Theme) {
	W, H := float32(c.lw), float32(c.lh)
	c.verts = c.verts[:0]
	var idx []uint16
	for k, x0 := range []float32{0.22, 0.63} {
		ang := -math.Pi/2 + 0.42*math.Sin(c.t*0.13+float64(k)*2.2)
		spread := 0.045
		L := float64(H) * 1.1
		ox, oy := x0*W, H
		col := theme.Mix(th.Accent2, th.FG, 0.3)
		r, g, b, _ := gfx.Premul(col, 1)
		base := uint16(len(c.verts))
		add := func(x, y float64, a float32) {
			c.verts = append(c.verts, ebiten.Vertex{DstX: float32(x), DstY: float32(y), SrcX: 1, SrcY: 1,
				ColorR: r * a, ColorG: g * a, ColorB: b * a, ColorA: a})
		}
		add(float64(ox), float64(oy), 0.10)
		add(float64(ox)+L*math.Cos(ang-spread), float64(oy)+L*math.Sin(ang-spread), 0)
		add(float64(ox)+L*math.Cos(ang+spread), float64(oy)+L*math.Sin(ang+spread), 0)
		idx = append(idx, base, base+1, base+2)
	}
	c.beams.Clear()
	op := &ebiten.DrawTrianglesOptions{ColorScaleMode: ebiten.ColorScaleModePremultipliedAlpha, Blend: ebiten.BlendLighter}
	c.beams.DrawTriangles(c.verts, idx, gfx.White(), op)
	bop := &ebiten.DrawImageOptions{Blend: ebiten.BlendLighter}
	bop.GeoM.Scale(float64(c.g), float64(c.g))
	dst.DrawImage(c.beams, bop)
}
