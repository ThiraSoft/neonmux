package ui

import (
	"image"
	"slices"
	"strings"
	"unicode"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

type command struct {
	label  string
	hint   string
	action string
}

type scored struct {
	cmd   command
	score int
}

// Palette is the fuzzy-search command palette (cmd+p).
type Palette struct {
	app    *App
	title  string
	status string
	query  []rune
	all    []command
	items  []scored
	sel    int
	top    int
	input  func(string) // free input: enter returns the typed text
	// pick receives the chosen item of a free input palette that also
	// lists items (bookmarks): enter takes it once the selection was
	// moved, or with nothing typed, and the typed text otherwise.
	pick  func(command)
	moved bool
	// onClose closes what the chosen item points to (mod+w), in the
	// pane picker.
	onClose func(command)
}

func (a *App) openPalette(title string, cmds []command) {
	a.palette = &Palette{app: a, title: title}
	a.palette.setItems(cmds)
	a.needRedraw = true
}

func (p *Palette) setItems(cmds []command) {
	p.all = cmds
	p.filter()
}

// refresh replaces the items, keeping the query and, as far as possible,
// the selection position.
func (p *Palette) refresh(cmds []command) {
	sel := p.sel
	p.setItems(cmds)
	p.sel = min(sel, max(len(p.items)-1, 0))
}

var accentFold = strings.NewReplacer("à", "a", "â", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "ô", "o", "ö", "o", "ù", "u", "û", "u", "ü", "u", "ç", "c")

func fold(s string) string { return accentFold.Replace(strings.ToLower(s)) }

// fuzzy scores a subsequence match: consecutive letters and word starts
// score higher.
func fuzzy(query, target string) (int, bool) {
	q := []rune(fold(query))
	t := []rune(fold(target))
	if len(q) == 0 {
		return 0, true
	}
	score, qi, prev := 0, 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		s := 1
		if ti == prev+1 {
			s += 5
		}
		if ti == 0 || !unicode.IsLetter(t[ti-1]) {
			s += 8
		}
		score += s
		prev = ti
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	return score*10 - len(t), true
}

func (p *Palette) filter() {
	p.items = p.items[:0]
	q := string(p.query)
	for _, c := range p.all {
		if s, ok := fuzzy(q, c.label+" "+c.hint); ok {
			p.items = append(p.items, scored{c, s})
		}
	}
	if q != "" {
		slices.SortStableFunc(p.items, func(a, b scored) int { return b.score - a.score })
	}
	p.sel, p.top = 0, 0
	p.moved = false
	p.app.needRedraw = true
}

// selecting reports whether enter takes the selected item rather than
// the typed text.
func (p *Palette) selecting() bool {
	return p.input == nil || p.pick != nil && (p.moved || len(p.query) == 0)
}

func (p *Palette) move(d int) {
	if len(p.items) == 0 {
		return
	}
	// The first move of a free input palette only reveals the selection.
	if p.selecting() {
		p.sel = (p.sel + d + len(p.items)) % len(p.items)
	}
	p.moved = true
	p.app.needRedraw = true
}

func (p *Palette) scroll(d int) { p.move(-d) }

func (p *Palette) key(ev KeyEvent) {
	ctrl := ev.Mods&ModCtrl != 0
	closeKey := p.onClose != nil && p.app.bindings[Combo{Mods: ev.Mods, Name: ev.Name}] == "pane.close"
	switch {
	case closeKey:
		// Held down, the key does not close pane after pane.
		if p.sel < len(p.items) && !ev.Repeat {
			p.onClose(p.items[p.sel].cmd)
		}
	case ev.Name == "escape":
		p.app.palette = nil
	case ev.Name == "enter" && p.input != nil:
		p.app.palette = nil
		if p.pick != nil && p.selecting() && p.sel < len(p.items) {
			p.pick(p.items[p.sel].cmd)
			return
		}
		p.input(string(p.query))
	case ev.Name == "enter":
		if p.sel < len(p.items) {
			act := p.items[p.sel].cmd.action
			p.app.palette = nil
			p.app.do(act)
		}
	case ev.Name == "down", ev.Name == "tab" && ev.Mods&ModShift == 0, ctrl && (ev.Name == "j" || ev.Name == "n"):
		p.move(1)
	case ev.Name == "up", ev.Name == "tab", ctrl && (ev.Name == "k" || ev.Name == "p"):
		p.move(-1)
	case ev.Name == "backspace":
		if len(p.query) > 0 {
			p.query = p.query[:len(p.query)-1]
			p.filter()
		}
	case ctrl && ev.Name == "u":
		p.query = p.query[:0]
		p.filter()
	}
	p.app.needRedraw = true
}

func (p *Palette) chars(rs []rune) {
	for _, r := range rs {
		if unicode.IsPrint(r) {
			p.query = append(p.query, r)
		}
	}
	p.filter()
}

func (p *Palette) draw(ctx *Ctx, dst *ebiten.Image) {
	a := p.app
	f := a.uiFont
	th := a.theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)

	// Veil over the whole screen.
	P.Rect(0, 0, float32(a.w), float32(a.h), th.BG, 0.55)

	visible := 12
	w := min(float32(a.w)*0.7, cw*72)
	h := ch * (2 + float32(visible)*1.2 + 2.4)
	x := (float32(a.w) - w) / 2
	y := float32(a.h) * 0.16
	P.Rect(x, y, w, h, th.BarBG, 0.98)
	r := image.Rect(int(x), int(y), int(x+w), int(y+h))
	P.Border(r, 2*a.px, th.Accent2, 1)
	P.Border(r.Inset(-int(a.px)), a.px, th.Accent2, 0.3)

	title := p.title
	if title == "" {
		title = "COMMANDS"
	}
	P.Rect(x, y, w, ch*1.6, th.Accent2, 0.15)
	P.Text(f, x+cw, y+ch*0.3, "› "+string(p.query), th.FG, 1)
	cursorX := x + cw + float32(gfx.TextWidth(f, "› "+string(p.query)))
	P.Rect(cursorX, y+ch*0.3, cw*0.6, ch, th.Accent, 0.9)
	tw := float32(gfx.TextWidth(f, title))
	P.Text(f, x+w-tw-cw, y+ch*0.3, title, th.Accent2, 1)

	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+visible {
		p.top = p.sel - visible + 1
	}
	ly := y + ch*2
	maxLabel := int(w/cw) - 18
	for i := p.top; i < len(p.items) && i < p.top+visible; i++ {
		it := p.items[i].cmd
		sel := i == p.sel && p.selecting()
		if sel {
			P.Rect(x+a.px*2, ly-ch*0.1, w-a.px*4, ch*1.2, th.Accent, 0.22)
			P.Rect(x+a.px*2, ly-ch*0.1, 3*a.px, ch*1.2, th.Accent, 1)
		}
		c := th.BarFG
		if sel {
			c = th.FG
		}
		// The label stops before the hint on the right, whatever its
		// length (a shortcut, or an agent's state).
		room := maxLabel
		if it.hint != "" {
			hw := float32(gfx.TextWidth(f, it.hint))
			P.Text(f, x+w-hw-cw, ly, it.hint, th.Dim, 1)
			room = min(room, int((w-hw-cw*3.5)/cw))
		}
		P.Text(f, x+cw*1.5, ly, gfx.Truncate(it.label, max(room, 8)), c, 1)
		ly += ch * 1.2
	}
	footer := p.status
	if footer == "" {
		footer = itoa(len(p.items)) + " results   ↑↓ or ctrl+j/k   enter select   esc close"
	}
	P.Rect(x, y+h-ch*1.7, w, a.px, th.Border, 1)
	P.Text(f, x+cw, y+h-ch*1.3, gfx.Truncate(footer, int(w/cw)-2), th.Dim, 1)
	P.Flush(dst)
}
