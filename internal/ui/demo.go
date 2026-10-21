package ui

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/server"
)

// Demo assets generated on the fly: no binary data in the repository.

// demoDir returns the directory of the demo assets, inside the private
// (0700) instance directory, the one that holds the daemon socket: in a
// shared /tmp, another account could plant symbolic links there for the
// write to follow. Empty if that directory is not safe.
func demoDir() string {
	inst := server.InstanceDir()
	d := filepath.Join(inst, "demo")
	if server.PrivateDir(inst) != nil || os.MkdirAll(d, 0o700) != nil {
		return ""
	}
	return d
}

// demoSource writes a short Go program for the editor pane, so that the
// demo looks the same wherever it is launched.
func demoSource() string {
	dir := demoDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "neon.go")
	if os.WriteFile(path, []byte(demoProgram), 0o644) != nil {
		return ""
	}
	return path
}

const demoProgram = `// Command neon prints a sign in the colors of the current theme.
package main

import (
	"fmt"
	"strings"
	"time"
)

var palette = []int{201, 199, 213, 51, 45, 39}

func main() {
	sign := "N E O N M U X"
	for i := 0; ; i++ {
		var b strings.Builder
		for j, r := range sign {
			color := palette[(i+j)%len(palette)]
			fmt.Fprintf(&b, "\x1b[38;5;%dm%c", color, r)
		}
		fmt.Print("\r", b.String(), "\x1b[0m")
		time.Sleep(120 * time.Millisecond)
	}
}
`

// demoImage draws a synthwave landscape in pixel art (160×90).
func demoImage() string {
	dir := demoDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "sunset.png")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	const w, h = 160, 90
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	horizon := 56
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.RGBA
			if y < horizon {
				t := float64(y) / float64(horizon)
				c = color.RGBA{uint8(20 + 120*t), uint8(8 + 20*t), uint8(60 + 60*(1-t)), 255}
				// Striped sun.
				dx, dy := float64(x-80), float64(y-40)
				if d := math.Hypot(dx, dy*1.1); d < 26 {
					stripe := y > 34 && (y-34)%5 < (y-30)/6
					if !stripe {
						k := float64(y-14) / 30
						c = color.RGBA{255, uint8(220 - 150*k), uint8(90 + 60*k), 255}
					}
				}
				// Stars.
				if (x*7919+y*104729)%211 == 0 && y < 30 {
					c = color.RGBA{255, 255, 255, 255}
				}
			} else {
				c = color.RGBA{18, 6, 34, 255}
				z := float64(y-horizon+1) / float64(h-horizon)
				wx := (float64(x) - 80) / z
				if int(math.Abs(wx))%24 < 2 || int(1/z*12)%4 == 0 {
					c = color.RGBA{255, 46, 136, 255}
				}
			}
			img.Set(x, y, c)
		}
	}
	// Mountain silhouettes.
	for x := 0; x < w; x++ {
		top := horizon - int(8+6*math.Sin(float64(x)/9)+4*math.Sin(float64(x)/3.7))
		for y := top; y < horizon; y++ {
			img.Set(x, y, color.RGBA{40, 12, 70, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	_ = png.Encode(f, img)
	return path
}

// demoAudio composes a few bars of chiptune (square and triangle waves),
// written as 16-bit stereo WAV.
func demoAudio() string {
	dir := demoDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "chiptune.wav")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	const rate = 44100
	bpm := 132.0
	step := 60 / bpm / 4
	melody := []int{69, 72, 76, 72, 79, 76, 72, 76, 67, 71, 74, 71, 77, 74, 71, 74,
		65, 69, 72, 69, 76, 72, 69, 72, 64, 68, 71, 68, 76, 71, 68, 71}
	bass := []int{45, 45, 43, 43, 41, 41, 40, 40}
	loops := 4
	n := int(float64(len(melody)*loops) * step * rate)
	data := make([]byte, 0, n*4)
	note := func(m int) float64 { return 440 * math.Pow(2, float64(m-69)/12) }
	for i := 0; i < n; i++ {
		t := float64(i) / rate
		s := int(t / step)
		ph := t - float64(s)*step
		env := math.Exp(-ph * 9)
		lead := 0.0
		if math.Mod(t*note(melody[s%len(melody)]), 1) < 0.25 {
			lead = 1
		} else {
			lead = -1
		}
		bf := note(bass[(s/8)%len(bass)])
		tri := 4*math.Abs(math.Mod(t*bf, 1)-0.5) - 1
		kick := 0.0
		if s%4 == 0 {
			kick = math.Sin(2*math.Pi*55*ph*(1+3*math.Exp(-ph*40))) * math.Exp(-ph*18)
		}
		hat := 0.0
		if s%2 == 1 {
			hat = (math.Mod(float64(i)*12.9898, 1)*2 - 1) * math.Exp(-ph*60) * 0.4
		}
		l := 0.22*lead*env + 0.3*tri + 0.5*kick + 0.15*hat
		r := 0.18*lead*env + 0.3*tri + 0.5*kick + 0.2*hat
		data = binary.LittleEndian.AppendUint16(data, uint16(int16(l*24000)))
		data = binary.LittleEndian.AppendUint16(data, uint16(int16(r*24000)))
	}
	var hdr []byte
	hdr = append(hdr, "RIFF"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(36+len(data)))
	hdr = append(hdr, "WAVEfmt "...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 16)
	hdr = binary.LittleEndian.AppendUint16(hdr, 1)
	hdr = binary.LittleEndian.AppendUint16(hdr, 2)
	hdr = binary.LittleEndian.AppendUint32(hdr, rate)
	hdr = binary.LittleEndian.AppendUint32(hdr, rate*4)
	hdr = binary.LittleEndian.AppendUint16(hdr, 4)
	hdr = binary.LittleEndian.AppendUint16(hdr, 16)
	hdr = append(hdr, "data"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(data)))
	if err := os.WriteFile(path, append(hdr, data...), 0o644); err != nil {
		return ""
	}
	return path
}

// handleDrops opens the files dropped onto the window (images and sounds).
func (a *App) handleDrops(ctx *Ctx) {
	dropped := ebiten.DroppedFiles()
	if dropped == nil {
		return
	}
	for _, p := range droppedPaths(dropped) {
		a.OpenPath(p)
	}
}

// droppedPaths returns the real path of the dropped images and sounds,
// including those inside dropped directories. Since Ebitengine 2.10, the
// entries of DroppedFiles implement ebiten.AbsPather: the files open where
// they are, without being read in Update or copied.
func droppedPaths(dropped fs.FS) []string {
	var out []string
	_ = fs.WalkDir(dropped, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isImage(path) && !isAudio(path) {
			return nil
		}
		if ap, ok := d.(ebiten.AbsPather); ok {
			out = append(out, ap.AbsPath())
		}
		return nil
	})
	return out
}
