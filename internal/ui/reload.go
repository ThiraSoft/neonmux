package ui

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// The configuration is reloaded as soon as it is saved (after cmd+, for
// example). fsnotify relies on kqueue (macOS) or inotify (Linux): no wakeup
// as long as the file does not change. We watch the directory, because many
// editors save by replacing the file.

func (a *App) watchConfig() {
	path := ConfigPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_, _ = watchConfigFile(path, func() { a.post(a.reloadConfig) })
	// Read with an error at launch: say so in the window too (main's message
	// cannot be seen when launched from the Finder).
	if err := cfgFile.broken; err != nil {
		a.post(func() {
			a.toast(configErrMsg(err)+": using default settings", a.theme.Warn)
		})
	}
}

// configRetry spaces out new attempts when a watched directory has
// disappeared, or after an fsnotify error.
var configRetry = 2 * time.Second

// configWatch watches the config file's directory and, if the file is a
// symbolic link (stow, home-manager), its target's directory: inotify does
// not follow links inside a watched directory.
type configWatch struct {
	w       *fsnotify.Watcher
	path    string          // config.json as configured
	names   []string        // path, then the targets of its links
	watched map[string]bool // watched directories
}

// watchConfigFile calls changed when path changes, after a short delay that
// groups the events of a single save. A directory that disappears is
// checked for until it comes back, then watched again.
func watchConfigFile(path string, changed func()) (stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return func() {}, err
	}
	cw := &configWatch{w: w, path: filepath.Clean(path), watched: map[string]bool{}}
	ok := cw.sync()
	done := make(chan struct{})
	go cw.run(changed, ok, done)
	var once sync.Once
	return func() { once.Do(func() { close(done); w.Close() }) }, nil
}

// linkChain returns path, then the successive targets of its symbolic
// links. Under kqueue, watching a directory opens each of its files (and
// follows links): we stop at the first target, so as never to watch a
// directory like /nix/store. inotify only costs one watch per directory.
func linkChain(path string) []string {
	chain := []string{path}
	depth := 1
	if runtime.GOOS == "linux" {
		depth = 8
	}
	for len(chain) <= depth {
		cur := chain[len(chain)-1]
		t, err := os.Readlink(cur)
		if err != nil {
			break
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(cur), t)
		}
		chain = append(chain, filepath.Clean(t))
	}
	return chain
}

// sync matches the watched directories to the current link chain. Returns
// false if one of them is still missing.
func (cw *configWatch) sync() bool {
	cw.names = linkChain(cw.path)
	var want []string
	for _, n := range cw.names {
		d := filepath.Dir(n)
		if !slices.ContainsFunc(want, func(w string) bool { return sameDir(w, d) }) {
			want = append(want, d)
		}
	}
	ok := true
	for _, d := range want {
		if cw.watched[d] {
			continue
		}
		if cw.w.Add(d) == nil {
			cw.watched[d] = true
		} else {
			ok = false
		}
	}
	for d := range cw.watched {
		if !slices.Contains(want, d) {
			_ = cw.w.Remove(d)
			delete(cw.watched, d)
		}
	}
	return ok
}

// sameDir reports whether two paths point to the same directory (/var and
// /private/var on macOS, for example).
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func (cw *configWatch) run(changed func(), ok bool, done chan struct{}) {
	var debounce *time.Timer
	fire := func() {
		// A save often produces several events.
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(150*time.Millisecond, changed)
	}
	var retry <-chan time.Time
	if !ok {
		retry = time.After(configRetry)
	}
	for {
		select {
		case <-done:
			if debounce != nil {
				debounce.Stop()
			}
			return
		case ev, ok := <-cw.w.Events:
			if !ok {
				return
			}
			name := filepath.Clean(ev.Name)
			switch {
			case cw.watched[name] && ev.Has(fsnotify.Remove|fsnotify.Rename):
				// The directory itself is gone: wait for it to come back.
				_ = cw.w.Remove(name)
				delete(cw.watched, name)
				retry = time.After(configRetry)
			case slices.Contains(cw.names, name) && ev.Has(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove):
				// A replaced link may have a new target. A deleted file is
				// read again too: once it is gone, a broken file no longer
				// blocks writes.
				if !cw.sync() {
					retry = time.After(configRetry)
				}
				fire()
			}
		case _, ok := <-cw.w.Errors:
			if !ok {
				return
			}
			// Events may have been lost (kernel queue full…): check the
			// watches and read the file again to be safe.
			if !cw.sync() {
				retry = time.After(configRetry)
			}
			fire()
		case <-retry:
			retry = nil
			if !cw.sync() {
				retry = time.After(configRetry)
				continue
			}
			fire() // the file may have changed while the directory was gone
		}
	}
}

func (a *App) reloadConfig() {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) && cfgFile.broken != nil {
			// The unreadable file was deleted: nothing left to protect.
			cfgFile.broken = nil
		}
		return // file being replaced: the next event will do
	}
	h := sha256.Sum256(data)
	if h == a.cfgHash {
		return // our own write, or nothing new
	}
	a.cfgHash = h
	nc, warn, err := ParseConfig(data)
	// A failure blocks writes (saveConfig); a successful read becomes the
	// new reference.
	noteConfigRead(nc, err)
	if err != nil {
		a.toast(configErrMsg(err)+", config unchanged", a.theme.Warn)
		return
	}
	changed := a.applyConfig(nc)
	msg := "config reloaded"
	if len(changed) > 0 {
		msg += ": " + strings.Join(changed, ", ")
	}
	if warn != "" {
		msg += " (" + warn + ")"
	}
	a.toast(msg, a.theme.Accent2)
}

// applyConfig applies a configuration on the fly, without writing it back,
// and returns what changed.
func (a *App) applyConfig(nc Config) []string {
	old := a.cfg
	a.cfg = nc
	var changed []string
	note := func(cond bool, label string) bool {
		if cond {
			changed = append(changed, label)
		}
		return cond
	}

	if note(nc.Theme != a.theme.Name, "theme") {
		a.applyTheme(theme.Get(nc.Theme))
	}
	fontChanged := nc.Font != old.Font || nc.FontSize != old.FontSize || nc.UIFont != old.UIFont || nc.UIFontSize != old.UIFontSize
	if nc.SymbolsFont != old.SymbolsFont {
		a.symbols = "" // read again by loadFonts
		fontChanged = true
	}
	if note(fontChanged, "font") {
		a.loadFonts()
	}
	if note(nc.Background != old.Background || nc.Animate != old.Animate, "background") {
		a.applyBackground()
	}
	if note(nc.Power != old.Power, "power") {
		a.pacer.SetMaxFPS(maxFPSFor(nc.Power))
	}
	note(nc.Battery != old.Battery, "battery")
	if note(nc.Decorations != old.Decorations, "decorations") {
		ebiten.SetWindowDecorated(nc.Decorations)
	}
	bindingsChanged := nc.Leader != old.Leader || nc.Mod != old.Mod || len(nc.Bindings) != len(old.Bindings)
	for k, v := range nc.Bindings {
		if old.Bindings[k] != v {
			bindingsChanged = true
		}
	}
	if note(bindingsChanged, "shortcuts") {
		a.mod = ParseMods(nc.Mod)
		a.buildBindings()
	}
	if note(nc.Shell != old.Shell, "shell") && nc.Shell != "" {
		os.Setenv("SHELL", nc.Shell)
	}
	// The spare shell follows the shell and the prewarm option.
	if (nc.Shell != old.Shell || !nc.Prewarm) && a.spare != nil {
		a.spare.p.Close()
		a.spare = nil
		a.spareAt = time.Now()
	}
	note(nc.Prewarm != old.Prewarm, "prewarmed terminal")
	note(nc.CursorBlink != old.CursorBlink, "cursor")
	note(nc.FocusBorder != old.FocusBorder || nc.SoloBorder != old.SoloBorder || nc.FocusRest != old.FocusRest ||
		nc.FocusFade != old.FocusFade, "focus border")
	note(nc.PaneOpacity != old.PaneOpacity, "pane opacity")
	note(nc.AgentFlash != old.AgentFlash || nc.AgentFlashCount != old.AgentFlashCount ||
		nc.AgentFlashDuration != old.AgentFlashDuration, "agent flash")
	note(nc.TypingFX != old.TypingFX, "sparks")
	note(nc.CopyOnSelect != old.CopyOnSelect || nc.OptionAsMeta != old.OptionAsMeta, "keyboard and mouse")
	note(nc.MaxPanes != old.MaxPanes || nc.Scrollback != old.Scrollback || nc.Editor != old.Editor ||
		!slices.Equal(nc.RestoreCmds, old.RestoreCmds), "options")
	if nc.Persist != old.Persist {
		changed = append(changed, "persist (on next launch)")
	}
	if nc.Font != gfx.FontPixel && a.termFont != nil && a.termFont.Name == gfx.FontPixel && nc.Font != "" {
		// loadFonts may have fallen back to the pixel font (file not found):
		// the error toast is already shown.
		a.cfg.Font = gfx.FontPixel
	}
	a.needRedraw = true
	a.relayout()
	return changed
}
