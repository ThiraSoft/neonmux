package ui

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/shell"
)

// stubDaemon listens on a test socket and reads the window's frames
// without ever replying: enough for the client to think it is being served.
// It starts no process and never touches the real daemon.
type stubDaemon struct {
	ln      net.Listener
	mu      sync.Mutex
	conns   []net.Conn
	creates []server.CreateReq // MsgCreate received, in order
}

func startStubDaemon(t *testing.T, sock string) *stubDaemon {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &stubDaemon{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d.mu.Lock()
			d.conns = append(d.conns, c)
			d.mu.Unlock()
			go d.read(c)
		}
	}()
	return d
}

// read decodes the frames: type, key length, key, data length (u32 little
// endian), data.
func (d *stubDaemon) read(c net.Conn) {
	r := bufio.NewReader(c)
	for {
		var h [2]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return
		}
		key := make([]byte, h[1])
		var l [4]byte
		if _, err := io.ReadFull(r, key); err != nil {
			return
		}
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return
		}
		data := make([]byte, binary.LittleEndian.Uint32(l[:]))
		if _, err := io.ReadFull(r, data); err != nil {
			return
		}
		var req server.CreateReq
		if h[0] == server.MsgCreate && json.Unmarshal(data, &req) == nil {
			d.mu.Lock()
			d.creates = append(d.creates, req)
			d.mu.Unlock()
		}
	}
}

// stop makes the daemon go away: socket closed and removed, connections cut.
func (d *stubDaemon) stop(sock string) {
	d.ln.Close()
	os.Remove(sock)
	d.mu.Lock()
	for _, c := range d.conns {
		c.Close()
	}
	d.mu.Unlock()
}

func (d *stubDaemon) created(key string) (server.CreateReq, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.creates {
		if r.Key == key {
			return r, true
		}
	}
	return server.CreateReq{}, false
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func toastsWith(a *App, s string) int {
	n := 0
	for _, t := range a.toasts {
		if strings.Contains(t.text, s) {
			n++
		}
	}
	return n
}

// TestDaemonLostKeepsPanes: the daemon goes away. Its terminals stay where
// they are (layout intact, never saved emptied), a single toast reports it,
// and a new terminal runs inside the window. When the daemon comes back, the
// lost terminal is created again with Restore: the daemon reattaches it if
// it still runs, or restarts it from its snapshot on disk.
func TestDaemonLostKeepsPanes(t *testing.T) {
	tmp, err := os.MkdirTemp(os.TempDir(), "nmx") // socket: 104 bytes at most
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	sock := filepath.Join(tmp, "s.sock")
	t.Setenv("NEONMUX_SERVER_SOCK", sock)
	t.Setenv("NEONMUX_STATE", filepath.Join(tmp, "state"))

	d := startStubDaemon(t, sock)
	cl, err := server.Connect(sock, func() error { return errors.New("no daemon to start") })
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	a, _ := coreApp(t, 0)
	a.cfg.Prewarm = false // no prewarmed terminal: it would start the user's shell
	a.client = cl

	p := NewTermPane(a.newID(), []string{"/bin/sh"}, tmp, nil)
	a.addPane(p)
	if _, ok := p.s.(*server.Remote); !ok {
		t.Fatalf("session %T, want a daemon session", p.s)
	}
	waitUntil(t, "the create in the daemon", func() bool { _, ok := d.created(p.key); return ok })
	a.Update()
	os.Remove(layoutPath())
	a.layoutDirty, a.layoutSavedAt = true, time.Time{}

	d.stop(sock)
	waitUntil(t, "the daemon to be lost", cl.Lost)
	for range 3 {
		a.Update()
	}
	if !slices.Contains(a.panes, Pane(p)) {
		t.Fatal("the terminal lost with the daemon was closed")
	}
	if n := toastsWith(a, "daemon lost"); n != 1 {
		t.Errorf("%d daemon lost toasts, want 1", n)
	}
	if _, err := os.Stat(layoutPath()); err == nil {
		t.Error("layout saved while the daemon was lost")
	}
	if !strings.HasPrefix(p.Info(), "offline") {
		t.Errorf("header of the lost terminal: %q", p.Info())
	}

	// While the daemon is lost, a new terminal runs inside the window.
	q := NewTermPane(a.newID(), []string{"/bin/sh"}, tmp, nil)
	a.addPane(q)
	defer q.Close()
	if _, ok := q.s.(*shell.Session); !ok {
		t.Fatalf("session %T while the daemon is lost, want a local session", q.s)
	}

	d = startStubDaemon(t, sock)
	defer d.stop(sock)
	waitUntil(t, "the daemon to come back", cl.Alive)
	a.Update()
	// The stub records the request from its reading goroutine.
	waitUntil(t, "the terminal created again with Restore", func() bool {
		req, ok := d.created(p.key)
		return ok && req.Restore
	})
	if rm, ok := p.s.(*server.Remote); !ok || rm.Lost() {
		t.Error("the terminal is not reattached to the daemon that came back")
	}
	if toastsWith(a, "daemon back") != 1 {
		t.Error("the daemon coming back is not reported")
	}
	a.layoutDirty, a.layoutSavedAt = true, time.Time{}
	a.Update()
	if _, ok := d.created(q.key); ok {
		t.Error("the local terminal opened while the daemon was lost must not move to the daemon")
	}
	// Once the daemon is back, the full layout is saved.
	data, err := os.ReadFile(layoutPath())
	if err != nil || !strings.Contains(string(data), p.key) || !strings.Contains(string(data), q.key) {
		t.Errorf("layout after the daemon came back: %v\n%s", err, data)
	}
}
