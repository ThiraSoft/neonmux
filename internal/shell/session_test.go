package shell

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Tests for the PTY plumbing: a program that does not read its input, that
// floods the terminal with queries, that ignores SIGHUP or that leaves a
// detached process behind must never block the session, nor whoever writes
// to it.

func startTest(t *testing.T, script string) *Session {
	t.Helper()
	s, err := Start(Options{Argv: []string{"/bin/sh", "-c", script}, Dir: os.TempDir(), Cols: 80, Rows: 24, Scrollback: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.Pid() > 0 {
			_ = syscall.Kill(-s.Pid(), syscall.SIGKILL)
		}
	})
	return s
}

// within fails if fn does not return before d.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s blocked for %v", what, d)
	}
}

// TestWriteNeverBlocks: a program that does not read its input blocks
// neither Write nor the hot restart freeze.
func TestWriteNeverBlocks(t *testing.T) {
	for _, script := range []string{"sleep 30", "stty raw -echo; sleep 30"} {
		s := startTest(t, script)
		time.Sleep(300 * time.Millisecond)
		chunk := []byte(strings.Repeat("a", 1023) + "\r")
		within(t, 2*time.Second, script+": Write", func() {
			for i := 0; i < 2000; i++ {
				s.Write(chunk)
			}
		})
		within(t, 2*time.Second, script+": Freeze", func() {
			if _, _, err := s.Freeze(); err != nil {
				t.Errorf("Freeze: %v", err)
			}
		})
		// Input that could not go out is handed over to the new daemon.
		if len(s.PendingInput()) == 0 {
			t.Errorf("%s: pending input lost on freeze", script)
		}
	}
}

// TestInputQueueBounded: past the cap, input is thrown away, never piled up
// without end.
func TestInputQueueBounded(t *testing.T) {
	s := startTest(t, "stty raw -echo; sleep 30")
	time.Sleep(300 * time.Millisecond)
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'b'
	}
	for i := 0; i < 2*maxInput/len(chunk); i++ {
		s.Write(chunk)
	}
	s.inMu.Lock()
	n := len(s.inBuf)
	s.inMu.Unlock()
	if n > maxInput {
		t.Fatalf("input queue of %d bytes, cap %d", n, maxInput)
	}
	if s.Dropped() == 0 {
		t.Fatal("no input counted as dropped")
	}
}

// TestReplyFloodDoesNotBlockReading: a program that sends queries (ESC [ c)
// without ever reading the replies does not block reading from its PTY
// (replies go out from the read loop, under the session lock).
func TestReplyFloodDoesNotBlockReading(t *testing.T) {
	q := strings.Repeat(`\033[c`, 50)
	s := startTest(t, `stty raw -echo; i=0; while [ $i -lt 400 ]; do printf '`+q+`'; i=$((i+1)); done; echo end-of-flood; sleep 30`)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(strings.Join(s.Term.ScreenText(), "\n"), "end-of-flood") {
		if time.Now().After(deadline) {
			t.Fatal("reading from the PTY is blocked by the replies")
		}
		time.Sleep(50 * time.Millisecond)
	}
	within(t, 2*time.Second, "Atomic", func() { s.Atomic(func() {}) })
	within(t, 2*time.Second, "Write", func() { s.Write([]byte("\x03")) })
}

// detachedCmd returns a command that leaves behind a process in another
// session, which keeps the PTY slave open.
func detachedCmd(t *testing.T, secs string) string {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err == nil {
		return "setsid sleep " + secs + " & sleep 0.2; exit 0"
	}
	if _, err := exec.LookPath("perl"); err == nil {
		return `perl -e 'use POSIX; POSIX::setsid(); sleep ` + secs + `' & sleep 0.2; exit 0`
	}
	t.Skip("neither setsid nor perl")
	return ""
}

// TestDetachedChildEndsSession: the shell has exited, but a detached
// process keeps the slave open. The pane must still close.
func TestDetachedChildEndsSession(t *testing.T) {
	s := startTest(t, detachedCmd(t, "20"))
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done never closed after the shell ended")
	}
	s.fdMu.RLock()
	closed := s.fdClosed
	s.fdMu.RUnlock()
	if !closed {
		t.Fatal("PTY still open after Done")
	}
}

// TestFdNotReused: the read loop of a finished session must never read the
// PTY of a later session that got the same descriptor number.
func TestFdNotReused(t *testing.T) {
	s := startTest(t, detachedCmd(t, "6"))
	<-s.waited
	time.Sleep(time.Second)
	s2 := startTest(t, "sleep 0.5; for i in 1 2 3 4 5; do echo output-of-s2-$i; sleep 0.1; done; sleep 5")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(s2.Term.ScreenText(), "\n"), "output-of-s2-5") {
		if time.Now().After(deadline) {
			t.Fatalf("s2 did not get its output:\n%s", strings.Join(s2.Term.ScreenText(), "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(strings.Join(s.Term.ScreenText(), "\n"), "output-of-s2") {
		t.Fatal("the finished session reads the next one's PTY")
	}
}

// TestCloseKillsIgnoredHup: closing a pane also ends a program that ignores
// SIGHUP, and its children in the same group.
func TestCloseKillsIgnoredHup(t *testing.T) {
	s := startTest(t, "trap '' HUP; sleep 30 & while :; do sleep 0.2; done")
	time.Sleep(300 * time.Millisecond)
	pgid := s.Pid()
	s.Close()
	select {
	case <-s.waited:
	case <-time.After(closeGrace + 2*time.Second):
		t.Fatalf("still alive %v after Close", closeGrace+2*time.Second)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(-pgid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("processes of the group survive Close")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done never closed after Close")
	}
}

// TestKillWaits: Kill returns when the program is dead, at the latest after
// the grace period and the SIGKILL.
func TestKillWaits(t *testing.T) {
	s := startTest(t, "trap '' HUP; while :; do sleep 0.2; done")
	time.Sleep(300 * time.Millisecond)
	t0 := time.Now()
	s.Kill(500 * time.Millisecond)
	if syscall.Kill(s.Pid(), 0) == nil {
		select {
		case <-s.waited:
		case <-time.After(time.Second):
			t.Fatalf("still alive after Kill (%v)", time.Since(t0))
		}
	}
	// A shell that obeys SIGHUP exits right away.
	s2 := startTest(t, "sleep 30")
	time.Sleep(200 * time.Millisecond)
	t0 = time.Now()
	s2.Kill(3 * time.Second)
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("Kill waited %v for a program that exits on SIGHUP", d)
	}
}

// TestThawResumes: after a freeze then a thaw (failed handover), the
// session works and the read loop does not spin.
func TestThawResumes(t *testing.T) {
	s := startTest(t, "while read l; do echo \"read:$l\"; done")
	time.Sleep(200 * time.Millisecond)
	if _, _, err := s.Freeze(); err != nil {
		t.Fatal(err)
	}
	s.Thaw()
	var before, after unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &before)
	time.Sleep(500 * time.Millisecond)
	_ = unix.Getrusage(unix.RUSAGE_SELF, &after)
	cpu := time.Duration(after.Utime.Nano()+after.Stime.Nano()-before.Utime.Nano()-before.Stime.Nano()) * time.Nanosecond
	if cpu > 250*time.Millisecond {
		t.Fatalf("%v of CPU in 500 ms while idle after Thaw: the read loop spins", cpu)
	}
	s.Write([]byte("hello\r"))
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(strings.Join(s.Term.ScreenText(), "\n"), "read:hello") {
		if time.Now().After(deadline) {
			t.Fatalf("no reply after Thaw:\n%s", strings.Join(s.Term.ScreenText(), "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestFreezeHandsOverBlocking: the descriptor handed to exec goes back to
// blocking mode (what an older daemon expects) and survives exec.
func TestFreezeHandsOverBlocking(t *testing.T) {
	s := startTest(t, "sleep 30")
	time.Sleep(100 * time.Millisecond)
	fd, _, err := s.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	fl, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil || fl&unix.O_NONBLOCK != 0 {
		t.Fatalf("flags after Freeze: %#x %v", fl, err)
	}
	fdfl, err := unix.FcntlInt(fd, unix.F_GETFD, 0)
	if err != nil || fdfl&unix.FD_CLOEXEC != 0 {
		t.Fatalf("FD_CLOEXEC after Freeze: %#x %v", fdfl, err)
	}
	s.Thaw()
	fdfl, _ = unix.FcntlInt(fd, unix.F_GETFD, 0)
	if fdfl&unix.FD_CLOEXEC == 0 {
		t.Fatal("FD_CLOEXEC not restored by Thaw")
	}
}

// TestResizeKeepsNonblocking: resizing does not put the master back in
// blocking mode (os.File.Fd would on Linux).
func TestResizeKeepsNonblocking(t *testing.T) {
	s := startTest(t, "sleep 30")
	s.Resize(100, 30, 800, 600)
	_ = s.ForegroundName()
	fl, err := unix.FcntlInt(uintptr(s.fd), unix.F_GETFL, 0)
	if err != nil || fl&unix.O_NONBLOCK == 0 {
		t.Fatalf("master blocking after Resize: %#x %v", fl, err)
	}
	ws, err := unix.IoctlGetWinsize(s.fd, unix.TIOCGWINSZ)
	if err != nil || ws.Col != 100 || ws.Row != 30 {
		t.Fatalf("PTY size: %+v %v", ws, err)
	}
}

// TestCloseAfterExit: Close on a session that already ended does no harm.
func TestCloseAfterExit(t *testing.T) {
	s := startTest(t, "exit 0")
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("never ended")
	}
	within(t, time.Second, "Close", s.Close)
	if err := syscall.Kill(-s.Pid(), 0); err == nil || !errors.Is(err, syscall.ESRCH) {
		t.Logf("group %d: %v", s.Pid(), err)
	}
}
