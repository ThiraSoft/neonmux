package ui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"slices"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

func redPNG(w, h int) string {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i] = 255   // R
		m.Pix[i+1] = 0   // G
		m.Pix[i+2] = 0   // B
		m.Pix[i+3] = 255 // A
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, m)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func isRed(c color.Color) bool {
	r, g, b, a := c.RGBA()
	return r>>8 > 200 && g>>8 < 50 && b>>8 < 50 && a>>8 > 200
}

func TestKittyImageDrawn(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Move cursor to (0, 0) and feed red 20x20 PNG with a=T.
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100;%s\x1b\\", redPNG(20, 20))))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap := p.Capture(ctx)
	px := p.origin.X + 10
	py := p.origin.Y + 10
	if !isRed(cap.At(px, py)) {
		t.Fatalf("pixel at (%d, %d) is %v, want red", px, py, cap.At(px, py))
	}
}

func TestKittyPlaceholderDrawn(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Transmit red 20x20 PNG with id=1, then virtual placement U=1, c=4, r=1.
	p.t.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=1;%s\x1b\\", redPNG(20, 20))))
	p.t.Write([]byte("\x1b_Ga=p,i=1,U=1,c=4,r=1\x1b\\"))

	// Move cursor to (0, 0) and print 4 placeholder characters on row 0.
	ph := "\U0010EEEE"
	p.t.Write([]byte("\x1b[1;1H\x1b[38;2;0;0;1m" + ph + ph + ph + ph))

	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap := p.Capture(ctx)
	// Cell 1 is inside the centered image box.
	px := p.origin.X + cw + cw/2
	py := p.origin.Y + ch/2
	if !isRed(cap.At(px, py)) {
		t.Fatalf("pixel on placeholder cell at (%d, %d) is %v, want red", px, py, cap.At(px, py))
	}
}

func TestKittyImageScrollsAway(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Move cursor to (0, 0) and feed red 20x20 PNG with a=T.
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100;%s\x1b\\", redPNG(20, 20))))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap := p.Capture(ctx)
	px := p.origin.X + 10
	py := p.origin.Y + 10
	if !isRed(cap.At(px, py)) {
		t.Fatalf("before scroll: pixel at (%d, %d) is %v, want red", px, py, cap.At(px, py))
	}

	// Move cursor to bottom row and scroll 30 lines so image scrolls 30 lines into history.
	p.t.Write([]byte("\x1b[24;1H"))
	for i := 0; i < 30; i++ {
		p.t.Write([]byte("\r\n"))
	}
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap = p.Capture(ctx)
	if isRed(cap.At(px, py)) {
		t.Fatalf("after scroll off screen: pixel at (%d, %d) is still red, want cleared", px, py)
	}

	// Scroll back into history.
	p.scrollBy(30)
	p.dirty = true
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap = p.Capture(ctx)
	if !isRed(cap.At(px, py)) {
		t.Fatalf("scrolled back into history: pixel at (%d, %d) is %v, want red to return", px, py, cap.At(px, py))
	}
}

func TestKittyPixelSizeFollowsFont(t *testing.T) {
	a, _ := coreApp(t, 1)
	p := NewTermPane(a.newID(), nil, "", nil)
	be := newFakeBackend(80, 24)
	p.s, p.t, p.cols, p.rows = be, be.term, 80, 24
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	if pxW, pxH := p.t.PixelSize(); pxW != 80*cw || pxH != 24*ch {
		t.Fatalf("initial pixel size = %dx%d, want %dx%d", pxW, pxH, 80*cw, 24*ch)
	}

	// Change font size, keeping cols and rows at 80x24.
	a.cfg.FontSize *= 2
	a.loadFonts()
	newW, newH := a.termFont.CellW, a.termFont.CellH
	if newW == cw && newH == ch {
		t.Fatal("font size did not change")
	}

	content = image.Rect(100, 100, 100+80*newW, 100+24*newH)
	ctx = a.frameCtx(time.Now())
	p.Layout(ctx, content)

	if pxW, pxH := p.t.PixelSize(); pxW != 80*newW || pxH != 24*newH {
		t.Fatalf("after font change: terminal PixelSize = %dx%d, want %dx%d", pxW, pxH, 80*newW, 24*newH)
	}
	wantSub := fmt.Sprintf("%dx%d@%dx%d", p.cols, p.rows, 80*newW, 24*newH)
	be.mu.Lock()
	resized := append([]string(nil), be.resized...)
	be.mu.Unlock()
	if !slices.Contains(resized, wantSub) {
		t.Fatalf("backend did not receive resize %s: got %v", wantSub, resized)
	}
}

func TestKittyCopyModeHidesImages(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100;%s\x1b\\", redPNG(20, 20))))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap := p.Capture(ctx)
	px, py := p.origin.X+10, p.origin.Y+10
	if !isRed(cap.At(px, py)) {
		t.Fatal("expected image to be drawn before copy mode")
	}

	p.EnterCopyMode()
	if p.cm == nil {
		t.Fatal("copy mode not active")
	}
	p.dirty = true
	p.Draw(ctx, dst, content, true)
	cap = p.Capture(ctx)
	if isRed(cap.At(px, py)) {
		t.Fatal("expected image to be hidden in copy mode")
	}

	p.exitCopyMode()
	p.Draw(ctx, dst, content, true)
	cap = p.Capture(ctx)
	if !isRed(cap.At(px, py)) {
		t.Fatal("expected image to be restored after exiting copy mode")
	}
}

func TestKittyCaptureIgnoresPlaceholders(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	p.t.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=1;%s\x1b\\", redPNG(20, 20))))
	p.t.Write([]byte("\x1b_Ga=p,i=1,U=1,c=4,r=1\x1b\\"))

	ph := "\U0010EEEE"
	p.t.Write([]byte("\x1b[1;1H\x1b[38;2;0;0;1m" + ph + ph + ph + ph))
	p.Update(ctx)

	// ScreenText (used by neonmux capture) must return blanks for placeholders.
	lines := p.t.ScreenText()
	if len(lines) == 0 {
		t.Fatal("no lines returned by ScreenText")
	}
	for _, r := range lines[0] {
		if r >= 0x110000 || r == 0x10EEEE {
			t.Fatalf("ScreenText contains placeholder rune: %U", r)
		}
	}
}

func TestKittyRGBFormats(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Format 24 (RGB, 2x2 red: 12 bytes: 255,0,0 255,0,0 255,0,0 255,0,0)
	rgb := []byte{255, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0}
	b64RGB := base64.StdEncoding.EncodeToString(rgb)
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=24,s=2,v=2;%s\x1b\\", b64RGB)))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap := p.Capture(ctx)
	px, py := p.origin.X+1, p.origin.Y+1
	if !isRed(cap.At(px, py)) {
		t.Fatalf("format 24 RGB pixel at (%d,%d) is %v, want red", px, py, cap.At(px, py))
	}

	// Format 32 (RGBA, 2x2 green: 16 bytes)
	rgba := []byte{
		0, 255, 0, 255, 0, 255, 0, 255,
		0, 255, 0, 255, 0, 255, 0, 255,
	}
	b64RGBA := base64.StdEncoding.EncodeToString(rgba)
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=32,s=2,v=2;%s\x1b\\", b64RGBA)))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	cap = p.Capture(ctx)
	c := cap.At(px, py)
	r, g, b, _ := c.RGBA()
	if !(r>>8 < 50 && g>>8 > 200 && b>>8 < 50) {
		t.Fatalf("format 32 RGBA pixel at (%d,%d) is %v, want green", px, py, c)
	}
}

