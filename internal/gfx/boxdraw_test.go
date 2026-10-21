package gfx

import (
	"fmt"
	"image"
	"testing"
)

type cellSize struct {
	w, h  int
	pixel bool
	scale int
}

func (c cellSize) String() string { return fmt.Sprintf("%dx%d pixel=%v", c.w, c.h, c.pixel) }

// boxSizes covers the pixel font from ×1 to ×4 and TTF cells from 7 to 22
// pixels wide.
func boxSizes() []cellSize {
	var out []cellSize
	for s := 1; s <= 4; s++ {
		out = append(out, cellSize{pixelW * s, pixelH * s, true, s})
	}
	for w := 7; w <= 22; w++ {
		for _, h := range []int{2*w + 1, 2*w + 2, 2*w + 3} {
			out = append(out, cellSize{w, h, false, 1})
		}
	}
	return out
}

func boxMask(t *testing.T, r rune, c cellSize) *image.Alpha {
	t.Helper()
	m := image.NewAlpha(image.Rect(0, 0, c.w, c.h))
	if !drawProcedural(m, r, c.pixel, c.scale) {
		t.Fatalf("%c not drawn", r)
	}
	return m
}

func on(m *image.Alpha, x, y int) bool { return m.Pix[y*m.Stride+x] > 127 }

// column and row read a column or a row of the mask.
func column(m *image.Alpha, x int) string {
	s := ""
	for y := 0; y < m.Rect.Dy(); y++ {
		s += map[bool]string{true: "#", false: "."}[on(m, x, y)]
	}
	return s
}

func row(m *image.Alpha, y int) string {
	s := ""
	for x := 0; x < m.Rect.Dx(); x++ {
		s += map[bool]string{true: "#", false: "."}[on(m, x, y)]
	}
	return s
}

// span returns the first and last lit position of a row or column read by
// column/row.
func span(s string) (int, int) {
	a, b := -1, -1
	for i, c := range s {
		if c == '#' {
			if a < 0 {
				a = i
			}
			b = i
		}
	}
	return a, b
}

// gap returns the gap between the two lines of a double line.
func gap(s string) (int, int) {
	a, b := span(s)
	lo, hi := -1, -1
	for i := a; i <= b; i++ {
		if s[i] == '.' {
			if lo < 0 {
				lo = i
			}
			hi = i
		}
	}
	return lo, hi
}

// Each arm reaches the cell edge exactly like the straight line of the same
// weight: neighboring cells join up.
func TestBoxEdgesJoin(t *testing.T) {
	for _, c := range boxSizes() {
		refH := map[byte]string{'1': column(boxMask(t, '─', c), 0), '2': column(boxMask(t, '━', c), 0), '3': column(boxMask(t, '═', c), 0)}
		refV := map[byte]string{'1': row(boxMask(t, '│', c), 0), '2': row(boxMask(t, '┃', c), 0), '3': row(boxMask(t, '║', c), 0)}
		for r, spec := range boxArms {
			m := boxMask(t, r, c)
			for i, got := range []string{row(m, 0), column(m, c.w-1), row(m, c.h-1), column(m, 0)} {
				want := ""
				if spec[i] != '0' {
					if i%2 == 0 {
						want = refV[spec[i]]
					} else {
						want = refH[spec[i]]
					}
				}
				if want != "" && got != want {
					t.Fatalf("%c %v, side %d: %s instead of %s", r, c, i, got, want)
				}
			}
		}
	}
}

// A single arm meeting a continuous double line stops on the nearest line:
// the gap stays open (╤ ╧ ╟ ╢, widely used by Midnight Commander).
func TestBoxSingleTeeKeepsDoubleGapOpen(t *testing.T) {
	for _, c := range boxSizes() {
		hg0, hg1 := gap(column(boxMask(t, '═', c), 0))
		vg0, vg1 := gap(row(boxMask(t, '║', c), 0))
		for _, r := range []rune{'╤', '╧'} {
			m := boxMask(t, r, c)
			a, b := span(row(m, 0))
			if r == '╤' {
				a, b = span(row(m, c.h-1))
			}
			for x := a; x <= b; x++ {
				for y := hg0; y <= hg1; y++ {
					if on(m, x, y) {
						t.Fatalf("%c %v: the gap of the double line is closed\n%s", r, c, dumpMask(m))
					}
				}
			}
		}
		for _, r := range []rune{'╟', '╢'} {
			m := boxMask(t, r, c)
			a, b := span(column(m, c.w-1))
			if r == '╢' {
				a, b = span(column(m, 0))
			}
			for y := a; y <= b; y++ {
				for x := vg0; x <= vg1; x++ {
					if on(m, x, y) {
						t.Fatalf("%c %v: the gap of the double line is closed\n%s", r, c, dumpMask(m))
					}
				}
			}
		}
	}
}

// Mixed corners close the angle without a spur: nothing sticks out past
// the single arm, and the drawing is in one piece.
func TestBoxMixedCornersHaveNoSpur(t *testing.T) {
	for _, c := range boxSizes() {
		for _, r := range []rune{'╒', '╕', '╘', '╛'} {
			m := boxMask(t, r, c)
			edge := 0
			if r == '╒' || r == '╕' {
				edge = c.h - 1
			}
			a, b := span(row(m, edge)) // the vertical single arm
			for y := 0; y < c.h; y++ {
				l, rr := span(row(m, y))
				if l < 0 {
					continue
				}
				if (r == '╒' || r == '╘') && l < a || (r == '╕' || r == '╛') && rr > b {
					t.Fatalf("%c %v: spur on row %d\n%s", r, c, y, dumpMask(m))
				}
			}
			if n := components(m); n != 1 {
				t.Fatalf("%c %v: %d pieces\n%s", r, c, n, dumpMask(m))
			}
		}
		for _, r := range []rune{'╓', '╖', '╙', '╜'} {
			m := boxMask(t, r, c)
			edge := 0
			if r == '╓' || r == '╙' {
				edge = c.w - 1
			}
			a, b := span(column(m, edge)) // the horizontal single arm
			for x := 0; x < c.w; x++ {
				top, bot := span(column(m, x))
				if top < 0 {
					continue
				}
				if (r == '╓' || r == '╖') && top < a || (r == '╙' || r == '╜') && bot > b {
					t.Fatalf("%c %v: spur on column %d\n%s", r, c, x, dumpMask(m))
				}
			}
			if n := components(m); n != 1 {
				t.Fatalf("%c %v: %d pieces\n%s", r, c, n, dumpMask(m))
			}
		}
	}
}

// components counts the connected pieces (4-neighborhood).
func components(m *image.Alpha) int {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	seen := make([]bool, w*h)
	n := 0
	for i := range seen {
		if seen[i] || !on(m, i%w, i/w) {
			continue
		}
		n++
		stack := []int{i}
		seen[i] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%w, p/w
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx >= 0 && ny >= 0 && nx < w && ny < h && !seen[ny*w+nx] && on(m, nx, ny) {
					seen[ny*w+nx] = true
					stack = append(stack, ny*w+nx)
				}
			}
		}
	}
	return n
}

func dumpMask(m *image.Alpha) string {
	s := ""
	for y := 0; y < m.Rect.Dy(); y++ {
		s += row(m, y) + "\n"
	}
	return s
}
