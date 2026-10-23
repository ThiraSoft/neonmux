package server

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/remote"
)

type fakeWatcher struct {
	mu           sync.Mutex
	changedCalls int
	exitedCalls  int
	cols         int
	rows         int
}

var _ remote.Watcher = (*fakeWatcher)(nil)

func newFakeWatcher(cols, rows int) *fakeWatcher {
	return &fakeWatcher{cols: cols, rows: rows}
}

func (w *fakeWatcher) Changed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.changedCalls++
}

func (w *fakeWatcher) Exited() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.exitedCalls++
}

func (w *fakeWatcher) Size() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cols, w.rows
}

func (w *fakeWatcher) changedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changedCalls
}

func (w *fakeWatcher) exitedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exitedCalls
}

type blockingSignalWatcher struct {
	ch   chan struct{}
	cols int
	rows int
}

func newBlockingSignalWatcher(cols, rows int) *blockingSignalWatcher {
	return &blockingSignalWatcher{
		ch:   make(chan struct{}, 1),
		cols: cols,
		rows: rows,
	}
}

func (w *blockingSignalWatcher) Changed() {
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (w *blockingSignalWatcher) Exited() {}

func (w *blockingSignalWatcher) Size() (int, int) {
	return w.cols, w.rows
}

func TestWatcherNotifiedOnOutput(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r := cl.Create(CreateReq{Key: "k_out", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	w := newFakeWatcher(80, 24)
	unwatch, ok := s.addWatcher("k_out", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	if got := w.changedCount(); got != 1 {
		t.Fatalf("changed calls after addWatcher = %d, want 1", got)
	}

	r.Write([]byte("printf hi\n"))
	waitFor(t, "watcher notified of output", 3*time.Second, func() bool {
		return w.changedCount() >= 2
	})
	waitText(t, r, "hi")
}

func TestWatcherNeverBlocksPTY(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	// 10 MB of output through the PTY.
	const total = 10 << 20
	cmd := "head -c " + strconv.Itoa(total) + " /dev/zero"
	r := cl.Create(CreateReq{Key: "k_10m", Argv: []string{"/bin/sh", "-c", cmd}, Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	w := newBlockingSignalWatcher(80, 24)
	unwatch, ok := s.addWatcher("k_10m", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	se := s.get("k_10m")
	if se == nil {
		t.Fatal("session not found")
	}

	start := time.Now()
	select {
	case <-se.s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("PTY output timed out (took over 2 s)")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("10 MB through PTY took %v, want under 2 s", elapsed)
	}
}

func TestWatcherInputTakesSize(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r := cl.Create(CreateReq{Key: "k_take", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	w := newFakeWatcher(40, 20)
	unwatch, ok := s.addWatcher("k_take", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	se := s.get("k_take")
	if se == nil {
		t.Fatal("session not found")
	}

	s.inputFrom(se, w, []byte("echo hi\n"))

	waitFor(t, "terminal resized to 40x20", 3*time.Second, func() bool {
		c, rows := sizeOf(se.s.Term)
		return c == 40 && rows == 20
	})

	waitFor(t, "window received MsgResized 40x20", 3*time.Second, func() bool {
		c, rows := sizeOf(r.Terminal())
		return c == 40 && rows == 20
	})

	if got := w.changedCount(); got < 2 {
		t.Fatalf("watcher changed calls = %d, want at least 2", got)
	}
}

func TestWindowInputTakesBackAllItsSessions(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r1 := cl.Create(CreateReq{Key: "s1", Dir: t.TempDir(), Cols: 80, Rows: 24})
	r2 := cl.Create(CreateReq{Key: "s2", Dir: t.TempDir(), Cols: 100, Rows: 30})
	waitFor(t, "s1 ready", 3*time.Second, func() bool { return r1.Ready() })
	waitFor(t, "s2 ready", 3*time.Second, func() bool { return r2.Ready() })

	se1 := s.get("s1")
	se2 := s.get("s2")
	if se1 == nil || se2 == nil {
		t.Fatal("sessions not found")
	}

	// Watcher takes the first session.
	w := newFakeWatcher(40, 20)
	unwatch, ok := s.addWatcher("s1", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	s.inputFrom(se1, w, []byte("w"))
	waitFor(t, "s1 resized to 40x20", 3*time.Second, func() bool {
		c, rows := sizeOf(se1.s.Term)
		return c == 40 && rows == 20
	})

	// Window input on the second session takes back all its sessions.
	r2.Write([]byte("x"))

	waitFor(t, "s1 back to 80x24", 3*time.Second, func() bool {
		c, rows := sizeOf(se1.s.Term)
		return c == 80 && rows == 24
	})

	waitFor(t, "window s1 term back to 80x24", 3*time.Second, func() bool {
		c, rows := sizeOf(r1.Terminal())
		return c == 80 && rows == 24
	})
}

func TestWindowResizeIgnoredWhileWatcherOwns(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r := cl.Create(CreateReq{Key: "k_ign", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	se := s.get("k_ign")
	if se == nil {
		t.Fatal("session not found")
	}

	w := newFakeWatcher(40, 20)
	unwatch, ok := s.addWatcher("k_ign", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	s.inputFrom(se, w, []byte("w"))
	waitFor(t, "watcher took 40x20", 3*time.Second, func() bool {
		c, rows := sizeOf(se.s.Term)
		return c == 40 && rows == 20
	})

	// Window MsgResize 90x25: size unchanged while watcher owns.
	r.Resize(90, 25, 0, 0)
	time.Sleep(150 * time.Millisecond)

	c, rows := sizeOf(se.s.Term)
	if c != 40 || rows != 20 {
		t.Fatalf("size changed prematurely to %dx%d, want 40x20", c, rows)
	}

	// Window input takes ownership and applies 90x25.
	r.Write([]byte("x"))
	waitFor(t, "term resized to 90x25 on window input", 3*time.Second, func() bool {
		c, rows := sizeOf(se.s.Term)
		return c == 90 && rows == 25
	})
	waitFor(t, "window term updated to 90x25", 3*time.Second, func() bool {
		c, rows := sizeOf(r.Terminal())
		return c == 90 && rows == 25
	})
}

func TestWindowReattachKeepsWatcherSize(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r := cl.Create(CreateReq{Key: "k_reatt", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	se := s.get("k_reatt")
	if se == nil {
		t.Fatal("session not found")
	}

	w := newFakeWatcher(40, 20)
	unwatch, ok := s.addWatcher("k_reatt", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	s.inputFrom(se, w, []byte("w"))
	waitFor(t, "watcher took 40x20", 3*time.Second, func() bool {
		c, rows := sizeOf(se.s.Term)
		return c == 40 && rows == 20
	})

	// Window detaches.
	r.Detach()
	time.Sleep(100 * time.Millisecond)

	// Window attaches again asking 80x24: must still be 40x20.
	r2 := cl.Attach("k_reatt", 80, 24, 100)
	waitFor(t, "reattached remote ready", 3*time.Second, func() bool { return r2.Ready() })

	c, rows := sizeOf(se.s.Term)
	if c != 40 || rows != 20 {
		t.Fatalf("daemon term size = %dx%d, want 40x20", c, rows)
	}

	c2, rows2 := sizeOf(r2.Terminal())
	if c2 != 40 || rows2 != 20 {
		t.Fatalf("window term size = %dx%d, want 40x20", c2, rows2)
	}
}

func TestWatcherGetsExited(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r := cl.Create(CreateReq{Key: "k_exit", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	w := newFakeWatcher(80, 24)
	unwatch, ok := s.addWatcher("k_exit", w)
	if !ok {
		t.Fatal("addWatcher failed")
	}
	defer unwatch()

	r.Close()

	waitFor(t, "watcher received Exited", 3*time.Second, func() bool {
		return w.exitedCount() == 1
	})

	w2 := newFakeWatcher(80, 24)
	if _, ok2 := s.addWatcher("k_exit", w2); ok2 {
		t.Fatal("addWatcher on closed session returned ok == true")
	}
}
