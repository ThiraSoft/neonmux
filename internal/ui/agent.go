package ui

import (
	"fmt"
	"image/color"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// Agent state: every terminal follows the status its program announces
// through the Program Status Protocol (OSC 7501, see vt/progstatus.go).
// Claude Code sends it on its own: ready, working (with what it is doing),
// blocked on a permission, done. No setup is needed.

// trackProg reads the program status again and reacts to state changes.
// It returns true if the display must change. A change is spotted through
// Gen, identical in the daemon and the window: reattaching to the daemon
// therefore does not pass for a new transition.
func (p *TermPane) trackProg(ctx *Ctx) bool {
	st, ok := p.t.ProgramStatus()
	old, had := p.prog, p.hasProg
	p.prog, p.hasProg = st, ok
	if ok != had || st.State != old.State || st.Gen != old.Gen {
		ctx.App.progChanged(p, old, had)
		return true
	}
	return st.Msg != old.Msg || st.Progress != old.Progress || st.Title != old.Title
}

// progChanged reacts to a state change of an agent: a notification when it
// gets blocked, a toast when it finishes out of sight. An agent in the
// garage rings like the others.
func (a *App) progChanged(p *TermPane, old vt.ProgStatus, had bool) {
	b := p.Base()
	st := p.prog
	if had && old.State == vt.PSBlocked && (!p.hasProg || st.State != vt.PSBlocked) {
		b.notify = "" // the wait is over
	}
	a.needRedraw = true
	if !p.hasProg {
		// No agent anymore: its change counter goes away with it.
		p.progSeen = false
		p.git = gitInfo{}
		return
	}
	// In zen mode only the background shows: nothing is "watched".
	watched := !a.zen && a.focused() == Pane(p) && ebiten.IsFocused()
	// A state older than the window start (reattach) does not trigger a
	// toast: it would already be stale.
	fresh := st.Since.After(a.start)
	switch st.State {
	case vt.PSBlocked:
		switch msg := displayName(p) + " · " + blockedLabel(st); {
		case watched:
		case fresh:
			a.notifyPane(p, "", msg)
		default:
			b.notify = msg // the border pulses, no toast for an old wait
		}
	case vt.PSDone, vt.PSError:
		// Already seen before a window restart (layout.json)?
		p.progSeen = watched || p.seenGen != 0 && st.Gen == p.seenGen
		msg, c := "done", a.theme.Accent2
		if st.State == vt.PSError {
			msg, c = "failed", a.theme.Warn
		}
		// On screen, the border flashes: the eye finds which agent finished.
		if fresh && !b.hidden && !a.zen && a.cfg.AgentFlash {
			b.flashAt, b.flashC = time.Now(), c
			b.flashN, b.flashEach = a.cfg.AgentFlashCount, time.Duration(a.cfg.AgentFlashDuration*float64(time.Second))
		}
		if !p.progSeen && fresh {
			a.toast(a.paneTag(p)+" "+displayName(p)+": "+msg, c)
		}
	}
	a.refreshGit(p)
}

func blockedLabel(st vt.ProgStatus) string {
	k := map[string]string{"permission": "permission", "question": "question", "auth": "login"}[st.Kind]
	if k == "" {
		k = "waiting"
	}
	if st.Msg != "" {
		return k + ": " + st.Msg
	}
	return k
}

// progBadge returns the header badge of an agent: its state icon (drawn as
// pixel art, sharp in every font), its text and its color. Empty text: no
// agent.
func (p *TermPane) progBadge(th *theme.Theme, now time.Time) (icon, text string, c color.RGBA) {
	if !p.hasProg {
		return "", "", color.RGBA{}
	}
	st := p.prog
	d := fmtDur(now.Sub(st.Since))
	icon, c = p.progIcon(th)
	switch st.State {
	case vt.PSWorking:
		s := firstOf(st.Msg, st.Title, "working")
		if st.Progress >= 0 {
			s += " " + itoa(st.Progress) + "%"
		}
		return icon, s + " · " + d, c
	case vt.PSBlocked:
		return icon, blockedLabel(st) + " · " + d, c
	case vt.PSDone:
		return icon, firstOf(st.Msg, "done") + " · " + d, c
	case vt.PSError:
		return icon, firstOf(st.Msg, "error") + " · " + d, c
	}
	return icon, "ready", c
}

// progIcon returns the state icon of an agent and its color.
func (p *TermPane) progIcon(th *theme.Theme) (string, color.RGBA) {
	if !p.hasProg {
		return "", color.RGBA{}
	}
	c := th.Dim
	switch p.prog.State {
	case vt.PSWorking:
		c = th.Accent2
	case vt.PSBlocked, vt.PSError:
		c = th.Warn
	case vt.PSDone:
		if !p.progSeen {
			c = th.FG
		}
	}
	return stateIcons[p.prog.State], c
}

// barIcon returns the agent icon in the bottom bar: nothing when it is
// ready, nor when it already has the ● of a notification.
func (p *TermPane) barIcon(th *theme.Theme) (string, color.RGBA) {
	if !p.hasProg || p.notify != "" || p.prog.State == vt.PSIdle {
		return "", color.RGBA{}
	}
	return p.progIcon(th)
}

// stateIcons maps each state to its pixel art icon (explorer.go).
var stateIcons = map[vt.ProgState]string{
	vt.PSWorking: "st-working", vt.PSBlocked: "st-blocked", vt.PSDone: "st-done",
	vt.PSError: "st-error", vt.PSIdle: "st-idle",
}

// badgeIcon returns the icon of the header badge.
func (p *TermPane) badgeIcon() string {
	if !p.hasProg {
		return ""
	}
	return stateIcons[p.prog.State]
}

// iconSize returns the scale of the pixel art icons for a font.
func iconSize(f *gfx.Font) float32 { return float32(max(1, f.CellH/9)) }

// badgeWidth returns the width of a badge (icon included).
func badgeWidth(f *gfx.Font, icon, text string) float32 {
	w := float32(gfx.TextWidth(f, text))
	if icon != "" {
		w += 6*iconSize(f) + float32(f.CellW)
	}
	return w
}

// drawBadge draws a badge starting at x and returns its width.
func drawBadge(P *gfx.Painter, f *gfx.Font, x, y float32, icon, text string, c color.RGBA) float32 {
	x0 := x
	if icon != "" {
		s := iconSize(f)
		drawIcon(P, icon, x, y+(float32(f.CellH)-6*s)/2, s, c)
		x += 6*s + float32(f.CellW)
	}
	return x - x0 + P.Text(f, x, y, text, c, 1)
}

// progWake returns the next moment the badge changes (its duration).
func (p *TermPane) progWake(now time.Time) time.Time {
	if !p.hasProg || p.prog.State == vt.PSIdle {
		return time.Time{}
	}
	return durWake(p.prog.Since, now)
}

// needsYou reports an agent that needs the user, and its rank:
// 0 blocked, 2 done or failed and not seen yet (1: notifications).
func (p *TermPane) needsYou() (int, bool) {
	if !p.hasProg {
		return 0, false
	}
	switch p.prog.State {
	case vt.PSBlocked:
		return 0, true
	case vt.PSDone, vt.PSError:
		return 2, !p.progSeen
	}
	return 0, false
}

// pulsing reports a pane whose border pulses: a notification, or an agent
// asking a question. The question keeps pulsing under the focus, until it
// gets an answer.
func pulsing(p Pane) bool {
	if p.Base().notify != "" {
		return true
	}
	t, ok := p.(*TermPane)
	return ok && t.hasProg && t.prog.State == vt.PSBlocked && t.prog.Kind == "question"
}

// markSeen records that a finished agent was seen.
func (p *TermPane) markSeen() {
	if p.hasProg && (p.prog.State == vt.PSDone || p.prog.State == vt.PSError) && !p.progSeen {
		p.progSeen = true
		p.dirty = true
	}
}

// attention returns the panes that need the user, from most to least
// urgent: blocked agents (oldest first), notifications, then finished agents
// not seen yet (oldest first).
func (a *App) attention() []Pane {
	type cand struct {
		p     Pane
		rank  int
		since time.Time
	}
	var cs []cand
	for _, p := range a.panes {
		t, isTerm := p.(*TermPane)
		if isTerm {
			if rank, ok := t.needsYou(); ok {
				cs = append(cs, cand{p, rank, t.prog.Since})
				continue
			}
		}
		if p.Base().notify != "" {
			cs = append(cs, cand{p, 1, time.Time{}})
		}
	}
	slices.SortStableFunc(cs, func(x, y cand) int {
		if x.rank != y.rank {
			return x.rank - y.rank
		}
		return x.since.Compare(y.since)
	})
	out := make([]Pane, len(cs))
	for i, c := range cs {
		out[i] = c.p
	}
	return out
}

// nextAttention goes to the next pane that needs the user. A pane in the
// garage comes out in the center, without touching the layout. The list is
// walked from the focused pane: a blocked agent stays in it until it gets
// an answer, and starting over from the top would make mod+n bounce
// between the first two.
func (a *App) nextAttention() {
	list := a.attention()
	cur := slices.Index(list, a.focused())
	for k := 1; k <= len(list); k++ {
		if p := list[(cur+k)%len(list)]; p != a.focused() {
			a.goTo(p)
			return
		}
	}
	a.toast("no agent or pane is waiting", a.theme.Dim)
}

// focusBack sends the pane that came out of the garage back into it, and the
// focus returns where it was. Otherwise, back to the pane that had the focus
// just before.
func (a *App) focusBack() {
	if p := a.focused(); p != nil && p == a.visitor {
		a.sendBack()
		return
	}
	if slices.Contains(a.panes, a.prevFocus) {
		a.goTo(a.prevFocus)
	}
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// fmtDur writes a short duration: "12s", "4m", "1h05m".
func fmtDur(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// durWake returns the next moment fmtDur(now-since) changes.
func durWake(since, now time.Time) time.Time {
	step := time.Second
	if now.Sub(since) >= time.Minute {
		step = time.Minute
	}
	return since.Add((now.Sub(since)/step + 1) * step)
}

// cleanTitle removes the activity symbol some programs put in front of
// their title ("✳ Claude Code", "⠂ task"): a dingbat, a braille pattern or
// a middle dot, followed by a space.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	r, size := utf8.DecodeRuneInString(s)
	spinner := r >= 0x2700 && r <= 0x27bf || r >= 0x2800 && r <= 0x28ff || r == '·'
	if spinner && strings.HasPrefix(s[size:], " ") {
		return strings.TrimSpace(s[size:])
	}
	return s
}

// workDir returns the directory a terminal works in: the foreground
// program's for an agent (Claude Code moves into its worktree while the
// shell stays put), otherwise the shell's.
func (p *TermPane) workDir() string {
	if p.hasProg && p.fgCwd != "" {
		return p.fgCwd
	}
	return firstOf(p.cwd, p.dir)
}

// flashLevel returns the brightness of the end-of-work flash, d after it
// started: n flashes lasting each, then nothing.
func flashLevel(d time.Duration, n int, each time.Duration) float32 {
	if d < 0 || d >= time.Duration(n)*each {
		return 0
	}
	s := math.Sin(math.Pi * float64(d) / float64(each))
	return float32(s * s)
}
