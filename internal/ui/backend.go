package ui

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"

	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// termBackend is what a TermPane needs: a local session (shell.Session) or
// a session kept by the daemon (server.Remote).
type termBackend interface {
	Terminal() *vt.Terminal
	Key() string
	Write([]byte)
	Resize(cols, rows, pxW, pxH int)
	Done() <-chan struct{}
	ForegroundName() string
	ForegroundCwd() string // directory of the foreground program, empty for the shell
	Cwd() string
	Close()  // ends the process
	Detach() // leaves the window without killing the process
}

// remoteBackend adds what only the daemon can do.
type remoteBackend interface {
	termBackend
	Missing() bool
	TakePending() string
	Lost() bool // lost with the daemon, not ended by its program
}

var (
	_ termBackend   = (*shell.Session)(nil)
	_ remoteBackend = (*server.Remote)(nil)
)

func newKey() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Commands relaunched after the Mac restarts, with their arguments.
// Builds, servers and scripts never are.
var defaultRelaunch = []string{"nvim", "vim", "vi", "less", "man", "htop", "btop", "top", "lazygit", "tig", "claude"}

// startSession starts a terminal's session: in the daemon if it can be
// reached (persistence), otherwise in the window. So while the daemon is
// lost, a new terminal runs in the window, without persistence.
func (a *App) startSession(p *TermPane, cols, rows int, restore bool) (termBackend, error) {
	if a.client != nil && a.client.Alive() {
		return a.remoteSession(p, cols, rows, restore), nil
	}
	env := append(append([]string(nil), p.env...), "NEONMUX_PANE="+p.key)
	if a.opts.Socket != "" {
		env = append(env, "NEONMUX_SOCK="+a.opts.Socket)
	}
	return shell.Start(shell.Options{
		Argv: p.argv, Dir: p.dir, Env: env, Key: p.key,
		Cols: cols, Rows: rows, Scrollback: a.cfg.Scrollback,
		// A closed pane no longer wakes up rendering, not even at the end
		// of the read: it has already left the screen.
		OnOutput: func() {
			if !p.quiet.Load() {
				a.pacer.Kick()
			}
		},
	})
}

// remoteSession creates a terminal's session in the daemon, or reattaches
// to it. If the daemon was lost in the meantime, the session starts out
// lost.
func (a *App) remoteSession(p *TermPane, cols, rows int, restore bool) *server.Remote {
	env := append(append([]string(nil), p.env...), "NEONMUX_PANE="+p.key, "NEONMUX_SOCK="+server.SocketPath())
	if p.attach && !restore {
		return a.client.Attach(p.key, cols, rows, a.cfg.Scrollback)
	}
	relaunch := a.cfg.RestoreCmds
	if relaunch == nil {
		relaunch = defaultRelaunch
	}
	return a.client.Create(server.CreateReq{
		Key: p.key, Argv: p.argv, Dir: p.dir, Env: env,
		Cols: cols, Rows: rows, Scrollback: a.cfg.Scrollback,
		Restore: restore, Relaunch: relaunch, Ephemeral: p.ephemeral,
	})
}

// daemonLost reports a daemon terminal lost along with the daemon: its
// program may still be running, or will come back from its snapshot. It is
// not closed, and keeps its last screen and its place in the layout.
func (p *TermPane) daemonLost() bool {
	rm, ok := p.s.(remoteBackend)
	return ok && rm.Lost()
}

// revive reattaches a terminal lost with the daemon once the daemon is
// back. Restore covers both cases: a daemon that stopped answering but is
// still running reattaches the session, a fresh daemon relaunches it from
// its snapshot on disk (saved every 15s, relaunch of nvim or claude
// included).
func (p *TermPane) revive(a *App) {
	p.attach = false
	p.useSession(a.remoteSession(p, p.cols, p.rows, true))
	p.applyColors(a.theme)
}

// watchDaemon follows the connection to the daemon, on every frame. When it
// is lost, a single toast. When it comes back, the lost terminals are
// recreated, and the local spare shell prepared in the meantime gives way
// to one in the daemon.
func (a *App) watchDaemon(now time.Time) {
	if a.client == nil {
		return
	}
	lost := a.client.Lost()
	if lost && !a.daemonLost {
		a.toast("daemon lost: terminals frozen, new ones without persistence", a.theme.Warn)
	}
	a.daemonLost = lost
	if !a.client.Alive() {
		return
	}
	if sp := a.spare; sp != nil {
		if _, remote := sp.p.s.(remoteBackend); !remote {
			sp.p.Close()
			a.spare, a.spareAt = nil, now.Add(spareDelay)
		}
	}
	if !a.revive {
		return
	}
	a.revive = false
	n := 0
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.daemonLost() {
			t.revive(a)
			n++
		}
	}
	switch {
	case n == 1:
		a.toast("daemon back: 1 terminal recovered", a.theme.Accent2)
	case n > 1:
		a.toast("daemon back: "+itoa(n)+" terminals recovered", a.theme.Accent2)
	}
}

// connectServer connects to the daemon, starting it if needed.
func (a *App) connectServer() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.MkdirAll(server.StateDir(), 0o700)
	cl, err := server.Connect(server.SocketPath(), func() error { return server.Spawn(exe) })
	if err != nil {
		a.toast("daemon unavailable, terminals will not persist: "+err.Error(), a.theme.Warn)
		return
	}
	cl.OnWake = a.pacer.Kick
	cl.OnRequest = func(r server.Request) { a.post(func() { a.handleRequest(r) }) }
	a.client = cl
	a.sendColors()
}

// sendColors sends the theme to the daemon, which answers color queries
// (OSC 10/11) on behalf of the window.
func (a *App) sendColors() {
	if a.client == nil {
		return
	}
	th := a.theme
	c := server.Colors{FG: theme.RGB(th.FG), BG: theme.RGB(th.BG), Cursor: theme.RGB(th.Cursor)}
	for i := range c.Palette {
		c.Palette[i] = theme.RGB(th.Palette(uint8(i)))
	}
	a.client.SetColors(c)
}
