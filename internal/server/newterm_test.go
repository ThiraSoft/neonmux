package server

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

// phoneShell keeps the terminals opened for the phone off the user's shell
// and home: /bin/sh in a temporary HOME.
func phoneShell(t *testing.T) func(*Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return func(s *Server) { s.newTermArgv = []string{"/bin/sh"} }
}

// nextRequest reads the window's frames until a MsgRequest.
func (w *rawWindow) nextRequest(t *testing.T, within time.Duration) (Request, bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case f, open := <-w.frames:
			if !open {
				t.Fatal("window connection closed")
			}
			if f.typ != MsgRequest {
				continue
			}
			var r Request
			if err := json.Unmarshal(f.data, &r); err != nil {
				t.Fatal(err)
			}
			return r, true
		case <-deadline:
			return Request{}, false
		}
	}
}

// Without a window, the daemon opens the phone's terminal itself, at the
// phone's size, in the home directory; the next window adopts it (and
// only that window: once it attaches, it is in its layout).
func TestRemoteNewTermNoWindow(t *testing.T) {
	s, sock := inProcess(t, phoneShell(t))
	h := &remoteHost{s: s}
	key, err := h.NewTerm(100, 30)
	if err != nil {
		t.Fatal(err)
	}
	se := s.get(key)
	if se == nil || se.ephemeral.Load() {
		t.Fatalf("session %q not created", key)
	}
	if c, r := sizeOf(se.s.Term); c != 100 || r != 30 {
		t.Fatalf("size %dx%d, want the phone's 100x30", c, r)
	}
	listed := false
	for _, i := range h.Sessions() {
		listed = listed || i.Key == key
	}
	if !listed {
		t.Fatal("the new terminal is not in the phone's list")
	}

	w := dialWindow(t, sock, protoVersion)
	req, ok := w.nextRequest(t, 3*time.Second)
	if !ok || req.Cmd != "remote-adopt" || !slices.Equal(req.Args, []string{key}) {
		t.Fatalf("window got %+v (%v), want remote-adopt [%s]", req, ok, key)
	}
	w.send(t, MsgAttach, key, AttachReq{Cols: 90, Rows: 20})
	waitFor(t, "the window's attach", 3*time.Second, func() bool { return se.att.Load() != nil })

	w2 := dialWindow(t, sock, protoVersion)
	if req, ok := w2.nextRequest(t, 300*time.Millisecond); ok {
		t.Fatalf("a terminal already in a window is adopted again: %+v", req)
	}
}

// With a window, the daemon asks it to open the terminal, under a key the
// daemon picked, and waits for the window's create.
func TestRemoteNewTermAsksWindow(t *testing.T) {
	s, sock := inProcess(t, phoneShell(t))
	h := &remoteHost{s: s}
	w := dialWindow(t, sock, protoVersion)
	w.send(t, MsgCreate, "", CreateReq{Key: "existing", Argv: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	waitFor(t, "the window's first session", 3*time.Second, func() bool { return s.get("existing") != nil })

	type res struct {
		key string
		err error
	}
	done := make(chan res, 1)
	go func() {
		k, err := h.NewTerm(100, 30)
		done <- res{k, err}
	}()
	req, ok := w.nextRequest(t, 3*time.Second)
	if !ok || req.Cmd != "remote-new" || len(req.Args) != 1 || !validKey(req.Args[0]) {
		t.Fatalf("window got %+v (%v), want remote-new <key>", req, ok)
	}
	key := req.Args[0]
	select {
	case r := <-done:
		t.Fatalf("NewTerm returned %+v before the window created the session", r)
	case <-time.After(100 * time.Millisecond):
	}
	w.send(t, MsgCreate, "", CreateReq{Key: key, Argv: []string{"/bin/sh"}, Cols: 120, Rows: 40})
	r := <-done
	if r.err != nil || r.key != key {
		t.Fatalf("NewTerm = %+v, want %s", r, key)
	}
	if se := s.get(key); se == nil || se.att.Load() == nil {
		t.Fatal("the session is not the window's")
	}
	s.mu.Lock()
	n := len(s.orphans)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d terminals waiting for adoption, want 0", n)
	}
}

// A window that does not answer (an older one, or no room): the daemon
// opens the terminal itself after the wait, and the next window adopts it.
func TestRemoteNewTermWindowSilent(t *testing.T) {
	s, sock := inProcess(t, phoneShell(t), func(s *Server) { s.newTermWait = 200 * time.Millisecond })
	h := &remoteHost{s: s}
	w := dialWindow(t, sock, protoVersion)
	waitFor(t, "the window", 3*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.gui != nil
	})
	key, err := h.NewTerm(100, 30)
	if err != nil {
		t.Fatal(err)
	}
	if req, ok := w.nextRequest(t, time.Second); !ok || req.Cmd != "remote-new" {
		t.Fatalf("window got %+v (%v), want remote-new", req, ok)
	}
	if s.get(key) == nil {
		t.Fatal("no session after the window stayed silent")
	}
	s.mu.Lock()
	orphan := s.orphans[key]
	s.mu.Unlock()
	if !orphan {
		t.Fatal("the terminal is not waiting for a window to adopt it")
	}
}

// A hot restart keeps the terminals waiting for a window.
func TestResumeKeepsOrphans(t *testing.T) {
	s := bareServer(t)
	path, _ := fakeUpgrade(t, s, 2, func(i int, u *upSess) {})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st upState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	st.Adopt = []string{"k1"}
	data, _ = json.Marshal(st)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(path); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.orphans["k1"] || s.orphans["k0"] {
		t.Fatalf("orphans %v, want k1 only", s.orphans)
	}
}

// A hot restart in progress: the phone's terminal is not opened, since the
// handover would not carry it (and the restart closes the phone anyway).
func TestRemoteNewTermDuringRestart(t *testing.T) {
	t.Run("before spawn", func(t *testing.T) {
		s, _ := inProcess(t, phoneShell(t))
		h := &remoteHost{s: s}
		s.upgrading.Store(true)
		defer s.upgrading.Store(false)
		if key, err := h.NewTerm(100, 30); err == nil {
			t.Fatalf("NewTerm during a restart opened %q", key)
		}
		if n := len(s.list()); n != 0 {
			t.Fatalf("%d sessions after a refused NewTerm, want 0", n)
		}
	})
	t.Run("while waiting for the window", func(t *testing.T) {
		s, sock := inProcess(t, phoneShell(t), func(s *Server) { s.newTermWait = 5 * time.Second })
		h := &remoteHost{s: s}
		w := dialWindow(t, sock, protoVersion)
		waitFor(t, "the window", 3*time.Second, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.gui != nil
		})
		done := make(chan error, 1)
		go func() {
			_, err := h.NewTerm(100, 30)
			done <- err
		}()
		if req, ok := w.nextRequest(t, 3*time.Second); !ok || req.Cmd != "remote-new" {
			t.Fatalf("window got %+v (%v), want remote-new", req, ok)
		}
		s.upgrading.Store(true)
		defer s.upgrading.Store(false)
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("NewTerm succeeded during a restart")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("NewTerm kept waiting for the window during a restart")
		}
		if n := len(s.list()); n != 0 {
			t.Fatalf("%d sessions after a refused NewTerm, want 0", n)
		}
	})
}
