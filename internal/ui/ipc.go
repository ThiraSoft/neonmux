package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ThiraSoft/neonmux/internal/keys"
	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Request is a message from the CLI (`neonmux open file.png`) to the app,
// sent over the Unix socket exported in $NEONMUX_SOCK (the daemon's, or the
// window's in daemonless mode).
type Request = server.Request

// ipcTimeout bounds the wait for a window that no longer drains its queue.
var ipcTimeout = 3 * time.Second

// StartIPC listens on path and returns the cleanup function. The socket
// accepts send-keys, so it drives the terminals: it is mode 0600, and any
// connection from another uid is refused (the directory main picks is
// already private).
func StartIPC(a *App, path string) (func(), error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return func() {}, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return func() {}, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if uc, ok := c.(*net.UnixConn); ok && server.PeerUID(uc) != os.Getuid() {
				c.Close()
				continue
			}
			go a.serveIPC(c)
		}
	}()
	return func() { l.Close(); os.Remove(path) }, nil
}

// serveIPC handles the requests of one connection, one per line.
func (a *App) serveIPC(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var r Request
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			fmt.Fprintln(c, "error: invalid request")
			continue
		}
		deadline := time.Now().Add(ipcTimeout)
		if r.Cmd == "capture" || r.Cmd == "send-keys" {
			// Synchronous reply: the screen or the error must come before "ok".
			res := make(chan []string, 1)
			if !a.postWithin(func() { res <- a.paneRequest(r) }, ipcTimeout) {
				fmt.Fprintln(c, "error: the window is not responding")
				continue
			}
			select {
			case lines := <-res:
				for _, l := range lines {
					fmt.Fprintln(c, l)
				}
			case <-time.After(time.Until(deadline)):
				fmt.Fprintln(c, "error: the window is not responding")
			}
			continue
		}
		if !a.postWithin(func() { a.handleRequest(r) }, ipcTimeout) {
			fmt.Fprintln(c, "error: the window is not responding")
			continue
		}
		fmt.Fprintln(c, "ok")
	}
}

// postWithin posts fn like post, without waiting more than d for the queue
// to free up: a frozen window with a full queue must block neither the
// socket nor the shutdown on a signal. Returns false if fn could not be
// posted.
func (a *App) postWithin(fn func(), d time.Duration) bool {
	select {
	case a.posted <- fn:
	default:
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case a.posted <- fn:
		case <-t.C:
			return false
		}
	}
	a.pacer.Kick()
	return true
}

// PostWithin is the exported version of postWithin (shutdown on a signal).
func (a *App) PostWithin(fn func(), d time.Duration) bool { return a.postWithin(fn, d) }

func (a *App) paneByID(id string) Pane {
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.key == id {
			return p
		}
	}
	n, err := strconv.Atoi(id)
	if err == nil {
		for _, p := range a.panes {
			if p.Base().id == n {
				return p
			}
		}
	}
	return a.focused()
}

func (a *App) handleRequest(r Request) {
	p := a.paneByID(r.Pane)
	arg := strings.Join(r.Args, " ")
	switch r.Cmd {
	case "open":
		for _, f := range r.Args {
			if !filepath.IsAbs(f) {
				f = filepath.Join(r.Cwd, f)
			}
			a.OpenPath(f)
		}
	case "notify":
		if p != nil {
			a.notifyPane(p, "", arg)
		}
	case "fx":
		if p != nil {
			a.runFX(p, arg)
		}
	case "theme":
		a.setTheme(theme.Get(arg))
	case "toast":
		a.toast(arg, a.theme.Accent2)
	case "restart":
		a.restartAll()
	case "rename":
		if p != nil {
			p.Base().name = strings.TrimSpace(arg)
			a.layoutDirty = true
		}
	case "agent":
		a.agentRequest(r.Args, firstOf(r.Cwd, a.cwd()))
	case "diff":
		dir := firstOf(arg, r.Cwd, a.cwd())
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(r.Cwd, dir)
		}
		a.openDiff(dir)
	case "float":
		cwd := r.Cwd
		if cwd == "" {
			cwd = a.cwd()
		}
		a.floatRequest(p, r.Args, cwd)
	}
}

// Send sends a request to the daemon (or to the window that started this
// shell) and returns the lines of the response.
func Send(r Request) ([]string, error) {
	sock := os.Getenv("NEONMUX_SOCK")
	if sock == "" {
		sock = server.SocketPath()
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("no reachable neonmux instance (%s)", sock)
	}
	defer c.Close()
	if r.Pane == "" {
		r.Pane = os.Getenv("NEONMUX_PANE")
	}
	r.Cwd, _ = os.Getwd()
	data, _ := json.Marshal(r)
	if _, err := c.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		l := sc.Text()
		if l == "ok" {
			return lines, nil
		}
		// "erreur" comes from an older window or daemon still running
		// during an upgrade.
		if strings.HasPrefix(l, "error") || strings.HasPrefix(l, "erreur") {
			return lines, fmt.Errorf("%s", l)
		}
		lines = append(lines, l)
	}
	return lines, fmt.Errorf("incomplete response")
}

// paneRequest serves neonmux capture and neonmux send-keys in daemonless
// mode, with the same format as the daemon: lines prefixed with "|", then
// "ok".
func (a *App) paneRequest(r Request) []string {
	t, err := a.findTermPane(r.Pane)
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	var out []string
	if r.Cmd == "capture" {
		for _, l := range t.t.ScreenText() {
			out = append(out, "|"+l)
		}
	} else {
		b, err := keys.EncodeTokens(r.Args, t.t.Modes())
		if err != nil {
			return []string{"error: " + err.Error()}
		}
		t.write(b)
	}
	return append(out, "ok")
}

// findTermPane finds a terminal by its key, its number (the one shown in the
// bottom bar), its name or its foreground program. Empty: the focused pane.
// A pane in the garage has no number, but the other ways still find it.
func (a *App) findTermPane(id string) (*TermPane, error) {
	if id == "" {
		if t, ok := a.focused().(*TermPane); ok && t.t != nil {
			return t, nil
		}
		return nil, fmt.Errorf("the focused pane is not a terminal")
	}
	var found []*TermPane
	for i, p := range a.panes {
		t, ok := p.(*TermPane)
		if !ok || t.t == nil {
			continue
		}
		if t.key == id || !t.parked && strconv.Itoa(i+1) == id {
			return t, nil
		}
		fg := ""
		if t.s != nil {
			fg = strings.TrimPrefix(t.s.ForegroundName(), "-")
		}
		if t.Title() == id || fg == id {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no pane \"%s\"", id)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("several panes match \"%s\": use its number", id)
}
