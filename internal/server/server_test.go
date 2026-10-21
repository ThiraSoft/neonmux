package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// The daemon is tested for real: compiled binary, subprocess, Unix socket,
// then a full cycle create → detach → reattach → stop (SIGTERM, as on
// computer shutdown) → restore from disk.

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "neonmux")
	cmd := exec.Command("go", "build", "-o", bin, "../..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

type daemon struct {
	cmd  *exec.Cmd
	sock string
}

func startDaemon(t *testing.T, bin, tmp, state string) *daemon {
	t.Helper()
	sock := filepath.Join(tmp, "neonmux", "server.sock")
	cmd := exec.Command(bin, "server")
	cmd.Env = append(os.Environ(), "NEONMUX_SERVER_SOCK="+sock, "NEONMUX_STATE="+state, "SHELL=/bin/sh")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &daemon{cmd: cmd, sock: sock}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return d
}

func connect(t *testing.T, sock string) *Client {
	t.Helper()
	var cl *Client
	var err error
	for i := 0; i < 100; i++ {
		if cl, err = Connect(sock, nil); err == nil {
			return cl
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("connecting to the daemon: %v", err)
	return nil
}

func termText(term *vt.Terminal) string {
	term.Lock()
	defer term.Unlock()
	var b strings.Builder
	first, screen := term.AbsBase()
	cols, rows := term.Size()
	_ = cols
	var buf []vt.Cell
	for a := first; a < screen+int64(rows); a++ {
		line, _ := term.AbsLine(a, buf)
		var lb strings.Builder
		for _, c := range line {
			if c.Attr()&vt.AttrWideCont != 0 {
				continue
			}
			r := c.Rune()
			if r == 0 {
				r = ' '
			}
			lb.WriteRune(r)
		}
		b.WriteString(strings.TrimRight(lb.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func waitText(t *testing.T, r *Remote, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(termText(r.Terminal()), sub) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("%q not found in:\n%s", sub, termText(r.Terminal()))
}

func TestDaemonLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	// Unix socket: a short path is required (104 bytes max on macOS).
	tmp := shortTmp(t)
	state := filepath.Join(tmp, "state")

	d := startDaemon(t, bin, tmp, state)
	cl := connect(t, d.sock)
	r := cl.Create(CreateReq{Key: "k1", Dir: tmp, Cols: 60, Rows: 10, Scrollback: 100})
	r.Write([]byte("echo hello-neon\n"))
	waitText(t, r, "hello-neon\n")

	// The window closes: the session keeps running.
	r.Detach()
	cl.Close()
	time.Sleep(100 * time.Millisecond)

	cl2 := connect(t, d.sock)
	r2 := cl2.Attach("k1", 70, 12, 100)
	waitText(t, r2, "hello-neon")
	r2.Write([]byte("echo still-here\n"))
	waitText(t, r2, "still-here\n")
	// The resize requested on attach arrives in the stream.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r2.Terminal().Lock()
		c, _ := r2.Terminal().Size()
		r2.Terminal().Unlock()
		if c == 70 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c, _ := sizeOf(r2.Terminal()); c != 70 {
		t.Fatalf("width after reattach = %d", c)
	}

	// Shutdown: SIGTERM must save the session to disk.
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	_, _ = d.cmd.Process.Wait()
	if _, err := os.Stat(filepath.Join(state, "terms", "k1.snap")); err != nil {
		t.Fatalf("snapshot missing after SIGTERM: %v", err)
	}

	// "After the reboot": new daemon, the session no longer exists and is
	// restored from disk, history and separator included.
	d2 := startDaemon(t, bin, tmp, state)
	cl3 := connect(t, d2.sock)
	r3 := cl3.Attach("k1", 70, 12, 100)
	deadline = time.Now().Add(3 * time.Second)
	for !r3.Missing() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !r3.Missing() {
		t.Fatal("the session should no longer exist in the new daemon")
	}
	r4 := cl3.Create(CreateReq{Key: "k1", Cols: 70, Rows: 12, Scrollback: 100, Restore: true})
	waitText(t, r4, "session restored")
	waitText(t, r4, "still-here")
	r4.Write([]byte("echo after-reboot\n"))
	waitText(t, r4, "after-reboot\n")

	// Closing the pane really ends the session and erases the save.
	r4.Close()
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(state, "terms", "k1.snap")); !os.IsNotExist(err) {
		t.Fatalf("the save should be erased: %v", err)
	}
}

// TestExitClosesPane: a program that exits on its own (:q in nvim) closes
// its pane right away. An exit with an error or from a signal, as on
// computer shutdown, keeps the grace period.
func TestExitClosesPane(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, bin, tmp, filepath.Join(tmp, "state"))
	cl := connect(t, d.sock)
	for _, c := range []struct {
		code      string
		immediate bool
	}{{"0", true}, {"1", false}} {
		argv := []string{"/bin/sh", "-c", "read x; exit " + c.code}
		r := cl.Create(CreateReq{Key: "exit" + c.code, Argv: argv, Dir: tmp, Cols: 60, Rows: 10, Scrollback: 100})
		t0 := time.Now()
		r.Write([]byte("\n"))
		select {
		case <-r.Done():
		case <-time.After(exitGrace + 2*time.Second):
			t.Fatalf("exit %s: pane never closed", c.code)
		}
		took := time.Since(t0)
		if c.immediate && took > time.Second {
			t.Fatalf("exit %s: pane closed in %v, want right away", c.code, took)
		}
		if !c.immediate && took < exitGrace {
			t.Fatalf("exit %s: pane closed in %v, before the grace period", c.code, took)
		}
	}
}

func TestRelaunchable(t *testing.T) {
	allowed := []string{"nvim", "claude"}
	if got := relaunchable([]string{"/opt/homebrew/bin/nvim", "main.go"}, allowed); strings.Join(got, " ") != "nvim main.go" {
		t.Fatalf("nvim: %q", got)
	}
	if got := relaunchable([]string{"claude"}, allowed); strings.Join(got, " ") != "claude --continue" {
		t.Fatalf("claude: %q", got)
	}
	if got := relaunchable([]string{"make", "deploy"}, allowed); got != nil {
		t.Fatalf("make must never be relaunched: %q", got)
	}
	if got := shellQuote([]string{"nvim", "my file.go"}); got != "nvim 'my file.go'" {
		t.Fatalf("quote: %q", got)
	}
}

// TestHotUpgrade hot restarts the daemon (exec on the same binary) and
// checks that the shell goes on: same process, same variable, same PID,
// client reconnected and reattached on its own.
func TestHotUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, bin, tmp, filepath.Join(tmp, "state"))
	daemonPid := d.cmd.Process.Pid

	cl := connect(t, d.sock)
	r := cl.Create(CreateReq{Key: "hot", Dir: tmp, Cols: 70, Rows: 12, Scrollback: 100})
	// $((1+1)) only becomes 2 in the output, never in the typed command:
	// whatever the shell and its prompt, the marker only finds the output.
	r.Write([]byte("x=42; echo before-$((1+1)) pid=$$\n"))
	waitText(t, r, "before-2 pid=")
	before := termText(r.Terminal())
	i := strings.Index(before, "before-2 pid=")
	shellPid := strings.Fields(before[i+len("before-2 pid="):])[0]

	cl.Upgrade()
	// The client loses the connection, reconnects and reattaches on its own.
	time.Sleep(500 * time.Millisecond)
	r.Write([]byte("echo after x=$x pid=$$\n"))
	waitText(t, r, "after x=42 pid="+shellPid)
	if err := syscall.Kill(daemonPid, 0); err != nil {
		t.Fatalf("the daemon changed process: %v", err)
	}
	// The history from before the handover is still there.
	waitText(t, r, "before-2 pid="+shellPid)
}

func TestCaptureAndSendKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, bin, tmp, filepath.Join(tmp, "state"))
	cl := connect(t, d.sock)
	r := cl.Create(CreateReq{Key: "k1", Dir: tmp, Cols: 60, Rows: 10, Scrollback: 100})
	cli := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "NEONMUX_SOCK="+d.sock, "NEONMUX_PANE=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// The arithmetic only shows up evaluated in the output: a shell without
	// line editing (dash) echoes typed-ahead input before its prompt, so the
	// output does not always start a line.
	if out, err := cli("send-keys", "k1", "text:echo ok-$((1+1))", "enter", "text:echo capture-$((6*7))", "enter"); err != nil {
		t.Fatalf("send-keys: %v %s", err, out)
	}
	waitText(t, r, "capture-42")
	out, err := cli("capture", "k1")
	if err != nil || !strings.Contains(out, "ok-2") || !strings.Contains(out, "capture-42") {
		t.Fatalf("capture: %v\n%s", err, out)
	}
	// A pane can also be named by its program, as neonmux ls shows it (on
	// macOS, /bin/sh runs bash).
	ls, _ := cli("ls")
	name := ""
	for _, l := range strings.Split(ls, "\n") {
		if f := strings.Split(l, "\t"); len(f) >= 3 && f[0] == "k1" {
			name = f[2]
		}
	}
	if out, err := cli("capture", name); name == "" || err != nil || !strings.Contains(out, "capture-42") {
		t.Fatalf("capture by name %q: %v\n%s", name, err, out)
	}
	if out, err := cli("capture", "absent"); err == nil || !strings.Contains(out, "no pane") {
		t.Fatalf("unknown pane: %v\n%s", err, out)
	}
	if out, err := cli("send-keys", "k1", "cmd+e"); err == nil || !strings.Contains(out, "stay with neonmux") {
		t.Fatalf("cmd shortcut: %v\n%s", err, out)
	}
}

func TestRelaunchClaude(t *testing.T) {
	allowed := []string{"claude", "nvim"}
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"claude"}, "claude --continue"},
		// neonmux agent: neither the initial prompt nor --worktree (the
		// resume happens in the worktree directory).
		{[]string{"claude", "--worktree", "fix", "fix the bug"}, "claude --continue"},
		{[]string{"claude", "--model", "haiku", "--dangerously-skip-permissions", "explain"}, "claude --model haiku --dangerously-skip-permissions --continue"},
		{[]string{"claude", "-p", "summarize"}, "claude --continue"},
		{[]string{"claude", "--resume", "abc123"}, "claude --resume abc123"},
		{[]string{"/Users/x/.local/bin/claude", "-w", "trial"}, "claude --continue"},
		{[]string{"nvim", "main.go"}, "nvim main.go"},
		{[]string{"make", "test"}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(relaunchable(c.argv, allowed), " "); got != c.want {
			t.Errorf("%q: %q, want %q", c.argv, got, c.want)
		}
	}
}
