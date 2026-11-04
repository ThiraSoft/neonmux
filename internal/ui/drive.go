package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
)

// Driving the window from a script or an agent: what the window shows
// (state, shot), what it does (action, key) and where the focus goes
// (focus). These commands only exist on the window's own socket, which
// answers them: the daemon relays requests to the window one way and could
// not carry the reply. The CLI finds that socket by itself (WindowSocket).
// skills/neonmux/SKILL.md teaches these commands to agents: keep it in step.

// driveCmds are the commands answered by drive.
var driveCmds = map[string]bool{"state": true, "actions": true, "action": true, "key": true, "focus": true, "shot": true}

// IsDriveCmd reports a command served by the window socket alone.
func IsDriveCmd(cmd string) bool { return driveCmds[cmd] }

// driveTimeout bounds a key sequence, waits included.
var driveTimeout = 60 * time.Second

// driveStep separates the keys of neonmux key: enough for a frame to go by
// between two of them, far quicker than the 150 ms of -script.
const driveStep = 30 * time.Millisecond

// drive serves a drive command on the UI thread. reply is called once,
// possibly from a later frame (key, shot), with the lines of the answer:
// "ok" or "error: …" last.
func (a *App) drive(r Request, reply func([]string)) {
	fail := func(f string, args ...any) { reply([]string{"error: " + fmt.Sprintf(f, args...)}) }
	switch r.Cmd {
	case "state":
		b, _ := json.Marshal(a.stateOf(time.Now()))
		reply([]string{string(b), "ok"})
	case "actions":
		var out []string
		for _, c := range a.knownActions() {
			out = append(out, c.action+"\t"+a.hintFor(c.action)+"\t"+c.label)
		}
		reply(append(out, "ok"))
	case "action":
		if len(r.Args) == 0 {
			fail("which action? (neonmux actions lists them)")
			return
		}
		for _, act := range r.Args {
			if !a.isAction(act) {
				fail("unknown action %q (neonmux actions lists them)", act)
				return
			}
		}
		for _, act := range r.Args {
			a.do(act)
		}
		reply([]string{"ok"})
	case "focus":
		p, err := a.findPane(strings.Join(r.Args, " "))
		if err != nil {
			fail("%v", err)
			return
		}
		a.goTo(p)
		a.needRedraw = true
		reply([]string{"ok"})
	case "key":
		for _, tok := range r.Args {
			if err := a.checkToken(tok); err != nil {
				fail("%v", err)
				return
			}
		}
		if len(r.Args) == 0 {
			fail("which keys? (mod+c, ctrl+shift+v, text:hello, wait:1s…)")
			return
		}
		a.script = append(a.script, r.Args...)
		a.scriptStep = driveStep
		a.scriptWaiters = append(a.scriptWaiters, func() { reply([]string{"ok"}) })
	case "shot":
		a.shotReq(r, reply)
	default:
		fail("unknown command %q", r.Cmd)
	}
}

// checkToken validates a token of neonmux key before anything runs: a typo
// halfway through must not leave half a sequence typed.
func (a *App) checkToken(tok string) error {
	switch {
	case strings.HasPrefix(tok, "text:"):
		return nil
	case strings.HasPrefix(tok, "wait:"):
		if _, err := time.ParseDuration(strings.TrimPrefix(tok, "wait:")); err != nil {
			return fmt.Errorf("bad wait %q (wait:500ms, wait:2s)", tok)
		}
		return nil
	}
	c, err := ParseCombo(tok, a.mod)
	if err != nil {
		return fmt.Errorf("bad key %q: %v", tok, err)
	}
	if !knownKeyName(c.Name) {
		return fmt.Errorf("bad key %q: unknown key %q (enter, tab, escape, up, f5, pageup, a…)", tok, c.Name)
	}
	return nil
}

// knownKeyName reports a key name the window would produce itself: one
// character, or the name of a key (enter, f5, pageup, kp_enter…).
func knownKeyName(name string) bool {
	if utf8.RuneCountInString(name) == 1 {
		return true
	}
	for k := ebiten.Key(0); k <= ebiten.KeyMax; k++ {
		if name == strings.ToLower(strings.TrimPrefix(k.String(), "Key")) {
			return true
		}
	}
	for _, n := range specialNames {
		if n == name {
			return true
		}
	}
	for _, n := range keypadNames {
		if n == name {
			return true
		}
	}
	return regexp.MustCompile(`^f([1-9]|1[0-2])$`).MatchString(name)
}

// scriptIdle answers the neonmux key requests once the sequence is over.
// Called by runScript once the last key has had its step.
func (a *App) scriptIdle() {
	ws := a.scriptWaiters
	a.scriptWaiters = nil
	a.scriptStep = 0
	for _, w := range ws {
		w()
	}
}

// knownActions lists every action, the palette's first, then the shortcuts
// that have no entry in it (workspaces, focus, swaps), without duplicates.
func (a *App) knownActions() []command {
	var out []command
	seen := map[string]bool{}
	add := func(c command) {
		if c.action != "" && !seen[c.action] {
			seen[c.action] = true
			out = append(out, c)
		}
	}
	for _, c := range a.commands() {
		add(c)
	}
	for _, b := range defaultBindings {
		add(command{label: "(shortcut)", action: b[1]})
	}
	for _, b := range leaderExtras {
		add(command{label: "(shortcut)", action: b[1]})
	}
	for _, b := range linuxExtras {
		add(command{label: "(shortcut)", action: b[1]})
	}
	return out
}

// isAction reports an action that do understands: a listed one, or one
// with an argument (font.set:<path>).
func (a *App) isAction(act string) bool {
	if strings.HasPrefix(act, "font.set:") {
		return true
	}
	return slices.ContainsFunc(a.knownActions(), func(c command) bool { return c.action == act })
}

// findPane finds a pane of any kind by its number in the bar, its key,
// its name or title, or its foreground program. Empty: the focused pane.
func (a *App) findPane(id string) (Pane, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		if p := a.focused(); p != nil {
			return p, nil
		}
		return nil, fmt.Errorf("no pane has the focus")
	}
	var found []Pane
	for i, p := range a.panes {
		b := p.Base()
		t, isTerm := p.(*TermPane)
		if isTerm && t.key == id || !b.parked && strconv.Itoa(i+1) == id || "#"+strconv.Itoa(b.id) == id {
			return p, nil
		}
		if b.name == id || p.Title() == id || isTerm && t.program() == id {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no pane %q (neonmux state lists them)", id)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("several panes match %q: use its number or its key", id)
}

// program returns the terminal's foreground program, without the login
// dash.
func (t *TermPane) program() string {
	if t.s == nil {
		return ""
	}
	return strings.TrimPrefix(t.s.ForegroundName(), "-")
}

// driveState is what neonmux state prints, as JSON.
type driveState struct {
	Version   string      `json:"version,omitempty"`
	OS        string      `json:"os"`
	Window    driveWindow `json:"window"`
	Workspace int         `json:"workspace"`
	Layout    string      `json:"layout"`
	Zoomed    bool        `json:"zoomed,omitempty"`
	Mode      string      `json:"mode,omitempty"`
	Mod       string      `json:"mod"`
	Leader    string      `json:"leader"`
	Theme     string      `json:"theme"`
	Font      string      `json:"font"`
	FontSize  float64     `json:"font_size"`
	Zen       bool        `json:"zen,omitempty"`
	Braille   bool        `json:"braille,omitempty"`
	Palette   *drivePal   `json:"palette,omitempty"`
	Renaming  *string     `json:"renaming,omitempty"`
	Explorer  string      `json:"explorer,omitempty"`
	Toasts    []string    `json:"toasts,omitempty"`
	Panes     []drivePane `json:"panes"`
}

type driveWindow struct {
	W       int     `json:"w"`
	H       int     `json:"h"`
	Scale   float64 `json:"scale"`
	Focused bool    `json:"focused"`
}

type drivePal struct {
	Title    string   `json:"title"`
	Query    string   `json:"query"`
	Selected string   `json:"selected,omitempty"`
	Items    []string `json:"items,omitempty"`
}

type drivePane struct {
	N         int            `json:"n,omitempty"` // number in the bar, absent in the garage
	ID        string         `json:"id"`          // "#<id>": always unique
	Key       string         `json:"key,omitempty"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Name      string         `json:"name,omitempty"`
	Program   string         `json:"program,omitempty"`
	Dir       string         `json:"dir,omitempty"`
	Info      string         `json:"info,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	Focused   bool           `json:"focused,omitempty"`
	Floating  bool           `json:"floating,omitempty"`
	Garage    bool           `json:"garage,omitempty"`
	Workspace int            `json:"workspace"`
	Hidden    bool           `json:"hidden,omitempty"`
	Notify    string         `json:"notify,omitempty"`
	Rect      *[4]int        `json:"rect,omitempty"` // x, y, w, h in window pixels
	Cells     *[2]int        `json:"cells,omitempty"`
	Cursor    *[2]int        `json:"cursor,omitempty"` // column, row
	Extra     map[string]any `json:"extra,omitempty"`
}

func (a *App) stateOf(now time.Time) driveState {
	s := driveState{
		Version: a.opts.Version, OS: goos,
		Window:    driveWindow{W: a.w, H: a.h, Scale: a.dsf, Focused: a.wasFocused},
		Workspace: a.curWS(), Layout: a.layout, Zoomed: a.zoomed, Mode: a.mode,
		Mod: Combo{Mods: a.mod}.String(), Leader: a.leader.String(),
		Font: a.cfg.Font, FontSize: a.cfg.FontSize, Zen: a.zen, Braille: a.braille,
		Panes: []drivePane{},
	}
	s.Mod = strings.TrimSuffix(s.Mod, "+")
	if a.theme != nil {
		s.Theme = a.theme.Name
	}
	if p := a.palette; p != nil {
		dp := &drivePal{Title: p.title, Query: string(p.query)}
		for i, it := range p.items {
			if i == p.sel {
				dp.Selected = it.cmd.label
			}
			if i < 20 {
				dp.Items = append(dp.Items, it.cmd.label)
			}
		}
		s.Palette = dp
	}
	if a.renaming != nil {
		r := string(a.renameBuf)
		s.Renaming = &r
	}
	if a.explorer != nil {
		s.Explorer = a.explorer.dir
	}
	for _, t := range a.toasts {
		if now.Sub(t.at) < toastLife {
			s.Toasts = append(s.Toasts, t.text)
		}
	}
	focused := a.focused()
	for i, p := range a.panes {
		b := p.Base()
		dp := drivePane{
			ID: "#" + strconv.Itoa(b.id), Kind: p.Kind(), Title: p.Title(), Name: b.name,
			Info: p.Info(), Mode: p.Mode(), Focused: p == focused, Floating: b.floating,
			Garage: inGarage(p), Workspace: b.away, Hidden: b.hidden, Notify: b.notify,
		}
		if !b.parked {
			dp.N = i + 1
			dp.Workspace = a.curWS()
		}
		if st, ok := p.(subtitled); ok {
			dp.Dir = st.Subtitle()
		}
		if !b.hidden && !b.parked {
			r := b.target // where the pane ends up, animation or not
			dp.Rect = &[4]int{r.Min.X, r.Min.Y, r.Dx(), r.Dy()}
		}
		if t, ok := p.(*TermPane); ok {
			dp.Key, dp.Program = t.key, t.program()
			if dp.Dir == "" && t.s != nil {
				dp.Dir = t.s.Cwd()
			}
			dp.Cells = &[2]int{t.cols, t.rows}
			if t.t != nil {
				x, y, _, _ := t.t.Cursor()
				dp.Cursor = &[2]int{x, y}
			}
		}
		s.Panes = append(s.Panes, dp)
	}
	return s
}

// shotReq takes a PNG of the window at the next frame: the whole window,
// or one pane (-p). The path comes from the CLI (absolute already), a
// temporary file otherwise; the reply gives it.
func (a *App) shotReq(r Request, reply func([]string)) {
	var path, pane string
	for i := 0; i < len(r.Args); i++ {
		switch arg := r.Args[i]; {
		case arg == "-p" && i+1 < len(r.Args):
			i++
			pane = r.Args[i]
		case path == "":
			path = arg
		default:
			reply([]string{"error: usage: neonmux shot [-p pane] [file.png]"})
			return
		}
	}
	var crop image.Rectangle
	if pane != "" {
		p, err := a.findPane(pane)
		if err != nil {
			reply([]string{"error: " + err.Error()})
			return
		}
		if b := p.Base(); b.hidden || b.parked {
			reply([]string{"error: that pane is not on screen (neonmux focus brings it)"})
			return
		}
		crop = p.Base().target
	}
	if path == "" {
		// Private and unpredictable: the screen may show secrets, and
		// os.TempDir() is shared on Linux.
		f, err := os.CreateTemp("", "neonmux-shot-*.png")
		if err != nil {
			reply([]string{"error: " + err.Error()})
			return
		}
		path = f.Name()
		f.Close()
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}
	a.shots = append(a.shots, shotJob{path: path, crop: crop, reply: reply})
	a.needRedraw = true
}

// shotJob is a screenshot waiting for the next frame.
type shotJob struct {
	path  string
	crop  image.Rectangle
	reply func([]string)
}

// readScreen reads the pixels of the frame just drawn (replaced by the
// tests: ebiten refuses ReadPixels before its loop).
var readScreen = func(screen interface{ ReadPixels([]byte) }, b image.Rectangle) *image.RGBA {
	img := image.NewRGBA(b)
	screen.ReadPixels(img.Pix)
	return img
}

// takeShots serves the pending screenshots with the frame just drawn. The
// PNG is encoded off the UI thread.
func (a *App) takeShots(screen interface {
	ReadPixels([]byte)
	Bounds() image.Rectangle
}) {
	if len(a.shots) == 0 {
		return
	}
	jobs := a.shots
	a.shots = nil
	img := readScreen(screen, screen.Bounds())
	for _, j := range jobs {
		var sub image.Image = img
		if !j.crop.Empty() {
			sub = img.SubImage(j.crop.Intersect(img.Bounds()))
		}
		go func(j shotJob, sub image.Image) {
			// 0600: what is on screen stays the user's.
			f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err == nil {
				err = png.Encode(f, sub)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
			if err != nil {
				j.reply([]string{"error: " + err.Error()})
				return
			}
			b := sub.Bounds()
			j.reply([]string{fmt.Sprintf("%s\t%dx%d", j.path, b.Dx(), b.Dy()), "ok"})
		}(j, sub)
	}
}
