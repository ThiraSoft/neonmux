package gfx

import (
	"image"
	"math"
)

// Box drawing characters, blocks, braille and Powerline separators are
// drawn by hand rather than read from the font: that way the lines join up
// from one cell to the next, whatever the font.

// boxArms describes the up, right, down, left arms of each character:
// 0 none, 1 light, 2 heavy, 3 double.
var boxArms = map[rune]string{
	'─': "0101", '━': "0202", '│': "1010", '┃': "2020",
	'┌': "0110", '┍': "0210", '┎': "0120", '┏': "0220",
	'┐': "0011", '┑': "0012", '┒': "0021", '┓': "0022",
	'└': "1100", '┕': "1200", '┖': "2100", '┗': "2200",
	'┘': "1001", '┙': "1002", '┚': "2001", '┛': "2002",
	'├': "1110", '┝': "1210", '┞': "2110", '┟': "1120", '┠': "2120", '┡': "2210", '┢': "1220", '┣': "2220",
	'┤': "1011", '┥': "1012", '┦': "2011", '┧': "1021", '┨': "2021", '┩': "2012", '┪': "1022", '┫': "2022",
	'┬': "0111", '┭': "0112", '┮': "0211", '┯': "0212", '┰': "0121", '┱': "0122", '┲': "0221", '┳': "0222",
	'┴': "1101", '┵': "1102", '┶': "1201", '┷': "1202", '┸': "2101", '┹': "2102", '┺': "2201", '┻': "2202",
	'┼': "1111", '┽': "1112", '┾': "1211", '┿': "1212", '╀': "2111", '╁': "1121", '╂': "2121",
	'╃': "2112", '╄': "2211", '╅': "1122", '╆': "1221", '╇': "2212", '╈': "1222", '╉': "2122", '╊': "2221", '╋': "2222",
	'═': "0303", '║': "3030",
	'╒': "0310", '╓': "0130", '╔': "0330", '╕': "0013", '╖': "0031", '╗': "0033",
	'╘': "1300", '╙': "3100", '╚': "3300", '╛': "1003", '╜': "3001", '╝': "3003",
	'╞': "1310", '╟': "3130", '╠': "3330", '╡': "1013", '╢': "3031", '╣': "3033",
	'╤': "0313", '╥': "0131", '╦': "0333", '╧': "1303", '╨': "3101", '╩': "3303",
	'╪': "1313", '╫': "3131", '╬': "3333",
	'╴': "0001", '╵': "1000", '╶': "0100", '╷': "0010", '╸': "0002", '╹': "2000", '╺': "0200", '╻': "0020",
	'╼': "0201", '╽': "1020", '╾': "0102", '╿': "2010",
}

// Dashed lines: number of dashes, thickness, orientation.
var boxDashes = map[rune][3]int{
	'┄': {3, 1, 0}, '┅': {3, 2, 0}, '┆': {3, 1, 1}, '┇': {3, 2, 1},
	'┈': {4, 1, 0}, '┉': {4, 2, 0}, '┊': {4, 1, 1}, '┋': {4, 2, 1},
	'╌': {2, 1, 0}, '╍': {2, 2, 0}, '╎': {2, 1, 1}, '╏': {2, 2, 1},
}

func drawProcedural(m *image.Alpha, r rune, pixel bool, scale int) bool {
	switch {
	case r >= 0x2500 && r <= 0x257f:
		return drawBox(m, r, pixel, scale)
	case r >= 0x2580 && r <= 0x259f:
		drawBlock(m, r, pixel, scale)
		return true
	case r >= 0x2800 && r <= 0x28ff:
		drawBraille(m, r)
		return true
	case r >= 0xe0b0 && r <= 0xe0b6 && r != 0xe0b5:
		drawPowerline(m, r, pixel, scale)
		return true
	case r == '❯' || r == '❮':
		drawChevron(m, r == '❮', pixel, scale)
		return true
	}
	return false
}

// drawChevron draws ❯ and ❮, widely used by prompts (pure, starship) and
// missing from the pixel font.
func drawChevron(m *image.Alpha, left bool, pixel bool, scale int) {
	w, h := float64(m.Rect.Dx()), float64(m.Rect.Dy())
	t := float64(lineThickness(m.Rect.Dx(), pixel, scale)) * 1.6
	x0, x1 := w*0.2, w*0.8
	y0, y1 := h*0.3, h*0.75
	mid := (y0 + y1) / 2
	cover(m, pixel, func(x, y float64) bool {
		if left {
			x = w - x
		}
		if y < y0 || y > y1 {
			return false
		}
		edge := x0 + (x1-x0)*(1-math.Abs(y-mid)/(mid-y0))
		return x <= edge && x >= edge-t
	})
}

func lineThickness(w int, pixel bool, scale int) int {
	if pixel {
		return max(1, scale)
	}
	return max(1, int(math.Round(float64(w)/8)))
}

func armThick(weight, t int) int {
	switch weight {
	case 1, 3:
		return t
	case 2:
		return 2 * t
	}
	return 0
}

func drawBox(m *image.Alpha, r rune, pixel bool, scale int) bool {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	t := lineThickness(w, pixel, scale)
	cx, cy := w/2, h/2

	if d, ok := boxDashes[r]; ok {
		drawDashes(m, d[0], armThick(d[1], t), d[2] == 1)
		return true
	}
	switch r {
	case '╭', '╮', '╯', '╰':
		drawRounded(m, r, t, pixel)
		return true
	case '╱', '╲', '╳':
		drawDiagonal(m, r, t, pixel)
		return true
	}
	spec, ok := boxArms[r]
	if !ok {
		return false
	}
	u, rt, d, l := int(spec[0]-'0'), int(spec[1]-'0'), int(spec[2]-'0'), int(spec[3]-'0')
	single := func(wt int) bool { return wt == 1 || wt == 2 }
	dd := t
	half := t / 2

	// A single arm meeting a double line with no single arm opposite: in a
	// tee (╤ ╧ ╟ ╢) it stops on the nearest line and the gap stays open; in
	// a corner (╒ ╓) it goes to the farthest line to close the angle. With
	// an arm opposite (╪ ╞), it crosses as before.
	hDouble := l == 3 || rt == 3
	vDouble := u == 3 || d == 3
	tv := max(armThick(u, t), armThick(d, t))
	th := max(armThick(l, t), armThick(rt, t))
	if single(l) {
		tt := armThick(l, t)
		T := max(tv, tt)
		x1 := cx - T/2 + T
		if vDouble && rt == 0 {
			x1 = cx + dd - half + t // corner: up to the far line
			if u == 3 && d == 3 {
				x1 = cx - half // tee: on the near line
			}
		}
		fillRect(m, 0, cy-tt/2, x1, tt, 255)
	}
	if single(rt) {
		tt := armThick(rt, t)
		T := max(tv, tt)
		x0 := cx - T/2
		if vDouble && l == 0 {
			x0 = cx - dd - half
			if u == 3 && d == 3 {
				x0 = cx + dd - half
			}
		}
		fillRect(m, x0, cy-tt/2, w, tt, 255)
	}
	if single(u) {
		tt := armThick(u, t)
		T := max(th, tt)
		y1 := cy - T/2 + T
		if hDouble && d == 0 {
			y1 = cy + dd - half + t
			if l == 3 && rt == 3 {
				y1 = cy - half
			}
		}
		fillRect(m, cx-tt/2, 0, tt, y1, 255)
	}
	if single(d) {
		tt := armThick(d, t)
		T := max(th, tt)
		y0 := cy - T/2
		if hDouble && u == 0 {
			y0 = cy - dd - half
			if l == 3 && rt == 3 {
				y0 = cy + dd - half
			}
		}
		fillRect(m, cx-tt/2, y0, tt, h, 255)
	}

	// Double lines: each line stops on the nearest perpendicular line when
	// an arm exists on its side, otherwise it goes to the farthest one. This
	// gives the inner and outer corners of ╔ or ╬ without a dedicated table.
	// Facing a single arm (╒ ╓), "the farthest" is the edge of that arm: the
	// line does not stick out.
	sv, sh := 0, 0 // thickness of the vertical and horizontal single arms
	for _, wt := range []int{u, d} {
		if single(wt) {
			sv = max(sv, armThick(wt, t))
		}
	}
	for _, wt := range []int{l, rt} {
		if single(wt) {
			sh = max(sh, armThick(wt, t))
		}
	}
	for _, s := range []int{-1, 1} {
		if hDouble {
			y := cy + s*dd - half
			blocked := (s < 0 && u != 0) || (s > 0 && d != 0)
			if l == 3 {
				x1 := cx + dd - half + t
				switch {
				case blocked:
					x1 = cx - dd - half + t
				case sv > 0:
					x1 = cx - sv/2 + sv
				}
				fillRect(m, 0, y, x1, t, 255)
			}
			if rt == 3 {
				x0 := cx - dd - half
				switch {
				case blocked:
					x0 = cx + dd - half
				case sv > 0:
					x0 = cx - sv/2
				}
				fillRect(m, x0, y, w-x0, t, 255)
			}
		}
		if vDouble {
			x := cx + s*dd - half
			blocked := (s < 0 && l != 0) || (s > 0 && rt != 0)
			if u == 3 {
				y1 := cy + dd - half + t
				switch {
				case blocked:
					y1 = cy - dd - half + t
				case sh > 0:
					y1 = cy - sh/2 + sh
				}
				fillRect(m, x, 0, t, y1, 255)
			}
			if d == 3 {
				y0 := cy - dd - half
				switch {
				case blocked:
					y0 = cy + dd - half
				case sh > 0:
					y0 = cy - sh/2
				}
				fillRect(m, x, y0, t, h-y0, 255)
			}
		}
	}
	return true
}

func drawDashes(m *image.Alpha, n, t int, vertical bool) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	length := w
	if vertical {
		length = h
	}
	seg := length / n
	gap := max(1, seg/3)
	for i := 0; i < n; i++ {
		a := i*seg + gap/2
		b := (i+1)*seg - (gap - gap/2)
		if vertical {
			fillRect(m, w/2-t/2, a, t, b-a, 255)
		} else {
			fillRect(m, a, h/2-t/2, b-a, t, 255)
		}
	}
}

// cover fills the mask according to a membership function, with 4×4
// supersampling outside pixel mode for smooth edges.
func cover(m *image.Alpha, pixel bool, inside func(x, y float64) bool) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	ss := 4
	if pixel {
		ss = 1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					if inside(float64(x)+(float64(sx)+0.5)/float64(ss), float64(y)+(float64(sy)+0.5)/float64(ss)) {
						n++
					}
				}
			}
			if n > 0 {
				v := uint8(n * 255 / (ss * ss))
				i := y*m.Stride + x
				m.Pix[i] = max(m.Pix[i], v)
			}
		}
	}
}

func drawRounded(m *image.Alpha, r rune, t int, pixel bool) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	cx, cy := w/2, h/2
	fcx := float64(cx-t/2) + float64(t)/2
	fcy := float64(cy-t/2) + float64(t)/2
	R := math.Min(fcx, fcy)
	sx, sy := 1.0, 1.0 // direction of the arms
	switch r {
	case '╮':
		sx = -1
	case '╯':
		sx, sy = -1, -1
	case '╰':
		sy = -1
	}
	ax, ay := fcx+sx*R, fcy+sy*R
	ht := float64(t) / 2
	cover(m, pixel, func(x, y float64) bool {
		// Only the quarter facing the center of the arc is drawn.
		if (x-ax)*sx > 0 || (y-ay)*sy > 0 || (x-fcx)*sx < -ht || (y-fcy)*sy < -ht {
			return false
		}
		d := math.Hypot(x-ax, y-ay)
		return math.Abs(d-R) <= ht
	})
	// Straight extensions up to the edges.
	if sy > 0 {
		fillRect(m, cx-t/2, int(ay), t, h, 255)
	} else {
		fillRect(m, cx-t/2, 0, t, int(math.Ceil(ay)), 255)
	}
	if sx > 0 {
		fillRect(m, int(ax), cy-t/2, w, t, 255)
	} else {
		fillRect(m, 0, cy-t/2, int(math.Ceil(ax)), t, 255)
	}
}

func drawDiagonal(m *image.Alpha, r rune, t int, pixel bool) {
	w, h := float64(m.Rect.Dx()), float64(m.Rect.Dy())
	ht := float64(t) / 2 * math.Hypot(w, h) / h
	cover(m, pixel, func(x, y float64) bool {
		up := math.Abs(x-(h-y)*w/h) <= ht // ╱
		down := math.Abs(x-y*w/h) <= ht   // ╲
		switch r {
		case '╱':
			return up
		case '╲':
			return down
		}
		return up || down
	})
}

// 4×4 dither patterns for ░ ▒ ▓ in pixel mode.
var shadePatterns = [3][4]uint8{
	{0b1000, 0b0000, 0b0010, 0b0000},
	{0b1010, 0b0101, 0b1010, 0b0101},
	{0b0111, 0b1111, 0b1101, 0b1111},
}

func drawBlock(m *image.Alpha, r rune, pixel bool, scale int) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	eighthH := func(n int) int { return (h*n + 4) / 8 }
	eighthW := func(n int) int { return (w*n + 4) / 8 }
	switch {
	case r == 0x2580:
		fillRect(m, 0, 0, w, h/2, 255)
	case r >= 0x2581 && r <= 0x2588:
		n := int(r - 0x2580)
		fillRect(m, 0, h-eighthH(n), w, eighthH(n), 255)
	case r >= 0x2589 && r <= 0x258f:
		fillRect(m, 0, 0, eighthW(int(0x2590-r)), h, 255)
	case r == 0x2590:
		fillRect(m, w/2, 0, w-w/2, h, 255)
	case r >= 0x2591 && r <= 0x2593:
		level := int(r - 0x2591)
		if !pixel {
			fillRect(m, 0, 0, w, h, uint8(64*(level+1)))
			return
		}
		s := max(1, scale)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if shadePatterns[level][(y/s)%4]&(1<<uint(3-(x/s)%4)) != 0 {
					m.Pix[y*m.Stride+x] = 255
				}
			}
		}
	case r == 0x2594:
		fillRect(m, 0, 0, w, eighthH(1), 255)
	case r == 0x2595:
		fillRect(m, w-eighthW(1), 0, eighthW(1), h, 255)
	default:
		quads := [...]uint8{0b0100, 0b1000, 0b0001, 0b1101, 0b1001, 0b0111, 0b1011, 0b0010, 0b0110, 0b1110}
		q := quads[r-0x2596]
		hw, hh := w/2, h/2
		if q&1 != 0 {
			fillRect(m, 0, 0, hw, hh, 255)
		}
		if q&2 != 0 {
			fillRect(m, hw, 0, w-hw, hh, 255)
		}
		if q&4 != 0 {
			fillRect(m, 0, hh, hw, h-hh, 255)
		}
		if q&8 != 0 {
			fillRect(m, hw, hh, w-hw, h-hh, 255)
		}
	}
}

func drawBraille(m *image.Alpha, r rune) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	bits := int(r - 0x2800)
	cw, rh := w/2, h/4
	ds := max(1, min(cw, rh)*2/3)
	pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	for i, p := range pos {
		if bits&(1<<uint(i)) == 0 {
			continue
		}
		x := p[0]*cw + (cw-ds)/2
		y := p[1]*rh + (rh-ds)/2
		fillRect(m, x, y, ds, ds, 255)
	}
}

func drawPowerline(m *image.Alpha, r rune, pixel bool, scale int) {
	w, h := float64(m.Rect.Dx()), float64(m.Rect.Dy())
	t := float64(lineThickness(m.Rect.Dx(), pixel, scale))
	mid := h / 2
	mirror := r == 0xe0b2 || r == 0xe0b3 || r == 0xe0b6
	cover(m, pixel, func(x, y float64) bool {
		if mirror {
			x = w - x
		}
		dy := math.Abs(y-mid) / mid // 0 at the center, 1 at the edges
		switch r {
		case 0xe0b0, 0xe0b2: // solid triangle
			return x <= w*(1-dy)
		case 0xe0b1, 0xe0b3: // thin chevron
			edge := w * (1 - dy)
			return x <= edge && x >= edge-t*1.5
		default: // solid half circle
			nx := x / w
			return nx*nx+dy*dy <= 1
		}
	})
}
