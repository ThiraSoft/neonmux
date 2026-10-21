package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/vt"
)

// Daemon robustness tests: a pane that does not read its input, a slow
// window, a daemon that disappears, a damaged hot resume or a booby-trapped
// file must neither block nor compromise anything else.

// shortTmp returns a unique directory in $TMPDIR, with a path short enough
// for a Unix socket (104 bytes at most on macOS).
func shortTmp(t *testing.T) string {
	t.Helper()
	tmp, err := os.MkdirTemp(os.TempDir(), "nmx")
	if err != nil {
		t.Fatal(err)
	}
	if len(tmp)+len("/neonmux/server.sock") >= 104 {
		t.Fatalf("socket path too long: %s", tmp)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	return tmp
}

// cliReq sends a CLI request to the daemon and returns its response.
func cliReq(sock, line string, wait time.Duration) (string, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(wait))
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		return "", err
	}
	var b strings.Builder
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
		if sc.Text() == "ok" {
			return b.String(), nil
		}
	}
	if sc.Err() == nil {
		return b.String(), io.ErrUnexpectedEOF
	}
	return b.String(), sc.Err()
}

// bareServer sets up a daemon inside the test process (without os.Exit)
// and without a socket: resume provides its own.
func bareServer(t *testing.T) *Server {
	t.Helper()
	tmp := shortTmp(t)
	s := newServer(filepath.Join(tmp, "s.sock"), filepath.Join(tmp, "state"))
	if err := os.MkdirAll(filepath.Join(s.dir, "terms"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.ln != nil {
			s.ln.Close()
		}
		for _, se := range s.list() {
			se.s.Kill(time.Second)
		}
	})
	return s
}

// inProcess starts a daemon inside the test process, on a socket in
// $TMPDIR. Optional setup: settings applied before the first connection.
func inProcess(t *testing.T, setup ...func(*Server)) (*Server, string) {
	t.Helper()
	s := bareServer(t)
	for _, f := range setup {
		f(s)
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	go s.acceptLoop()
	t.Cleanup(func() { s.upgrading.Store(true) }) // acceptLoop returns instead of looping
	return s, s.sock
}

// listenerFd returns a listening socket descriptor number, without an
// *os.File whose finalizer would close it under our feet.
func listenerFd(t *testing.T) uintptr {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(shortTmp(t), "u.sock"))
	if err != nil {
		t.Fatal(err)
	}
	lf, _ := ln.(*net.UnixListener).File()
	fd, err := unix.Dup(int(lf.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	lf.Close()
	ln.Close()
	return uintptr(fd)
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestInputStallAcrossPanes: a pane whose program does not read its input
// does not block input to the other panes of the same window.
func TestInputStallAcrossPanes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, bin, tmp, filepath.Join(tmp, "state"))
	cl := connect(t, d.sock)
	defer cl.Close()
	a := cl.Create(CreateReq{Key: "a", Argv: []string{"/bin/sh", "-c", "stty raw -echo; sleep 60"}, Dir: tmp, Cols: 80, Rows: 24})
	b := cl.Create(CreateReq{Key: "b", Dir: tmp, Cols: 80, Rows: 24})
	b.Write([]byte("echo ready-b\n"))
	waitText(t, b, "ready-b\n")
	chunk := []byte(strings.Repeat("x", 1023) + "\r")
	for i := 0; i < 300; i++ {
		a.Write(chunk)
	}
	time.Sleep(300 * time.Millisecond)
	b.Write([]byte("echo alive-b\n"))
	waitText(t, b, "alive-b\n")
}

// TestUpgradeWithStalledInput: a hot restart with input stuck in the queue
// does not block the daemon. The pending input moves to the new binary,
// sessions are saved before exec, and SIGTERM still stops the daemon.
func TestUpgradeWithStalledInput(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	state := filepath.Join(tmp, "state")
	d := startDaemon(t, bin, tmp, state)
	cl := connect(t, d.sock)
	defer cl.Close()
	const total = 300000
	a := cl.Create(CreateReq{Key: "a", Argv: []string{"/bin/sh", "-c", "stty raw -echo; sleep 3; head -c " + strconv.Itoa(total) + " | wc -c; sleep 60"}, Dir: tmp, Cols: 80, Rows: 24})
	time.Sleep(300 * time.Millisecond)
	a.Write(bytes.Repeat([]byte("x"), total))
	time.Sleep(300 * time.Millisecond)
	if out, err := cliReq(d.sock, `{"cmd":"restart-server"}`, 2*time.Second); err != nil {
		t.Fatalf("restart-server: %v %q", err, out)
	}
	time.Sleep(500 * time.Millisecond)
	out, err := cliReq(d.sock, `{"cmd":"ls"}`, 3*time.Second)
	if err != nil || !strings.Contains(out, "a\t") {
		t.Fatalf("after restart-server, ls: %v %q", err, out)
	}
	if _, err := os.Stat(filepath.Join(state, "terms", "a.snap")); err != nil {
		t.Errorf("no save before exec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "upgrade.json")); !os.IsNotExist(err) {
		t.Errorf("upgrade.json should be deleted after a successful resume: %v", err)
	}
	// The program finally reads its input: nothing was lost in the handover.
	waitText(t, a, strconv.Itoa(total))

	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() { _, _ = d.cmd.Process.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon ignores SIGTERM")
	}
}

// TestKillServerEndsIgnoredHup: kill-server also ends a program that
// ignores SIGHUP.
func TestKillServerEndsIgnoredHup(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, bin, tmp, filepath.Join(tmp, "state"))
	cl := connect(t, d.sock)
	defer cl.Close()
	a := cl.Create(CreateReq{Key: "a", Argv: []string{"/bin/sh", "-c", "trap '' HUP; echo pid=$$; while :; do sleep 0.2; done"}, Dir: tmp, Cols: 80, Rows: 24})
	waitText(t, a, "pid=")
	txt := termText(a.Terminal())
	pid, _ := strconv.Atoi(strings.Fields(txt[strings.Index(txt, "pid=")+4:])[0])
	if pid <= 1 {
		t.Fatalf("pid not found: %q", txt)
	}
	if out, err := cliReq(d.sock, `{"cmd":"kill-server"}`, 2*time.Second); err != nil {
		t.Fatalf("kill-server: %v %q", err, out)
	}
	waitFor(t, "the end of the program that ignores SIGHUP", killGrace+3*time.Second, func() bool {
		return syscall.Kill(pid, 0) != nil
	})
}

// TestSocketPermissions: socket with mode 0600 in a directory with mode 0700.
func TestSocketPermissions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, bin, tmp, filepath.Join(tmp, "state"))
	cl := connect(t, d.sock)
	cl.Close()
	st, err := os.Stat(d.sock)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", st.Mode(), err)
	}
	st, err = os.Stat(filepath.Dir(d.sock))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory: %v %v", st.Mode(), err)
	}
}

// TestPrivateDir: the socket directory must be ours, with mode 0700, and not
// a symlink (another user may have created it in /tmp before us).
func TestPrivateDir(t *testing.T) {
	tmp := shortTmp(t)
	fresh := filepath.Join(tmp, "a", "neonmux")
	if err := privateDir(fresh); err != nil {
		t.Fatalf("new directory: %v", err)
	}
	if st, _ := os.Stat(fresh); st.Mode().Perm() != 0o700 {
		t.Fatalf("mode of the new directory: %v", st.Mode())
	}
	if err := privateDir(fresh); err != nil {
		t.Fatalf("existing directory: %v", err)
	}
	open := filepath.Join(tmp, "open")
	_ = os.Mkdir(open, 0o700)
	_ = os.Chmod(open, 0o777)
	if err := privateDir(open); err == nil {
		t.Fatal("directory with mode 0777 accepted")
	}
	link := filepath.Join(tmp, "link")
	_ = os.Symlink(fresh, link)
	if err := privateDir(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

// TestPeerUID: the daemon reads the uid of the process at the other end of the socket.
func TestPeerUID(t *testing.T) {
	tmp := shortTmp(t)
	ln, err := net.Listen("unix", filepath.Join(tmp, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := net.Dial("unix", filepath.Join(tmp, "p.sock")); err == nil {
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if uid := peerUID(c.(*net.UnixConn)); uid != os.Getuid() {
		t.Fatalf("peer uid = %d, want %d", uid, os.Getuid())
	}
}

// TestResumeTapRace: on resume, the PTY already has pending bytes and
// reading starts before resume is done, so tap must not read the
// half-built session (a race, or even a nil pointer).
func TestResumeTapRace(t *testing.T) {
	s := bareServer(t)
	path, cmds := fakeUpgrade(t, s, 10, func(i int, u *upSess) {})
	if err := s.resume(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	for _, c := range cmds {
		_ = c.Process.Kill()
	}
}

// TestResumeExitingSessions: a resumed session that exits right away is
// removed from the table by its watcher while resume adds the next ones
// (go test -race).
func TestResumeExitingSessions(t *testing.T) {
	s := bareServer(t)
	path, _ := fakeUpgradeScript(t, s, 20, "printf 'already-here\\n'; exit 0", func(i int, u *upSess) {})
	if err := s.resume(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end of the sessions", 5*time.Second, func() bool { return len(s.list()) == 0 })
}

// fakeUpgrade prepares an upgrade.json as upgrade would leave it: a
// listener and n sessions whose output is already waiting in the PTY.
func fakeUpgrade(t *testing.T, s *Server, n int, edit func(i int, u *upSess)) (string, []*exec.Cmd) {
	t.Helper()
	return fakeUpgradeScript(t, s, n, "printf 'already-here\\n'; sleep 5", edit)
}

func fakeUpgradeScript(t *testing.T, s *Server, n int, script string, edit func(i int, u *upSess)) (string, []*exec.Cmd) {
	t.Helper()
	st := upState{Listener: listenerFd(t), At: time.Now()}
	var cmds []*exec.Cmd
	for i := 0; i < n; i++ {
		cmd := exec.Command("/bin/sh", "-c", script)
		f, err := pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
		time.Sleep(20 * time.Millisecond) // the output waits in the PTY
		fd, _ := unix.Dup(int(f.Fd()))
		f.Close()
		u := upSess{Key: "k" + strconv.Itoa(i), Fd: uintptr(fd), Pid: cmd.Process.Pid, Snap: vt.New(80, 24, 0).Snapshot()}
		edit(i, &u)
		st.Sessions = append(st.Sessions, u)
	}
	t.Cleanup(func() {
		for _, c := range cmds {
			_ = c.Process.Kill()
		}
	})
	data, _ := json.Marshal(st)
	path := filepath.Join(s.dir, "upgrade.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, cmds
}

// TestResumeBadSnapshot: an unreadable snapshot does not make the session
// get dropped (a program with no reader, a descriptor leaking into the next
// shells): it is resumed with a blank terminal.
func TestResumeBadSnapshot(t *testing.T) {
	s := bareServer(t)
	var fd uintptr
	path, _ := fakeUpgrade(t, s, 1, func(i int, u *upSess) {
		u.Snap = []byte("not a snapshot")
		fd = u.Fd
	})
	if err := s.resume(path); err != nil {
		t.Fatal(err)
	}
	if s.get("k0") == nil {
		t.Fatal("session dropped because of its snapshot")
	}
	if fl, err := unix.FcntlInt(fd, unix.F_GETFD, 0); err != nil || fl&unix.FD_CLOEXEC == 0 {
		t.Fatalf("the resumed PTY would leak into the next shells: %#x %v", fl, err)
	}
	waitFor(t, "the pending output", 3*time.Second, func() bool {
		return strings.Contains(strings.Join(s.get("k0").s.Term.ScreenText(), "\n"), "already-here")
	})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("upgrade.json should be deleted after the resume: %v", err)
	}
}

// TestResumeBadJSONKeepsFile: an unreadable upgrade.json is not deleted.
func TestResumeBadJSONKeepsFile(t *testing.T) {
	s := bareServer(t)
	path := filepath.Join(s.dir, "upgrade.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	if err := s.resume(path); err == nil {
		t.Fatal("resume of an unreadable file accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("upgrade.json deleted although the resume failed: %v", err)
	}
}

// TestResumeOldFormat: an upgrade.json written by the previous version
// (without the fields added since) can be read back.
func TestResumeOldFormat(t *testing.T) {
	s := bareServer(t)
	lfd := listenerFd(t)
	cmd := exec.Command("/bin/sh", "-c", "sleep 5")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	fd, _ := unix.Dup(int(f.Fd()))
	f.Close()
	term := vt.New(40, 5, 100)
	_, _ = term.Write([]byte("old-format\r\n"))
	old := fmt.Sprintf(`{"listener":%d,"sessions":[{"key":"v1","fd":%d,"pid":%d,"snap":%q,"pending":"bell","saved_seq":3}],"at":"2026-10-20T10:00:00Z"}`,
		lfd, fd, cmd.Process.Pid, term.Snapshot())
	// %q is not base64: go through json.Marshal for the snap field.
	snap, _ := json.Marshal(term.Snapshot())
	old = strings.Replace(old, fmt.Sprintf("%q", term.Snapshot()), string(snap), 1)
	path := filepath.Join(s.dir, "upgrade.json")
	_ = os.WriteFile(path, []byte(old), 0o600)
	if err := s.resume(path); err != nil {
		t.Fatal(err)
	}
	se := s.get("v1")
	if se == nil || !strings.Contains(strings.Join(se.s.Term.ScreenText(), "\n"), "old-format") {
		t.Fatal("session in the old format resumed incorrectly")
	}
	if p := se.pending.Load(); p == nil || *p != "bell" {
		t.Fatal("pending notification lost")
	}
}

// TestRelaunchRefusesControlChars: the relaunch is typed into the shell as
// keystrokes. An argument with a control character (a booby-trapped file
// name) must never be typed: ^C would cancel the line and the rest would
// run.
func TestRelaunchRefusesControlChars(t *testing.T) {
	cases := []struct {
		argv []string
		cwd  string
		ok   bool
	}{
		{[]string{"nvim", "main.go"}, "", true},
		{[]string{"nvim", "my file.go"}, "/tmp/a b", true},
		{[]string{"nvim", "été.go"}, "", true},
		{[]string{"nvim", "a\x03echo PWNED\r"}, "", false},
		{[]string{"nvim", "a\x7f"}, "", false},
		{[]string{"nvim", "a\u009bb"}, "", false}, // 8-bit CSI
		{[]string{"nvim", "a\xffb"}, "", false},   // invalid UTF-8
		{[]string{"nvim", "x"}, "/tmp/\x1b[201~", false},
	}
	for _, c := range cases {
		got := relaunchCmd(c.argv, c.cwd, "/home")
		if (got != "") != c.ok {
			t.Errorf("%q in %q: %q", c.argv, c.cwd, got)
		}
	}
	if got := relaunchCmd([]string{"nvim", "x"}, "/tmp/a b", "/home"); got != "cd '/tmp/a b' && nvim x" {
		t.Errorf("cd: %q", got)
	}
}

// TestRelaunchInjectionEndToEnd: restoring a session whose save points to
// a booby-trapped file. The command hidden in its name must never run in
// the restored shell.
func TestRelaunchInjectionEndToEnd(t *testing.T) {
	s, _ := inProcess(t)
	term := vt.New(80, 10, 100)
	_, _ = term.Write([]byte("before\r\n"))
	_ = os.WriteFile(s.snapPath("k"), term.Snapshot(), 0o600)
	m, _ := json.Marshal(savedMeta{Dir: os.TempDir(), FgArgv: []string{"/usr/bin/nvim", "a\x03echo PWNED-$((6*7))\r"}})
	_ = os.WriteFile(s.metaPath("k"), m, 0o600)
	a, b := net.Pipe()
	defer a.Close()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	// A line interpreter that ignores SIGINT from the start (inherited from
	// the test), like an interactive shell that is ready: ^C clears the
	// current line and the rest goes through on its own. A shell that is not
	// ready yet dies from the ^C instead.
	signal.Ignore(syscall.SIGINT)
	defer signal.Reset(syscall.SIGINT)
	sh := []string{"/bin/sh", "-c", `while IFS= read -r l; do eval "$l"; done`}
	s.create(newConn(a, nil), CreateReq{Key: "k", Argv: sh, Cols: 80, Rows: 10, Scrollback: 100,
		Restore: true, Relaunch: []string{"nvim"}})
	se := s.get("k")
	if se == nil {
		t.Fatal("session not created")
	}
	time.Sleep(1500 * time.Millisecond)
	if strings.Contains(strings.Join(se.s.Term.ScreenText(), "\n"), "PWNED-42") {
		t.Fatalf("injected command ran:\n%s", strings.Join(se.s.Term.ScreenText(), "\n"))
	}
}

// TestWriteAtomicConcurrent: two simultaneous saves of the same file (loop
// and shutdown) do not step on each other.
func TestWriteAtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.snap")
	var wg sync.WaitGroup
	var fails atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if writeAtomic(path, bytes.Repeat([]byte{byte('a' + i)}, 4096)) != nil {
					fails.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	if fails.Load() > 0 {
		t.Fatalf("%d failed writes", fails.Load())
	}
	data, _ := os.ReadFile(path)
	if len(data) != 4096 || bytes.Count(data, data[:1]) != 4096 {
		t.Fatal("mixed-up file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) > 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

// startSess starts a real session in the test daemon.
func startSess(t *testing.T, s *Server, key, script string) *sess {
	t.Helper()
	se := &sess{key: key}
	term := vt.New(80, 24, 100)
	sh, err := shell.Start(shell.Options{Argv: []string{"/bin/sh", "-c", script}, Dir: os.TempDir(), Cols: 80, Rows: 24, Term: term,
		Tap: func(b []byte) { s.tap(se, term, b) }})
	if err != nil {
		t.Fatal(err)
	}
	se.s = sh
	s.mu.Lock()
	s.sessions[key] = se
	s.mu.Unlock()
	return se
}

// TestMetaAndSaveRaces: metadata and saves shared between the loop,
// attaches and shutdown, without races (go test -race).
func TestMetaAndSaveRaces(t *testing.T) {
	s, _ := inProcess(t)
	se := startSess(t, s, "m", "while :; do echo tick; sleep 0.05; done")
	a, b := net.Pipe()
	defer a.Close()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	c := newConn(a, nil)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); s.refresh([]*sess{se}) }()
		go func() { defer wg.Done(); s.attach(c, "m", 80, 24) }()
		go func() { defer wg.Done(); s.save(se, true) }()
	}
	wg.Wait()
	if _, err := os.Stat(s.snapPath("m")); err != nil {
		t.Fatal(err)
	}
}

// TestWatchAfterFailedUpgrade: a session that exited during a failed
// handover does not stay in the table.
func TestWatchAfterFailedUpgrade(t *testing.T) {
	s, _ := inProcess(t)
	s.upgrading.Store(true)
	se := startSess(t, s, "w", "exit 0")
	done := make(chan struct{})
	go func() { s.watch(se); close(done) }()
	time.Sleep(500 * time.Millisecond)
	s.upgrading.Store(false) // the handover failed
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watch does not return")
	}
	if s.get("w") != nil {
		t.Fatal("exited session left in the table")
	}
}

// fakeDaemon accepts connections on sock and passes them to handle.
func fakeDaemon(t *testing.T, sock string, handle func(net.Conn)) (net.Listener, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			go handle(c)
		}
	}()
	return ln, &n
}

// TestClientLost: the daemon disappears and does not come back in time.
// The client declares itself lost, accepts nothing more without ever
// blocking, then reconnects on its own when the daemon comes back.
func TestClientLost(t *testing.T) {
	old := reconnectWait
	reconnectWait = 300 * time.Millisecond
	defer func() { reconnectWait = old }()
	tmp := shortTmp(t)
	sock := filepath.Join(tmp, "s")
	conns := make(chan net.Conn, 4)
	ln, _ := fakeDaemon(t, sock, func(c net.Conn) { conns <- c })
	cl, err := Connect(sock, func() error { return errors.New("daemon not found") })
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	rm := cl.Create(CreateReq{Key: "k", Cols: 80, Rows: 24})
	c := <-conns
	ln.Close()
	os.Remove(sock)
	c.Close() // the daemon dies and does not come back
	select {
	case <-rm.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the loss was not reported")
	}
	if cl.Alive() || !cl.Lost() || !rm.Lost() {
		t.Fatalf("after the loss: Alive=%v Lost=%v rm.Lost=%v", cl.Alive(), cl.Lost(), rm.Lost())
	}
	done := make(chan struct{})
	go func() {
		r2 := cl.Create(CreateReq{Key: "k2", Cols: 80, Rows: 24})
		for i := 0; i < 10000; i++ {
			r2.Write([]byte("a"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sends block after the daemon was lost")
	}
	// The daemon comes back: the client reconnects in the background.
	_, _ = fakeDaemon(t, sock, func(c net.Conn) { conns <- c })
	waitFor(t, "the reconnection", 5*time.Second, cl.Alive)
}

// TestClientSendNeverBlocks: during a reconnection, the send queue never
// blocks the caller (the Ebiten thread).
func TestClientSendNeverBlocks(t *testing.T) {
	tmp := shortTmp(t)
	sock := filepath.Join(tmp, "s")
	ln, _ := fakeDaemon(t, sock, func(c net.Conn) {}) // never reads
	defer ln.Close()
	cl, err := Connect(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	rm := cl.Attach("k", 80, 24, 0)
	done := make(chan struct{})
	go func() {
		chunk := bytes.Repeat([]byte("z"), 4096)
		for i := 0; i < 20000; i++ {
			rm.Write(chunk)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Remote.Write blocks when the daemon does not read")
	}
}

// TestClientSkipsOversizedFrame: a frame that is too large is skipped,
// without disconnecting or reconnecting in a loop.
func TestClientSkipsOversizedFrame(t *testing.T) {
	tmp := shortTmp(t)
	sock := filepath.Join(tmp, "s")
	ln, n := fakeDaemon(t, sock, func(c net.Conn) {
		_, _ = io.Copy(io.Discard, io.LimitReader(c, 1)) // waits for the attach
		w := bufio.NewWriter(c)
		hdr := []byte{MsgSnapshot, 1, 'k'}
		hdr = binary.LittleEndian.AppendUint32(hdr, maxFrame+1)
		_, _ = w.Write(hdr)
		_, _ = w.Write(make([]byte, maxFrame+1))
		_ = writeFrame(w, frame{typ: MsgOutput, key: "k", data: []byte("after-the-frame")})
		_ = w.Flush()
		_, _ = io.Copy(io.Discard, c)
	})
	defer ln.Close()
	cl, err := Connect(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	rm := cl.Attach("k", 40, 5, 0)
	waitFor(t, "the next frame", 10*time.Second, func() bool {
		return strings.Contains(strings.Join(rm.Terminal().ScreenText(), "\n"), "after-the-frame")
	})
	time.Sleep(300 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("%d connections: the client reconnected", n.Load())
	}
}

// TestHello: a recent window learns the daemon's version; with an older
// daemon (which ignores the frame), nothing changes.
func TestHello(t *testing.T) {
	_, sock := inProcess(t)
	cl, err := Connect(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	waitFor(t, "the hello reply", 3*time.Second, func() bool { return cl.ServerProto() == protoVersion })

	// Old daemon: it ignores unknown types and does not answer.
	tmp := shortTmp(t)
	old := filepath.Join(tmp, "old")
	got := make(chan frame, 8)
	ln, _ := fakeDaemon(t, old, func(c net.Conn) {
		r := bufio.NewReader(c)
		for {
			f, err := readFrame(r)
			if err != nil {
				return
			}
			got <- f
		}
	})
	defer ln.Close()
	cl2, err := Connect(old, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl2.Close()
	cl2.Attach("k", 10, 5, 0)
	for f := range got {
		if f.typ == MsgAttach {
			break
		}
		if f.typ != MsgHello {
			t.Fatalf("unexpected frame before the attach: %d", f.typ)
		}
	}
	if cl2.ServerProto() != 0 || !cl2.Alive() {
		t.Fatal("an old daemon must stay usable")
	}
}

// TestOldClientGetsNoHello: a window from the previous version never
// receives a frame it does not know.
func TestOldClientGetsNoHello(t *testing.T) {
	s, sock := inProcess(t)
	startSess(t, s, "k", "echo hello; sleep 5")
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := bufio.NewWriter(c)
	data, _ := json.Marshal(AttachReq{Cols: 80, Rows: 24})
	_ = writeFrame(w, frame{typ: MsgAttach, key: "k", data: data})
	_ = w.Flush()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	r := bufio.NewReader(c)
	for {
		f, err := readFrame(r)
		if err != nil {
			break
		}
		if f.typ > MsgUpgrade {
			t.Fatalf("frame type unknown to the old window: %d", f.typ)
		}
	}
}

// TestConnCoalesce: consecutive outputs of a session go out in a single
// frame.
func TestConnCoalesce(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	k := makeConn(a, nil) // writeLoop not started yet: the queue fills up
	for i := 0; i < 100; i++ {
		k.sendOutput("k", []byte("ab"))
	}
	k.sendOutput("j", []byte("cd"))
	k.mu.Lock()
	n := len(k.q)
	k.mu.Unlock()
	go k.writeLoop()
	if n != 2 {
		t.Fatalf("%d frames queued, want 2", n)
	}
	r := bufio.NewReader(b)
	f, err := readFrame(r)
	if err != nil || f.typ != MsgOutput || f.key != "k" || len(f.data) != 200 {
		t.Fatalf("first frame: %v %d %q %d", err, f.typ, f.key, len(f.data))
	}
}

// TestConnResync: a window that cannot keep up with a session's stream is
// no longer disconnected: that session's pending output is dropped, then a
// fresh snapshot resyncs it once the queue has drained.
func TestConnResync(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	resynced := make(chan string, 4)
	var k *conn
	k = newConn(a, func(key string) {
		k.unstale(key)
		k.send(frame{typ: MsgSnapshot, key: key, data: []byte("snapshot")})
		resynced <- key
	})
	chunk := bytes.Repeat([]byte("o"), 64*1024)
	for i := 0; i < 2*outBudget/len(chunk); i++ {
		k.sendOutput("k", chunk)
	}
	k.sendOutput("other", []byte("x"))
	select {
	case <-k.closed:
		t.Fatal("connection dropped for a single lagging session")
	default:
	}
	// The window finally reads: it receives the catch-up snapshot, and the
	// other session lost nothing.
	r := bufio.NewReader(b)
	var gotSnap, gotOther bool
	deadline := time.Now().Add(5 * time.Second)
	for !(gotSnap && gotOther) && time.Now().Before(deadline) {
		_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
		f, err := readFrame(r)
		if err != nil {
			break
		}
		gotSnap = gotSnap || (f.typ == MsgSnapshot && f.key == "k")
		gotOther = gotOther || (f.typ == MsgOutput && f.key == "other")
	}
	if !gotSnap || !gotOther {
		t.Fatalf("catch-up snapshot %v, output of the other session %v", gotSnap, gotOther)
	}
}

// TestConnResyncAlone: the queue only held the lagging session's output;
// once dropped, it leaves the queue empty, and the snapshot must still go
// out.
func TestConnResyncAlone(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	var k *conn
	k = makeConn(a, func(key string) {
		k.unstale(key)
		k.send(frame{typ: MsgSnapshot, key: key, data: []byte("snapshot")})
	})
	chunk := bytes.Repeat([]byte("o"), 64*1024)
	for i := 0; i < 2*outBudget/len(chunk); i++ {
		k.sendOutput("k", chunk)
	}
	go k.writeLoop()
	r := bufio.NewReader(b)
	_ = b.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := readFrame(r)
	if err != nil || f.typ != MsgSnapshot {
		t.Fatalf("first frame: %v %d", err, f.typ)
	}
}

// TestFitSnapshot: a snapshot too big for a frame loses its oldest
// history, never the screen or the recent lines.
func TestFitSnapshot(t *testing.T) {
	term := vt.New(80, 10, 5000)
	for i := 0; i < 3000; i++ {
		_, _ = term.Write([]byte(fmt.Sprintf("line %d %s\r\n", i, strings.Repeat("=", 60))))
	}
	snap := term.Snapshot()
	limit := len(snap) / 3
	fit := fitSnapshot(snap, limit)
	if len(fit) > limit {
		t.Fatalf("%d bytes, limit %d", len(fit), limit)
	}
	got := vt.New(2, 1, 0)
	if err := got.LoadSnapshot(fit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got.ScreenText(), "\n"), "line 2999") {
		t.Fatal("screen lost")
	}
	got.Lock()
	n := got.HistoryLen()
	got.Unlock()
	if n == 0 || n >= 3000 {
		t.Fatalf("history of %d lines", n)
	}
	if small := term.Snapshot(); len(fitSnapshot(small, len(small))) != len(small) {
		t.Fatal("a snapshot that fits must not change")
	}
}

// TestCLILimits: a CLI request that never arrives, or an oversized one,
// does not hold a daemon goroutine forever.
func TestCLILimits(t *testing.T) {
	_, sock := inProcess(t, func(s *Server) { s.cliTimeout = 300 * time.Millisecond })
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte(`{"cmd":"ls"`)) // never an end of line
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("the daemon did not cut off the unfinished request: %v", err)
	}
	c2, _ := net.Dial("unix", sock)
	defer c2.Close()
	go func() {
		big := append([]byte(`{"cmd":"ls","args":["`), bytes.Repeat([]byte("a"), 2*maxCLILine)...)
		_, _ = c2.Write(big)
	}()
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	out, _ := io.ReadAll(c2)
	if !strings.Contains(string(out), "error") {
		t.Fatalf("oversized request: %q", out)
	}
}

// errListener returns an error on every Accept, as with EMFILE.
type errListener struct {
	net.Listener
	calls atomic.Int32
}

func (l *errListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	return nil, errors.New("too many open files")
}

// TestAcceptBackoff: a repeated Accept error (EMFILE) does not make
// acceptLoop spin.
func TestAcceptBackoff(t *testing.T) {
	s := newServer("", t.TempDir())
	l := &errListener{}
	s.ln = l
	go s.acceptLoop()
	time.Sleep(300 * time.Millisecond)
	s.upgrading.Store(true)
	n := l.calls.Load()
	go func() { <-s.acceptStopped }()
	if n > 20 {
		t.Fatalf("%d calls to Accept in 300 ms", n)
	}
}
