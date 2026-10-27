package ui

import (
	"context"
	"image"
	"slices"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/vt"
)

// Agent board: a pane (floating at the top right by default) that lists the
// terminals whose program announces a status, from most to least urgent,
// with their directory, their branch and their changes.

// AgentsPane is the agent board.
type AgentsPane struct {
	paneBase
	list     []*TermPane
	sel, top int
	content  image.Rectangle
	visible  int // agents shown (three lines each)
	sig      string
	closed   bool
}

func NewAgentsPane(id int) *AgentsPane {
	p := &AgentsPane{}
	p.id = id
	return p
}

func (p *AgentsPane) Kind() string  { return "agents" }
func (p *AgentsPane) Title() string { return "agents" }
func (p *AgentsPane) Mode() string  { return "" }

func (p *AgentsPane) Info() string {
	waiting := 0
	for _, t := range p.list {
		if _, ok := t.needsYou(); ok {
			waiting++
		}
	}
	if waiting == 1 {
		return "1 waiting · " + plural(len(p.list), "agent")
	}
	if waiting > 1 {
		return itoa(waiting) + " waiting · " + plural(len(p.list), "agent")
	}
	return plural(len(p.list), "agent")
}

func (p *AgentsPane) Layout(ctx *Ctx, content image.Rectangle) {
	p.content = content
	p.visible = max(1, (content.Dy()/ctx.Term.CellH-2)/3)
	p.sig = "" // redraw
}

// agentRank orders the board: blocked, done and not seen, working, done
// and seen, ready.
func agentRank(t *TermPane) int {
	switch t.prog.State {
	case vt.PSBlocked:
		return 0
	case vt.PSDone, vt.PSError:
		if !t.progSeen {
			return 1
		}
		return 3
	case vt.PSWorking:
		return 2
	}
	return 4
}

func (p *AgentsPane) Update(ctx *Ctx) bool {
	a := ctx.App
	var sel *TermPane
	if p.sel < len(p.list) {
		sel = p.list[p.sel]
	}
	p.list = p.list[:0]
	for _, q := range a.panes {
		if t, ok := q.(*TermPane); ok && t.hasProg {
			p.list = append(p.list, t)
		}
	}
	slices.SortStableFunc(p.list, func(x, y *TermPane) int {
		if rx, ry := agentRank(x), agentRank(y); rx != ry {
			return rx - ry
		}
		return x.prog.Since.Compare(y.prog.Since)
	})
	// The selection follows the agent, not its position.
	if i := slices.Index(p.list, sel); i >= 0 {
		p.sel = i
	}
	p.sel = min(p.sel, max(len(p.list)-1, 0))
	var b strings.Builder
	for _, t := range p.list {
		icon, badge, _ := t.progBadge(ctx.Theme, ctx.Now)
		b.WriteString(icon + displayName(t) + t.prog.Title + badge + t.git.branch + itoa(t.git.add) + itoa(t.git.del) + a.paneTag(t) + "|")
	}
	b.WriteString(itoa(p.sel))
	if s := b.String(); s != p.sig {
		p.sig = s
		return true
	}
	return false
}

func (p *AgentsPane) AnimFPS(ctx *Ctx) int        { return 0 }
func (p *AgentsPane) NextWake(ctx *Ctx) time.Time { return time.Time{} }
func (p *AgentsPane) Close()                      {}
func (p *AgentsPane) Done() bool                  { return p.closed }

func (p *AgentsPane) move(d int) {
	if len(p.list) == 0 {
		return
	}
	p.sel = min(max(p.sel+d, 0), len(p.list)-1)
	p.sig = ""
}

func (p *AgentsPane) selected() *TermPane {
	if p.sel < len(p.list) {
		return p.list[p.sel]
	}
	return nil
}

// goTo gives the focus to the picked agent (an agent in the garage comes
// out in the center); the board stays open.
func (p *AgentsPane) goTo(a *App) {
	if t := p.selected(); t != nil {
		a.goTo(t)
	}
}

func (p *AgentsPane) Key(ctx *Ctx, ev KeyEvent) bool {
	switch ev.Name {
	case "down":
		p.move(1)
	case "up":
		p.move(-1)
	case "enter":
		p.goTo(ctx.App)
	case "escape":
		p.closed = true
	default:
		return false
	}
	return true
}

func (p *AgentsPane) Chars(ctx *Ctx, rs []rune) {
	for _, r := range rs {
		switch r {
		case 'j':
			p.move(1)
		case 'k':
			p.move(-1)
		case 'g':
			p.move(-len(p.list))
		case 'G':
			p.move(len(p.list))
		case 'd':
			if t := p.selected(); t != nil {
				ctx.App.openDiff(t.workDir())
			}
		case 'q':
			p.closed = true
		}
	}
}

func (p *AgentsPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	switch ev.Kind {
	case MouseWheel:
		if ev.Wheel > 0 {
			p.move(-1)
		} else if ev.Wheel < 0 {
			p.move(1)
		}
	case MousePress:
		row := (ev.Y-p.content.Min.Y)/ctx.Term.CellH - 2
		if i := p.top + row/3; row >= 0 && i < len(p.list) {
			p.sel = i
			p.sig = ""
			if ev.Clicks >= 2 {
				p.goTo(ctx.App)
			}
		}
	}
	return true
}

func (p *AgentsPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	if content != p.content {
		return
	}
	a := ctx.App
	f := ctx.Term
	th := ctx.Theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	x0, y0 := float32(content.Min.X)+cw/2, float32(content.Min.Y)
	cols := content.Dx()/f.CellW - 1
	w := float32(content.Dx()) - cw

	if len(p.list) == 0 {
		lines := []string{
			"No agents yet.",
			"",
			"Run claude in a terminal: its state",
			"shows up here by itself, no setup needed.",
			"",
			a.hintFor("notify.next") + "  next waiting agent",
			"neonmux agent <name> [prompt]  agent on a worktree",
		}
		for i, l := range lines {
			c := th.Dim
			if i == 0 {
				c = th.FG
			}
			P.Text(f, x0, y0+ch*float32(i), gfx.Truncate(l, cols), c, 1)
		}
		return
	}
	hint := "enter go to   d diff   q close"
	P.Text(f, x0, y0, gfx.Truncate(hint, cols), th.Dim, 1)
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+p.visible {
		p.top = p.sel - p.visible + 1
	}
	for i := 0; i < p.visible && p.top+i < len(p.list); i++ {
		t := p.list[p.top+i]
		y := y0 + ch*float32(2+3*i)
		if p.top+i == p.sel {
			P.Rect(x0-cw/2, y, w+cw, ch*2, th.Accent, 0.14)
			P.Rect(x0-cw/2, y, 2*ctx.Px, ch*2, th.Accent, 1)
		}
		// Line 1: number, name and topic; the badge on the right.
		icon, badge, bc := t.progBadge(th, ctx.Now)
		badge = gfx.Truncate(badge, cols/2)
		bw := int(badgeWidth(f, icon, badge)/cw) + 1
		// An agent in the garage or in a hidden workspace has no number: its line
		// 2 starts with "garage" or "ws n".
		name := itoa(slices.Index(a.panes, Pane(t))+1) + " " + displayName(t)
		if isParked(t) {
			name = "· " + displayName(t)
		}
		if title := cleanTitle(t.prog.Title); title != "" {
			name += " · " + title
		} else if title := cleanTitle(t.t.Title()); title != "" && !strings.EqualFold(title, "claude code") {
			name += " · " + title
		}
		P.Text(f, x0, y, gfx.Truncate(name, cols-bw-2), th.FG, 1)
		drawBadge(P, f, x0+float32(cols-bw)*cw, y, icon, badge, bc)
		// Line 2: directory, branch, changes.
		y += ch
		branch := gfx.Truncate(t.git.branch, 24)
		room := cols - 26 - gfx.TextCells(branch)
		tag := ""
		switch {
		case isAway(t):
			tag = "ws " + itoa(t.away)
		case isParked(t):
			tag = "garage"
		}
		if tag != "" {
			room -= gfx.TextCells(tag) + 2
		}
		where := compactPath(shortPath(t.workDir()), max(8, room))
		x := x0 + cw*2
		if tag != "" {
			x += P.Text(f, x, y, tag, th.Accent2, 1) + cw*2
		}
		x += P.Text(f, x, y, where, th.Dim, 1)
		if branch != "" {
			x += cw * 2
			x += P.Text(f, x, y, branch, th.Accent2, 0.8)
		}
		for _, s := range t.git.statParts(th) {
			x += cw
			x += P.Text(f, x, y, s.text, s.c, 1)
		}
		if t.git.files > 0 {
			P.Text(f, x+cw, y, plural(t.git.files, "file"), th.Dim, 1)
		}
	}
}

// toggleAgents opens the agent board at the top right. Already open, the
// same shortcut gives it the focus, then closes it.
func (a *App) toggleAgents() {
	for i, p := range a.panes {
		if _, ok := p.(*AgentsPane); ok {
			if i == a.focus {
				a.closePane(i)
			} else {
				a.goTo(p) // in the garage, it comes out
			}
			return
		}
	}
	n := 0
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.hasProg {
			n++
		}
	}
	b := NewAgentsPane(a.newID())
	switch {
	case a.countPanes(true) < maxFloats:
		w, h := a.cellsToGeom(64, 2+3*max(n, 2))
		b.floating = true
		b.geom = floatGeom{X: 1, Y: 0, W: w, H: h}
	case a.countPanes(false) >= a.cfg.MaxPanes:
		a.toast("agents: close a pane first", a.theme.Warn)
		return
	}
	a.addPane(b)
}

// newAgent starts Claude Code on a new worktree (claude --worktree), in a
// tile named after it. When Claude exits, a shell stays in the worktree if
// it still exists. Claude refuses a directory that was never trusted: its
// message stays on screen above the shell.
func (a *App) newAgent(name, prompt, dir string) {
	if !validWorktreeName(name) {
		a.toast("invalid worktree name \""+name+"\" (letters, digits, . _ -)", a.theme.Warn)
		return
	}
	// The repository root is looked up in the background: a slow disk does
	// not freeze the screen.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		out, err := gitCmd(ctx, dir, "rev-parse", "--show-toplevel")
		cancel()
		a.post(func() {
			if err != nil {
				a.toast("no git repository here: "+shortPath(dir), a.theme.Warn)
				return
			}
			a.startAgent(name, prompt, strings.TrimSpace(string(out)))
		})
	}()
}

// startAgent starts the agent in a new tile. When the tiles are full, the
// focused tile goes to the garage (addPane).
func (a *App) startAgent(name, prompt, root string) {
	if !a.roomForPane() {
		return
	}
	sh := shell.DefaultShell()
	line := "claude --worktree " + shellQuote(name)
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		line += " " + shellQuote(prompt)
	}
	// A child shell rather than exec: restarting zsh under the same PID
	// upsets gitstatus (powerlevel10k), and Claude's message must stay
	// readable. The shell path is hardcoded: fish does not know ${SHELL:-…}.
	line += "; cd " + shellQuote(".claude/worktrees/"+name) + " 2>/dev/null; " + shellQuote(sh) + " -l"
	argv := []string{sh, "-l", "-i", "-c", line}
	t := NewTermPane(a.newID(), argv, root, a.paneEnv())
	t.cmdName = "claude"
	t.name = name
	a.addPane(t)
	a.toast("agent \""+name+"\" on a new worktree", a.theme.Accent2)
}

// validWorktreeName accepts a name that also makes a valid git branch
// (worktree-<name>): no "..", no trailing ".lock", no trailing dot.
func validWorktreeName(s string) bool {
	if s == "" || len(s) > 64 || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "-") ||
		strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// promptNewAgent asks for the worktree name (then the prompt, optional) in
// the palette.
func (a *App) promptNewAgent() {
	dir := a.paneDir(a.focused())
	a.openPalette("NEW AGENT", nil)
	a.palette.status = "worktree name, then the prompt (optional)   enter start   esc cancel"
	a.palette.input = func(s string) {
		name, prompt, _ := strings.Cut(strings.TrimSpace(s), " ")
		a.newAgent(name, prompt, dir)
	}
}

// agentRequest serves `neonmux agent <name> [prompt…]`.
func (a *App) agentRequest(args []string, cwd string) {
	if len(args) == 0 {
		a.toast("neonmux agent: the worktree name is missing", a.theme.Warn)
		return
	}
	a.newAgent(args[0], strings.Join(args[1:], " "), cwd)
}
