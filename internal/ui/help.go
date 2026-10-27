package ui

import (
	"fmt"
	"image"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

// helpRow is a line of the help: a section title, or a key and what it
// does.
type helpRow struct {
	section string
	keys    string
	desc    string
}

// HelpPane lists the active shortcuts, config included, with a filter
// like the file explorer's. It opens as a floating pane in the center
// (toggleHelp).
type HelpPane struct {
	paneBase
	app         *App
	headerRunes func(rune) rune
	drawRunes   func(rune) rune
	rows        []helpRow
	view        []helpRow
	top         int
	filter      []rune
	filtering   bool
	pendingG    bool
	content     image.Rectangle
	visible     int // lines shown
	dirty       bool
	closed      bool
}

func NewHelpPane(id int) *HelpPane {
	h := &HelpPane{}
	h.id = id
	return h
}

func (h *HelpPane) Kind() string { return "help" }
func (h *HelpPane) Title() string {
	if h.app != nil && h.app.opts.Version != "" {
		return "help · neonmux " + h.app.opts.Version
	}
	return "help"
}

func (h *HelpPane) Info() string {
	if h.app != nil {
		h.headerRunes = h.app.painter.Runes
	}
	if len(h.filter) > 0 {
		return fmt.Sprintf("%d lines", h.matches())
	}
	return "/ filter  q close"
}

func (h *HelpPane) Mode() string {
	if h.filtering {
		return "FILTER"
	}
	return ""
}

func (h *HelpPane) matches() int {
	n := 0
	for _, r := range h.view {
		if r.section == "" {
			n++
		}
	}
	return n
}

func (h *HelpPane) Layout(ctx *Ctx, content image.Rectangle) {
	h.content = content
	// Rebuilt on every layout: a shortcut changed in the reloaded config
	// shows up right away.
	h.rows = ctx.App.helpRows()
	h.applyFilter()
	h.visible = max(1, content.Dy()/ctx.Term.CellH-2)
	h.dirty = true
}

func (h *HelpPane) Update(ctx *Ctx) bool {
	d := h.dirty
	h.dirty = false
	return d
}

func (h *HelpPane) AnimFPS(ctx *Ctx) int        { return 0 }
func (h *HelpPane) NextWake(ctx *Ctx) time.Time { return time.Time{} }
func (h *HelpPane) Close()                      {}
func (h *HelpPane) Done() bool                  { return h.closed }

// applyFilter keeps the lines that contain the filter (keys, text or
// section), preceded by their section title.
func (h *HelpPane) applyFilter() {
	h.dirty = true
	if len(h.filter) == 0 {
		h.view = h.rows
		h.scroll(0)
		return
	}
	q := strings.ToLower(string(h.filter))
	h.view = nil
	section, shown := helpRow{}, false
	for _, r := range h.rows {
		if r.section != "" {
			section, shown = r, false
			continue
		}
		hay := strings.ToLower(r.keys + " " + r.desc + " " + section.section)
		if !strings.Contains(hay, q) {
			continue
		}
		if !shown && section.section != "" {
			h.view = append(h.view, section)
			shown = true
		}
		h.view = append(h.view, r)
	}
	h.top = 0
}

func (h *HelpPane) scroll(d int) {
	top := min(max(h.top+d, 0), max(len(h.view)-h.visible, 0))
	if top != h.top {
		h.top = top
	}
	h.dirty = true
}

func (h *HelpPane) Key(ctx *Ctx, ev KeyEvent) bool {
	if h.filtering {
		switch ev.Name {
		case "escape":
			h.filtering = false
			h.filter = nil
			h.applyFilter()
		case "enter":
			h.filtering = false
		case "backspace":
			if len(h.filter) > 0 {
				h.filter = h.filter[:len(h.filter)-1]
				h.applyFilter()
			}
		}
		return true
	}
	half := max(1, h.visible/2)
	switch {
	case ev.Name == "down":
		h.scroll(1)
	case ev.Name == "up":
		h.scroll(-1)
	case ev.Mods&ModCtrl != 0 && ev.Name == "d", ev.Name == "pagedown":
		h.scroll(half)
	case ev.Mods&ModCtrl != 0 && ev.Name == "u", ev.Name == "pageup":
		h.scroll(-half)
	case ev.Name == "escape":
		// A first esc clears the filter, the next one closes the help.
		if len(h.filter) > 0 {
			h.filter = nil
			h.applyFilter()
		} else {
			h.closed = true
		}
	default:
		return false
	}
	return true
}

func (h *HelpPane) Chars(ctx *Ctx, rs []rune) {
	for _, r := range rs {
		if h.filtering {
			h.filter = append(h.filter, r)
			h.applyFilter()
			continue
		}
		g := h.pendingG
		h.pendingG = false
		switch r {
		case 'j':
			h.scroll(1)
		case 'k':
			h.scroll(-1)
		case ' ':
			h.scroll(h.visible)
		case 'g':
			if g {
				h.scroll(-len(h.view))
			} else {
				h.pendingG = true
			}
		case 'G':
			h.scroll(len(h.view))
		case '/':
			h.filtering = true
			h.filter = nil
			h.applyFilter()
		case 'q':
			h.closed = true
		}
	}
}

func (h *HelpPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	if ev.Kind == MouseWheel {
		if ev.Wheel > 0 {
			h.scroll(-3)
		} else if ev.Wheel < 0 {
			h.scroll(3)
		}
	}
	return true
}

func (h *HelpPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	h.drawRunes = ctx.P.Runes
	if content != h.content {
		return // layout animation: wait for the final position
	}
	f := ctx.Term
	th := ctx.Theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	x0, y0 := float32(content.Min.X)+cw, float32(content.Min.Y)
	cols := content.Dx()/f.CellW - 2

	top := "Active shortcuts, config included"
	if h.filtering || len(h.filter) > 0 {
		top = "/ " + string(h.filter)
		if h.filtering {
			top += "▏"
		}
	}
	P.Text(f, x0, y0, gfx.Truncate(top, cols), th.Accent2, 1)

	// The key column takes the width of the longest keys.
	keyW := 0
	for _, r := range h.view {
		keyW = max(keyW, gfx.TextCells(r.keys))
	}
	keyW = min(keyW+2, cols/2)
	for i := 0; i < h.visible && h.top+i < len(h.view); i++ {
		r := h.view[h.top+i]
		y := y0 + ch*float32(i+2)
		if r.section != "" {
			w := P.Text(f, x0, y, gfx.Truncate(r.section, cols), th.Accent, 1)
			P.Rect(x0+w+cw, y+ch/2, max(0, float32(cols)*cw-w-cw), ctx.Px, th.Border, 1)
			continue
		}
		P.Text(f, x0, y, gfx.Truncate(r.keys, keyW-1), th.Accent2, 1)
		P.Text(f, x0+float32(keyW)*cw, y, gfx.Truncate(r.desc, cols-keyW), th.FG, 1)
	}
	if len(h.view) == 0 {
		P.Text(f, x0, y0+ch*2, "no shortcut matches", th.Dim, 1)
	}
	// Discreet scrollbar when not everything fits.
	if n := len(h.view); n > h.visible {
		trackY := y0 + ch*2
		trackH := float32(content.Dy()) - ch*2
		thumbH := max(ch, trackH*float32(h.visible)/float32(n))
		thumbY := trackY + (trackH-thumbH)*float32(h.top)/float32(n-h.visible)
		x := float32(content.Max.X) - 2*ctx.Px
		P.Rect(x, trackY, ctx.Px, trackH, th.Border, 1)
		P.Rect(x-ctx.Px/2, thumbY, 2*ctx.Px, thumbH, th.Accent, 0.8)
	}
}

// helpRows builds the help from the shortcuts actually in effect: a
// shortcut changed in the config, or shadowed by another one, shows up.
func (a *App) helpRows() []helpRow {
	var rows []helpRow
	section := func(s string) { rows = append(rows, helpRow{section: s}) }
	row := func(keys, desc string) {
		if keys != "" {
			rows = append(rows, helpRow{keys: keys, desc: desc})
		}
	}
	act := func(action, desc string) { row(a.hintFor(action), desc) }
	// group shows several shortcuts with the same modifier as one:
	// "cmd+h/j/k/l".
	group := func(desc string, actions ...string) {
		hints := make([]string, len(actions))
		for i, ac := range actions {
			hints[i] = a.hintFor(ac)
		}
		row(joinHints(hints), desc)
	}
	mod := strings.TrimSuffix(Combo{Mods: a.mod}.String(), "+")
	resize := a.hintFor("mode.resize")

	section("General")
	row(a.leader.String()+" then the key", "every shortcut also exists behind the leader, without "+mod)
	act("help", "this help (a second time: close it)")
	act("palette", "fuzzy-search command palette")
	act("config.open", "open the configuration")
	act("quit", "quit (terminals keep running in the daemon)")

	section("Panes")
	act("term.new", "new terminal, in the current terminal's directory")
	act("files.open", "file explorer, above the current pane (same key to close it)")
	act("pane.close", "close the pane")
	act("pane.rename", "rename the pane (empty: automatic name)")
	act("pane.zoom", "zoom the pane (a floating pane grows on its own)")
	act("layout.next", "next layout: tiles, columns, rows, monocle")
	act("view.zen", "zen mode: hide all panes (same key to come back)")
	act("view.braille", "braille view: text in 8-dot braille (same key to come back)")
	act("window.fullscreen", "fullscreen (same key to leave it)")

	section("Workspaces")
	if first, last := a.hintFor("ws.1"), a.hintFor("ws.9"); first != "" && last != "" {
		row(first+"…"+last[strings.LastIndex(last, "+")+1:], "show workspace n (top row or keypad)")
	}
	if first, last := a.hintFor("ws.move.1"), a.hintFor("ws.move.9"); first != "" && last != "" {
		row(first+"…"+last[strings.LastIndex(last, "+")+1:], "send the pane to workspace n")
	}
	row("1 2 3", "bottom bar: workspaces in use, the one shown highlighted, in warning color if an agent waits there")
	row("garage", "shared by every workspace; the visitor goes back to it on a switch")

	section("Agents")
	act("agents.pick", "go to a pane or agent, garage included: filter by project, branch or topic")
	act("notify.next", "next agent that is waiting (blocked first, then finished)")
	act("focus.back", "back to the previous pane")
	act("agents.board", "agent board: state, directory, branch, changes")
	act("diff.open", "diff of the active pane's repository (J K files, enter: editor)")
	row("status icons", "working, waiting for your answer, finished, failed (header, bottom bar)")
	row("+120 −34", "lines added and removed since the last commit")
	row("neonmux agent name [prompt]", "Claude on a new worktree, in a tile")

	section("Garage")
	act("pane.park", "send the pane to the garage: it keeps running, without being shown")
	act("notify.next", "a parked agent that is waiting comes out large in the center, without touching the layout")
	act("focus.back", "send the pane brought out back to the garage, focus returns where you were")
	act("pane.float", "keep the pane brought out of the garage on screen (as a tile if one is free)")
	act("agents.pick", "every pane, garage included ("+a.hintFor("pane.close")+" closes the chosen one)")
	row("garage 12 · 2 waiting", "bottom bar: panes in the garage, and those waiting for you (pulses if one is blocked)")
	row("tiles full", "a new pane sends the active tile to the garage, without stopping it")

	section("Games")
	row("Game: typing test", "palette: type a random Wikipedia section, monkeytype style")
	row("Game: vim kata", "palette: vim missions on Wikipedia text, in fewest keys (needs Neovim 0.10)")
	row("leaving a game", "it goes to the garage, paused ("+a.hintFor("focus.back")+" brings it back)")
	row("tab", "typing test: new text")
	row("left/right", "typing test: time or words mode, before the first key")
	row("alt+backspace", "typing test: delete the word")
	row(":q", "vim kata: give up and see the solution")
	row("enter / n / r", "vim kata, end of a mission: new text, next mission, same text again")

	section("Navigation")
	group("focus the neighbor (from a tile: tiles only; in monocle, left and right go through the tiles in order)", "focus.left", "focus.down", "focus.up", "focus.right")
	group("swap with the neighbor (a floating pane snaps to the edge)", "swap.left", "swap.down", "swap.up", "swap.right")
	act("focus.layer", "switch from tiles to floating panes, and back")
	act("mode.resize", "resize the tiles: h j k l, = rebalances, esc exits")

	section("Floating panes")
	act("pane.float", "float the pane, or put it back as a tile")
	act("focus.layer", "focus: last tile, then last floating pane")
	group("snap to the edge (right then up: top right)", "swap.left", "swap.down", "swap.up", "swap.right")
	row(resize+" then h j k l", "size")
	row(resize+" then H J K L", "position (shift and the arrows too)")
	row(resize+" then y u b n", "corners: top left, top right, bottom left, bottom right")
	row(resize+" then c, =", "center, default size")
	row("drag the header", "move (or "+mod+" and drag anywhere)")
	row("drag an edge", "resize (corners too)")
	row("neonmux float -c br cmd", "run a command in a new floating pane")

	section("Terminal")
	act("copy", "copy the selection")
	act("paste", "paste")
	act("mode.copy", "vim-style copy mode in the scrollback")
	row("h j k l  w b e  W B E  0 ^ $", "copy mode: move")
	row("f F t T  ; ,  %", "copy mode: to a character, repeat, matching bracket")
	row("gg G  ctrl+u/d", "copy mode: top, bottom, half page")
	row("{  }", "copy mode: previous, next blank line")
	row("v  V  y", "copy mode: select, by lines, copy")
	row("vip viw viW vi( vi\"", "copy mode: paragraph, word, WORD, brackets, quotes (a: around)")
	row("3j  v2iw", "copy mode: counts, objects grow the selection")
	row("/  ?  n N  * #", "copy mode: search down, up, next, previous, word under the cursor")
	row("q  esc", "copy mode: exit")

	section("File explorer")
	row("j k  gg G", "navigate")
	row("l  enter", "open: image, sound, otherwise the editor, in the shell below if it sits at its prompt")
	row("h  -  ~", "go up, back to home")
	row("/  .", "filter, hidden files")
	row("y  o", "copy the path, open with the system application")
	row("c  t", "cd the shell below (or the last terminal) here, new terminal here")
	row("q  esc", "close; it also closes once a file is opened or a path copied")

	section("Image and audio")
	row("+ -  0 1", "image: zoom, fit, actual size")
	row("h j k l  i  n p", "image: pan, smoothing, neighboring image")
	row("space", "audio: play, pause")
	row("h l  H L", "audio: ±5s, ±30s")
	row("j k  n p  v", "audio: volume, neighboring track, spectrum or oscilloscope")

	section("Browser")
	act("browser.open", "open an address or search in a new pane (Brave, Chromium, Chrome, Edge, headless)")
	row("ctrl+l  f6", "type another address (click on the address does the same)")
	row("alt+← →  ctrl+r f5", "back, forward, reload (‹ › ↻ in the bar)")
	row("mod+c  mod+v", "copy the page's selection, paste into the page")

	section("Appearance")
	group("next, previous theme", "theme.next", "theme.prev")
	group("bigger, smaller, default font", "font.bigger", "font.smaller", "font.reset")
	act("font.pick", "choose an installed font")
	act("bg.next", "next background")
	act("hud.toggle", "performance HUD")

	// Shortcuts added in the config, sorted by key.
	var custom []helpRow
	for k, v := range a.cfg.Bindings {
		if c, err := ParseCombo(k, a.mod); err == nil {
			custom = append(custom, helpRow{keys: c.String(), desc: v})
		}
	}
	if len(custom) > 0 {
		sort.Slice(custom, func(i, j int) bool { return custom[i].keys < custom[j].keys })
		section("Your configuration")
		rows = append(rows, custom...)
	}
	return rows
}

// joinHints merges shortcuts that differ only by the key: "cmd+h" and
// "cmd+j" give "cmd+h/j". Otherwise they are placed side by side.
func joinHints(hints []string) string {
	hints = slices.DeleteFunc(slices.Clone(hints), func(h string) bool { return h == "" })
	if len(hints) == 0 {
		return ""
	}
	cut := strings.LastIndex(hints[0], "+")
	prefix := hints[0][:cut+1]
	keys := make([]string, len(hints))
	for i, h := range hints {
		if !strings.HasPrefix(h, prefix) || strings.LastIndex(h, "+") != cut || cut < 0 {
			return strings.Join(hints, "  ")
		}
		keys[i] = h[cut+1:]
	}
	return prefix + strings.Join(keys, "/")
}

// toggleHelp opens the help as a floating pane in the center. When it is
// already open, the same shortcut focuses it, then closes it.
func (a *App) toggleHelp() {
	for i, p := range a.panes {
		if _, ok := p.(*HelpPane); ok {
			if i == a.focus {
				a.closePane(i)
			} else {
				a.goTo(p) // brought out if it is in the garage
			}
			return
		}
	}
	h := NewHelpPane(a.newID())
	h.app = a
	switch {
	case a.countPanes(true) < maxFloats:
		area := a.floatArea()
		w, _ := a.cellsToGeom(90, 1)
		h.floating = true
		h.geom = floatGeom{X: 0.5, Y: 0.5, W: w, H: float64(area.Dy()) * 0.85 / a.dsf}
	case a.countPanes(false) >= a.cfg.MaxPanes:
		// No free floating slot and no free tile: the help never replaces a pane.
		a.toast("help: close a pane first ("+itoa(maxFloats)+" floating panes and "+itoa(a.cfg.MaxPanes)+" tiles open)", a.theme.Warn)
		return
	}
	a.addPane(h)
}
