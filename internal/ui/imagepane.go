package ui

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

// Decoding limits. An image header announces its size: it is read first
// (image.DecodeConfig), and an image that is too large is refused before
// anything is allocated. 64 Mpx lets 48 Mpx photos through, not a
// 10000×10000. Decoding memory also depends on the bit depth (8 bytes per
// pixel for a 16-bit PNG).
const (
	maxImagePixels = 64_000_000
	maxImageBytes  = 256 << 20
	imageMaxSide   = 4096 // long side kept for display
	// An animated GIF keeps all its composed frames: its canvas is
	// scaled down to 2048 px per side.
	gifMaxSide = 2048
	// gif.DecodeAll keeps every frame of the file in memory (one byte
	// per pixel): past this total, the following frames are ignored.
	gifMaxPixels = 64 << 20
	gifMaxFile   = 64 << 20 // maximum bytes read
)

// openRegular opens a regular file for reading. A named pipe or a device
// is refused: on a pipe, os.Open would wait for a writer forever.
// O_NONBLOCK covers the case where the file is replaced by a pipe between
// the stat and the open.
func openRegular(path string) (*os.File, error) {
	if st, err := os.Stat(path); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, errNotRegular(st.Mode())
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errNotRegular(st.Mode())
		}
		return nil, err
	}
	return f, nil
}

// errNotRegular describes a special file.
func errNotRegular(m os.FileMode) error {
	kind := "special file"
	switch {
	case m&os.ModeNamedPipe != 0:
		kind = "named pipe"
	case m&os.ModeSocket != 0:
		kind = "socket"
	case m&os.ModeDevice != 0:
		kind = "device"
	case m.IsDir():
		kind = "directory"
	}
	return errors.New(kind + ": no preview")
}

// bytesPerPixel estimates the memory of a decoded pixel from the color
// model announced by the header (YCbCr counted at worst, as 4:4:4).
func bytesPerPixel(m color.Model) int64 {
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	switch m {
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	case color.Gray16Model:
		return 2
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.YCbCrModel:
		return 3
	}
	return 4
}

// checkImageSize refuses an image whose decoding would exceed the limits.
func checkImageSize(c image.Config) error {
	px := int64(c.Width) * int64(c.Height)
	if px > maxImagePixels {
		return fmt.Errorf("image too large: %d×%d (%.0f Mpx), at most %d Mpx", c.Width, c.Height, float64(px)/1e6, maxImagePixels/1_000_000)
	}
	if n := px * bytesPerPixel(c.ColorModel); n > maxImageBytes {
		return fmt.Errorf("image too large: %d×%d (%d MB once decoded), at most %d MB", c.Width, c.Height, n>>20, maxImageBytes>>20)
	}
	return nil
}

// fitSide caps maxSide at the maximum size of an Ebiten image: beyond it,
// the atlas panics on the first draw. MaxImageSize is 0 until the graphics
// driver is ready.
func fitSide(maxSide int) int {
	if m := ebiten.MaxImageSize(); m > 0 && (maxSide <= 0 || maxSide > m) {
		return m
	}
	return maxSide
}

// decodeImageFile decodes an image and scales it down if its long side
// exceeds maxSide: a 24 Mpx photo would otherwise take ~100 MB of RAM and
// VRAM. The header is read first: an image that is too large is refused
// without being decoded.
func decodeImageFile(path string, maxSide int) (image.Image, string, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return nil, "", err
	}
	if err := checkImageSize(cfg); err != nil {
		return nil, format, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	img, format, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return nil, "", err
	}
	return shrink(img, fitSide(maxSide)), format, nil
}

func shrink(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	if maxSide <= 0 || (b.Dx() <= maxSide && b.Dy() <= maxSide) {
		return img
	}
	s := float64(maxSide) / float64(max(b.Dx(), b.Dy()))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(b.Dx())*s)), max(1, int(float64(b.Dy())*s))))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

type decodedImage struct {
	frames []image.Image
	delays []time.Duration
	format string
	w, h   int
	size   int64
	err    error
}

// decodeAll also handles animated GIFs. A decoder that panics on a
// malformed file gives an error shown in the pane: restored from
// layout.json, it would otherwise crash every launch.
func decodeAll(path string) (d decodedImage) {
	defer func() {
		if r := recover(); r != nil {
			d = decodedImage{err: fmt.Errorf("unreadable image: %v", r)}
		}
	}()
	if strings.ToLower(filepath.Ext(path)) == ".gif" {
		return decodeGIF(path)
	}
	img, format, err := decodeImageFile(path, imageMaxSide)
	if err != nil {
		return decodedImage{err: err}
	}
	b := img.Bounds()
	return decodedImage{frames: []image.Image{img}, format: format, w: b.Dx(), h: b.Dy()}
}

// decodeGIF composes the frames of a GIF according to their disposal mode,
// on a canvas scaled down to gifMaxSide, within a limit of 64 MB of frames.
func decodeGIF(path string) decodedImage {
	f, err := openRegular(path)
	if err != nil {
		return decodedImage{err: err}
	}
	defer f.Close()
	cfg, err := gif.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return decodedImage{err: err}
	}
	if err := checkImageSize(image.Config{ColorModel: color.RGBAModel, Width: cfg.Width, Height: cfg.Height}); err != nil {
		return decodedImage{err: err}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return decodedImage{err: err}
	}
	data, err := io.ReadAll(io.LimitReader(f, gifMaxFile))
	if err != nil {
		return decodedImage{err: err}
	}
	g, err := gif.DecodeAll(bytes.NewReader(gifPrefix(data, gifMaxPixels)))
	if err != nil {
		return decodedImage{err: err}
	}
	w, h := g.Config.Width, g.Config.Height
	d := decodedImage{format: "gif", w: w, h: h}
	// Scaled-down canvas: each frame is drawn onto it to scale.
	s := 1.0
	if side := fitSide(gifMaxSide); max(w, h) > side {
		s = float64(side) / float64(max(w, h))
	}
	scaled := func(r image.Rectangle) image.Rectangle {
		return image.Rect(int(math.Floor(float64(r.Min.X)*s)), int(math.Floor(float64(r.Min.Y)*s)),
			int(math.Ceil(float64(r.Max.X)*s)), int(math.Ceil(float64(r.Max.Y)*s)))
	}
	canvas := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*s)), max(1, int(float64(h)*s))))
	budget := 64 << 20
	for i, fr := range g.Image {
		if budget -= len(canvas.Pix); budget < 0 && i > 0 {
			break
		}
		var before *image.RGBA
		if g.Disposal[i] == gif.DisposalPrevious {
			before = image.NewRGBA(canvas.Bounds())
			copy(before.Pix, canvas.Pix)
		}
		if s == 1 {
			draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		} else {
			xdraw.ApproxBiLinear.Scale(canvas, scaled(fr.Bounds()), fr, fr.Bounds(), xdraw.Over, nil)
		}
		out := image.NewRGBA(canvas.Bounds())
		copy(out.Pix, canvas.Pix)
		d.frames = append(d.frames, out)
		delay := time.Duration(g.Delay[i]) * 10 * time.Millisecond
		if delay < 20*time.Millisecond {
			delay = 100 * time.Millisecond
		}
		d.delays = append(d.delays, delay)
		switch g.Disposal[i] {
		case gif.DisposalBackground:
			draw.Draw(canvas, scaled(fr.Bounds()), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = before
		}
	}
	return d
}

// gifPrefix cuts a GIF after the last complete frame that fits within
// maxPixels total pixels (the first one is always kept), and ends it like a
// complete file. gif.DecodeAll keeps every frame in memory: a few MB of
// uniform frames, which compress very well, would take up gigabytes.
func gifPrefix(data []byte, maxPixels int) []byte {
	if len(data) < 13 {
		return data
	}
	pos := 13 // header and logical screen
	if data[10]&0x80 != 0 {
		pos += 3 << (data[10]&7 + 1) // global color table
	}
	// subBlocks skips a run of sub-blocks; -1 if it is incomplete.
	subBlocks := func(p int) int {
		for p < len(data) {
			n := int(data[p])
			p += 1 + n
			if n == 0 {
				return p
			}
		}
		return -1
	}
	end, pixels, frames := pos, 0, 0
scan:
	for pos < len(data) {
		switch data[pos] {
		case 0x21: // extension: label then sub-blocks
			p := subBlocks(pos + 2)
			if p < 0 {
				break scan
			}
			pos = p
		case 0x2C: // image: descriptor, local color table, LZW code, data
			if pos+10 > len(data) {
				break scan
			}
			px := (int(data[pos+5]) | int(data[pos+6])<<8) * (int(data[pos+7]) | int(data[pos+8])<<8)
			p := pos + 10
			if data[pos+9]&0x80 != 0 {
				p += 3 << (data[pos+9]&7 + 1)
			}
			if p = subBlocks(p + 1); p < 0 || frames > 0 && pixels+px > maxPixels {
				break scan
			}
			pixels += px
			frames++
			pos, end = p, p
		case 0x3B: // trailer
			return data[:pos+1]
		default:
			break scan
		}
	}
	return append(data[:end:end], 0x3B)
}

// ImagePane shows an image (animated GIFs included), with vim-style zoom and panning.
type ImagePane struct {
	paneBase
	path     string
	frames   []*ebiten.Image
	delays   []time.Duration
	frame    int
	frameAt  time.Time
	w, h     int
	format   string
	size     int64
	zoom     float64 // 0 = fit to the pane
	panX     float64
	panY     float64
	nearest  bool
	err      error
	loading  bool
	content  image.Rectangle
	dirty    bool
	drag     bool
	dragFrom image.Point
}

// NewImagePane opens an image; decoding happens in the background.
func NewImagePane(id int, path string) *ImagePane {
	p := &ImagePane{path: path}
	p.id = id
	return p
}

func (p *ImagePane) Kind() string  { return "image" }
func (p *ImagePane) Title() string { return filepath.Base(p.path) }
func (p *ImagePane) Mode() string  { return "" }
func (p *ImagePane) Done() bool    { return false }

func (p *ImagePane) Info() string {
	if p.err != nil {
		return ""
	}
	if p.loading || p.w == 0 {
		return "loading…"
	}
	z := "fit"
	if p.zoom > 0 {
		z = fmt.Sprintf("%.0f%%", p.zoom*100)
	}
	s := fmt.Sprintf("%d×%d %s %s %s", p.w, p.h, strings.ToUpper(p.format), humanSize(p.size), z)
	if len(p.frames) > 1 {
		s += fmt.Sprintf(" %d frames", len(p.frames))
	}
	return s
}

func (p *ImagePane) start(app *App) {
	p.loading = true
	path := p.path
	go func() {
		d := decodeAll(path)
		if st, err := os.Stat(path); err == nil {
			d.size = st.Size()
		}
		app.post(func() {
			if path != p.path {
				return
			}
			p.loading = false
			p.err = d.err
			p.disposeFrames()
			for _, fr := range d.frames {
				p.frames = append(p.frames, ebiten.NewImageFromImage(fr))
			}
			p.delays, p.w, p.h, p.format, p.size = d.delays, d.w, d.h, d.format, d.size
			p.frame, p.frameAt = 0, time.Now()
			// Pixel art: small images are enlarged without smoothing.
			p.nearest = p.w <= 512 && p.h <= 512
			p.dirty = true
		})
	}()
}

func (p *ImagePane) disposeFrames() {
	for _, f := range p.frames {
		f.Deallocate()
	}
	p.frames = nil
}

func (p *ImagePane) Layout(ctx *Ctx, content image.Rectangle) {
	p.content = content
	if !p.loading && p.frames == nil && p.err == nil {
		p.start(ctx.App)
	}
	p.dirty = true
}

func (p *ImagePane) Update(ctx *Ctx) bool {
	if len(p.frames) > 1 && ctx.Now.Sub(p.frameAt) >= p.delays[p.frame] {
		p.frame = (p.frame + 1) % len(p.frames)
		p.frameAt = ctx.Now
		p.dirty = true
	}
	d := p.dirty
	p.dirty = false
	return d
}

func (p *ImagePane) AnimFPS(ctx *Ctx) int {
	if p.loading {
		return 12 // small spinner while decoding
	}
	return 0
}

// NextWake schedules the next frame of a GIF instead of animating continuously.
func (p *ImagePane) NextWake(ctx *Ctx) time.Time {
	if len(p.frames) > 1 {
		return p.frameAt.Add(p.delays[p.frame])
	}
	return time.Time{}
}

func (p *ImagePane) Close() { p.disposeFrames() }

func (p *ImagePane) siblings(delta int) {
	dir := filepath.Dir(p.path)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var imgs []string
	for _, e := range ents {
		if !e.IsDir() && isImage(e.Name()) {
			imgs = append(imgs, filepath.Join(dir, e.Name()))
		}
	}
	i := slices.Index(imgs, p.path)
	if i < 0 || len(imgs) < 2 {
		return
	}
	p.path = imgs[(i+delta+len(imgs))%len(imgs)]
	p.zoom, p.panX, p.panY = 0, 0, 0
	p.frames, p.w = nil, 0
}

func (p *ImagePane) fitScale() float64 {
	if p.w == 0 {
		return 1
	}
	s := min(float64(p.content.Dx())/float64(p.w), float64(p.content.Dy())/float64(p.h))
	if p.nearest && s >= 1 {
		s = float64(int(s)) // integer scaling: sharp pixels
	}
	return s
}

func (p *ImagePane) setZoom(z float64) {
	p.zoom = min(max(z, 0.05), 64)
	p.dirty = true
}

func (p *ImagePane) Key(ctx *Ctx, ev KeyEvent) bool {
	step := float64(ctx.UI.CellH * 3)
	switch ev.Name {
	case "left":
		p.panX += step
	case "right":
		p.panX -= step
	case "up":
		p.panY += step
	case "down":
		p.panY -= step
	default:
		return false
	}
	p.dirty = true
	return true
}

func (p *ImagePane) Chars(ctx *Ctx, rs []rune) {
	step := float64(ctx.UI.CellH * 3)
	for _, r := range rs {
		cur := p.zoom
		if cur == 0 {
			cur = p.fitScale()
		}
		switch r {
		case '+', '=':
			p.setZoom(cur * 1.25)
		case '-':
			p.setZoom(cur / 1.25)
		case '0':
			p.zoom, p.panX, p.panY = 0, 0, 0
		case '1':
			p.setZoom(1)
		case 'h':
			p.panX += step
		case 'l':
			p.panX -= step
		case 'k':
			p.panY += step
		case 'j':
			p.panY -= step
		case 'i':
			p.nearest = !p.nearest
		case 'n':
			p.siblings(1)
			p.start(ctx.App)
		case 'p':
			p.siblings(-1)
			p.start(ctx.App)
		case 'q':
			ctx.App.closePane(slices.Index(ctx.App.panes, Pane(p)))
		}
		p.dirty = true
	}
}

func (p *ImagePane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	switch ev.Kind {
	case MouseWheel:
		cur := p.zoom
		if cur == 0 {
			cur = p.fitScale()
		}
		if ev.Wheel > 0 {
			p.setZoom(cur * 1.1)
		} else {
			p.setZoom(cur / 1.1)
		}
	case MousePress:
		p.drag, p.dragFrom = true, image.Pt(ev.X, ev.Y)
	case MouseMove:
		if p.drag {
			p.panX += float64(ev.X - p.dragFrom.X)
			p.panY += float64(ev.Y - p.dragFrom.Y)
			p.dragFrom = image.Pt(ev.X, ev.Y)
			p.dirty = true
		}
	case MouseRelease:
		p.drag = false
	}
	return true
}

func (p *ImagePane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	th := ctx.Theme
	P := ctx.P
	// Checkerboard for transparency: a repeated 2×2 texture, a single quad.
	drawChecker(dst, content, th.BG, float32(8*ctx.Px))
	if p.err != nil {
		// The message (an image that is too large, for example) is wrapped
		// to the width of the pane.
		f := ctx.UI
		for i, l := range wrapWords(p.err.Error(), (content.Dx()-16)/f.CellW) {
			P.Text(f, float32(content.Min.X+8), float32(content.Min.Y+8+i*f.CellH), l, th.Warn, 1)
		}
		return
	}
	if p.loading || len(p.frames) == 0 {
		// Pixel spinner: eight rotating blocks.
		cx, cy := float32(content.Min.X+content.Dx()/2), float32(content.Min.Y+content.Dy()/2)
		k := int(ctx.Now.UnixMilli()/90) % 8
		for i := 0; i < 8; i++ {
			dx := [8]float32{0, 1, 1, 1, 0, -1, -1, -1}[i]
			dy := [8]float32{-1, -1, 0, 1, 1, 1, 0, -1}[i]
			s := 4 * ctx.Px
			a := float32((i-k+8)%8) / 8
			P.Rect(cx+dx*s*2-s/2, cy+dy*s*2-s/2, s, s, th.Accent, 1-a)
		}
		return
	}
	img := p.frames[p.frame]
	s := p.zoom
	if s == 0 {
		s = p.fitScale()
	}
	w, h := float64(p.w)*s, float64(p.h)*s
	x := float64(content.Min.X) + (float64(content.Dx())-w)/2 + p.panX
	y := float64(content.Min.Y) + (float64(content.Dy())-h)/2 + p.panY
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(s, s)
	op.GeoM.Translate(x, y)
	op.Filter = ebiten.FilterLinear
	if p.nearest {
		op.Filter = ebiten.FilterNearest
	}
	sub := dst.SubImage(content).(*ebiten.Image)
	sub.DrawImage(img, op)
}

// wrapWords splits s into lines of at most cols cells, between words (a
// word that is too long is truncated).
func wrapWords(s string, cols int) []string {
	if cols <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case gfx.TextCells(cur)+1+gfx.TextCells(w) <= cols:
			cur += " " + w
		default:
			lines = append(lines, gfx.Truncate(cur, cols))
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, gfx.Truncate(cur, cols))
	}
	return lines
}

var (
	checkerImg   *ebiten.Image
	checkerColor color.RGBA
)

func drawChecker(dst *ebiten.Image, r image.Rectangle, base color.RGBA, sq float32) {
	if checkerImg == nil || checkerColor != base {
		if checkerImg == nil {
			checkerImg = ebiten.NewImage(2, 2)
		}
		checkerColor = base
		hi := color.RGBA{uint8(min(255, int(base.R)+14)), uint8(min(255, int(base.G)+14)), uint8(min(255, int(base.B)+18)), 255}
		checkerImg.WritePixels([]byte{
			hi.R, hi.G, hi.B, 255, base.R, base.G, base.B, 255,
			base.R, base.G, base.B, 255, hi.R, hi.G, hi.B, 255,
		})
	}
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	u, v := float32(r.Dx())/sq, float32(r.Dy())/sq
	vs := []ebiten.Vertex{
		{DstX: x0, DstY: y0, SrcX: 0, SrcY: 0, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: x1, DstY: y0, SrcX: u, SrcY: 0, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: x0, DstY: y1, SrcX: 0, SrcY: v, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
		{DstX: x1, DstY: y1, SrcX: u, SrcY: v, ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1},
	}
	dst.DrawTriangles(vs, []uint16{0, 1, 2, 1, 3, 2}, checkerImg, &ebiten.DrawTrianglesOptions{Address: ebiten.AddressRepeat})
}
