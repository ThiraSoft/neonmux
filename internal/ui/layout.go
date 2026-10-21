package ui

import (
	"image"
	"math"
)

// Available layouts, in cycle order.
var layouts = []string{"tiles", "columns", "rows", "monocle"}

var layoutLabels = map[string]string{
	"tiles": "TILES", "columns": "COLS", "rows": "ROWS", "monocle": "MONO",
}

// tile spreads n panes over area, i3/aerospace style. ratio sets the
// master column, ratio2 the vertical split.
func tile(layout string, n int, area image.Rectangle, ratio, ratio2 float64) []image.Rectangle {
	out := make([]image.Rectangle, n)
	if n == 0 {
		return out
	}
	splitX := func(r image.Rectangle, t float64) (image.Rectangle, image.Rectangle) {
		x := r.Min.X + int(float64(r.Dx())*t)
		return image.Rect(r.Min.X, r.Min.Y, x, r.Max.Y), image.Rect(x, r.Min.Y, r.Max.X, r.Max.Y)
	}
	splitY := func(r image.Rectangle, t float64) (image.Rectangle, image.Rectangle) {
		y := r.Min.Y + int(float64(r.Dy())*t)
		return image.Rect(r.Min.X, r.Min.Y, r.Max.X, y), image.Rect(r.Min.X, y, r.Max.X, r.Max.Y)
	}
	switch layout {
	case "monocle":
		for i := range out {
			out[i] = area
		}
	case "columns":
		for i := range out {
			out[i] = image.Rect(area.Min.X+area.Dx()*i/n, area.Min.Y, area.Min.X+area.Dx()*(i+1)/n, area.Max.Y)
		}
	case "rows":
		for i := range out {
			out[i] = image.Rect(area.Min.X, area.Min.Y+area.Dy()*i/n, area.Max.X, area.Min.Y+area.Dy()*(i+1)/n)
		}
	default:
		switch n {
		case 1:
			out[0] = area
		case 2:
			out[0], out[1] = splitX(area, ratio)
		case 3:
			var right image.Rectangle
			out[0], right = splitX(area, ratio)
			out[1], out[2] = splitY(right, ratio2)
		case 4:
			left, right := splitX(area, ratio)
			out[0], out[2] = splitY(left, ratio2)
			out[1], out[3] = splitY(right, ratio2)
		default:
			cols := int(math.Ceil(math.Sqrt(float64(n))))
			rows := (n + cols - 1) / cols
			for i := range out {
				c, r := i%cols, i/cols
				out[i] = image.Rect(area.Min.X+area.Dx()*c/cols, area.Min.Y+area.Dy()*r/rows,
					area.Min.X+area.Dx()*(c+1)/cols, area.Min.Y+area.Dy()*(r+1)/rows)
			}
		}
	}
	return out
}

// neighbor finds the closest pane in a direction (h, j, k, l).
func neighbor(rects []image.Rectangle, from int, dir string) int {
	cur := rects[from]
	cc := cur.Min.Add(cur.Max).Div(2)
	best, bestD := -1, math.MaxFloat64
	for i, r := range rects {
		if i == from || r.Empty() {
			continue
		}
		c := r.Min.Add(r.Max).Div(2)
		var primary, secondary float64
		switch dir {
		case "left":
			if r.Max.X > cur.Min.X+2 {
				continue
			}
			primary, secondary = float64(cur.Min.X-r.Max.X), math.Abs(float64(c.Y-cc.Y))
		case "right":
			if r.Min.X < cur.Max.X-2 {
				continue
			}
			primary, secondary = float64(r.Min.X-cur.Max.X), math.Abs(float64(c.Y-cc.Y))
		case "up":
			if r.Max.Y > cur.Min.Y+2 {
				continue
			}
			primary, secondary = float64(cur.Min.Y-r.Max.Y), math.Abs(float64(c.X-cc.X))
		case "down":
			if r.Min.Y < cur.Max.Y-2 {
				continue
			}
			primary, secondary = float64(r.Min.Y-cur.Max.Y), math.Abs(float64(c.X-cc.X))
		}
		if d := primary + secondary*0.5; d < bestD {
			best, bestD = i, d
		}
	}
	return best
}
