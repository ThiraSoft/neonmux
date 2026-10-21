package ui

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"math/cmplx"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/audio/mp3"
	"github.com/hajimehoshi/ebiten/v2/audio/vorbis"
	"github.com/hajimehoshi/ebiten/v2/audio/wav"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// 44.1 kHz: the sample rate of most MP3 and WAV files, so no resampling.
const sampleRate = 44100

var (
	audioOnce sync.Once
	audioCtx  *audio.Context
)

// The audio context is only created on first playback: with no sound, no
// audio thread runs.
func audioContext() *audio.Context {
	audioOnce.Do(func() { audioCtx = audio.NewContext(sampleRate) })
	return audioCtx
}

// muted turns the sound off (automated screenshots).
var muted bool

const ringSize = 1 << 15 // ~0.7 s of mono samples

// tap sits between the decoder and the player to keep the latest samples,
// which the visualizer then reads.
type tap struct {
	src   io.ReadSeeker
	mu    sync.Mutex
	ring  [ringSize]float32
	total int64 // mono samples seen since the start of the stream
}

func (t *tap) Read(p []byte) (int, error) {
	n, err := t.src.Read(p)
	t.mu.Lock()
	for i := 0; i+3 < n; i += 4 {
		l := int16(binary.LittleEndian.Uint16(p[i:]))
		r := int16(binary.LittleEndian.Uint16(p[i+2:]))
		t.ring[t.total%ringSize] = (float32(l) + float32(r)) / 65536
		t.total++
	}
	t.mu.Unlock()
	return n, err
}

func (t *tap) Seek(off int64, whence int) (int64, error) {
	pos, err := t.src.Seek(off, whence)
	t.mu.Lock()
	t.total = pos / 4
	clear(t.ring[:])
	t.mu.Unlock()
	return pos, err
}

// window copies the n samples that end at index end.
func (t *tap) window(end int64, dst []float32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range dst {
		idx := end - int64(len(dst)) + int64(i)
		if idx < 0 || idx >= t.total || t.total-idx >= ringSize {
			dst[i] = 0
			continue
		}
		dst[i] = t.ring[idx%ringSize]
	}
}

const (
	fftN  = 2048
	bands = 40
)

// AudioPane plays an audio file and draws a spectrum or an oscilloscope.
type AudioPane struct {
	paneBase
	path    string
	file    *os.File
	player  *audio.Player
	tap     *tap
	length  time.Duration
	err     error
	volume  float64
	view    int // 0 spectrum, 1 oscilloscope
	samples [fftN]float32
	buf     [fftN]complex128
	level   [bands]float32
	peak    [bands]float32
	content image.Rectangle
	dirty   bool
	paused  bool // open without starting playback (restore)
	opening bool // file being opened (open)
	gen     int  // number of the last requested open
}

// NewAudioPane prepares a file for playback.
func NewAudioPane(id int, path string) *AudioPane {
	p := &AudioPane{path: path, volume: 0.8}
	p.id = id
	return p
}

func (p *AudioPane) Kind() string  { return "audio" }
func (p *AudioPane) Title() string { return filepath.Base(p.path) }
func (p *AudioPane) Mode() string  { return "" }
func (p *AudioPane) Done() bool    { return false }

func (p *AudioPane) Info() string {
	if p.opening {
		return "loading…"
	}
	if p.player == nil {
		return ""
	}
	state := "■"
	if p.player.IsPlaying() {
		state = "▶"
	}
	return fmt.Sprintf("%s %s / %s  vol %d%%", state, clock(p.player.Position()), clock(p.length), int(p.volume*100))
}

func clock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// audioSource is an open audio file, ready to play.
type audioSource struct {
	file   *os.File
	src    io.ReadSeeker
	length time.Duration
	err    error
}

// openAudio opens an audio file and sets up its decoder. go-mp3 reads the
// whole file to compute its length (268 ms for an hour, seconds on a NAS):
// it is never called on the main thread.
func openAudio(path string) audioSource {
	f, err := os.Open(path)
	if err != nil {
		return audioSource{err: err}
	}
	var src io.ReadSeeker
	var length int64
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		s, e := mp3.DecodeWithSampleRate(sampleRate, f)
		src, length, err = s, lengthOf(s, e), e
	case ".ogg":
		s, e := vorbis.DecodeWithSampleRate(sampleRate, f)
		src, length, err = s, lengthOf(s, e), e
	default:
		s, e := wav.DecodeWithSampleRate(sampleRate, f)
		src, length, err = s, lengthOf(s, e), e
	}
	if err != nil {
		f.Close()
		return audioSource{err: err}
	}
	return audioSource{file: f, src: src, length: time.Duration(length/4) * time.Second / sampleRate}
}

// open opens p.path in the background, like ImagePane.start: the pane
// shows "loading…", then the player is created on the main thread. The
// result for a track left in the meantime is dropped.
func (p *AudioPane) open(app *App) {
	p.closePlayer()
	p.gen++
	gen, path := p.gen, p.path
	p.opening, p.err, p.dirty = true, nil, true
	go func() {
		res := openAudio(path)
		app.post(func() {
			if gen != p.gen {
				if res.file != nil {
					res.file.Close()
				}
				return // track changed, or pane closed
			}
			p.opening, p.dirty = false, true
			if res.err != nil {
				p.err = res.err
				return
			}
			p.play(res)
		})
	}()
}

// play creates the player for a file opened by openAudio.
func (p *AudioPane) play(res audioSource) {
	p.file = res.file
	p.tap = &tap{src: res.src}
	p.length = res.length
	pl, err := audioContext().NewPlayer(p.tap)
	if err != nil {
		p.err = err
		return
	}
	// Small buffer: the spectrum stays in sync with the sound, and less memory.
	pl.SetBufferSize(60 * time.Millisecond)
	if muted {
		p.volume = 0
	}
	pl.SetVolume(p.volume)
	if !p.paused {
		pl.Play()
	}
	p.paused = false
	p.player = pl
	p.err = nil
}

type lengther interface{ Length() int64 }

func lengthOf(s lengther, err error) int64 {
	if err != nil {
		return 0
	}
	return s.Length()
}

func (p *AudioPane) closePlayer() {
	if p.player != nil {
		p.player.PauseAndStopReading()
		p.player = nil
	}
	if p.file != nil {
		p.file.Close()
		p.file = nil
	}
}

func (p *AudioPane) Layout(ctx *Ctx, content image.Rectangle) {
	p.content = content
	if p.player == nil && p.err == nil && !p.opening {
		p.open(ctx.App)
	}
	p.dirty = true
}

func (p *AudioPane) playing() bool { return p.player != nil && p.player.IsPlaying() }

func (p *AudioPane) Update(ctx *Ctx) bool {
	if p.playing() {
		p.analyze(float32(ctx.DT))
		return true
	}
	// After a pause, the bars fall back down, then everything stops.
	if p.decay(float32(ctx.DT)) {
		return true
	}
	d := p.dirty
	p.dirty = false
	return d
}

func (p *AudioPane) decay(dt float32) bool {
	moving := false
	for i := range p.level {
		if p.level[i] > 0.001 || p.peak[i] > 0.001 {
			p.level[i] *= float32(math.Pow(0.02, float64(dt)))
			p.peak[i] = max(0, p.peak[i]-dt*0.6)
			moving = true
		}
	}
	return moving
}

func (p *AudioPane) AnimFPS(ctx *Ctx) int {
	moving := false
	for _, v := range p.peak {
		if v > 0.001 {
			moving = true
			break
		}
	}
	if !p.playing() && !moving {
		return 0
	}
	if ctx.App.cfg.Power == "eco" {
		return 20
	}
	return 30
}

func (p *AudioPane) NextWake(ctx *Ctx) time.Time {
	if p.player != nil && !p.playing() {
		return time.Time{}
	}
	return ctx.Now.Add(time.Second) // the time counter in the header
}

func (p *AudioPane) Close() {
	p.gen++ // an open still in flight will be dropped
	p.opening = false
	p.closePlayer()
}

// analyze computes the spectrum over the window playing right now.
func (p *AudioPane) analyze(dt float32) {
	end := int64(p.player.Position().Seconds() * sampleRate)
	p.tap.window(end, p.samples[:])
	for i, s := range p.samples {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftN-1)) // Hann window
		p.buf[i] = complex(float64(s)*w, 0)
	}
	fft(p.buf[:])
	lo, hi := math.Log(40), math.Log(16000)
	for b := 0; b < bands; b++ {
		f0 := math.Exp(lo + (hi-lo)*float64(b)/bands)
		f1 := math.Exp(lo + (hi-lo)*float64(b+1)/bands)
		i0 := int(f0 * fftN / sampleRate)
		i1 := max(i0+1, int(f1*fftN/sampleRate))
		var m float64
		for i := i0; i < i1 && i < fftN/2; i++ {
			m = max(m, cmplx.Abs(p.buf[i]))
		}
		v := float32(math.Max(0, (20*math.Log10(m+1e-9)+10)/50)) // ~-10 dB to +40 dB
		v = min(v, 1)
		if v > p.level[b] {
			p.level[b] = v
		} else {
			p.level[b] = max(v, p.level[b]-dt*1.8)
		}
		if p.level[b] >= p.peak[b] {
			p.peak[b] = p.level[b]
		} else {
			p.peak[b] = max(0, p.peak[b]-dt*0.35)
		}
	}
}

// fft: iterative fast Fourier transform (radix 2, in place).
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := a[start+k], a[start+k+size/2]*wk
				a[start+k], a[start+k+size/2] = u+v, u-v
				wk *= w
			}
		}
	}
}

func (p *AudioPane) seek(d time.Duration) {
	if p.player == nil {
		return
	}
	pos := min(max(p.player.Position()+d, 0), p.length)
	_ = p.player.SetPosition(pos)
	p.dirty = true
}

func (p *AudioPane) siblings(app *App, delta int) {
	dir := filepath.Dir(p.path)
	ents, _ := os.ReadDir(dir)
	var list []string
	for _, e := range ents {
		if !e.IsDir() && isAudio(e.Name()) {
			list = append(list, filepath.Join(dir, e.Name()))
		}
	}
	if i := slices.Index(list, p.path); i >= 0 && len(list) > 1 {
		p.path = list[(i+delta+len(list))%len(list)]
		p.open(app)
	}
}

func (p *AudioPane) Key(ctx *Ctx, ev KeyEvent) bool {
	switch ev.Name {
	case "left":
		p.seek(-5 * time.Second)
	case "right":
		p.seek(5 * time.Second)
	case "up":
		p.setVolume(p.volume + 0.1)
	case "down":
		p.setVolume(p.volume - 0.1)
	default:
		return false
	}
	return true
}

func (p *AudioPane) setVolume(v float64) {
	p.volume = min(max(v, 0), 1.5)
	if p.player != nil {
		p.player.SetVolume(p.volume)
	}
	p.dirty = true
}

func (p *AudioPane) Chars(ctx *Ctx, rs []rune) {
	for _, r := range rs {
		switch r {
		case ' ':
			if p.player == nil {
				if !p.opening {
					p.open(ctx.App)
				}
			} else if p.player.IsPlaying() {
				p.player.Pause()
			} else {
				if p.player.Position() >= p.length-100*time.Millisecond {
					_ = p.player.Rewind()
				}
				p.player.Play()
			}
		case 'h':
			p.seek(-5 * time.Second)
		case 'l':
			p.seek(5 * time.Second)
		case 'H':
			p.seek(-30 * time.Second)
		case 'L':
			p.seek(30 * time.Second)
		case 'k':
			p.setVolume(p.volume + 0.1)
		case 'j':
			p.setVolume(p.volume - 0.1)
		case 'n':
			p.siblings(ctx.App, 1)
		case 'p':
			p.siblings(ctx.App, -1)
		case 'v':
			p.view = (p.view + 1) % 2
		case '0':
			p.seek(-p.length)
		case 'q':
			ctx.App.closePane(slices.Index(ctx.App.panes, Pane(p)))
		}
		p.dirty = true
	}
}

func (p *AudioPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	if ev.Kind == MousePress && p.player != nil {
		// Click on the progress bar: jump to that position.
		bar := p.barRect(ctx)
		if image.Pt(ev.X, ev.Y).In(bar.Inset(-int(ctx.Px) * 4)) {
			t := float64(ev.X-bar.Min.X) / float64(bar.Dx())
			_ = p.player.SetPosition(time.Duration(t * float64(p.length)))
		}
	}
	if ev.Kind == MouseWheel {
		p.setVolume(p.volume + ev.Wheel*0.05)
	}
	return true
}

func (p *AudioPane) barRect(ctx *Ctx) image.Rectangle {
	c := p.content
	ch := ctx.UI.CellH
	y := c.Max.Y - ch*2
	return image.Rect(c.Min.X+ch, y, c.Max.X-ch, y+int(ctx.Px)*4)
}

func (p *AudioPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	if content != p.content {
		return
	}
	th := ctx.Theme
	P := ctx.P
	f := ctx.UI
	ch := float32(f.CellH)
	if p.err != nil {
		P.Text(f, float32(content.Min.X)+ch, float32(content.Min.Y)+ch, p.err.Error(), th.Warn, 1)
		return
	}
	if p.opening {
		P.Text(f, float32(content.Min.X)+ch, float32(content.Min.Y)+ch, "loading…", th.Dim, 1)
		return
	}
	area := image.Rect(content.Min.X+f.CellH, content.Min.Y+f.CellH, content.Max.X-f.CellH, content.Max.Y-f.CellH*4)
	if p.view == 0 {
		p.drawBars(ctx, area)
	} else {
		p.drawScope(ctx, area)
	}

	bar := p.barRect(ctx)
	P.Rect(float32(bar.Min.X), float32(bar.Min.Y), float32(bar.Dx()), float32(bar.Dy()), th.Border, 1)
	if p.player != nil && p.length > 0 {
		t := float32(p.player.Position()) / float32(p.length)
		P.Rect(float32(bar.Min.X), float32(bar.Min.Y), float32(bar.Dx())*t, float32(bar.Dy()), th.Accent, 1)
		P.Rect(float32(bar.Min.X)+float32(bar.Dx())*t-2*ctx.Px, float32(bar.Min.Y)-2*ctx.Px, 4*ctx.Px, float32(bar.Dy())+4*ctx.Px, th.FG, 1)
	}
	help := "␣ play  h/l ±5s  j/k volume  n/p track  v view"
	P.Text(f, float32(bar.Min.X), float32(bar.Max.Y)+ch*0.5, help, th.Dim, 1)
}

func (p *AudioPane) drawBars(ctx *Ctx, area image.Rectangle) {
	th := ctx.Theme
	P := ctx.P
	px := ctx.Px
	bw := float32(area.Dx()) / bands
	block := max(3*px, float32(area.Dy())/28)
	nblocks := int(float32(area.Dy()) / block)
	for b := 0; b < bands; b++ {
		x := float32(area.Min.X) + float32(b)*bw
		lit := int(p.level[b] * float32(nblocks))
		for k := 0; k < lit; k++ {
			y := float32(area.Max.Y) - float32(k+1)*block
			t := float32(k) / float32(max(1, nblocks-1))
			c := theme.Mix(th.Accent2, th.Accent, t)
			if t > 0.85 {
				c = th.Warn
			}
			P.Rect(x+px, y+px, bw-2*px, block-px, c, 1)
		}
		if pk := int(p.peak[b] * float32(nblocks)); pk > 0 {
			y := float32(area.Max.Y) - float32(pk+1)*block
			P.Rect(x+px, y+block-2*px, bw-2*px, 2*px, th.FG, 0.9)
		}
		// Neon-style reflection on the floor.
		if lit > 0 {
			P.Rect(x+px, float32(area.Max.Y)+px, bw-2*px, block*float32(min(lit, 3))*0.5, th.Accent2, 0.15)
		}
	}
}

func (p *AudioPane) drawScope(ctx *Ctx, area image.Rectangle) {
	th := ctx.Theme
	P := ctx.P
	px := ctx.Px
	mid := float32(area.Min.Y + area.Dy()/2)
	P.Rect(float32(area.Min.X), mid, float32(area.Dx()), px, th.Border, 1)
	cols := int(float32(area.Dx()) / (2 * px))
	n := len(p.samples) / 2
	for c := 0; c < cols; c++ {
		i0 := c * n / cols
		i1 := max(i0+1, (c+1)*n/cols)
		lo, hi := float32(1), float32(-1)
		for _, s := range p.samples[n+i0 : n+i1] {
			lo, hi = min(lo, s), max(hi, s)
		}
		amp := float32(area.Dy()) * 0.9
		y0, y1 := mid-hi*amp, mid-lo*amp
		x := float32(area.Min.X) + float32(c)*2*px
		P.Rect(x, y0, 2*px, max(y1-y0, 2*px), th.Accent2, 0.9)
		P.Rect(x-px, y0-px, 4*px, max(y1-y0, 2*px)+2*px, th.Accent2, 0.15)
	}
}
