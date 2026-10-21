package ui

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// allocated returns the bytes allocated during fn.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// hugePNG writes a grayscale PNG of w×h black pixels: a few hundred KB on
// disk, but w×h bytes once decoded.
func hugePNG(t *testing.T, w, h int) string {
	t.Helper()
	var png bytes.Buffer
	png.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		binary.Write(&png, binary.BigEndian, uint32(len(data)))
		png.WriteString(typ)
		png.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		binary.Write(&png, binary.BigEndian, crc.Sum32())
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8] = 8 // 8 bits, grayscale
	chunk("IHDR", ihdr)
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	row := make([]byte, w+1)
	for range h {
		zw.Write(row)
	}
	zw.Close()
	chunk("IDAT", z.Bytes())
	chunk("IEND", nil)
	p := filepath.Join(t.TempDir(), "huge.png")
	if err := os.WriteFile(p, png.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// gifFile encodes img, then replaces the logical screen size with w×h
// (0: the size of the image).
func gifFile(t *testing.T, name string, img *image.Paletted, w, h int) string {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	if w > 0 {
		binary.LittleEndian.PutUint16(data[6:], uint16(w))
		binary.LittleEndian.PutUint16(data[8:], uint16(h))
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

var twoColors = color.Palette{color.Black, color.White}

// A 100 Mpx image is refused based on its header, before anything is
// allocated (382 MB before, for a mere thumbnail).
func TestDecodeRefusesHugeImage(t *testing.T) {
	p := hugePNG(t, 10000, 10000)
	var err error
	n := allocated(func() { _, _, err = decodeImageFile(p, 384) })
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected an error, got %v", err)
	}
	if n > 16<<20 {
		t.Errorf("%d MB allocated for a refused image", n>>20)
	}
}

// A GIF's canvas comes from its header: 6000×6000 announced for a single
// pixel used to allocate 412 MB. It is now scaled down.
func TestDecodeGIFCanvasBounded(t *testing.T) {
	p := gifFile(t, "canvas.gif", image.NewPaletted(image.Rect(0, 0, 1, 1), twoColors), 6000, 6000)
	var d decodedImage
	n := allocated(func() { d = decodeAll(p) })
	if d.err != nil || len(d.frames) != 1 {
		t.Fatalf("%+v", d.err)
	}
	if b := d.frames[0].Bounds(); b.Dx() > gifMaxSide || b.Dy() > gifMaxSide {
		t.Errorf("frame of %v", b)
	}
	if n > 64<<20 {
		t.Errorf("%d MB allocated for a GIF of a few bytes", n>>20)
	}
	// 65535×65535: 17 GB per buffer. Refused, without a panic.
	p = gifFile(t, "giant.gif", image.NewPaletted(image.Rect(0, 0, 1, 1), twoColors), 65535, 65535)
	if d := decodeAll(p); d.err == nil || !strings.Contains(d.err.Error(), "too large") {
		t.Fatalf("giant GIF accepted: %v", d.err)
	}
}

// Every frame of a GIF goes through shrink: wider than MaxImageSize
// (16384), it made the Ebiten atlas panic.
func TestDecodeGIFFramesShrunk(t *testing.T) {
	p := gifFile(t, "banner.gif", image.NewPaletted(image.Rect(0, 0, 20000, 4), twoColors), 0, 0)
	d := decodeAll(p)
	if d.err != nil || len(d.frames) == 0 {
		t.Fatal(d.err)
	}
	for _, fr := range d.frames {
		if b := fr.Bounds(); b.Dx() > gifMaxSide || b.Dy() > gifMaxSide {
			t.Fatalf("frame of %v, want at most %d", b, gifMaxSide)
		}
	}
	if d.w != 20000 || d.h != 4 {
		t.Errorf("reported size %d×%d, want the size of the file", d.w, d.h)
	}
}

// GIF bomb: the same large frame repeated, which compresses very well.
// Only the first frames are decoded.
func TestDecodeGIFBombBounded(t *testing.T) {
	var b bytes.Buffer
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 4000, 4000), twoColors), nil); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	head := 13 + 3<<(data[10]&7+1)
	frame := data[head : len(data)-1]
	bomb := append([]byte(nil), data[:head]...)
	for range 60 {
		bomb = append(bomb, frame...)
	}
	bomb = append(bomb, 0x3B)
	p := filepath.Join(t.TempDir(), "bomb.gif")
	os.WriteFile(p, bomb, 0o644)
	var d decodedImage
	n := allocated(func() { d = decodeAll(p) })
	if d.err != nil || len(d.frames) == 0 {
		t.Fatal(d.err)
	}
	if n > 400<<20 {
		t.Errorf("%d MB allocated (%d KB on disk)", n>>20, len(bomb)>>10)
	}
}

// The pane shows the error instead of crashing, including when it is
// restored from layout.json at launch.
func TestImagePaneShowsTooBig(t *testing.T) {
	p := gifFile(t, "giant.gif", image.NewPaletted(image.Rect(0, 0, 1, 1), twoColors), 65535, 65535)
	a := featureApp()
	ip := NewImagePane(1, p)
	ip.start(a)
	drain(t, a, 5*time.Second, func() bool { return !ip.loading })
	if ip.err == nil || !strings.Contains(ip.err.Error(), "too large") || len(ip.frames) != 0 {
		t.Fatalf("error %v, %d frames", ip.err, len(ip.frames))
	}
}

// A small animated GIF keeps its frames, its delays and its disposal
// modes.
func TestDecodeGIFAnimationKept(t *testing.T) {
	pal := color.Palette{color.Transparent, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	g := &gif.GIF{}
	for i, c := range []uint8{1, 2, 1} {
		fr := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		for j := range fr.Pix {
			fr.Pix[j] = c
		}
		if i == 2 {
			fr = fr.SubImage(image.Rect(0, 0, 4, 4)).(*image.Paletted)
		}
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, 5)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "anim.gif")
	os.WriteFile(p, b.Bytes(), 0o644)
	d := decodeAll(p)
	if d.err != nil || len(d.frames) != 3 || d.delays[0] != 50*time.Millisecond || d.w != 8 {
		t.Fatalf("%v %d %v", d.err, len(d.frames), d.delays)
	}
	// The third frame only covers a corner: the rest comes from the second one.
	last := d.frames[2]
	if r, _, _, _ := last.At(1, 1).RGBA(); r>>8 != 255 {
		t.Errorf("corner: %v", last.At(1, 1))
	}
	if _, _, bl, _ := last.At(6, 6).RGBA(); bl>>8 != 255 {
		t.Errorf("rest of the previous frame: %v", last.At(6, 6))
	}
}

// The error message fits in the pane: wrapped between words.
func TestWrapWords(t *testing.T) {
	got := wrapWords("image too large: 65535×65535 (4295 Mpx), at most 64 Mpx", 24)
	want := []string{"image too large:", "65535×65535 (4295 Mpx),", "at most 64 Mpx"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}
