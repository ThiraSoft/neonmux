package remote

import (
	"fmt"
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
