package ui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"slices"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/vt"
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

	if len(p.images.ops) != 1 {
		t.Fatalf("expected 1 draw op, got %d", len(p.images.ops))
	}
	op := p.images.ops[0]
	wantDst := image.Rect(p.origin.X, p.origin.Y, p.origin.X+20, p.origin.Y+20)
	if op.dst != wantDst {
		t.Fatalf("op dst = %v, want %v", op.dst, wantDst)
	}
	if op.src != image.Rect(0, 0, 20, 20) {
		t.Fatalf("op src = %v, want (0, 0, 20, 20)", op.src)
	}
	if op.filter != ebiten.FilterNearest {
		t.Fatalf("op filter = %v, want Nearest", op.filter)
	}
	if op.img == nil || op.img.Bounds().Dx() != 20 || op.img.Bounds().Dy() != 20 {
		t.Fatalf("op texture bounds = %v, want 20x20", op.img.Bounds())
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

	if len(p.images.ops) != 4 {
		t.Fatalf("expected 4 placeholder draw ops, got %d", len(p.images.ops))
	}
	for i, op := range p.images.ops {
		if op.img == nil || op.img.Bounds().Dx() != 20 || op.img.Bounds().Dy() != 20 {
			t.Fatalf("op %d: invalid image texture", i)
		}
		wantSrc, wantDst := vt.PlaceholderSlice(20, 20, 4, 1, cw, ch, 0, i)
		expectedDst := image.Rect(p.origin.X+i*cw+wantDst.Min.X, p.origin.Y+wantDst.Min.Y,
			p.origin.X+i*cw+wantDst.Max.X, p.origin.Y+wantDst.Max.Y)
		if op.src != wantSrc {
			t.Fatalf("op %d src = %v, want %v", i, op.src, wantSrc)
		}
		if op.dst != expectedDst {
			t.Fatalf("op %d dst = %v, want %v", i, op.dst, expectedDst)
		}
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

	if len(p.images.ops) != 1 {
		t.Fatal("image should be visible initially")
	}

	// Move cursor to bottom row and scroll 30 lines so image scrolls 30 lines into history.
	p.t.Write([]byte("\x1b[24;1H"))
	for i := 0; i < 30; i++ {
		p.t.Write([]byte("\r\n"))
	}
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	if len(p.images.ops) != 0 {
		t.Fatal("image should have scrolled off screen into history")
	}

	// Scroll back into history.
	p.scrollBy(30)
	p.dirty = true
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	if len(p.images.ops) != 1 {
		t.Fatal("image should be visible again when scrolled back into history")
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

	if len(p.images.ops) != 1 {
		t.Fatal("expected image to be visible before copy mode")
	}

	p.EnterCopyMode()
	if p.cm == nil {
		t.Fatal("copy mode not active")
	}
	p.images.ops = nil
	p.Draw(ctx, dst, content, true)

	if len(p.images.ops) != 0 {
		t.Fatal("expected images drawing to be skipped during copy mode")
	}

	p.exitCopyMode()
	p.Draw(ctx, dst, content, true)
	if len(p.images.ops) != 1 {
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

	// Format 24 (RGB, 2x2 red: 12 bytes)
	rgb := []byte{255, 0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0}
	b64RGB := base64.StdEncoding.EncodeToString(rgb)
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=24,s=2,v=2;%s\x1b\\", b64RGB)))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	if len(p.images.ops) != 1 {
		t.Fatalf("expected 1 draw op for RGB format, got %d", len(p.images.ops))
	}
	op := p.images.ops[0]
	if op.img == nil || op.img.Bounds().Dx() != 2 || op.img.Bounds().Dy() != 2 {
		t.Fatalf("format 24 texture bounds = %v, want 2x2", op.img.Bounds())
	}

	// Format 32 (RGBA, 3x3 green: 36 bytes)
	rgba := make([]byte, 3*3*4)
	for i := 0; i < len(rgba); i += 4 {
		rgba[i+1] = 255
		rgba[i+3] = 255
	}
	b64RGBA := base64.StdEncoding.EncodeToString(rgba)
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=32,s=3,v=3;%s\x1b\\", b64RGBA)))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	if len(p.images.ops) == 0 {
		t.Fatal("expected draw ops for RGBA format")
	}
	lastOp := p.images.ops[len(p.images.ops)-1]
	if lastOp.img == nil || lastOp.img.Bounds().Dx() != 3 || lastOp.img.Bounds().Dy() != 3 {
		t.Fatalf("format 32 texture bounds = %v, want 3x3", lastOp.img.Bounds())
	}
}

func TestKittyImageCacheEvictsOldGen(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Send image 1 ten times with different data/generations
	for i := 1; i <= 10; i++ {
		p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100,i=1;%s\x1b\\", redPNG(10+i, 10+i))))
		p.Update(ctx)
		p.Draw(ctx, dst, content, true)
	}

	if len(p.images.cache) != 1 {
		t.Fatalf("cache holds %d entries, want 1", len(p.images.cache))
	}
}

func TestKittySyncDefersRedraw(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Enter synchronized output mode 2026
	p.t.Write([]byte("\x1b[?2026h"))
	p.Update(ctx)
	p.dirty = false

	// Transmit an image while sync is active
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100;%s\x1b\\", redPNG(20, 20))))

	// Update must NOT trigger redraw while sync is active
	if p.Update(ctx) || p.dirty {
		t.Fatal("graphics update triggered redraw during synchronized output")
	}

	// Exit synchronized output mode 2026
	p.t.Write([]byte("\x1b[?2026l"))

	// Now Update must trigger redraw
	if !p.Update(ctx) || !p.dirty {
		t.Fatal("graphics update did not trigger redraw after synchronized output ended")
	}
}

func TestKittyZoomedPaneShowsImage(t *testing.T) {
	a, ts := coreApp(t, 2)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	settle(a, dst)

	// Send an image to pane 0
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100;%s\x1b\\", redPNG(20, 20))))
	ctx := a.frameCtx(time.Now())
	p.Update(ctx)
	p.Draw(ctx, dst, p.content, true)

	if len(p.images.ops) == 0 {
		t.Fatal("image not visible before zoom")
	}

	// Zoom pane 0
	a.focus = 0
	a.do("pane.zoom")
	settle(a, dst)

	// Redraw zoomed pane
	ctx = a.frameCtx(time.Now())
	p.Draw(ctx, dst, p.content, true)

	if len(p.images.ops) == 0 {
		t.Fatal("image not visible when pane is zoomed")
	}
}

func TestKittyFailedDecodeNotRetried(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	ctx := a.frameCtx(time.Now())
	cw, ch := a.termFont.CellW, a.termFont.CellH
	content := image.Rect(100, 100, 100+80*cw, 100+24*ch)
	p.Layout(ctx, content)

	// Feed invalid PNG data with a=T, i=1
	badPNG := base64.StdEncoding.EncodeToString([]byte("not a valid png file"))
	p.t.Write([]byte(fmt.Sprintf("\x1b[1;1H\x1b_Ga=T,f=100,i=1;%s\x1b\\", badPNG)))
	p.Update(ctx)
	p.Draw(ctx, dst, content, true)

	// Second frame: prepare should not attempt to decode again
	p.images.prepare(p.t, p.offset)
	if len(p.images.toDecode) > 0 {
		t.Fatalf("failed image was queued for decode again: %v", p.images.toDecode)
	}
}

func TestKittyStretchNatural(t *testing.T) {
	term := vt.New(80, 24, 100)
	term.SetPixelSize(800, 480) // cell is 10x20

	// 1. Natural size: neither c nor r specified
	term.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=1;%s\x1b\\", redPNG(15, 25))))
	term.Write([]byte("\x1b_Ga=p,i=1,p=1\x1b\\"))

	term.Lock()
	nat := term.PlacementNatural(1, 1)
	term.Unlock()
	if !nat {
		t.Fatal("placement without c/r must be marked natural")
	}

	// 2. Explicit c/r specified matching cell count (c=2, r=2 for 15x25 on 10x20 cells)
	term.Write([]byte("\x1b_Ga=p,i=1,p=2,c=2,r=2\x1b\\"))

	term.Lock()
	nat = term.PlacementNatural(1, 2)
	term.Unlock()
	if nat {
		t.Fatal("placement with explicit c/r must NOT be marked natural")
	}

	// Verify that in UI drawing, explicit c/r scales to the box instead of staying at natural size
	ti := &termImages{}
	ti.prepare(term, 0)
	ti.syncCache()
	ops := ti.computeOps(image.Pt(0, 0), 10, 20)
	// p=1 (natural) vs p=2 (explicit c=2, r=2)
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ops))
	}
	// Placement 1 (natural): size in pixels should be natural dimensions (15x25)
	if ops[0].dst.Dx() != 15 || ops[0].dst.Dy() != 25 {
		t.Fatalf("natural op dst size = %dx%d, want 15x25", ops[0].dst.Dx(), ops[0].dst.Dy())
	}
	// Placement 2 (explicit c=2, r=2 -> 20x40 box): image aspect ratio 15:25 (3:5)
	// Fits 20x40 box: width 20, height 20 * 25 / 15 = 33 (aspect preserved fit)
	// Definitely NOT natural 15x25!
	if ops[1].dst.Dx() == 15 && ops[1].dst.Dy() == 25 {
		t.Fatal("explicit c/r placement should not retain natural size")
	}
	if ops[1].dst.Dx() != 20 || ops[1].dst.Dy() != 40 {
		t.Fatalf("explicit c=2, r=2 op dst size = %dx%d, want 20x40", ops[1].dst.Dx(), ops[1].dst.Dy())
	}
	if !ops[0].natural {
		t.Fatal("placement 1 op must have natural = true")
	}
	if ops[1].natural {
		t.Fatal("placement 2 op must have natural = false")
	}

	// 3. Two direct placements with id 0: one explicit c=3,r=1, one natural
	term.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=2;%s\x1b\\", redPNG(15, 25))))
	term.Write([]byte("\x1b_Ga=p,i=2,c=3,r=1\x1b\\")) // explicit c=3, r=1 -> 30x20
	term.Write([]byte("\x1b_Ga=p,i=2\x1b\\"))         // natural -> 15x25

	ti = &termImages{}
	ti.prepare(term, 0)
	ti.syncCache()
	ops = ti.computeOps(image.Pt(0, 0), 10, 20)
	// Should have 4 ops: 2 for img 1, 2 for img 2
	if len(ops) != 4 {
		t.Fatalf("expected 4 ops, got %d", len(ops))
	}
	// Placement for img 2 with c=3, r=1: stretched to 30x20, natural = false
	if ops[2].dst.Dx() != 30 || ops[2].dst.Dy() != 20 || ops[2].natural {
		t.Fatalf("img 2 explicit op: dst=%v, natural=%v; want 30x20, natural=false", ops[2].dst, ops[2].natural)
	}
	// Placement for img 2 natural: 15x25, natural = true
	if ops[3].dst.Dx() != 15 || ops[3].dst.Dy() != 25 || !ops[3].natural {
		t.Fatalf("img 2 natural op: dst=%v, natural=%v; want 15x25, natural=true", ops[3].dst, ops[3].natural)
	}
}

func TestTextDrawPosDuringLayoutAnimation(t *testing.T) {
	a, ts := coreApp(t, 1)
	p := ts[0]
	dst := ebiten.NewImage(a.w, a.h)
	now := time.Now()
	ctx := a.frameCtx(now)

	initialContent := image.Rect(100, 100, 500, 500)
	p.Layout(ctx, initialContent)

	// Pre-round-1 baseline: origin is calculated for initialContent
	f := ctx.Term
	cols := max(2, initialContent.Dx()/f.CellW)
	rows := max(1, initialContent.Dy()/f.CellH)
	origOrigin := image.Pt(initialContent.Min.X+(initialContent.Dx()-cols*f.CellW)/2,
		initialContent.Min.Y+(initialContent.Dy()-rows*f.CellH)/2)

	// Start layout animation
	p.Base().animAt = now
	intermediateContent := image.Rect(150, 150, 550, 550)

	// Pre-round-1 expected draw position:
	// sx = 1, sy = 1
	// translate(origOrigin.X - initialContent.Min.X, origOrigin.Y - initialContent.Min.Y)
	// scale(sx, sy)
	// translate(intermediateContent.Min.X, intermediateContent.Min.Y)
	wantX := float64(origOrigin.X-initialContent.Min.X) + float64(intermediateContent.Min.X)
	wantY := float64(origOrigin.Y-initialContent.Min.Y) + float64(intermediateContent.Min.Y)

	p.Draw(ctx, dst, intermediateContent, true)

	gotX, gotY := p.lastTextOp.GeoM.Apply(0, 0)
	if gotX != wantX || gotY != wantY {
		t.Fatalf("text draw position during animation = (%v, %v), want (%v, %v)", gotX, gotY, wantX, wantY)
	}
}
