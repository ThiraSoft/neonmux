package ui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThiraSoft/neonmux/internal/server"
)

func termByKey(a *App, key string) (*TermPane, int) {
	var found *TermPane
	n := 0
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.key == key {
			found = t
			n++
		}
	}
	return found, n
}

// The daemon asks the window for the phone's terminals: remote-new opens
// one as a new terminal of the window, under the daemon's key, in the home
// directory; remote-adopt puts the ones the daemon opened alone in the
// garage, attached, once each.
func TestRemoteNewTermRequests(t *testing.T) {
	tmp, err := os.MkdirTemp(os.TempDir(), "nmx") // socket: 104 bytes at most
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	sock := filepath.Join(tmp, "s.sock")
	t.Setenv("NEONMUX_SERVER_SOCK", sock)
	t.Setenv("NEONMUX_STATE", filepath.Join(tmp, "state"))
	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	d := startStubDaemon(t, sock)
	defer d.stop(sock)
	cl, err := server.Connect(sock, func() error { return errors.New("no daemon to start") })
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	a, _ := coreApp(t, 0)
	a.cfg.Prewarm = false
	a.client = cl

	const k1, k2 = "0123456789abcdef", "fedcba9876543210"
	a.handleRequest(Request{Cmd: "remote-new", Args: []string{k1}})
	p, n := termByKey(a, k1)
	if n != 1 {
		t.Fatalf("%d panes with the phone's key, want 1", n)
	}
	if p.Base().parked || a.focused() != Pane(p) {
		t.Error("the phone's terminal is not opened as a new terminal (shown, focused)")
	}
	waitUntil(t, "the create in the daemon", func() bool { _, ok := d.created(k1); return ok })
	if req, _ := d.created(k1); req.Dir != home {
		t.Errorf("created in %q, want the home directory %q", req.Dir, home)
	}
	a.handleRequest(Request{Cmd: "remote-new", Args: []string{k1}})
	if _, n := termByKey(a, k1); n != 1 {
		t.Fatalf("remote-new twice: %d panes", n)
	}

	before := len(a.panes)
	a.handleRequest(Request{Cmd: "remote-adopt", Args: []string{k2, k1, "../../etc", ""}})
	q, n := termByKey(a, k2)
	if n != 1 {
		t.Fatalf("%d adopted panes for %s, want 1", n, k2)
	}
	if !q.Base().parked || !q.Base().hidden {
		t.Error("an adopted terminal is not in the garage")
	}
	if _, ok := q.s.(*server.Remote); !ok {
		t.Errorf("adopted terminal session %T, want the daemon's", q.s)
	}
	if len(a.panes) != before+1 {
		t.Fatalf("%d panes after adopt, want %d", len(a.panes), before+1)
	}
	if _, ok := d.created(k2); ok {
		t.Error("an adopted terminal was created, not attached")
	}
}
