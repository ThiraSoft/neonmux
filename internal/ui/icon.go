package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// The application icon is drawn on a grid of 32×32 "big pixels", then
// scaled up without smoothing to each size macOS asks for.

func iconPixels() *image.RGBA {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	bg := color.RGBA{0x0b, 0x0a, 0x1a, 0xff}
	pink := color.RGBA{0xff, 0x2e, 0x88, 0xff}
	cyan := color.RGBA{0x00, 0xf0, 0xff, 0xff}
	gold := color.RGBA{0xff, 0xd4, 0x00, 0xff}
	set := func(x, y int, c color.RGBA) { img.SetRGBA(x, y, c) }
	inside := func(x, y int) bool {
		// Square with rounded corners (macOS icon template: margin of 2).
		if x < 2 || y < 2 || x > 29 || y > 29 {
			return false
		}
		cx, cy := min(max(x, 6), 25), min(max(y, 6), 25)
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= 16
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !inside(x, y) {
				continue
			}
			c := bg
			c.B += uint8(y / 4) // slight gradient toward the bottom
			if y%2 == 1 {       // scanlines
				c = color.RGBA{uint8(int(c.R) * 8 / 10), uint8(int(c.G) * 8 / 10), uint8(int(c.B) * 8 / 10), 0xff}
			}
			set(x, y, c)
			// Neon border.
			if !inside(x-1, y) || !inside(x+1, y) || !inside(x, y-1) || !inside(x, y+1) {
				set(x, y, pink)
			}
		}
	}
	// Title bar and its three dots.
	for x := 5; x <= 26; x++ {
		set(x, 7, color.RGBA{0x2a, 0x25, 0x50, 0xff})
	}
	set(6, 5, pink)
	set(8, 5, gold)
	set(10, 5, cyan)
	// Prompt chevron.
	for i := 0; i < 4; i++ {
		set(7+i, 11+i, pink)
		set(8+i, 11+i, pink)
		set(7+i, 18-i, pink)
		set(8+i, 18-i, pink)
	}
	// Block cursor.
	for y := 16; y <= 18; y++ {
		for x := 14; x <= 19; x++ {
			set(x, y, cyan)
		}
	}
	// Synthwave grid at the bottom.
	for x := 4; x <= 27; x++ {
		if inside(x, 24) {
			set(x, 24, color.RGBA{0xc0, 0x22, 0x6a, 0xff})
		}
	}
	for _, x := range []int{8, 12, 16, 20, 24} {
		for y := 25; y <= 27; y++ {
			dx := (x - 16) * (y - 23) / 3
			if inside(16+dx, y) {
				set(16+dx, y, color.RGBA{0x80, 0x1a, 0x50, 0xff})
			}
		}
	}
	return img
}

// WriteIconset writes the PNGs of an .iconset directory (to be converted by iconutil).
func WriteIconset(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src := iconPixels()
	for _, size := range []int{16, 32, 128, 256, 512} {
		for _, scale := range []int{1, 2} {
			px := size * scale
			out := image.NewRGBA(image.Rect(0, 0, px, px))
			for y := 0; y < px; y++ {
				for x := 0; x < px; x++ {
					out.Set(x, y, src.At(x*32/px, y*32/px))
				}
			}
			name := fmt.Sprintf("icon_%dx%d.png", size, size)
			if scale == 2 {
				name = fmt.Sprintf("icon_%dx%d@2x.png", size, size)
			}
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			err = png.Encode(f, out)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
