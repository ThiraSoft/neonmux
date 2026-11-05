package remote

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/ThiraSoft/neonmux/vt"
)

func testPalette() Palette {
	var pal Palette
	pal.FG = [3]uint8{200, 200, 200}
	pal.BG = [3]uint8{20, 20, 20}
	pal.Cursor = [3]uint8{255, 255, 255}
	// Index 9 is bright red
	pal.P[9] = [3]uint8{255, 50, 50}
	// Index 1 is red
	pal.P[1] = [3]uint8{200, 0, 0}
	return pal
}

func rgbToInt(c [3]uint8) int32 {
	return int32(c[0])<<16 | int32(c[1])<<8 | int32(c[2])
}

func TestCaptureRuns(t *testing.T) {
	term := vt.New(20, 3, 100)
	_, _ = term.Write([]byte("\x1b[1;31mred\x1b[0m plain\r\n\x1b[7minv\x1b[0m"))

	pal := testPalette()
	s := Capture(term, 0, pal)

	if len(s.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(s.Lines))
	}

	// Line 0: [{"red", bright red RGB, -1, 1}, {" plain", FG, -1, 0}]
	brightRed := rgbToInt(pal.P[9])
	fgColor := rgbToInt(pal.FG)
	bgColor := rgbToInt(pal.BG)

	line0 := s.Lines[0]
	if len(line0) != 2 {
		t.Fatalf("expected 2 runs in line 0, got %d: %+v", len(line0), line0)
	}
	if line0[0].Text != "red" || line0[0].FG != brightRed || line0[0].BG != -1 || line0[0].Attr != 1 {
		t.Errorf("line 0 run 0 mismatch: got %+v, want Text: red, FG: %x, BG: -1, Attr: 1", line0[0], brightRed)
	}
	if line0[1].Text != " plain" || line0[1].FG != fgColor || line0[1].BG != -1 || line0[1].Attr != 0 {
		t.Errorf("line 0 run 1 mismatch: got %+v, want Text: ' plain', FG: %x, BG: -1, Attr: 0", line0[1], fgColor)
	}

	// Line 1: "inv" with FG and BG swapped
	line1 := s.Lines[1]
	if len(line1) != 1 {
		t.Fatalf("expected 1 run in line 1, got %d: %+v", len(line1), line1)
	}
	if line1[0].Text != "inv" || line1[0].FG != bgColor || line1[0].BG != fgColor || line1[0].Attr != 0 {
		t.Errorf("line 1 run 0 mismatch: got %+v, want Text: inv, FG: %x, BG: %x, Attr: 0", line1[0], bgColor, fgColor)
	}

	// Line 2: trailing default spaces dropped -> empty
	line2 := s.Lines[2]
	if len(line2) != 0 {
		t.Errorf("expected 0 runs in line 2, got %d: %+v", len(line2), line2)
	}
}

func TestCaptureWide(t *testing.T) {
	term := vt.New(20, 3, 100)
	_, _ = term.Write([]byte("a漢b"))

	pal := testPalette()
	s := Capture(term, 0, pal)

	if len(s.Lines) < 1 {
		t.Fatalf("expected at least 1 line, got %d", len(s.Lines))
	}
	line0 := s.Lines[0]
	if len(line0) != 3 {
		t.Fatalf("expected 3 runs for a漢b, got %d: %+v", len(line0), line0)
	}
	if line0[0].Text != "a" || line0[0].Attr&32 != 0 {
		t.Errorf("run 0 mismatch: %+v", line0[0])
	}
	if line0[1].Text != "漢" || line0[1].Attr&32 == 0 {
		t.Errorf("run 1 mismatch (expected wide rune with Attr&32): %+v", line0[1])
	}
	if line0[2].Text != "b" || line0[2].Attr&32 != 0 {
		t.Errorf("run 2 mismatch (expected 'b' right after): %+v", line0[2])
	}
}

func TestCaptureOffset(t *testing.T) {
	term := vt.New(20, 3, 100)
	for i := 0; i < 10; i++ {
		if i > 0 {
			_, _ = term.Write([]byte("\r\n"))
		}
		_, _ = term.Write([]byte(fmt.Sprintf("line %d", i)))
	}

	pal := testPalette()
	s := Capture(term, 2, pal)

	if s.Rows != 3 {
		t.Fatalf("expected 3 rows, got %d", s.Rows)
	}
	if len(s.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(s.Lines))
	}

	// Offset 2 in 10-line terminal with 3 rows shows lines 5, 6, 7
	want := []string{"line 5", "line 6", "line 7"}
	for i, w := range want {
		if len(s.Lines[i]) == 0 {
			t.Fatalf("line %d empty, want %q", i, w)
		}
		if s.Lines[i][0].Text != w {
			t.Errorf("line %d text mismatch: got %q, want %q", i, s.Lines[i][0].Text, w)
		}
	}
}

func TestLineHashDiffers(t *testing.T) {
	l1 := Line{Run{Text: "test", FG: 0xff0000, BG: -1, Attr: 0}}
	l2 := Line{Run{Text: "test", FG: 0x00ff00, BG: -1, Attr: 0}}

	h1 := LineHash(l1)
	h2 := LineHash(l2)

	if h1 == h2 {
		t.Fatalf("LineHash collision on color change: h1=%d, h2=%d", h1, h2)
	}
}

func TestCaptureNegativeOffsetClamped(t *testing.T) {
	term := vt.New(20, 3, 100)
	_, _ = term.Write([]byte("hello world"))
	pal := testPalette()

	// Should not panic, should clamp offset to 0
	s := Capture(term, -5, pal)
	if s.Offset != 0 {
		t.Fatalf("expected offset clamped to 0, got %d", s.Offset)
	}
}

func TestCaptureHugeOffsetClamped(t *testing.T) {
	term := vt.New(20, 3, 100)
	for i := 0; i < 5; i++ {
		_, _ = term.Write([]byte("line\r\n"))
	}
	pal := testPalette()

	s := Capture(term, 1000, pal)
	if s.Offset != s.Hist {
		t.Fatalf("expected offset clamped to Hist (%d), got %d", s.Hist, s.Offset)
	}
}

func redPNG(w, h int) string {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i] = 255
		m.Pix[i+3] = 255
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, m)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestCaptureImgRects(t *testing.T) {
	term := vt.New(80, 24, 0)
	term.SetPixelSize(800, 480) // cellW = 10, cellH = 20

	// 1. Direct placement: image 1 (200x100), placed at (col 2, row 1), 6 cols, 3 rows
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b[2;3H\x1b_Ga=T,f=100,i=1,c=6,r=3;%s\x1b\\", redPNG(200, 100))))

	// 2. Virtual placement: image 2 (40x20), U=1, c=4, r=1
	_, _ = term.Write([]byte(fmt.Sprintf("\x1b_Ga=t,f=100,i=2;%s\x1b\\", redPNG(40, 20))))
	_, _ = term.Write([]byte("\x1b_Ga=p,i=2,U=1,c=4,r=1\x1b\\"))

	// Print 4 placeholder characters on row 5 (cursor at row 6, col 1 in 1-based VT coords)
	ph := "\U0010EEEE"
	_, _ = term.Write([]byte("\x1b[6;1H\x1b[38;2;0;0;2m" + ph + ph + ph + ph))

	pal := testPalette()
	s := Capture(term, 0, pal)

	// Expect 5 ImgRects: 1 direct + 4 placeholders
	if len(s.Imgs) != 5 {
		t.Fatalf("expected 5 ImgRects, got %d: %+v", len(s.Imgs), s.Imgs)
	}

	// Direct placement: ID 1, Gen 1, at (2, 1), 6x3 cols
	dir := s.Imgs[0]
	if dir.ID != 1 || dir.X != 2 || dir.Y != 1 || dir.W != 6 || dir.H != 3 {
		t.Errorf("direct placement mismatch: got %+v, want ID: 1, X: 2, Y: 1, W: 6, H: 3", dir)
	}
	if dir.SX != 0 || dir.SY != 0 || dir.SW != 200 || dir.SH != 100 {
		t.Errorf("direct placement src mismatch: got %d,%d %dx%d, want 0,0 200x100", dir.SX, dir.SY, dir.SW, dir.SH)
	}

	// 4 Placeholders for image 2 (40x20, box 4x1 cells of 10x20):
	for i := 0; i < 4; i++ {
		phRect := s.Imgs[1+i]
		if phRect.ID != 2 {
			t.Errorf("ph %d ID: got %d, want 2", i, phRect.ID)
		}
		wantSrc, wantDst := vt.PlaceholderSlice(40, 20, 4, 1, 10, 20, 0, i)
		wantX := float64(i) + float64(wantDst.Min.X)/10.0
		wantY := 5.0 + float64(wantDst.Min.Y)/20.0
		wantW := float64(wantDst.Dx()) / 10.0
		wantH := float64(wantDst.Dy()) / 20.0
		if phRect.X != wantX || phRect.Y != wantY || phRect.W != wantW || phRect.H != wantH {
			t.Errorf("ph %d cell rect mismatch: got (%f,%f %fx%f), want (%f,%f %fx%f)",
				i, phRect.X, phRect.Y, phRect.W, phRect.H, wantX, wantY, wantW, wantH)
		}
		if phRect.SX != wantSrc.Min.X || phRect.SY != wantSrc.Min.Y || phRect.SW != wantSrc.Dx() || phRect.SH != wantSrc.Dy() {
			t.Errorf("ph %d src mismatch: got (%d,%d %dx%d), want (%d,%d %dx%d)",
				i, phRect.SX, phRect.SY, phRect.SW, phRect.SH, wantSrc.Min.X, wantSrc.Min.Y, wantSrc.Dx(), wantSrc.Dy())
		}
	}

	// Check that placeholders on row 5 are emitted as blank spaces with no underline
	line5 := s.Lines[5]
	for _, run := range line5 {
		if strings.Contains(run.Text, ph) {
			t.Errorf("line 5 contains placeholder rune: %q", run.Text)
		}
		if run.Attr&4 != 0 {
			t.Errorf("line 5 has underline attribute on run: %+v", run)
		}
	}
}
