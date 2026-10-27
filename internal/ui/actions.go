package ui

import (
	"crypto/sha256"
	"os/exec"
	"syscall"

	"github.com/hajimehoshi/ebiten/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Default shortcuts. "mod" is cmd on macOS and alt elsewhere.
// Every shortcut also exists behind the leader (ctrl+space, then the key).
var defaultBindings = [][2]string{
	{"mod+enter", "term.new"},
	{"mod+e", "files.open"},
	{"mod+w", "pane.close"},
	{"mod+h", "focus.left"}, {"mod+j", "focus.down"}, {"mod+k", "focus.up"}, {"mod+l", "focus.right"},
	{"mod+shift+h", "swap.left"}, {"mod+shift+j", "swap.down"}, {"mod+shift+k", "swap.up"}, {"mod+shift+l", "swap.right"},
	// Workspaces: physical keys of the top row, and the keypad (keyName).
	{"mod+1", "ws.1"}, {"mod+2", "ws.2"}, {"mod+3", "ws.3"}, {"mod+4", "ws.4"},
	{"mod+5", "ws.5"}, {"mod+6", "ws.6"}, {"mod+7", "ws.7"}, {"mod+8", "ws.8"}, {"mod+9", "ws.9"},
	{"mod+shift+1", "ws.move.1"}, {"mod+shift+2", "ws.move.2"}, {"mod+shift+3", "ws.move.3"},
	{"mod+shift+4", "ws.move.4"}, {"mod+shift+5", "ws.move.5"}, {"mod+shift+6", "ws.move.6"},
	{"mod+shift+7", "ws.move.7"}, {"mod+shift+8", "ws.move.8"}, {"mod+shift+9", "ws.move.9"},
	{"mod+f", "pane.zoom"},
	{"mod+shift+f", "pane.float"},
	{"mod+a", "focus.layer"},
	{"mod+i", "help"},
	{"mod+shift+r", "pane.rename"},
	{"mod+g", "layout.next"},
	{"mod+r", "mode.resize"},
	{"mod+t", "theme.next"}, {"mod+shift+t", "theme.prev"},
	{"mod+p", "palette"}, {"mod+shift+p", "palette"},
	{"mod+c", "copy"}, {"mod+v", "paste"},
	{"mod+=", "font.bigger"}, {"mod++", "font.bigger"}, {"mod+shift+=", "font.bigger"},
	{"mod+-", "font.smaller"}, {"mod+0", "font.reset"},
	{"mod+u", "mode.copy"},
	{"mod+n", "notify.next"},
	{"mod+m", "pane.park"},
	{"mod+shift+n", "focus.back"},
	{"mod+d", "agents.pick"},
	{"mod+shift+d", "agents.board"},
	{"mod+shift+g", "diff.open"},
	{"mod+z", "view.zen"},
	{"mod+y", "view.braille"},
	// Fullscreen: ctrl+cmd+f like macOS apps, f11 like on Linux
	// (where mod+ctrl+f collides with zoom, mod+f).
	{"mod+ctrl+f", "window.fullscreen"},
	{"f11", "window.fullscreen"},
	{"mod+.", "hud.toggle"},
	{"mod+b", "browser.open"},
	{"mod+shift+b", "bg.next"},
	{"mod+,", "config.open"},
	{"mod+o", "font.pick"},
	{"mod+q", "quit"},
}

// Leader-only shortcuts (keys or characters). They must not shadow the
// ones that come from mod: leader b used to close the pane instead of
// doing what mod+b does. Only "," renames, tmux style.
var leaderExtras = [][2]string{
	{"leader+[", "mode.copy"},
	{"leader+:", "palette"},
	{"leader+?", "help"}, // like tmux
	{"leader+,", "pane.rename"},
}

func (a *App) buildBindings() {
	a.bindings = map[Combo]string{}
	for _, b := range defaultBindings {
		c, err := ParseCombo(b[0], a.mod)
		if err != nil {
			continue
		}
		// When mod already holds shift (ctrl+shift in the config), mod+shift+h gives the same
		// combination as mod+h. The first one in the list wins, and the
		// other stays reachable through the leader (shift+h).
		if _, taken := a.bindings[c]; !taken {
			a.bindings[c] = b[1]
		}
		// Same action behind the leader, without the modifier.
		name := "leader+" + strings.TrimPrefix(b[0], "mod+")
		a.bindings[Combo{Name: name}] = b[1]
	}
	for _, b := range leaderExtras {
		a.bindings[Combo{Name: b[0]}] = b[1]
	}
	for k, v := range a.cfg.Bindings {
		if c, err := ParseCombo(k, a.mod); err == nil {
			a.bindings[c] = v
		}
	}
	if c, err := ParseCombo(a.cfg.Leader, a.mod); err == nil {
		a.leader = c
	}
}

// hintFor returns the main shortcut of an action (shown in the palette):
// the first one that still leads to it.
func (a *App) hintFor(action string) string {
	first := ""
	for _, b := range defaultBindings {
		if b[1] != action {
			continue
		}
		c, err := ParseCombo(b[0], a.mod)
		if err != nil {
			continue
		}
		if a.bindings[c] == action {
			return c.String()
		}
		if first == "" {
			first = b[0]
		}
	}
	if first == "" {
		return ""
	}
	// Combination taken by another action (mod set to ctrl+shift in the
	// config): the leader still leads to this one.
	return a.leader.String() + " " + strings.TrimPrefix(first, "mod+")
}

func (a *App) cwd() string {
	if t := a.focusedTerm(); t != nil && t.s != nil {
		if d := t.s.Cwd(); d != "" {
			return d
		}
	}
	return a.opts.StartDir
}

func (a *App) do(action string) {
	a.needRedraw = true
	// Any action closes the explorer on the way; its own shortcut only
	// closes it.
	if a.explorer != nil {
		a.closeExplorer()
		if action == "files.open" {
			return
		}
	}
	switch {
	case action == "term.new":
		a.newShellPane(a.cwd(), false)
	case action == "files.open":
		a.openExplorer(a.cwd())
	case action == "pane.close":
		a.closePane(a.focus)
	case action == "focus.layer":
		a.focusLayer()
	case action == "game.typing":
		a.showTyping()
	case action == "game.kata":
		a.showKata()
	case action == "focus.back":
		a.focusBack()
	case strings.HasPrefix(action, "focus.id:"):
		// focus.back and focus.id come before "focus.": otherwise they would
		// be taken for a direction.
		for _, p := range a.panes {
			if itoa(p.Base().id) == strings.TrimPrefix(action, "focus.id:") {
				a.goTo(p)
				break
			}
		}
	case strings.HasPrefix(action, "focus."):
		a.focusDir(strings.TrimPrefix(action, "focus."))
	case strings.HasPrefix(action, "ws.move."):
		if n, err := strconv.Atoi(strings.TrimPrefix(action, "ws.move.")); err == nil {
			a.moveToWorkspace(n)
		}
	case strings.HasPrefix(action, "ws."):
		if n, err := strconv.Atoi(strings.TrimPrefix(action, "ws.")); err == nil {
			a.switchWorkspace(n)
		}
	case strings.HasPrefix(action, "swap."):
		dir := strings.TrimPrefix(action, "swap.")
		// A floating pane is not swapped: it snaps to the edge.
		if p := a.focused(); p != nil && isFloat(p) {
			a.snapFloat(p, dir)
			return
		}
		if i := a.neighborIn(dir, isTile); i >= 0 {
			a.panes[a.focus], a.panes[i] = a.panes[i], a.panes[a.focus]
			a.focus = i
			a.relayout()
		}
	case action == "help":
		a.toggleHelp()
	case action == "browser.open":
		a.promptBrowser(nil)
	case action == "diff.open":
		a.openDiff(a.paneDir(a.focused()))
	case action == "agents.board":
		a.toggleAgents()
	case action == "agents.pick":
		a.openAgentPicker()
	case action == "view.zen":
		a.toggleZen()
	case action == "view.braille":
		a.toggleBraille()
	case action == "agent.new":
		a.promptNewAgent()
	case action == "pane.float":
		a.toggleFloat(a.focused())
	case strings.HasPrefix(action, "float."):
		if x, y, ok := parseCorner(strings.TrimPrefix(action, "float.")); ok {
			a.moveFloat(a.focused(), x, y)
		}
	case action == "pane.rename":
		a.startRename(a.focused())
	case action == "pane.zoom":
		// On a floating pane, zoom enlarges that pane alone; the tiles' zoom stays as it is.
		if p := a.focused(); p != nil && isFloat(p) {
			if a.zoomFloat == p {
				a.zoomFloat = nil
			} else {
				a.zoomFloat = p
			}
		} else {
			a.zoomed = !a.zoomed
		}
		a.relayout()
	case action == "layout.next":
		for i, l := range layouts {
			if l == a.layout {
				a.layout = layouts[(i+1)%len(layouts)]
				break
			}
		}
		a.zoomed = false
		a.relayout()
		a.toast("layout: "+layoutLabels[a.layout], a.theme.Accent2)
	case strings.HasPrefix(action, "layout."):
		a.layout = strings.TrimPrefix(action, "layout.")
		a.zoomed = false
		a.relayout()
	case action == "mode.resize":
		a.mode = "resize"
		if p := a.focused(); p != nil && isFloat(p) {
			a.toast("h j k l size · H J K L move · y u b n corners · c center", a.theme.Accent2)
		}
	case action == "theme.next":
		a.setTheme(theme.Next(a.theme, 1))
	case action == "theme.prev":
		a.setTheme(theme.Next(a.theme, -1))
	case strings.HasPrefix(action, "theme."):
		a.setTheme(theme.Get(strings.TrimPrefix(action, "theme.")))
	case action == "palette":
		a.openPalette("", a.commands())
	case action == "copy":
		if b, ok := a.focused().(*BrowserPane); ok {
			b.selection(func(s string) {
				if s != "" {
					a.copyNotify(s, "copied")
				}
			})
		}
		if t := a.focusedTerm(); t != nil {
			if s := t.SelectedText(); s != "" {
				a.copyNotify(s, "copied")
			}
		}
	case action == "paste":
		if t := a.focusedTerm(); t != nil {
			a.pasteInto(t)
		}
		if b, ok := a.focused().(*BrowserPane); ok {
			pasteText(func(s string) { a.post(func() { b.paste(s) }) }, func(err error) {
				a.post(func() { a.toast("paste failed: "+err.Error(), a.theme.Warn) })
			})
		}
	case action == "font.bigger", action == "font.smaller":
		step := 1.0
		if a.termFont.Pixel {
			// The pixel font only grows by whole factors.
			s := a.termFont.Scale + 1
			if action == "font.smaller" {
				s = max(1, a.termFont.Scale-1)
			}
			a.cfg.FontSize = float64(s) * 7 / a.dsf
		} else {
			if action == "font.smaller" {
				step = -1
			}
			a.cfg.FontSize = min(max(a.cfg.FontSize+step, 6), 48)
		}
		a.loadFonts()
		a.saveConfig()
	case action == "font.reset":
		a.cfg.FontSize = DefaultConfig().FontSize
		a.loadFonts()
		a.saveConfig()
	case action == "font.pick":
		a.openFontPicker()
	case strings.HasPrefix(action, "font.set:"):
		want := strings.TrimPrefix(action, "font.set:")
		a.cfg.Font = want
		a.loadFonts()
		// Font not found: loadFonts has already shown the error and fallen
		// back to the pixel font. Nothing is saved.
		if a.cfg.Font == want {
			cfgFile.font = want
			a.saveConfig()
			a.toast("font: "+fontLabel(a.cfg.Font), a.theme.Accent2)
		}
	case action == "mode.copy":
		if t := a.focusedTerm(); t != nil {
			t.EnterCopyMode()
		}
	case action == "notify.next":
		a.nextAttention()
	case action == "pane.park":
		a.park(a.focused())
	case action == "hud.toggle":
		a.hud = !a.hud
	case action == "bg.next":
		cur := a.bg.Kind
		for i, b := range fx.Backgrounds {
			if b == cur {
				a.cfg.Background = fx.Backgrounds[(i+1)%len(fx.Backgrounds)]
			}
		}
		a.applyBackground()
		a.saveConfig()
		a.toast("background: "+a.bg.Kind, a.theme.Accent2)
	case action == "bg.animate":
		a.cfg.Animate = !a.cfg.Animate
		a.saveConfig()
	case strings.HasPrefix(action, "bg."):
		a.cfg.Background = strings.TrimPrefix(action, "bg.")
		a.applyBackground()
		a.saveConfig()
	case strings.HasPrefix(action, "power."):
		a.cfg.Power = strings.TrimPrefix(action, "power.")
		a.pacer.SetMaxFPS(maxFPSFor(a.cfg.Power))
		a.saveConfig()
		a.toast("power: "+a.cfg.Power, a.theme.Accent2)
	case action == "fx.confetti", action == "fx.glitch":
		if p := a.focused(); p != nil {
			a.runFX(p, strings.TrimPrefix(action, "fx."))
		}
	case action == "fx.typing":
		a.cfg.TypingFX = !a.cfg.TypingFX
		a.saveConfig()
	case action == "battery.toggle":
		a.cfg.Battery = !a.cfg.Battery
		a.saveConfig()
	case action == "config.open":
		a.createConfig()
		a.openInEditor(ConfigPath())
	case action == "quit":
		a.quit = true
	case action == "app.restart":
		a.restartAll()
	case action == "app.force-restart":
		a.forceRestart()
	case action == "quit.kill":
		// Close everything: the daemon ends the terminals, nothing is restored.
		if a.client != nil {
			a.client.KillAll()
			a.client, a.rlink = nil, nil
		}
		removeLayout()
		a.quit = true
	case action == "window.fullscreen":
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
		a.layoutDirty = true
	case strings.HasPrefix(action, "remote."):
		a.remoteAction(strings.TrimPrefix(action, "remote."))
	case action == "window.decorations":
		a.cfg.Decorations = !a.cfg.Decorations
		ebiten.SetWindowDecorated(a.cfg.Decorations)
		a.saveConfig()
	}
}

// saveConfig saves the settings changed from the app. Only the keys that
// differ from the reference config (the file that was read, plus the
// command-line options) are written, on top of the existing file.
func (a *App) saveConfig() {
	// An unknown theme name (one from a newer version, for example) stays
	// as it is: only a real theme change is written.
	if theme.Get(a.cfg.Theme) != a.theme {
		a.cfg.Theme = a.theme.Name
	}
	if cfgFile.broken != nil {
		// The file may have been fixed without the watcher noticing: read
		// again, it becomes the reference once more. Otherwise it keeps the
		// user's settings, which the in-memory config would overwrite.
		a.reloadConfig()
		if err := cfgFile.broken; err != nil {
			a.toast(configErrMsg(err)+": nothing saved", a.theme.Warn)
		}
		return
	}
	if !cfgFile.set {
		SetConfigBase(DefaultConfig())
	}
	cur := a.cfg
	if cur.Font == gfx.FontPixel && cfgFile.font != "" {
		cur.Font = cfgFile.font // loadFonts fallback, not a choice
	}
	data, err := cur.saveChanges(cfgFile.base)
	if err != nil {
		a.toast("config not saved: "+err.Error(), a.theme.Warn)
		return
	}
	cfgFile.base = cur
	if data != nil {
		// Our own write must not trigger a reload.
		a.cfgHash = sha256.Sum256(data)
	}
}

// createConfig writes a complete file (every key, to find your way around)
// if none exists yet: the defaults, plus the changes made in the app,
// without the command-line options. An existing file is never touched,
// even an unreadable one.
func (a *App) createConfig() {
	if theme.Get(a.cfg.Theme) != a.theme {
		a.cfg.Theme = a.theme.Name
	}
	if !cfgFile.set {
		SetConfigBase(DefaultConfig())
	}
	cur := a.cfg
	if cur.Font == gfx.FontPixel && cfgFile.font != "" {
		cur.Font = cfgFile.font
	}
	data, err := createConfigFile(mergeFields(fieldsOf(DefaultConfig()), changedFields(cfgFile.base, cur)))
	switch {
	case err != nil:
		a.toast("config not created: "+err.Error(), a.theme.Warn)
	case data != nil:
		cfgFile.base, cfgFile.broken = cur, nil
		a.cfgHash = sha256.Sum256(data)
	}
}

func (a *App) setTheme(t *theme.Theme) {
	a.applyTheme(t)
	a.cfg.Theme = t.Name
	a.saveConfig()
	a.toast("theme: "+t.Label, t.Accent)
}

// applyTheme switches theme without touching the config file.
func (a *App) applyTheme(t *theme.Theme) {
	a.theme = t
	for _, p := range a.panes {
		if tp, ok := p.(*TermPane); ok {
			tp.applyColors(t)
		}
	}
	a.applyBackground()
	a.sendColors()
}

func (a *App) editor() string {
	if a.cfg.Editor != "" {
		return a.cfg.Editor
	}
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	// Resolved by the login shell at launch: nvim if it exists, otherwise vi.
	return "$(command -v nvim || echo vi)"
}

// openInEditor goes through the login shell: launched from the Finder, the
// app does not have the terminal's PATH (Homebrew, ~/.local/bin…).
func (a *App) openInEditor(path string) {
	cmd := a.editor() + " " + shellQuote(path)
	argv := []string{shell.DefaultShell(), "-l", "-i", "-c", "exec " + cmd}
	a.addPane(NewTermPane(a.newID(), argv, filepath.Dir(path), a.paneEnv()))
}

// openInEditorAt opens path at line line. The pane never replaces a tile,
// which could be an agent at work: when there is no room, it floats.
func (a *App) openInEditorAt(path string, line int) {
	argv := []string{shell.DefaultShell(), "-l", "-i", "-c", "exec " + editorAt(a.editor(), path, line)}
	a.addBeside(NewTermPane(a.newID(), argv, filepath.Dir(path), a.paneEnv()))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// editorAt builds the command that opens path at line line: "+N" for vim,
// nvim, nano or emacs, "file:N" for Zed, Sublime or Helix, "-g file:N" for
// VS Code and its forks.
func editorAt(editor, path string, line int) string {
	if line <= 0 {
		return editor + " " + shellQuote(path)
	}
	fields := strings.Fields(editor)
	name := ""
	if len(fields) > 0 {
		name = filepath.Base(fields[0])
	}
	at := shellQuote(path + ":" + itoa(line))
	switch name {
	case "code", "code-insiders", "codium", "cursor", "windsurf":
		return editor + " -g " + at
	case "zed", "subl", "hx", "helix":
		return editor + " " + at
	}
	return editor + " +" + itoa(line) + " " + shellQuote(path)
}

// OpenPath opens a file in the right kind of pane: image, audio, directory or editor.
func (a *App) OpenPath(path string) {
	st, err := os.Stat(path)
	if err != nil {
		a.toast("not found: "+path, a.theme.Warn)
		return
	}
	switch {
	case st.IsDir():
		a.openExplorer(path)
	case isImage(path):
		a.addPane(NewImagePane(a.newID(), path))
	case isAudio(path):
		a.addPane(NewAudioPane(a.newID(), path))
	default:
		a.openInEditor(path)
	}
}

func fontLabel(name string) string {
	switch name {
	case gfx.FontIosevka:
		return "Iosevka Term (built-in)"
	case gfx.FontPixel:
		return "Pixel 6×13 (built-in)"
	case gfx.FontGoMono:
		return "Go Mono (built-in)"
	}
	return strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
}

func (a *App) openFontPicker() {
	build := func() []command {
		cmds := []command{
			{label: "Iosevka Term (built-in)", action: "font.set:" + gfx.FontIosevka},
			{label: "Pixel 6×13 (built-in)", action: "font.set:" + gfx.FontPixel},
			{label: "Go Mono (built-in)", action: "font.set:" + gfx.FontGoMono},
		}
		for _, f := range a.fontScan {
			cmds = append(cmds, command{label: f.Family, hint: f.Style, action: "font.set:" + f.Path})
		}
		return cmds
	}
	a.openPalette("FONT", build())
	if a.fontScan == nil {
		a.palette.status = "looking for installed monospace fonts…"
		go func() {
			found := scanFonts()
			a.post(func() {
				a.fontScan = found
				if a.palette != nil && a.palette.title == "FONT" {
					a.palette.setItems(build())
					a.palette.status = itoa(len(found)) + " monospace fonts found"
				}
			})
		}()
	}
}

func (a *App) commands() []command {
	c := []command{
		{label: "Help: all shortcuts", action: "help"},
		{label: "New terminal", action: "term.new"},
		{label: "File explorer", action: "files.open"},
		{label: "Close pane", action: "pane.close"},
		{label: "Zoom pane", action: "pane.zoom"},
		{label: "Floating pane ⇄ tile", action: "pane.float"},
		{label: "Focus: tiles ⇄ floating", action: "focus.layer"},
		{label: "Floating: top left corner", action: "float.tl"},
		{label: "Floating: top right corner", action: "float.tr"},
		{label: "Floating: bottom left corner", action: "float.bl"},
		{label: "Floating: bottom right corner", action: "float.br"},
		{label: "Floating: center", action: "float.center"},
		{label: "Rename pane…", action: "pane.rename"},
		{label: "Next layout", action: "layout.next"},
		{label: "Layout: tiles", action: "layout.tiles"},
		{label: "Layout: columns", action: "layout.columns"},
		{label: "Layout: rows", action: "layout.rows"},
		{label: "Layout: monocle", action: "layout.monocle"},
		{label: "Resize mode", action: "mode.resize"},
		{label: "Copy mode (vim-style scrollback)", action: "mode.copy"},
		{label: "Go to the next agent or pane that is waiting", action: "notify.next"},
		{label: "Back to the previous pane (sends the pane brought out back to the garage)", action: "focus.back"},
		{label: "Send pane to the garage (it keeps running)", action: "pane.park"},
		{label: "Repository diff (changes since the last commit)", action: "diff.open"},
		{label: "Browser: open an address or search…", action: "browser.open"},
		{label: "Go to a pane or agent… (garage included, filter by project)", action: "agents.pick"},
		{label: "Agent board", action: "agents.board"},
		{label: "Zen mode: hide all panes", action: "view.zen"},
		{label: "Braille view: show the text in braille", action: "view.braille"},
		{label: "New agent on a worktree…", action: "agent.new"},
		{label: "Game: typing test", action: "game.typing"},
		{label: "Game: vim kata", action: "game.kata"},
		{label: "Choose a font…", action: "font.pick"},
		{label: "Bigger font", action: "font.bigger"},
		{label: "Smaller font", action: "font.smaller"},
		{label: "Default font size", action: "font.reset"},
		{label: "Next background", action: "bg.next"},
		{label: "Background: synthwave grid", action: "bg.grid"},
		{label: "Background: Matrix rain", action: "bg.rain"},
		{label: "Background: stars", action: "bg.stars"},
		{label: "Background: arcane dust", action: "bg.dust"},
		{label: "Background: northern lights", action: "bg.aurora"},
		{label: "Background: contour lines", action: "bg.topo"},
		{label: "Background: lava lamp", action: "bg.lava"},
		{label: "Background: blaze", action: "bg.fire"},
		{label: "Background: game of life", action: "bg.life"},
		{label: "Background: swarm", action: "bg.swarm"},
		{label: "Background: circuit board", action: "bg.circuit"},
		{label: "Background: oscilloscope", action: "bg.scope"},
		{label: "Background: city at night", action: "bg.city"},
		{label: "Background: the theme's own", action: "bg.auto"},
		{label: "Background: none", action: "bg.none"},
		{label: "Animated background on/off", action: "bg.animate"},
		{label: "Power: eco (30 fps, still background)", action: "power.eco"},
		{label: "Power: auto (60 fps, paused when unfocused)", action: "power.balanced"},
		{label: "Power: max (120 fps, always animated)", action: "power.max"},
		{label: "FX: confetti", action: "fx.confetti"},
		{label: "FX: glitch", action: "fx.glitch"},
		{label: "FX: sparks while typing on/off", action: "fx.typing"},
		{label: "Battery in the status bar on/off", action: "battery.toggle"},
		{label: "Performance HUD", action: "hud.toggle"},
		{label: "Open configuration", action: "config.open"},
		{label: "Window: fullscreen on/off", action: "window.fullscreen"},
		{label: "Window: macOS decorations on/off", action: "window.decorations"},
		{label: "Restart neonmux (window and daemon, new version)", action: "app.restart"},
		{label: "Force restart (kills every terminal, starts from scratch)", action: "app.force-restart"},
		{label: "Quit (terminals keep running)", action: "quit"},
		{label: "Quit and close all terminals", action: "quit.kill"},
	}
	c = append(c, a.remoteCommands()...)
	for _, t := range theme.All {
		c = append(c, command{label: "Theme: " + t.Label, action: "theme." + t.Name})
	}
	for i := range c {
		if c[i].hint == "" {
			c[i].hint = a.hintFor(c[i].action)
		}
	}
	return c
}

func (a *App) setupDemo() {
	cwd := a.opts.StartDir
	if src := demoSource(); src != "" {
		a.openInEditor(src)
	} else {
		a.addPane(NewTermPane(a.newID(), nil, cwd, a.paneEnv()))
	}
	if img := demoImage(); img != "" {
		a.addPane(NewImagePane(a.newID(), img))
	}
	if snd := demoAudio(); snd != "" {
		a.addPane(NewAudioPane(a.newID(), snd))
	}
	a.focus = 0
	a.openExplorer(cwd)
}

// restartAll relaunches the window and the daemon on the binary currently
// on disk (after ./install.sh, for example). The daemon does a hot restart:
// programs keep running. The new window picks up the saved layout.
func (a *App) restartAll() {
	exe, args, err := relaunchCmd()
	if err != nil {
		a.toast("restart failed: "+err.Error(), a.theme.Warn)
		return
	}
	a.restartWith(exe, args)
}

// restartWith relaunches the window on exe. It only quits once the new one
// has started.
func (a *App) restartWith(exe string, args []string) {
	// Checked before anything is detached: a missing binary (an install in
	// progress, for example) leaves the window intact.
	if st, err := os.Stat(exe); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		msg := "binary not executable: " + exe
		if err != nil {
			msg = err.Error()
		}
		a.toast("restart failed: "+msg, a.theme.Warn)
		return
	}
	if a.client != nil {
		a.client.Upgrade()
	}
	// Detach, then close the connection before starting the new window: the
	// two must never fight over the same sessions.
	a.Shutdown()
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	if err := startDetached(cmd, nil); err != nil {
		// The daemon keeps the detached terminals: a window relaunched by
		// hand will find them again.
		a.toast("restart failed: "+err.Error()+" (relaunch neonmux by hand)", a.theme.Warn)
		return
	}
	a.quit = true
}

// startDetached starts cmd in its own session, without waiting for it. log,
// if not nil, receives its output; it is closed here in every case, since
// the started process keeps its own copy of the descriptor.
func startDetached(cmd *exec.Cmd, log *os.File) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if log != nil {
		cmd.Stdout, cmd.Stderr = log, log
		defer log.Close()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// relaunchCmd returns the binary (its real path, the one in the bundle) and
// the arguments to pass again to relaunch this window.
func relaunchCmd() (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, relaunchArgs(os.Args[1:]), nil
}

// forceRestart hands everything over to `neonmux restart --force`, started
// detached: it will kill this window too, then start a fresh one.
func (a *App) forceRestart() {
	exe, args, err := relaunchCmd()
	if err != nil {
		a.toast("forced restart failed: "+err.Error(), a.theme.Warn)
		return
	}
	cmd := exec.Command(exe, append([]string{"restart", "--force", "--detached", "--"}, args...)...)
	_ = os.MkdirAll(server.StateDir(), 0o700)
	log, err := os.OpenFile(filepath.Join(server.StateDir(), "force.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log = nil
	}
	if err := startDetached(cmd, log); err != nil {
		a.toast("forced restart failed: "+err.Error(), a.theme.Warn)
		return
	}
	a.toast("forced restart…", a.theme.Warn)
}

// relaunchArgs keeps the window settings (config, theme, size…) and drops
// the one-off options: a relaunch must never replay a script, take another
// screenshot or run a command again.
func relaunchArgs(in []string) []string {
	withValue := map[string]bool{"-script": true, "-screenshot": true, "-wait": true, "-e": true, "-cpuprofile": true}
	alone := map[string]bool{"-demo": true, "-fresh": true}
	var out []string
	for i := 0; i < len(in); i++ {
		a := in[i]
		name, _, hasEq := strings.Cut(strings.TrimPrefix(a, "-"), "=")
		key := "-" + name
		switch {
		case strings.HasPrefix(a, "-psn_"), alone[key]:
		case withValue[key]:
			if !hasEq {
				i++ // the value follows
			}
		default:
			out = append(out, a)
		}
	}
	return out
}
