package ui

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
)

// TestTermImageStraightAlpha: f=32 data is straight alpha, as kitty
// defines it. Decoded as premultiplied, a pixel whose color exceeds its
// alpha would be drawn as a bright fringe.
func TestTermImageStraightAlpha(t *testing.T) {
	img := vt.Image{ID: 1, Format: 32, W: 1, H: 1, Data: []byte{255, 0, 0, 128}}
	m, err := decodeTermImage(img, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := color.NRGBAModel.Convert(m.At(0, 0)).(color.NRGBA)
	if got != (color.NRGBA{255, 0, 0, 128}) {
		t.Fatalf("pixel %+v, want straight red at half alpha", got)
	}
}

// bigPNGHeader returns a PNG whose header announces w x h, with no pixel
// data: decoding it for real would allocate w*h*4 bytes first.
func bigPNGHeader(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// Signature (8), length (4), "IHDR" (4), then width and height.
	binary.BigEndian.PutUint32(b[16:], uint32(w))
	binary.BigEndian.PutUint32(b[20:], uint32(h))
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return b
}

// TestTermImageDecodeBounded: an image too large is refused from its
// header, before any pixel is allocated; a wide one is scaled down to the
// maximum side.
func TestTermImageDecodeBounded(t *testing.T) {
	huge := vt.Image{ID: 1, Format: 100, W: 10000, H: 10000, Data: bigPNGHeader(t, 10000, 10000)}
	if _, err := decodeTermImage(huge, 4096); !errors.Is(err, errTermImageTooLarge) {
		t.Fatalf("10000x10000 PNG: err %v, want errTermImageTooLarge", err)
	}
	lie := vt.Image{ID: 2, Format: 100, W: 10, H: 10, Data: bigPNGHeader(t, 10000, 10000)}
	if _, err := decodeTermImage(lie, 4096); err == nil {
		t.Fatal("PNG whose header differs from the announced size accepted")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 5000, 20))); err != nil {
		t.Fatal(err)
	}
	wide := vt.Image{ID: 3, Format: 100, W: 5000, H: 20, Data: buf.Bytes()}
	m, err := decodeTermImage(wide, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if b := m.Bounds(); b.Dx() > 4096 || b.Dy() > 20 {
		t.Fatalf("wide image decoded at %v, want a long side of at most 4096", b)
	}
}

// TestKittyShrunkImageKeepsGeometry: an image scaled down for the GPU is
// still placed and sized in its own pixels.
func TestKittyShrunkImageKeepsGeometry(t *testing.T) {
	term := vt.New(80, 24, 100)
	term.SetPixelSize(800, 480)
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 5000, 20))); err != nil {
		t.Fatal(err)
	}
	term.Write([]byte(fmt.Sprintf("\x1b_Ga=T,f=100,i=1,q=2;%s\x1b\\", base64.StdEncoding.EncodeToString(buf.Bytes()))))
	ti := &termImages{}
	ti.prepare(term, 0)
	ti.syncCache()
	settleDecodes(t, ti)
	ops := ti.computeOps(image.Pt(0, 0), 10, 20)
	if len(ops) != 1 {
		t.Fatalf("%d ops, want 1", len(ops))
	}
	op := ops[0]
	if op.dst.Dx() != 5000 || op.dst.Dy() != 20 {
		t.Fatalf("dst %v, want 5000x20", op.dst)
	}
	if b := op.img.Bounds(); b.Dx() > 4096 || !op.src.In(b) {
		t.Fatalf("src %v outside texture %v", op.src, b)
	}
}

// TestKittyDecodeOffRenderThread: drawing never decodes. The image shows
// up once the decoder is done, and the pane asks for a redraw then.
func TestKittyDecodeOffRenderThread(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	decodeSlots <- struct{}{} // the decoder is busy
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100;%s\x1b\\", redPNG(20, 20))))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)
	if len(p.images.ops) != 0 || len(p.images.cache) != 0 {
		<-decodeSlots
		t.Fatal("image decoded while drawing")
	}
	p.dirty = false
	<-decodeSlots
	waitUntil(t, "decode done", func() bool { return p.Update(ctx) && p.dirty })
	p.Draw(ctx, dst, content, true)
	if len(p.images.ops) != 1 {
		t.Fatalf("%d ops after decode, want 1", len(p.images.ops))
	}
}

// settleDecodes waits for ti's decodes and takes their results.
func settleDecodes(t testing.TB, ti *termImages) {
	t.Helper()
	ti.wg.Wait()
	ti.collect()
}

// drawSettled draws p, waits for the image decodes it started, and draws
// it again with their results.
func drawSettled(t testing.TB, p *TermPane, ctx *Ctx, dst *ebiten.Image, content image.Rectangle) {
	t.Helper()
	p.Draw(ctx, dst, content, true)
	settleDecodes(t, &p.images)
	p.Draw(ctx, dst, content, true)
}
