// Package shell runs a program in a pseudo-terminal and wires its output
// to a vt.Terminal.
package shell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/ThiraSoft/neonmux/vt"
)

// Session connects a process to a virtual terminal.
type Session struct {
	Term *vt.Terminal
	key  string

	feedMu sync.Mutex
	tap    func([]byte)
	frozen bool   // frozen for a hot restart: no more reads
	lp     *loops // running loops, recreated by Thaw (under feedMu)

	proc *os.Process
	pty  *os.File
	// fd is the master's number, in non-blocking mode. We never call
	// pty.Fd() again: on Linux, Fd() puts the descriptor back in blocking mode.
	fd int
	// fdClosed: the PTY is closed and its number may already refer to another
	// file. Every system call on fd goes through withFd.
	fdMu     sync.RWMutex
	fdClosed bool

	inMu    sync.Mutex
	inBuf   []byte        // input not yet accepted by the PTY
	inReady chan struct{} // wakes writeLoop when inBuf fills up
	dropped atomic.Int64  // input bytes thrown away, queue full

	exited   atomic.Bool
	success  atomic.Bool   // exited on its own with status 0
	hup      atomic.Bool   // process reaped without the PTY ending: stop reading
	closing  atomic.Bool   // Close requested: the read loop closes the PTY
	waited   chan struct{} // process reaped, success set
	done     chan struct{} // end of the process (PTY output drained)
	wakeR    *os.File      // wakes readLoop (freeze, end, close)
	wakeW    *os.File
	wwakeR   *os.File // wakes writeLoop while it waits for the PTY to accept
	wwakeW   *os.File
	wakeFd   int // read ends of both pipes, for poll
	wwakeFd  int
	killOnce sync.Once
	killed   chan struct{} // Kill is done (process dead or finished off)
	onOutput func()
}

// loops is the set of channels of one readLoop/writeLoop pair.
type loops struct {
	readStop chan struct{} // the read loop has ended (end or freeze)
	quitW    chan struct{} // asks the write loop to stop
	wStop    chan struct{} // the write loop has ended
	once     sync.Once
}

func newLoops() *loops {
	return &loops{readStop: make(chan struct{}), quitW: make(chan struct{}), wStop: make(chan struct{})}
}

const (
	// maxInput caps a session's pending input. A program that stops reading
	// its input loses whatever goes over, instead of blocking the window or
	// the daemon writing to it.
	maxInput = 4 << 20
	// closeGrace is how long a program gets to exit on SIGHUP before the
	// SIGKILL, when its pane is closed.
	closeGrace = 2500 * time.Millisecond
)

// Options describes the process to start.
type Options struct {
	Argv       []string // empty: $SHELL as a login shell
	Dir        string
	Env        []string // added to the current environment
	Cols, Rows int
	Scrollback int
	OnOutput   func() // called after each batch of output (wakes the renderer)

	Key  string       // stable session id (persistence)
	Term *vt.Terminal // terminal already filled in (restore), otherwise a new one
	// Tap gets each batch of output right after it is parsed, under the same
	// lock as Atomic: no byte lost or duplicated on reattach.
	Tap func([]byte)
}

// DefaultShell returns the user's shell, even when the app is launched from
// the Finder without $SHELL in its environment.
func DefaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if s := loginShell(); s != "" {
		return s
	}
	return "/bin/sh"
}

// Start starts the session.
func Start(o Options) (*Session, error) {
	argv := o.Argv
	if len(argv) == 0 {
		sh := DefaultShell()
		// argv[0] prefixed with "-": the shell behaves as a login shell.
		cmd := exec.Command(sh)
		cmd.Args = []string{"-" + filepath.Base(sh)}
		return start(cmd, o)
	}
	return start(exec.Command(argv[0], argv[1:]...), o)
}

func start(cmd *exec.Cmd, o Options) (*Session, error) {
	cmd.Dir = o.Dir
	env := os.Environ()
	env = append(env,
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=neonmux",
		"TERM_PROGRAM_VERSION=0.1",
	)
	env = append(env, o.Env...)
	cmd.Env = env

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(o.Cols), Rows: uint16(o.Rows)})
	if err != nil {
		return nil, err
	}
	term := o.Term
	if term == nil {
		term = vt.New(o.Cols, o.Rows, o.Scrollback)
	} else {
		term.Resize(o.Cols, o.Rows)
	}
	return run(f, cmd.Process, term, o.Key, o.Tap, o.OnOutput)
}

// AdoptOptions describes a session taken over after a hot restart of the
// daemon: the PTY and the process already exist, only the program changed.
type AdoptOptions struct {
	Fd       uintptr
	Pid      int
	Key      string
	Term     *vt.Terminal
	Tap      func([]byte)
	OnOutput func()
}

// Adopt takes over a session handed over by Freeze.
func Adopt(o AdoptOptions) (*Session, error) {
	f := os.NewFile(o.Fd, "/dev/ptmx")
	if f == nil {
		return nil, errors.New("invalid PTY descriptor")
	}
	unix.CloseOnExec(int(o.Fd))
	proc, err := os.FindProcess(o.Pid)
	if err != nil {
		return nil, err
	}
	return run(f, proc, o.Term, o.Key, o.Tap, o.OnOutput)
}

func run(f *os.File, proc *os.Process, term *vt.Terminal, key string, tap func([]byte), onOutput func()) (*Session, error) {
	// Fd() puts the descriptor back in blocking mode (on Linux, Go had made
	// it non-blocking): we take the number once and for all, then make it
	// non-blocking for good. Neither reads nor writes can then get stuck in
	// a system call.
	// On failure, the master is closed right away (the program hangs up):
	// left to f's finalizer, it would be closed later, under a number maybe
	// already reused by another PTY.
	fd := int(f.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		f.Close()
		return nil, err
	}
	wr, ww, err := os.Pipe()
	if err != nil {
		f.Close()
		return nil, err
	}
	wwr, www, err := os.Pipe()
	if err != nil {
		wr.Close()
		ww.Close()
		f.Close()
		return nil, err
	}
	wakeFd, wwakeFd := int(wr.Fd()), int(wwr.Fd())
	_ = unix.SetNonblock(wakeFd, true)
	_ = unix.SetNonblock(wwakeFd, true)
	s := &Session{
		Term: term, key: key, tap: tap, proc: proc, pty: f, fd: fd,
		inReady: make(chan struct{}, 1),
		waited:  make(chan struct{}),
		done:    make(chan struct{}),
		killed:  make(chan struct{}),
		wakeR:   wr, wakeW: ww, wwakeR: wwr, wwakeW: www,
		wakeFd: wakeFd, wwakeFd: wwakeFd,
		lp:       newLoops(),
		onOutput: onOutput,
	}
	s.Term.SetReply(func(b []byte) { s.Write(b) })
	go s.readLoop(s.lp)
	go s.writeLoop(s.lp)
	go func() {
		// After a hot restart, the process is still our child (same PID):
		// Wait works too.
		st, _ := proc.Wait()
		s.success.Store(st != nil && st.Success())
		close(s.waited)
		// Let the read loop drain the PTY buffer. If a detached process
		// keeps the slave open, the end of the PTY will never come: we wake
		// the read loop so it closes the PTY itself. Closing it from here
		// would not wake it on Linux, and the descriptor number would be
		// reused right away by the next PTY.
		select {
		case <-s.done:
		case <-time.After(300 * time.Millisecond):
			s.hup.Store(true)
			wake(s.wakeW)
		}
	}()
	return s, nil
}

// wake writes one byte to a wake-up pipe without ever blocking (a full pipe
// already wakes its loop). Does nothing on a closed pipe.
func wake(w *os.File) {
	rc, err := w.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Write(func(fd uintptr) bool {
		_, _ = unix.Write(int(fd), []byte{0})
		return true
	})
}

// drain empties a wake-up pipe (non-blocking).
func drain(fd int) {
	var b [64]byte
	for {
		if n, err := unix.Read(fd, b[:]); n <= 0 || err != nil {
			return
		}
	}
}

// readLoop waits with poll() until the PTY has data (or something wakes it),
// then reads under the session lock. Once the session is frozen, no read
// starts anymore: no byte is left in memory.
// It alone closes the PTY, once writing has stopped (finish).
func (s *Session) readLoop(lp *loops) {
	defer close(lp.readStop)
	buf := make([]byte, 64*1024)
	fds := []unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLIN}, {Fd: int32(s.wakeFd), Events: unix.POLLIN}}
	for {
		fds[0].Revents, fds[1].Revents = 0, 0
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			s.finish(lp)
			return
		}
		if fds[1].Revents != 0 {
			drain(s.wakeFd)
		}
		s.feedMu.Lock()
		if s.frozen {
			s.feedMu.Unlock()
			return
		}
		if s.closing.Load() || s.hup.Load() {
			s.feedMu.Unlock()
			s.finish(lp)
			return
		}
		if fds[0].Revents == 0 {
			s.feedMu.Unlock()
			continue
		}
		n, err := unix.Read(s.fd, buf)
		if n > 0 {
			// Terminal replies (ESC [ c, OSC 4…) go out through Write, which
			// never blocks: reading cannot get stuck behind a program that
			// does not read what it asks for.
			s.Term.Write(buf[:n])
			if s.tap != nil {
				s.tap(buf[:n])
			}
		}
		s.feedMu.Unlock()
		if n > 0 && s.onOutput != nil {
			s.onOutput()
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			s.finish(lp) // EOF or EIO: the slave is closed, the process is done
			return
		}
	}
}

// finish ends the session: writing stopped, PTY and pipes closed, then
// Done. Only the read loop calls it, exactly once.
func (s *Session) finish(lp *loops) {
	s.exited.Store(true)
	lp.stopWrite(s)
	<-lp.wStop
	s.fdMu.Lock()
	s.fdClosed = true
	s.pty.Close()
	s.fdMu.Unlock()
	s.wakeR.Close()
	s.wakeW.Close()
	s.wwakeR.Close()
	s.wwakeW.Close()
	s.inMu.Lock()
	s.inBuf = nil
	s.inMu.Unlock()
	close(s.done)
	if s.onOutput != nil {
		s.onOutput()
	}
}

// stopWrite asks the write loop to stop, even while it waits for the PTY to
// accept input.
func (lp *loops) stopWrite(s *Session) {
	lp.once.Do(func() { close(lp.quitW) })
	wake(s.wwakeW)
}

// writeLoop writes to the program in its own goroutine, without ever
// blocking: when the PTY is full, it waits with poll() until it accepts
// again, or until a freeze or a close wakes it.
func (s *Session) writeLoop(lp *loops) {
	defer close(lp.wStop)
	fds := []unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLOUT}, {Fd: int32(s.wwakeFd), Events: unix.POLLIN}}
	for {
		select {
		case <-s.inReady:
		case <-lp.quitW:
			// Freeze: whatever the PTY still accepts goes out before the
			// handover, the rest is passed to the new daemon (PendingInput).
			_, _ = s.writeSome()
			return
		}
		for {
			left, err := s.writeSome()
			if left == 0 {
				break
			}
			if err != nil && !errors.Is(err, unix.EAGAIN) {
				return // EIO: nobody reads the slave anymore
			}
			fds[0].Revents, fds[1].Revents = 0, 0
			if _, err := unix.Poll(fds, -1); err != nil && !errors.Is(err, unix.EINTR) {
				return
			}
			if fds[1].Revents != 0 {
				drain(s.wwakeFd)
			}
			select {
			case <-lp.quitW:
				_, _ = s.writeSome()
				return
			default:
			}
		}
	}
}

// writeSome writes what the PTY accepts without blocking and returns what is left.
func (s *Session) writeSome() (left int, err error) {
	s.inMu.Lock()
	defer s.inMu.Unlock()
	for len(s.inBuf) > 0 {
		n, err := unix.Write(s.fd, s.inBuf)
		if n > 0 {
			s.inBuf = s.inBuf[n:]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return len(s.inBuf), err
		}
		if n <= 0 {
			return len(s.inBuf), unix.EAGAIN
		}
	}
	s.inBuf = nil // frees the memory of a large paste
	return 0, nil
}

// Freeze stops the session without touching the process, for a hot restart
// of the daemon: no more reads, writing stopped, PTY descriptor kept open
// across exec and made blocking, as a daemon of any version expects.
// Returns what Adopt needs. The terminal snapshot and the input still queued
// (PendingInput) are to be taken afterwards.
// Nothing here waits for the program: it may no longer read anything.
func (s *Session) Freeze() (fd uintptr, pid int, err error) {
	s.feedMu.Lock()
	lp := s.lp
	s.frozen = true
	s.feedMu.Unlock()
	wake(s.wakeW)
	<-lp.readStop
	lp.stopWrite(s)
	<-lp.wStop
	if s.exited.Load() {
		return 0, 0, errors.New("session ended")
	}
	if err = unix.SetNonblock(s.fd, false); err == nil {
		_, err = unix.FcntlInt(uintptr(s.fd), unix.F_SETFD, 0)
	}
	if err != nil {
		s.Thaw()
		return 0, 0, err
	}
	return uintptr(s.fd), s.Pid(), nil
}

// Thaw restarts a frozen session whose handover failed (exec refused).
func (s *Session) Thaw() {
	unix.CloseOnExec(s.fd)
	_ = unix.SetNonblock(s.fd, true)
	lp := newLoops()
	s.feedMu.Lock()
	s.frozen = false
	s.lp = lp
	s.feedMu.Unlock()
	go s.readLoop(lp)
	go s.writeLoop(lp)
	s.kickWrite() // input kept during the freeze goes out again
}

// PendingInput returns the input the PTY has not accepted yet, to hand over
// to the new daemon after Freeze.
func (s *Session) PendingInput() []byte {
	s.inMu.Lock()
	defer s.inMu.Unlock()
	return append([]byte(nil), s.inBuf...)
}

// Dropped returns the number of input bytes thrown away for lack of room.
func (s *Session) Dropped() int64 { return s.dropped.Load() }

// Write sends bytes to the program (keyboard, paste, replies). It never
// blocks: input waits in a queue of at most maxInput bytes, which writeLoop
// empties as fast as the program reads. A write that no longer fits in the
// queue is thrown away whole, never cut: a truncated paste would leave the
// program in paste mode.
func (s *Session) Write(b []byte) {
	if s.exited.Load() || len(b) == 0 {
		return
	}
	s.inMu.Lock()
	if len(s.inBuf)+len(b) > maxInput {
		s.inMu.Unlock()
		s.dropped.Add(int64(len(b)))
		return
	}
	s.inBuf = append(s.inBuf, b...)
	s.inMu.Unlock()
	s.kickWrite()
}

func (s *Session) kickWrite() {
	select {
	case s.inReady <- struct{}{}:
	default:
	}
}

// withFd runs fn on the master as long as it is open.
func (s *Session) withFd(fn func(fd int)) bool {
	s.fdMu.RLock()
	defer s.fdMu.RUnlock()
	if s.fdClosed {
		return false
	}
	fn(s.fd)
	return true
}

// fgPgid returns the PTY's foreground group, 0 if it is closed.
func (s *Session) fgPgid() int {
	pgid := 0
	s.withFd(func(fd int) {
		if g, err := tcgetpgrp(fd); err == nil {
			pgid = g
		}
	})
	return pgid
}

// Resize passes the new size to the kernel (SIGWINCH on the program side).
func (s *Session) Resize(cols, rows, pxW, pxH int) {
	s.Term.Resize(cols, rows)
	s.Term.SetPixelSize(pxW, pxH)
	s.withFd(func(fd int) {
		_ = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{
			Row: uint16(rows), Col: uint16(cols), Xpixel: uint16(pxW), Ypixel: uint16(pxH),
		})
	})
}

// Atomic runs fn while no output is parsed in the meantime (snapshot +
// stream subscription, ordered resize).
func (s *Session) Atomic(fn func()) {
	s.feedMu.Lock()
	defer s.feedMu.Unlock()
	fn()
}

// Terminal returns the session's virtual terminal.
func (s *Session) Terminal() *vt.Terminal { return s.Term }

// Key returns the session's stable id.
func (s *Session) Key() string { return s.key }

// Detach does nothing for a local session: it lives with the window.
func (s *Session) Detach() {}

// Foreground returns the command line and directory of the foreground
// process, or nothing if it is the shell itself.
func (s *Session) Foreground() (argv []string, cwd string) {
	pgid := s.fgPgid()
	if pgid <= 0 || pgid == s.Pid() {
		return nil, ""
	}
	return procArgs(pgid), procCwd(pgid)
}

// Exited reports whether the process has ended.
func (s *Session) Exited() bool { return s.exited.Load() }

// Done is closed when the PTY output is drained.
func (s *Session) Done() <-chan struct{} { return s.done }

// Succeeded waits at most d for the process to end and reports whether it
// exited on its own with status 0 (not killed by a signal, not an error).
func (s *Session) Succeeded(d time.Duration) bool {
	select {
	case <-s.waited:
		return s.success.Load()
	case <-time.After(d):
		return false
	}
}

// Pid returns the PID of the main process.
func (s *Session) Pid() int {
	if s.proc == nil {
		return 0
	}
	return s.proc.Pid
}

// ForegroundName returns the name of the foreground process (nvim, go, ssh…).
func (s *Session) ForegroundName() string {
	pgid := s.fgPgid()
	if pgid <= 0 {
		return ""
	}
	return displayName(procName(pgid), func() []string { return procArgs(pgid) })
}

// ForegroundCwd returns the directory of the foreground process, or nothing
// if it is the shell itself. An agent can change directory (Claude Code
// moves into its worktree) without the shell moving.
func (s *Session) ForegroundCwd() string {
	pgid := s.fgPgid()
	if pgid <= 0 || pgid == s.Pid() {
		return ""
	}
	return procCwd(pgid)
}

var shellNames = map[string]bool{
	"zsh": true, "bash": true, "fish": true, "sh": true, "dash": true, "ksh": true,
	"tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true,
}

// IsShell reports a shell ("-zsh", the name of a login shell, included).
func IsShell(name string) bool { return shellNames[strings.TrimPrefix(name, "-")] }

// LeftForShell reports that the foreground just handed control back to the
// shell: a program (not a shell) was running, now it is a shell. A wrapper
// script (bash launching claude without exec) therefore triggers nothing.
func LeftForShell(prev, cur string) bool {
	return prev != "" && !IsShell(prev) && IsShell(cur)
}

// displayName fixes the short name of a process that took a version number
// as its title (Claude Code is called "2.1.304"): the name of the program
// launched, argv[0], says more.
func displayName(name string, args func() []string) string {
	if !looksLikeVersion(name) {
		return name
	}
	if a := args(); len(a) > 0 && a[0] != "" {
		return filepath.Base(a[0])
	}
	return name
}

func looksLikeVersion(s string) bool {
	dots := 0
	for i, r := range s {
		switch {
		case r == '.' && i > 0 && s[i-1] != '.':
			dots++
		case r < '0' || r > '9':
			return false
		}
	}
	return dots >= 1 && !strings.HasSuffix(s, ".")
}

// Cwd returns the shell's current directory.
func (s *Session) Cwd() string {
	if d := s.Term.Cwd(); d != "" {
		return d
	}
	return procCwd(s.Pid())
}

// Close ends the session without waiting (see Kill): it serves the daemon
// as well as a window without a daemon. SIGHUP goes out before it returns;
// only the wait and the SIGKILL happen in the background.
func (s *Session) Close() {
	s.killOnce.Do(func() { go s.reap(s.hangup(), closeGrace) })
}

// Kill ends the session: SIGHUP to the main process's group and to the
// foreground group, PTY closed (the slave hangs up), then SIGKILL to the
// groups that still have members after grace. A program that ignores
// SIGHUP therefore does not survive, unseen, the closing of its pane. Kill
// returns when everything is dead, around grace at the latest.
func (s *Session) Kill(grace time.Duration) {
	s.killOnce.Do(func() { go s.reap(s.hangup(), grace) })
	<-s.killed
}

// hangup sends SIGHUP to the session's groups, asks the read loop to close
// the PTY and returns the groups it targeted.
func (s *Session) hangup() []int {
	var groups []int
	select {
	case <-s.waited:
		// Already reaped: its group number may have been reused, so we no
		// longer signal anything.
	default:
		self := unix.Getpgrp()
		// The main process leads its session and its group (setsid).
		for _, g := range []int{s.Pid(), s.fgPgid()} {
			if g > 1 && g != self && !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
	}
	for _, g := range groups {
		_ = unix.Kill(-g, unix.SIGHUP)
	}
	s.closing.Store(true)
	wake(s.wakeW)
	return groups
}

// reap waits at most grace for the groups to empty, then finishes off the
// survivors with SIGKILL.
func (s *Session) reap(groups []int, grace time.Duration) {
	defer close(s.killed)
	alive := func() bool {
		for _, g := range groups {
			if unix.Kill(-g, 0) == nil {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(grace)
	for alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	for _, g := range groups {
		if unix.Kill(-g, 0) == nil {
			_ = unix.Kill(-g, unix.SIGKILL)
		}
	}
}
