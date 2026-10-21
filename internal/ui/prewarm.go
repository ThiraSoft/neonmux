package ui

import (
	"os"
	"path/filepath"
	"time"
)

// An interactive shell can take several seconds to initialize (nvm, conda,
// compinit, theme…). neonmux keeps one prewarmed: a new terminal adopts it
// instantly, and a new spare is prepared in the background a few seconds
// later.

const (
	spareDelay  = 2 * time.Second  // delay before preparing the next spare
	spareMaxAge = 30 * time.Minute // beyond that, the environment is considered stale
	spareHide   = 220 * time.Millisecond
	// The spare is checked on every frame: reading the rc files' dates
	// every time would cost nine stat calls (a network round trip each on
	// an NFS $HOME) in the render loop.
	rcCheckEvery = 3 * time.Second
)

type spareShell struct {
	p       *TermPane
	dir     string
	born    time.Time
	rcMod   time.Time
	checked time.Time // last read of the rc files' dates
}

var rcFiles = []string{".zshrc", ".zprofile", ".zshenv", ".zsh_env", ".zsh_aliases", ".bashrc", ".bash_profile", ".profile", ".config/fish/config.fish"}

// rcModTime returns the most recent modification time of the shell's init
// files: an older spare is recycled.
func rcModTime() time.Time {
	home, _ := os.UserHomeDir()
	var last time.Time
	for _, f := range rcFiles {
		if st, err := os.Stat(filepath.Join(home, f)); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
		}
	}
	return last
}

func (sp *spareShell) stale(now time.Time) bool {
	if sp.p.s == nil || sp.p.Done() || now.Sub(sp.born) > spareMaxAge {
		return true
	}
	if now.Sub(sp.checked) < rcCheckEvery {
		return false
	}
	sp.checked = now
	return rcModTime().After(sp.rcMod)
}

// prepareSpare starts the spare in the directory where the next terminal is
// most likely to open: the focused terminal's.
func (a *App) prepareSpare(now time.Time) {
	if !a.cfg.Prewarm || a.spare != nil || now.Before(a.spareAt) || a.shutdown || len(a.opts.Exec) > 0 {
		return
	}
	dir := a.cwd()
	p := NewTermPane(a.newID(), nil, dir, a.paneEnv())
	p.ephemeral = true
	cols, rows := 80, 24
	if t := a.focusedTerm(); t != nil && t.cols > 0 {
		cols, rows = t.cols, t.rows
	}
	s, err := a.startSession(p, cols, rows, false)
	if err != nil {
		a.spareAt = now.Add(time.Minute)
		return
	}
	p.s, p.t, p.cols, p.rows = s, s.Terminal(), cols, rows
	p.applyColors(a.theme)
	a.spare = &spareShell{p: p, dir: dir, born: now, rcMod: rcModTime(), checked: now}
}

// recycleSpare discards a stale spare (rc files modified, too old).
func (a *App) recycleSpare(now time.Time) {
	if a.spare != nil && a.spare.stale(now) {
		a.spare.p.Close()
		a.spare = nil
		a.spareAt = now
	}
}

// newShellPane opens a terminal in dir, adopting the spare if possible.
// Beside, it never takes the place of a tile (addBeside).
func (a *App) newShellPane(dir string, beside bool) {
	if !a.roomForPane() || beside && !a.roomBeside() {
		return // the spare stays for later
	}
	add := a.addPane
	if beside {
		add = func(p Pane) {
			a.floatIfFull(p)
			a.addPane(p)
		}
	}
	now := time.Now()
	if a.spare != nil {
		// When it is adopted, the dates are read again: a .zshrc modified
		// a moment ago counts.
		a.spare.checked = time.Time{}
	}
	a.recycleSpare(now)
	sp := a.spare
	if sp == nil {
		add(NewTermPane(a.newID(), nil, dir, a.paneEnv()))
		a.spareAt = now.Add(spareDelay)
		return
	}
	a.spare = nil
	a.spareAt = now.Add(spareDelay)
	p := sp.p
	p.ephemeral = false
	if ad, ok := p.s.(interface{ Adopt() }); ok {
		ad.Adopt()
	}
	p.dir = dir
	p.hideUntil = now.Add(spareHide)
	add(p)
	// The resize has already gone out (Layout): change directory if needed,
	// without touching the history (leading space), then ctrl+l redraws a
	// clean prompt at the right size.
	cmd := "\x0c"
	if dir != "" && dir != sp.dir {
		cmd = "\x15 cd -- " + shellQuote(dir) + "\r\x0c"
	}
	p.write([]byte(cmd))
}
