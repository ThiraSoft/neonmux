package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ThiraSoft/neonmux/internal/server"
)

// Subcommands that talk to the daemon (restart included, which goes
// through a fallthrough) must send their request, not print the help of
// another command. With no reachable daemon, the connection error is expected.
func TestCommandsReachTheDaemon(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "neonmux")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	absent := filepath.Join(t.TempDir(), "absent.sock")
	for _, args := range [][]string{{"restart"}, {"ls"}, {"capture", "x"}, {"send-keys", "x", "enter"}} {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "NEONMUX_SOCK="+absent, "NEONMUX_SERVER_SOCK="+absent)
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), "no reachable neonmux instance") {
			t.Errorf("neonmux %s: %q", strings.Join(args, " "), out)
		}
	}
}

// staleSocket leaves a socket with nobody behind it, like a killed
// window.
func staleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
}

// deadPID returns the PID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// shortTemp creates a temporary directory with a short path, since a Unix
// socket path cannot exceed 104 bytes on macOS.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	return dir
}

// The window socket lives in the daemon's private (0700) directory, not at
// a predictable path in the shared /tmp.
func TestWindowSocketPrivate(t *testing.T) {
	tmp := shortTemp(t)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("NEONMUX_SERVER_SOCK", filepath.Join(tmp, "inst", "server.sock"))
	p, err := windowSocket()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != server.InstanceDir() || len(p) > maxSockPath {
		t.Fatalf("socket %s", p)
	}
	if st, err := os.Stat(filepath.Dir(p)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", st.Mode(), err)
	}
	// Daemon directory too long for a socket: fall back to a short private
	// directory.
	t.Setenv("NEONMUX_SERVER_SOCK", filepath.Join(tmp, strings.Repeat("d", 100), "server.sock"))
	if p, err = windowSocket(); err != nil || len(p) > maxSockPath || !strings.HasPrefix(p, tmp) {
		t.Fatalf("fallback: %s %v", p, err)
	}
	if st, err := os.Stat(filepath.Dir(p)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("fallback directory: %v %v", st.Mode(), err)
	}
}

// Sockets of windows that are gone are cleaned up in the new location, and
// so are the old leftovers in os.TempDir() if they are ours.
func TestCleanStaleSockets(t *testing.T) {
	tmp := shortTemp(t)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("NEONMUX_SERVER_SOCK", filepath.Join(tmp, "inst", "server.sock"))
	if _, err := windowSocket(); err != nil {
		t.Fatal(err)
	}
	dead := strconv.Itoa(deadPID(t))
	stale := filepath.Join(server.InstanceDir(), "gui-"+dead+".sock")
	old := filepath.Join(tmp, "neonmux-"+dead+".sock")
	alive := filepath.Join(server.InstanceDir(), "gui-"+strconv.Itoa(os.Getppid())+".sock")
	for _, p := range []string{stale, old, alive} {
		staleSocket(t, p)
	}
	cleanStaleSockets()
	for p, want := range map[string]bool{stale: false, old: false, alive: true} {
		if _, err := os.Lstat(p); (err == nil) != want {
			t.Errorf("%s: present %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
