package fx

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Blaze: the fire from 90s demos (Hugo Elias). Each cell takes the average
// of the cells below, pushed by the wind, minus a cooling map that rises
// with the flames and cuts them into tongues. Embers escape upward. The
// colors follow the theme: red and gold on Blood Moon, a cold flame that
// turns white on Ice.

const (
	fireLevels = 6  // flame shades, as flat levels
	fireRows   = 96 // simulated height, in cells
	fireCoolH  = 64 // height of the cooling map
	fireStep   = 1.0 / 30
)

type ember struct{ x, y, vx, vy, life, max, phase float64 }

type fire struct {
	t, acc  float64
	heat    []float32 // h+2 rows: the last two are the hearth
	next    []float32
	cool    []float32
	src     []float32
	off     int
	w, h    int
	cell    int
	size    image.Point
	px      pixels
	dirty   bool
	pal     [fireLevels + 1][4]byte
	palFor  *theme.Theme
	top     color.RGBA
	embers  []ember
	rng     *rand.Rand
	seed    uint32
	noise   valueNoise
	nextEmb float64
}

func newFire(r *rand.Rand) decor {
	return &fire{rng: r, seed: r.Uint32() | 1, noise: newValueNoise(r), t: r.Float64() * 100}
}

func (f *fire) fps() int { return 30 }
func (f *fire) release() { f.px.release() }

func (f *fire) step(dt float64) {
	if f.heat == nil {
		return
	}
	f.acc += dt
	for f.acc >= fireStep {
		f.acc -= fireStep
		f.spread()
	}
	f.stepEmbers(dt)
}

func (f *fire) rand() uint32 {
	x := f.seed
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	f.seed = x
	return x
}

func (f *fire) init(s *stage) {
	f.size = s.size
	f.cell = max(s.grain(), s.size.Y/190)
	f.w = s.size.X/f.cell + 1
	f.h = min(fireRows, s.size.Y/f.cell+1)
	f.heat = make([]float32, f.w*(f.h+2))
	f.next = make([]float32, f.w*(f.h+2))
	f.src = make([]float32, f.w)
	f.coolMap()
	f.px.resize(f.w, f.h)
	f.embers = f.embers[:0]
	// The fire is already burning when the background appears, even when still.
	for i := 0; i < 160; i++ {
		f.spread()
		f.stepEmbers(fireStep)
	}
}

// coolMap builds the cooling map: a smoothed noise, squared so the tongues
// of fire stand out sharply.
func (f *fire) coolMap() {
	w, h := f.w, fireCoolH
	c := make([]float32, w*h)
	for i := range c {
		c[i] = f.rng.Float32()
	}
	tmp := make([]float32, w*h)
	for pass := 0; pass < 6; pass++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				l, r := c[y*w+(x+w-1)%w], c[y*w+(x+1)%w]
				u, d := c[((y+h-1)%h)*w+x], c[((y+1)%h)*w+x]
				tmp[y*w+x] = (c[y*w+x]*2 + l + r + u + d) / 6
			}
		}
		c, tmp = tmp, c
	}
	lo, hi := float32(1), float32(0)
	for _, v := range c {
		lo, hi = min(lo, v), max(hi, v)
	}
	for i, v := range c {
		n := (v - lo) / max(hi-lo, 1e-3)
		c[i] = 0.0035 + n*n*0.03
	}
	f.cool = c
}

// spread moves the fire up by one row.
func (f *fire) spread() {
	f.t += fireStep
	w, h := f.w, f.h
	// The hearth: hot spots that flare up and die down, more of them where
	// the fire breathes.
	for x := 0; x < w; x++ {
		if f.rand()&7 == 0 {
			n := f.noise.at(float64(x)*0.045 + f.t*0.35)
			v := float32(0.12)
			if float64(f.rand()&255)/256 < 0.25+0.6*n {
				v = 1
			}
			f.src[x] = v
		}
		f.heat[h*w+x] = f.src[x]
		f.heat[(h+1)*w+x] = f.src[x]
	}
	// The wind bends the flames to one side, then the other.
	wind := float32(0.45*math.Sin(f.t*0.19) + 0.2*math.Sin(f.t*0.61+1))
	wl, wr := 0.25*(1+wind), 0.25*(1-wind)
	f.off = (f.off + 1) % fireCoolH
	for y := 0; y < h; y++ {
		cr := ((y + f.off) % fireCoolH) * w
		b1, b2 := (y+1)*w, (y+2)*w
		for x := 0; x < w; x++ {
			l, r := max(x-1, 0), min(x+1, w-1)
			v := f.heat[b1+l]*wl + f.heat[b1+r]*wr + (f.heat[b1+x]+f.heat[b2+x])*0.25
			v -= f.cool[cr+x]
			f.next[y*w+x] = max(v, 0)
		}
	}
	copy(f.heat[:h*w], f.next[:h*w])
	f.dirty = true
}

func (f *fire) stepEmbers(dt float64) {
	cell := float64(f.cell)
	f.nextEmb -= dt
	for f.nextEmb <= 0 && len(f.embers) < 70 {
		f.nextEmb += 0.08 + f.rng.Float64()*0.2
		life := 1.6 + f.rng.Float64()*2.4
		f.embers = append(f.embers, ember{
			x:  f.rng.Float64() * float64(f.size.X),
			y:  float64(f.size.Y) - cell*float64(f.h)*(0.25+f.rng.Float64()*0.35),
			vx: (f.rng.Float64() - 0.5) * cell * 6, vy: -cell * (6 + f.rng.Float64()*9),
			life: life, max: life, phase: f.rng.Float64() * 6.28,
		})
	}
	wind := (0.28*math.Sin(f.t*0.21) + 0.1*math.Sin(f.t*0.67+1)) * cell * 14
	j := 0
	for i := range f.embers {
		e := &f.embers[i]
		e.life -= dt
		if e.life <= 0 {
			continue
		}
		e.x += (e.vx + wind + math.Sin(f.t*2.3+e.phase)*cell*3) * dt
		e.y += e.vy * dt
		e.vy *= 1 - 0.25*dt
		f.embers[j] = *e
		j++
	}
	f.embers = f.embers[:j]
}

// palette grades from the backdrop to the hottest flame. If the theme's
// accent and warning color are too far apart to blend without going
// through gray, the flame turns white toward the text color.
func (f *fire) palette(th *theme.Theme) {
	f.palFor = th
	top := th.Warn
	if hueDist(th.Accent, th.Warn) > 110 {
		top = th.FG
	}
	f.top = top
	white := color.RGBA{255, 255, 255, 255}
	stops := []struct {
		at float32
		c  color.RGBA
		a  float32
	}{
		{0, th.BG, 0},
		{0.1, theme.Mix(th.BG, th.Accent, 0.3), 0.55},
		{0.32, theme.Mix(th.BG, th.Accent, 0.8), 0.85},
		{0.58, theme.Mix(th.Accent, top, 0.5), 0.95},
		{0.82, top, 1},
		{1, theme.Mix(top, white, 0.5), 1},
	}
	for i := range f.pal {
		v := float32(i) / fireLevels
		k := 1
		for k < len(stops)-1 && stops[k].at < v {
			k++
		}
		a, b := stops[k-1], stops[k]
		u := (v - a.at) / (b.at - a.at)
		c := theme.Mix(a.c, b.c, u)
		al := (a.a + (b.a-a.a)*u) * 0.9
		f.pal[i] = [4]byte{byte(float32(c.R) * al), byte(float32(c.G) * al), byte(float32(c.B) * al), byte(255 * al)}
	}
	f.dirty = true
}

func (f *fire) draw(dst *ebiten.Image, s *stage) {
	if f.heat == nil || s.settled && (s.size != f.size || s.grain() > f.cell) {
		f.init(s)
	}
	if f.palFor != s.th {
		f.palette(s.th)
	}
	if f.dirty {
		for i, v := range f.heat[:f.w*f.h] {
			k := int(min(v*1.2, 1)*fireLevels + 0.5)
			copy(f.px.buf[i*4:i*4+4], f.pal[k][:])
		}
		f.px.img.WritePixels(f.px.buf)
		f.dirty = false
	}
	p, th := s.p, s.th
	W, H := float32(s.size.X), float32(s.size.Y)
	base := H - float32(f.h*f.cell)

	// The firelight on the background, in bands aligned on the cells.
	c := float32(f.cell)
	top, span := base-H*0.12, H-base+H*0.12
	for k := 0; k < 10; k++ {
		y0 := float32(math.Floor(float64((top+span*float32(k)/10)/c))) * c
		y1 := float32(math.Floor(float64((top+span*float32(k+1)/10)/c))) * c
		p.Rect(0, y0, W, y1-y0, th.Accent, 0.006*float32(k))
	}
	p.Flush(dst)

	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(f.cell), float64(f.cell))
	op.GeoM.Translate(0, float64(base))
	dst.DrawImage(f.px.img, op)

	for _, e := range f.embers {
		a := float32(e.life / e.max)
		tw := float32(0.6 + 0.4*math.Sin(f.t*9+e.phase*5))
		x := float32(math.Floor(e.x/float64(c))) * c
		y := float32(math.Floor(e.y/float64(c))) * c
		p.Rect(x-c, y-c, 3*c, 3*c, th.Accent, 0.1*a*tw)
		p.Rect(x, y, c, c, theme.Mix(th.Accent, f.top, a), (0.5+0.5*a)*tw)
	}
}

// hueDist returns the hue difference between two colors, in degrees.
func hueDist(a, b color.RGBA) float64 {
	d := math.Abs(hue(a) - hue(b))
	return math.Min(d, 360-d)
}

func hue(c color.RGBA) float64 {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	d := mx - mn
	if d == 0 {
		return 0
	}
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}
