package gfx

import (
	_ "embed"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/hajimehoshi/bitmapfont/v4"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Glyph styles.
const (
	StyleBold   = 1
	StyleItalic = 2
)

// Built-in fonts.
const (
	FontIosevka = "iosevka" // default font, terminal and interface
	FontPixel   = "pixel"
	FontGoMono  = "gomono"
)

// Iosevka Term, subset to the glyphs a terminal needs (see iosevka/README.md).
var (
	//go:embed iosevka/IosevkaTerm-Regular.ttf
	iosevkaRegular []byte
	//go:embed iosevka/IosevkaTerm-Bold.ttf
	iosevkaBold []byte
	//go:embed iosevka/IosevkaTerm-Italic.ttf
	iosevkaItalic []byte
	//go:embed iosevka/IosevkaTerm-BoldItalic.ttf
	iosevkaBoldItalic []byte
)

const (
	pixelW = 6
	pixelH = 13
)

// Font is a monospace font ready for grid rendering.
// Glyphs are rasterized only once, at the final size in physical pixels,
// then read from the atlas.
type Font struct {
	Name     string
	CellW    int
	CellH    int
	Baseline int
	Pixel    bool
	Scale    int // integer factor of the pixel font

	faces [4]font.Face // TTF: regular, bold, italic, bold italic
	fake  [4]int       // simulated styles of each face (fake bold, fake italic)
	pivot int          // row around which the fake italic leans
	files []*os.File
	syms  font.Face // fallback for Nerd Font icons (private use area)
	symsY int

	atlas atlas
	ascii [4][128]Glyph
	glyph map[uint32]Glyph
	tofu  [2]Glyph // box for missing glyphs, single and double width
	tmp   *image.Alpha
}

// PixelScale returns the integer factor of the pixel font for a size in
// points and a display scale factor.
func PixelScale(size, dsf float64) int {
	return max(1, int(math.Round(size*dsf/7)))
}

// LoadFont loads a font: "iosevka" (also for an empty name), "pixel",
// "gomono" or the path of a TTF/OTF/TTC file. size is in
// logical points, dsf is the display scale factor.
func LoadFont(name string, size, dsf float64) (*Font, error) {
	f := &Font{Name: name, glyph: map[uint32]Glyph{}}
	f.atlas.evict = f.evict
	switch name {
	case FontPixel:
		f.Pixel = true
		f.Scale = PixelScale(size, dsf)
		f.CellW, f.CellH = pixelW*f.Scale, pixelH*f.Scale
		f.Baseline = 11 * f.Scale
		return f, nil
	// "terminus" was the interface font until v0.1.1: a config that still
	// names it gets Iosevka.
	case "", FontIosevka, FontGoMono, "terminus":
		srcs := [][]byte{gomono.TTF, gomonobold.TTF, gomonoitalic.TTF, gomonobolditalic.TTF}
		if name != FontGoMono {
			f.Name = FontIosevka
			srcs = [][]byte{iosevkaRegular, iosevkaBold, iosevkaItalic, iosevkaBoldItalic}
		}
		for i, src := range srcs {
			face, err := newFace(src, 0, size*dsf)
			if err != nil {
				return nil, err
			}
			f.faces[i] = face
		}
	default:
		face, file, err := loadFaceFile(name, size*dsf)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		f.files = append(f.files, file)
		f.faces = [4]font.Face{face, face, face, face}
		f.fake = [4]int{0, StyleBold, StyleItalic, StyleBold | StyleItalic}
		for _, v := range []struct {
			style int
			names []string
		}{
			{StyleBold, []string{"Bold"}},
			{StyleItalic, []string{"Italic", "Oblique"}},
			{StyleBold | StyleItalic, []string{"BoldItalic", "BoldOblique"}},
		} {
			if p := siblingStyle(name, v.names...); p != "" {
				if vf, vfile, err := loadFaceFile(p, size*dsf); err == nil {
					f.files = append(f.files, vfile)
					f.faces[v.style], f.fake[v.style] = vf, 0
				}
			}
		}
		// Without bold italic, thicken the real italic, or failing that, slant
		// the real bold.
		if bi := StyleBold | StyleItalic; f.fake[bi] != 0 {
			switch {
			case f.fake[StyleItalic] == 0:
				f.faces[bi], f.fake[bi] = f.faces[StyleItalic], StyleBold
			case f.fake[StyleBold] == 0:
				f.faces[bi], f.fake[bi] = f.faces[StyleBold], StyleItalic
			}
		}
	}
	m := f.faces[0].Metrics()
	adv, ok := f.faces[0].GlyphAdvance('M')
	if !ok {
		adv = fixed.I(int(size * dsf * 0.6))
	}
	f.CellW = max(1, adv.Ceil())
	f.CellH = max(1, (m.Ascent+m.Descent).Ceil()+int(math.Round(size*dsf*0.12)))
	f.Baseline = m.Ascent.Ceil() + int(math.Round(size*dsf*0.06))
	f.pivot = f.Baseline - m.XHeight.Ceil()/2
	return f, nil
}

func newFace(data []byte, index int, px float64) (font.Face, error) {
	var otf *opentype.Font
	var err error
	if index < 0 {
		var col *opentype.Collection
		col, err = opentype.ParseCollection(data)
		if err == nil {
			otf, err = col.Font(0)
		}
	} else {
		otf, err = opentype.Parse(data)
	}
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(otf, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
}

// loadFaceFile opens a font without loading it into memory: glyphs are read
// from the file on demand, then kept in the atlas.
func loadFaceFile(path string, px float64) (font.Face, *os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	var otf *opentype.Font
	if strings.HasSuffix(strings.ToLower(path), ".ttc") {
		var col *opentype.Collection
		if col, err = opentype.ParseCollectionReaderAt(file); err == nil {
			otf, err = col.Font(0)
		}
	} else {
		otf, err = opentype.ParseReaderAt(file)
	}
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	face, err := opentype.NewFace(otf, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return face, file, nil
}

// siblingStyle guesses the file of a variant: JetBrainsMono-Regular.ttf
// becomes JetBrainsMono-Bold.ttf, SF-Mono-Regular.otf SF-Mono-RegularItalic.otf,
// DejaVuSansMono.ttf DejaVuSansMono-Oblique.ttf. The first style name that
// exists wins.
func siblingStyle(path string, styles ...string) string {
	dir, file := filepath.Split(path)
	ext := filepath.Ext(file)
	for _, style := range styles {
		var cands []string
		for _, from := range []string{"Regular", "regular", "Book"} {
			if strings.Contains(file, from) {
				cands = append(cands, strings.Replace(file, from, style, 1), strings.Replace(file, from, from+style, 1))
			}
		}
		if cands == nil {
			cands = []string{strings.TrimSuffix(file, ext) + "-" + style + ext}
		}
		for _, cand := range cands {
			if _, err := os.Stat(dir + cand); err == nil {
				return dir + cand
			}
		}
	}
	return ""
}

// SetSymbols plugs in an icon font (Nerd Font) used for the Unicode private
// use area when the main font does not have the glyph.
func (f *Font) SetSymbols(path string) {
	if path == "" {
		return
	}
	// Size chosen so that the icon fits in the cell height.
	face, file, err := loadFaceFile(path, float64(f.CellH)*0.72)
	if err != nil {
		return
	}
	f.files = append(f.files, file)
	f.syms = face
	m := face.Metrics()
	f.symsY = (f.CellH-(m.Ascent+m.Descent).Ceil())/2 + m.Ascent.Ceil()
}

func isPrivateUse(r rune) bool {
	return r >= 0xe000 && r <= 0xf8ff || r >= 0xf0000
}

// Dispose frees the atlas pages on the GPU.
func (f *Font) Dispose() {
	f.atlas.dispose()
	for _, file := range f.files {
		file.Close()
	}
	f.files = nil
	f.glyph = map[uint32]Glyph{}
	f.ascii = [4][128]Glyph{}
	f.tofu = [2]Glyph{}
}

// evict forgets the glyphs of a page the atlas recycles.
func (f *Font) evict(p *atlasPage) {
	for s := range f.ascii {
		for r := range f.ascii[s] {
			if f.ascii[s][r].pg == p {
				f.ascii[s][r] = Glyph{}
			}
		}
	}
	for k, g := range f.glyph {
		if g.pg == p {
			delete(f.glyph, k)
		}
	}
	for i := range f.tofu {
		if f.tofu[i].pg == p {
			f.tofu[i] = Glyph{}
		}
	}
}

// AtlasPages returns the number of atlas pages (HUD).
func (f *Font) AtlasPages() int { return len(f.atlas.pages) }

// GlyphCount returns the number of cached glyphs (HUD).
func (f *Font) GlyphCount() int {
	n := len(f.glyph)
	for s := range f.ascii {
		for _, g := range f.ascii[s] {
			if g.W > 0 {
				n++
			}
		}
	}
	return n
}

// Glyph returns the glyph of a rune, rasterized on demand.
// wide marks a double-width character.
func (f *Font) Glyph(r rune, style int, wide bool) Glyph {
	if r < 128 && !wide {
		g := &f.ascii[style][r]
		if g.W == 0 {
			*g = f.render(r, style, false)
		}
		g.touch()
		return *g
	}
	key := uint32(r) | uint32(style)<<21
	if wide {
		key |= 1 << 23
	}
	if g, ok := f.glyph[key]; ok {
		g.touch()
		return g
	}
	g := f.render(r, style, wide)
	f.glyph[key] = g
	g.touch()
	return g
}

func (f *Font) mask(w, h int) *image.Alpha {
	if f.tmp == nil || f.tmp.Rect.Dx() != w || f.tmp.Rect.Dy() != h {
		f.tmp = image.NewAlpha(image.Rect(0, 0, w, h))
	} else {
		clear(f.tmp.Pix)
	}
	return f.tmp
}

func (f *Font) render(r rune, style int, wide bool) Glyph {
	w := f.CellW
	if wide {
		w *= 2
	}
	m := f.mask(w, f.CellH)
	if !drawProcedural(m, r, f.Pixel, f.Scale) && !f.drawGlyph(m, r, style) {
		// The replacement box depends neither on the character nor on the
		// style: one per width, shared by all missing glyphs.
		t := &f.tofu[0]
		if wide {
			t = &f.tofu[1]
		}
		if t.W == 0 {
			m = f.mask(w, f.CellH)
			drawTofu(m, f.Scale)
			*t = f.atlas.upload(m)
		}
		return *t
	}
	return f.atlas.upload(m)
}

func (f *Font) drawGlyph(m *image.Alpha, r rune, style int) bool {
	if f.syms != nil && isPrivateUse(r) {
		if _, ok := f.syms.GlyphAdvance(r); ok {
			f.drawSymbol(m, r)
			return true
		}
	}
	if f.Pixel {
		return drawPixelGlyph(m, r, style, f.Scale, f.Baseline)
	}
	face := f.faces[style]
	if _, ok := face.GlyphAdvance(r); !ok {
		// Fall back to the pixel font, scaled to the nearest factor.
		s := max(1, f.CellH/pixelH)
		return drawPixelGlyph(m, r, style&StyleBold, s, (f.CellH-pixelH*s)/2+11*s)
	}
	d := font.Drawer{Dst: m, Src: image.Opaque, Face: face, Dot: fixed.P(0, f.Baseline)}
	d.DrawString(string(r))
	if f.fake[style]&StyleBold != 0 {
		emboldenMask(m, max(1, f.CellW/14))
	}
	if f.fake[style]&StyleItalic != 0 {
		slantMask(m, f.pivot, 0.2)
	}
	return true
}

// drawSymbol draws an icon centered in the cell. In pixel mode, the icon is
// thresholded to keep sharp edges.
func (f *Font) drawSymbol(m *image.Alpha, r rune) {
	adv, _ := f.syms.GlyphAdvance(r)
	x := (m.Rect.Dx() - adv.Ceil()) / 2
	d := font.Drawer{Dst: m, Src: image.Opaque, Face: f.syms, Dot: fixed.P(max(x, 0), f.symsY)}
	d.DrawString(string(r))
	if f.Pixel {
		for i, v := range m.Pix {
			if v > 110 {
				m.Pix[i] = 255
			} else {
				m.Pix[i] = 0
			}
		}
	}
}

var pixelSrc = image.NewAlpha(image.Rect(0, 0, 2*pixelW, 16))

// drawPixelGlyph draws a glyph of the bitmap font scaled by an integer
// factor, without smoothing: each source pixel becomes a sharp block.
func drawPixelGlyph(dst *image.Alpha, r rune, style, scale, baseline int) bool {
	dr, mask, mp, _, ok := bitmapfont.Face.Glyph(fixed.P(0, 12), r)
	if !ok {
		return false
	}
	clear(pixelSrc.Pix)
	draw.Draw(pixelSrc, dr, mask, mp, draw.Src)
	// The font answers "ok" with an empty mask for missing glyphs.
	if !unicode.IsSpace(r) && !slices.ContainsFunc(pixelSrc.Pix, func(v uint8) bool { return v > 127 }) {
		return false
	}
	sw := min(dst.Rect.Dx()/scale, pixelSrc.Rect.Dx())
	top := baseline/scale - 12 // the font puts its baseline at y=12
	for sy := 0; sy < pixelSrc.Rect.Dy(); sy++ {
		shift := 0
		if style&StyleItalic != 0 && sy < 7 {
			shift = 1 // pixel italic: the top of the letters shifted by one pixel
		}
		row := pixelSrc.Pix[sy*pixelSrc.Stride:]
		for sx := 0; sx < sw; sx++ {
			on := row[sx] > 127
			// Fake bold: thicken to the right without touching the spacing
			// column, otherwise the letters run together.
			if !on && style&StyleBold != 0 && sx > 0 && sx%pixelW < pixelW-1 {
				on = row[sx-1] > 127
			}
			if on {
				fillRect(dst, (sx+shift)*scale, (sy+top)*scale, scale, scale, 255)
			}
		}
	}
	return true
}

func emboldenMask(m *image.Alpha, px int) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	for y := 0; y < h; y++ {
		row := m.Pix[y*m.Stride : y*m.Stride+w]
		for x := w - 1; x >= px; x-- {
			row[x] = max(row[x], row[x-px])
		}
	}
}

// slantMask slants a glyph to simulate italic, like the pixel font: each
// row slides by a whole number of pixels, to the right above pivot and to
// the left below it. Edges stay as crisp steps, with no resampling.
func slantMask(m *image.Alpha, pivot int, slant float64) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	for y := 0; y < h; y++ {
		d := int(math.Round(float64(pivot-y) * slant))
		d = min(max(d, -w), w)
		row := m.Pix[y*m.Stride : y*m.Stride+w]
		switch {
		case d > 0:
			copy(row[d:], row[:w-d])
			clear(row[:d])
		case d < 0:
			copy(row, row[-d:])
			clear(row[w+d:])
		}
	}
}

func drawTofu(m *image.Alpha, scale int) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	s := max(1, scale)
	x0, y0, x1, y1 := w/6, h/5, w-w/6, h-h/5
	fillRect(m, x0, y0, x1-x0, s, 160)
	fillRect(m, x0, y1-s, x1-x0, s, 160)
	fillRect(m, x0, y0, s, y1-y0, 160)
	fillRect(m, x1-s, y0, s, y1-y0, 160)
}

func fillRect(m *image.Alpha, x, y, w, h int, v uint8) {
	r := image.Rect(x, y, x+w, y+h).Intersect(m.Rect)
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		row := m.Pix[yy*m.Stride:]
		for xx := r.Min.X; xx < r.Max.X; xx++ {
			row[xx] = max(row[xx], v)
		}
	}
}
