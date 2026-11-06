package mega

import (
	"strconv"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/sfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Life: puzzles on Conway's game of life. Place a few cells (zqsd or hjkl
// move the cursor, space toggles a cell), then enter runs the simulation:
// a living cell must reach the target before the generations run out.
// Cells go in the left zone only. Enter again stops and gives back the
// cells placed. A glider goes far.

const (
	lfCols, lfRows = 48, 26
	lfCell         = 8.0
	lfZone         = 12 // cells can only be placed left of this column
	lfTop          = 22.0
)

type lfLevel struct {
	cells, gens    int
	tx, ty, tw, th int // target, in cells
}

// lfLevels: the target moves away and the cells get scarce.
var lfLevels = []lfLevel{
	{cells: 3, gens: 6, tx: 12, ty: 10, tw: 2, th: 4},
	{cells: 5, gens: 60, tx: 22, ty: 6, tw: 2, th: 14},
	{cells: 5, gens: 90, tx: 32, ty: 2, tw: 2, th: 10},
	{cells: 5, gens: 110, tx: 34, ty: 14, tw: 3, th: 10},
	{cells: 6, gens: 110, tx: 30, ty: 0, tw: 3, th: 5},
	{cells: 5, gens: 100, tx: 30, ty: 20, tw: 2, th: 2},
}

type lifePuzzle struct {
	base
	level   int
	placed  [lfRows][lfCols]bool
	grid    [lfRows][lfCols]bool
	cx, cy  int
	running bool
	gen     int
	t       int
	won     int // frames since the target was reached
}

func newLifePuzzle(seed uint64) *lifePuzzle {
	g := &lifePuzzle{base: newBase(seed, 0x6c696665)}
	g.cx, g.cy = 6, lfRows/2
	return g
}

func (g *lifePuzzle) lv() lfLevel { return lfLevels[g.level] }

func (g *lifePuzzle) count() int {
	n := 0
	for r := range lfRows {
		for c := range lfCols {
			if g.placed[r][c] {
				n++
			}
		}
	}
	return n
}

func (g *lifePuzzle) Key(name string, repeat bool) {
	if g.again(name, repeat) {
		*g = *newLifePuzzle(g.rng.Uint64())
		return
	}
	if g.over || g.won > 0 {
		return
	}
	if g.running {
		if name == "enter" && !repeat {
			g.running = false
		}
		return
	}
	switch name {
	case "z", "up", "k":
		g.cy = max(0, g.cy-1)
	case "s", "down", "j":
		g.cy = min(lfRows-1, g.cy+1)
	case "q", "left", "h":
		g.cx = max(0, g.cx-1)
	case "d", "right", "l":
		g.cx = min(lfCols-1, g.cx+1)
	case "space":
		if !repeat && (g.placed[g.cy][g.cx] || g.count() < g.lv().cells) && g.cx < lfZone {
			g.placed[g.cy][g.cx] = !g.placed[g.cy][g.cx]
			sfx.Play(sfx.Bounce)
		}
	case "enter":
		if !repeat && g.count() > 0 {
			g.running, g.gen, g.t = true, 0, 0
			g.grid = g.placed
			sfx.Play(sfx.Toggle)
		}
	}
}

func (g *lifePuzzle) inTarget(c, r int) bool {
	l := g.lv()
	return c >= l.tx && c < l.tx+l.tw && r >= l.ty && r < l.ty+l.th
}

func (g *lifePuzzle) Step(dt float64, W, H int, paused bool) { g.loop(dt, W, H, paused, g.tick) }

func (g *lifePuzzle) tick() {
	if g.over {
		return
	}
	if g.won > 0 {
		if g.won++; g.won > 90 {
			g.won = 0
			if g.level++; g.level == len(lfLevels) {
				g.level = len(lfLevels) - 1
				g.over = true
				return
			}
			g.placed = [lfRows][lfCols]bool{}
			g.running = false
		}
		return
	}
	if !g.running {
		return
	}
	if g.t++; g.t%6 != 0 {
		return
	}
	var next [lfRows][lfCols]bool
	for r := range lfRows {
		for c := range lfCols {
			n := 0
			for dr := -1; dr <= 1; dr++ {
				for dc := -1; dc <= 1; dc++ {
					rr, cc := r+dr, c+dc
					if (dr != 0 || dc != 0) && rr >= 0 && rr < lfRows && cc >= 0 && cc < lfCols && g.grid[rr][cc] {
						n++
					}
				}
			}
			next[r][c] = n == 3 || n == 2 && g.grid[r][c]
		}
	}
	g.grid = next
	g.gen++
	for r := range lfRows {
		for c := range lfCols {
			if g.grid[r][c] && g.inTarget(c, r) {
				g.won = 1
				sfx.Play(sfx.Win)
				g.sparks(float64(c)*lfCell+4, float64(r)*lfCell+4, 20, 2, 1.4)
				return
			}
		}
	}
	if g.gen >= g.lv().gens {
		g.running = false
	}
}

func (g *lifePuzzle) Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int) {
	v := newView(P, f, th, W, H, 0)
	const bw = lfCols * lfCell
	x0 := float64(int((v.vw() - bw) / 2))
	o := theme.Mix(th.BG, black, 0.6)
	v.hud(x0-1, lfTop-1, bw+2, lfRows*lfCell+2, th.Accent2, 0.5)
	v.hud(x0, lfTop, bw, lfRows*lfCell, o, 0.75)
	v.hud(x0, lfTop, lfZone*lfCell, lfRows*lfCell, th.Accent2, 0.08)
	l := g.lv()
	tc := th.Warn
	if g.won > 0 && g.frame/5%2 == 0 {
		tc = th.FG
	}
	v.hud(x0+float64(l.tx)*lfCell, lfTop+float64(l.ty)*lfCell, float64(l.tw)*lfCell, float64(l.th)*lfCell, tc, 0.35)
	grid := &g.placed
	if g.running || g.won > 0 {
		grid = &g.grid
	}
	for r := range lfRows {
		for c := range lfCols {
			if grid[r][c] {
				v.hud(x0+float64(c)*lfCell+1, lfTop+float64(r)*lfCell+1, lfCell-2, lfCell-2, th.Accent2, 1)
				v.hud(x0+float64(c)*lfCell+2, lfTop+float64(r)*lfCell+2, 2, 2, th.FG, 0.8)
			}
		}
	}
	if !g.running && g.won == 0 && g.frame/15%3 != 0 {
		x, y := x0+float64(g.cx)*lfCell, lfTop+float64(g.cy)*lfCell
		v.hud(x, y, lfCell, 1, th.FG, 1)
		v.hud(x, y+lfCell-1, lfCell, 1, th.FG, 1)
		v.hud(x, y, 1, lfCell, th.FG, 1)
		v.hud(x+lfCell-1, y, 1, lfCell, th.FG, 1)
	}
	g.drawParts(v, x0, lfTop)
	info := "level " + strconv.Itoa(g.level+1) + "/" + strconv.Itoa(len(lfLevels)) +
		"   cells " + strconv.Itoa(g.count()) + "/" + strconv.Itoa(l.cells) + "   generations " + strconv.Itoa(l.gens)
	if g.running {
		info = "generation " + strconv.Itoa(g.gen) + "/" + strconv.Itoa(l.gens) + "   enter: stop"
	}
	v.text(info, x0, 6, th.FG, 1)
	if !g.running && !g.over {
		v.text("space: cell   enter: run", x0, lfTop+lfRows*lfCell+4, th.Dim, 1)
	}
	if g.won > 0 {
		v.center("REACHED", ViewH/2-30, th.Accent2, 1)
	}
	g.ending(v, "ALL SOLVED", "six puzzles, well done")
}
