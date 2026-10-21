package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ThiraSoft/neonmux/internal/shell"
)

// Forced restart: the way out when something is stuck. It relies on
// neither the daemon nor the window: it finds the processes itself, ends
// them along with all their descendants (SIGTERM, then SIGKILL), erases the
// saved state and starts from scratch.

// InstanceDir is the socket directory, which holds the PID files of the daemon and windows.
func InstanceDir() string { return filepath.Dir(SocketPath()) }

// WritePidFile records the PID of a process of the instance and returns
// the function that removes it. Nothing is written to a directory that is
// not ours (a link put there by another user would be followed).
func WritePidFile(name string) func() {
	if privateDir(InstanceDir()) != nil {
		return func() {}
	}
	p := filepath.Join(InstanceDir(), name)
	_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
	return func() { os.Remove(p) }
}

func readPid(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func alive(pid int) bool { return pid > 1 && syscall.Kill(pid, 0) == nil }

// daemonPID finds the daemon through its socket, even if it no longer answers.
func daemonPID() int {
	c, err := net.DialTimeout("unix", SocketPath(), 500*time.Millisecond)
	if err != nil {
		return 0
	}
	defer c.Close()
	if uc, ok := c.(*net.UnixConn); ok {
		return peerPID(uc)
	}
	return 0
}

var cliCommands = []string{"open", "notify", "fx", "theme", "toast", "rename", "float", "diff", "agent", "capture", "send-keys", "ls", "kill-server", "restart", "restart-server", "icon"}

// ForceReport summarizes a forced restart.
type ForceReport struct {
	Roots   int // daemon and windows found
	Killed  int // processes ended
	Forced  int // of which finished off with SIGKILL
	Escaped []int
}

// ForceKill ends the daemon, the windows and all their descendants, then
// erases the state (saved sessions, layout, socket). The config is kept.
func ForceKill() (ForceReport, error) {
	var rep ForceReport
	self := os.Getpid()
	selfPgid, _ := syscall.Getpgid(self)
	inst := InstanceDir()

	// While this runs, no window may start a daemon again (automatic
	// reconnection would do it if the daemon died before the window).
	// PID files in a directory that is not ours are not read: another user
	// could point them at any of our processes.
	trusted := privateDir(inst) == nil
	lock := filepath.Join(inst, "force.lock")
	if trusted {
		_ = os.WriteFile(lock, []byte(strconv.Itoa(self)), 0o600)
		defer os.Remove(lock)
	}

	guiRoots := map[int]bool{}
	daemonRoots := map[int]bool{}
	if pid := daemonPID(); alive(pid) {
		daemonRoots[pid] = true
	}
	var guis []string
	if trusted {
		if pid := readPid(filepath.Join(inst, "server.pid")); alive(pid) {
			daemonRoots[pid] = true
		}
		guis, _ = filepath.Glob(filepath.Join(inst, "gui-*.pid"))
	}
	for _, g := range guis {
		if pid := readPid(g); alive(pid) {
			guiRoots[pid] = true
		}
	}
	procs, err := shell.ListProcs()
	if err != nil {
		return rep, err
	}
	// Default instance: also catch the processes of a version that did not
	// write PID files yet. An isolated instance (tests,
	// NEONMUX_SERVER_SOCK) only touches its own processes.
	if os.Getenv("NEONMUX_SERVER_SOCK") == "" && os.Getenv("NEONMUX_STATE") == "" {
		for _, p := range procs {
			if p.Comm != "neonmux" || p.Pid == self {
				continue
			}
			argv := shell.ProcArgs(p.Pid)
			switch {
			case len(argv) > 1 && argv[1] == "server":
				daemonRoots[p.Pid] = true
			case len(argv) > 1 && slices.Contains(cliCommands, argv[1]):
			default:
				guiRoots[p.Pid] = true
			}
		}
	}
	delete(guiRoots, self)
	delete(daemonRoots, self)
	rep.Roots = len(guiRoots) + len(daemonRoots)

	// All descendants, collected before killing anything: once their parent
	// is dead, orphans would be reparented to launchd.
	children := map[int][]int{}
	pgidOf := map[int]int{}
	for _, p := range procs {
		children[p.Ppid] = append(children[p.Ppid], p.Pid)
		pgidOf[p.Pid] = p.Pgid
	}
	seen := map[int]bool{}
	tree := func(roots map[int]bool) map[int]bool {
		out := map[int]bool{}
		queue := make([]int, 0, len(roots))
		for pid := range roots {
			queue = append(queue, pid)
		}
		for len(queue) > 0 {
			pid := queue[0]
			queue = queue[1:]
			if seen[pid] || pid == self {
				continue
			}
			seen[pid] = true
			out[pid] = true
			queue = append(queue, children[pid]...)
		}
		return out
	}
	// Windows first, then the daemon: otherwise a window that sees its
	// daemon disappear could start a new one before dying.
	waves := []map[int]bool{tree(guiRoots), tree(daemonRoots)}
	for _, targets := range waves {
		killed, forced, escaped := killTree(targets, pgidOf, selfPgid)
		rep.Killed += killed
		rep.Forced += forced
		rep.Escaped = append(rep.Escaped, escaped...)
	}

	// Clean slate: nothing is restored on the next launch.
	if trusted {
		os.Remove(SocketPath())
		os.Remove(filepath.Join(inst, "server.pid"))
	}
	for _, g := range guis {
		os.Remove(g)
	}
	state := StateDir()
	snaps, _ := filepath.Glob(filepath.Join(state, "terms", "*"))
	for _, f := range snaps {
		os.Remove(f)
	}
	os.Remove(filepath.Join(state, "layout.json"))
	os.Remove(filepath.Join(state, "upgrade.json"))
	return rep, nil
}

// killTree sends SIGTERM to the targets and their groups, waits, then
// finishes off the survivors with SIGKILL.
func killTree(targets map[int]bool, pgidOf map[int]int, selfPgid int) (killed, forced int, escaped []int) {
	// Process groups of the targets, to catch detached jobs (nohup…). Only
	// groups led by a target: a window started by a script shares that
	// script's group, which must not be killed.
	groups := map[int]bool{}
	for pid := range targets {
		if g := pgidOf[pid]; g > 1 && g != selfPgid && targets[g] {
			groups[g] = true
		}
	}
	signalAll := func(sig syscall.Signal) {
		for g := range groups {
			_ = syscall.Kill(-g, sig)
		}
		for pid := range targets {
			_ = syscall.Kill(pid, sig)
		}
	}
	remaining := func() []int {
		var left []int
		for pid := range targets {
			if alive(pid) {
				left = append(left, pid)
			}
		}
		return left
	}
	signalAll(syscall.SIGTERM)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && len(remaining()) > 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if left := remaining(); len(left) > 0 {
		forced = len(left)
		signalAll(syscall.SIGKILL)
		time.Sleep(300 * time.Millisecond)
	}
	escaped = remaining()
	return len(targets) - len(escaped), forced, escaped
}

// Forcing reports that a forced restart is in progress: nobody may start
// a daemon again in the meantime.
func Forcing() bool {
	st, err := os.Stat(filepath.Join(InstanceDir(), "force.lock"))
	return err == nil && time.Since(st.ModTime()) < 30*time.Second
}

func (r ForceReport) String() string {
	s := fmt.Sprintf("%d processes ended (%d daemon(s) and window(s), plus their terminals)", r.Killed, r.Roots)
	if r.Forced > 0 {
		s += fmt.Sprintf(", %d of them finished off with SIGKILL", r.Forced)
	}
	if len(r.Escaped) > 0 {
		s += fmt.Sprintf(", %d still alive: %v", len(r.Escaped), r.Escaped)
	}
	return s
}
