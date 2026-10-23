package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/ThiraSoft/neonmux/internal/keys"
	"github.com/ThiraSoft/neonmux/internal/remote"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/vt"
)

const (
	saveEvery   = 15 * time.Second
	exitGrace   = 3 * time.Second // delay before forgetting a finished session
	idleTimeout = 60 * time.Second
	// outBudget caps the program output queued for a window. Beyond it,
	// only the overflowing session is resynced with a fresh snapshot. The
	// other frames (snapshots, metadata) get an extra maxFrame of headroom;
	// past that cap, the window is disconnected and reattaches with fresh
	// snapshots.
	outBudget = 16 << 20
	// coalesceMax caps a coalesced output frame.
	coalesceMax = 256 << 10
	// killGrace is how long programs get to exit on SIGHUP before SIGKILL,
	// when kill-server stops the daemon.
	killGrace = 2500 * time.Millisecond
	// maxCLILine caps a CLI request.
	maxCLILine = 1 << 20
	// defaultScrollback is used for the blank terminal of a resumed session
	// whose snapshot is unreadable.
	defaultScrollback = 10000
	// cliTimeout caps how long a CLI connection may last.
	cliTimeout = 10 * time.Second
)

// conn is a window connection. Its send queue never blocks the PTYs:
// consecutive outputs of a session are coalesced in it, and a session whose
// window can no longer keep up has its pending output replaced by a fresh
// snapshot, without touching the others.
type conn struct {
	c      net.Conn
	mu     sync.Mutex
	q      []frame         // pending frames, in order
	bytes  int             // bytes queued or being written
	stale  map[string]bool // sessions whose output is dropped until their snapshot
	ready  chan struct{}   // wakes writeLoop (one slot)
	resync func(key string)
	closed chan struct{}
	once   sync.Once
}

func newConn(c net.Conn, resync func(key string)) *conn {
	k := makeConn(c, resync)
	go k.writeLoop()
	return k
}

func makeConn(c net.Conn, resync func(key string)) *conn {
	return &conn{c: c, stale: map[string]bool{}, ready: make(chan struct{}, 1), resync: resync, closed: make(chan struct{})}
}

func (k *conn) close() {
	k.once.Do(func() {
		close(k.closed)
		k.c.Close()
	})
}

func (k *conn) isClosed() bool {
	select {
	case <-k.closed:
		return true
	default:
		return false
	}
}

func (k *conn) kick() {
	select {
	case k.ready <- struct{}{}:
	default:
	}
}

// send queues a frame. A frame larger than maxFrame is dropped (the window
// could not read it); a full queue closes the connection.
func (k *conn) send(f frame) bool {
	if len(f.data) > maxFrame {
		log.Printf("dropped frame %d of %d bytes for %q", f.typ, len(f.data), f.key)
		return false
	}
	k.mu.Lock()
	if k.isClosed() {
		k.mu.Unlock()
		return false
	}
	if k.bytes+len(f.data) > outBudget+maxFrame {
		k.mu.Unlock()
		k.close() // the window will reattach with fresh snapshots
		return false
	}
	k.q = append(k.q, f)
	k.bytes += len(f.data)
	k.mu.Unlock()
	k.kick()
	return true
}

// sendOutput queues program output, coalesced with the previous one if it
// belongs to the same session and is still waiting. Over budget, the
// pending output of that session is dropped and the session is marked:
// writeLoop will request its snapshot once the queue has drained.
func (k *conn) sendOutput(key string, data []byte) {
	k.mu.Lock()
	defer k.kick()
	defer k.mu.Unlock()
	if k.isClosed() || k.stale[key] {
		return
	}
	if k.bytes+len(data) > outBudget {
		kept := k.q[:0]
		for _, f := range k.q {
			if f.typ == MsgOutput && f.key == key {
				k.bytes -= len(f.data)
				continue
			}
			kept = append(kept, f)
		}
		clear(k.q[len(kept):])
		k.q = kept
		k.stale[key] = true
		return
	}
	if n := len(k.q); n > 0 && k.q[n-1].typ == MsgOutput && k.q[n-1].key == key && len(k.q[n-1].data)+len(data) <= coalesceMax {
		k.q[n-1].data = append(k.q[n-1].data, data...)
	} else {
		k.q = append(k.q, frame{typ: MsgOutput, key: key, data: append([]byte(nil), data...)})
	}
	k.bytes += len(data)
}

// unstale reopens the stream of a session. Call it under the session's read
// lock (Atomic), right before queueing its snapshot.
func (k *conn) unstale(key string) {
	k.mu.Lock()
	delete(k.stale, key)
	k.mu.Unlock()
}

func (k *conn) sendJSON(typ byte, key string, v any) {
	data, _ := json.Marshal(v)
	k.send(frame{typ: typ, key: key, data: data})
}

func (k *conn) writeLoop() {
	w := bufio.NewWriterSize(k.c, 64*1024)
	for {
		select {
		case <-k.ready:
		case <-k.closed:
			return
		}
		for {
			// Lagging sessions first: dropping their output may have
			// emptied the queue at once.
			k.resyncStale()
			k.mu.Lock()
			batch := k.q
			k.q = nil
			k.mu.Unlock()
			if len(batch) == 0 {
				break
			}
			for _, f := range batch {
				err := writeFrame(w, f)
				k.mu.Lock()
				k.bytes -= len(f.data)
				k.mu.Unlock()
				if err != nil && !errors.Is(err, errFrameTooBig) {
					k.close()
					return
				}
			}
			if w.Flush() != nil {
				k.close()
				return
			}
		}
	}
}

// resyncStale requests a fresh snapshot of the lagging sessions once the
// queue is half empty.
func (k *conn) resyncStale() {
	k.mu.Lock()
	if len(k.stale) == 0 || k.bytes > outBudget/2 || k.resync == nil {
		k.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(k.stale))
	for key := range k.stale {
		keys = append(keys, key)
	}
	k.mu.Unlock()
	for _, key := range keys {
		k.resync(key)
	}
}

type sess struct {
	key       string
	s         *shell.Session
	ephemeral atomic.Bool // spare not adopted yet: nothing on disk
	att       atomic.Pointer[conn]
	pending   atomic.Pointer[string]

	metaMu sync.Mutex
	meta   Meta   // last metadata sent
	lastFg string // foreground at the last pass of loop

	saveMu   sync.Mutex // one save at a time (loop, shutdown, handover)
	savedSeq uint64
	gone     bool // closed: nothing on disk anymore

	watchersMu  sync.Mutex
	watchers    map[remote.Watcher]struct{}
	numWatchers atomic.Int32
	owner       atomic.Value // sessionOwner: val is *conn, remote.Watcher, or nil (nil means window)
	guiCols     int          // last size the window asked for
	guiRows     int
	exitOnce    sync.Once
	exited      atomic.Bool
}

// swapMeta stores m and reports whether it changed.
func (se *sess) swapMeta(m Meta) bool {
	se.metaMu.Lock()
	defer se.metaMu.Unlock()
	changed := m != se.meta
	se.meta = m
	return changed
}

// Server is the `neonmux server` daemon.
type Server struct {
	sock, dir string
	mu        sync.Mutex
	sessions  map[string]*sess
	colors    *Colors
	gui       *conn
	conns     int
	idleSince time.Time
	shutting  atomic.Bool
	upgrading atomic.Bool
	ln        net.Listener

	acceptStopped chan struct{}
	resumeAccept  chan struct{}
	removePid     func()
	cliTimeout    time.Duration
}

func newServer(sock, dir string) *Server {
	return &Server{sock: sock, dir: dir, sessions: map[string]*sess{}, idleSince: time.Now(),
		acceptStopped: make(chan struct{}, 1), resumeAccept: make(chan struct{}), cliTimeout: cliTimeout}
}

// list returns the sessions, copied under the lock so they can then be
// walked without holding it.
func (s *Server) list() []*sess {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*sess, 0, len(s.sessions))
	for _, se := range s.sessions {
		out = append(out, se)
	}
	return out
}

// Run starts the daemon and only returns when it stops. resume is the
// state file left by a hot restart (empty otherwise).
func Run(sock, dir, resume string) error {
	if err := privateDir(filepath.Dir(sock)); err != nil {
		if resume == "" {
			return err
		}
		// The socket is already open: refusing to resume would kill the
		// programs of every session.
		log.Printf("socket directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "terms"), 0o700); err != nil {
		return err
	}
	s := newServer(sock, dir)
	if resume != "" {
		if err := s.resume(resume); err != nil {
			return fmt.Errorf("resume after restart: %w", err)
		}
	} else {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return fmt.Errorf("a daemon is already running on %s", sock)
		}
		_ = os.Remove(sock)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return err
		}
		s.ln = ln
	}
	// Only the user can connect to it (the directory already ensures that,
	// and each connection is checked against the peer's uid).
	_ = os.Chmod(sock, 0o600)

	// The forced restart finds the daemon through this file (or the socket).
	s.removePid = WritePidFile("server.pid")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigs
		// Computer shutdown or logout: save everything before leaving.
		s.shutdown(false)
	}()
	go s.loop()
	for {
		s.acceptLoop()
		// Accepting stopped: either the daemon is shutting down or a hot
		// handover is underway. If the handover fails, upgrade resumes accepting.
		<-s.resumeAccept
	}
}

func (s *Server) acceptLoop() {
	delay := 5 * time.Millisecond
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.shutting.Load() {
				select {}
			}
			if s.upgrading.Load() {
				s.acceptStopped <- struct{}{}
				return
			}
			// Too many open files (EMFILE) or something else: back off
			// instead of spinning.
			time.Sleep(delay)
			delay = min(2*delay, time.Second)
			continue
		}
		delay = 5 * time.Millisecond
		go s.serve(c)
	}
}

// upState is handed to the new binary by a hot restart. Any added field
// must stay optional: the next binary reads the file of an older version,
// and the other way around.
type upState struct {
	Listener uintptr   `json:"listener"`
	Colors   *Colors   `json:"colors,omitempty"`
	Sessions []upSess  `json:"sessions"`
	At       time.Time `json:"at"`
}

type upSess struct {
	Key      string  `json:"key"`
	Fd       uintptr `json:"fd"`
	Pid      int     `json:"pid"`
	Snap     []byte  `json:"snap"`
	Pending  string  `json:"pending,omitempty"`
	SavedSeq uint64  `json:"saved_seq"`
	// Input the program had not read yet when it was frozen.
	Input []byte `json:"input,omitempty"`
}

// upgrade replaces the daemon with the binary on disk (after an update)
// without interrupting programs: same PID, PTYs and socket kept open across
// exec, terminal state handed over. Nothing here waits on I/O while holding
// s.mu: the daemon keeps answering during the handover, and a SIGTERM can
// still save everything.
func (s *Server) upgrade() {
	if !s.upgrading.CompareAndSwap(false, true) {
		return
	}
	ul, ok := s.ln.(*net.UnixListener)
	if !ok {
		s.upgrading.Store(false)
		return
	}
	// Stop accepting: new connections wait in the kernel queue and will be
	// served by the new binary.
	_ = ul.SetDeadline(time.Now())
	<-s.acceptStopped
	lf, err := ul.File()
	if err == nil {
		_, err = unix.FcntlInt(lf.Fd(), unix.F_SETFD, 0)
	}
	if err != nil {
		s.abortUpgrade(nil, err)
		return
	}
	s.mu.Lock()
	st := upState{Listener: lf.Fd(), Colors: s.colors, At: time.Now()}
	var list []*sess
	for key, se := range s.sessions {
		if se.ephemeral.Load() {
			// The spare was never used: the window will prepare another one.
			se.s.Close()
			delete(s.sessions, key)
			continue
		}
		list = append(list, se)
	}
	s.mu.Unlock()
	// Freeze outside the lock. Freeze no longer waits for the program, which
	// may read nothing: its pending input travels with the state.
	var frozen []*sess
	ups := map[*sess]upSess{}
	for _, se := range list {
		fd, pid, err := se.s.Freeze()
		if err != nil {
			continue // session already finished
		}
		frozen = append(frozen, se)
		snap := se.s.Term.Snapshot()
		// Save before exec: if the new binary does not take the sessions
		// over, the window restores them from an up-to-date disk.
		seq := s.saveSnap(se, true, snap)
		u := upSess{Key: se.key, Fd: fd, Pid: pid, Snap: snap, SavedSeq: seq, Input: se.s.PendingInput()}
		if p := se.pending.Load(); p != nil {
			u.Pending = *p
		}
		ups[se] = u
	}
	// A session closed during the freeze is not handed over: thaw it so it
	// ends here, without leaving its descriptor to the new binary.
	s.mu.Lock()
	kept := frozen[:0]
	for _, se := range frozen {
		if s.sessions[se.key] != se {
			se.s.Thaw()
			continue
		}
		kept = append(kept, se)
		st.Sessions = append(st.Sessions, ups[se])
	}
	frozen = kept
	s.mu.Unlock()
	path := filepath.Join(s.dir, "upgrade.json")
	data, _ := json.Marshal(st)
	if err := writeAtomic(path, data); err != nil {
		s.abortUpgrade(frozen, err)
		return
	}
	exe, err := os.Executable()
	if err == nil {
		log.Printf("hot restart: %d session(s) handed to %s", len(st.Sessions), exe)
		err = syscall.Exec(exe, []string{exe, "server", "--resume", path}, os.Environ())
	}
	// exec only returns if it failed: carry on as before.
	os.Remove(path)
	lf.Close()
	s.abortUpgrade(frozen, err)
}

func (s *Server) abortUpgrade(frozen []*sess, err error) {
	log.Printf("hot restart failed: %v", err)
	for _, se := range frozen {
		se.s.Thaw()
	}
	if ul, ok := s.ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Time{})
	}
	s.upgrading.Store(false)
	s.resumeAccept <- struct{}{}
}

// resume takes over the state handed over by upgrade: socket, PTYs and
// processes are still there, only the program has changed. The file is
// only deleted once the resume is done: if unreadable, it stays for
// investigation.
func (s *Server) resume(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var st upState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	lf := os.NewFile(st.Listener, "listener")
	ln, err := net.FileListener(lf)
	lf.Close()
	if err != nil {
		return err
	}
	s.ln = ln
	s.colors = st.Colors
	n := 0
	for _, u := range st.Sessions {
		term := vt.New(2, 1, 0)
		if err := term.LoadSnapshot(u.Snap); err != nil {
			// Unreadable snapshot: the program goes on with a blank terminal
			// (the window will resize it), rather than with no reader and a
			// descriptor that would leak into every new shell.
			log.Printf("session %s: unreadable snapshot (%v), blank terminal", u.Key, err)
			term = vt.New(80, 24, defaultScrollback)
		}
		se := &sess{key: u.Key, savedSeq: u.SavedSeq, watchers: make(map[remote.Watcher]struct{})}
		sh, err := shell.Adopt(shell.AdoptOptions{
			Fd: u.Fd, Pid: u.Pid, Key: u.Key, Term: term,
			// term rather than se.s: reading starts in Adopt, before se.s
			// is assigned.
			Tap: func(b []byte) { s.tap(se, term, b) },
		})
		if err != nil {
			log.Printf("session %s: cannot resume: %v", u.Key, err)
			continue
		}
		se.s = sh
		if u.Pending != "" {
			msg := u.Pending
			se.pending.Store(&msg)
		}
		if len(u.Input) > 0 {
			sh.Write(u.Input)
		}
		if s.colors != nil {
			applyColors(sh.Term, s.colors)
		}
		// Under the lock: the watcher of a session already resumed may
		// remove it from the table while the next ones are being added.
		s.mu.Lock()
		s.sessions[u.Key] = se
		s.mu.Unlock()
		n++
		go s.watch(se)
	}
	os.Remove(path)
	log.Printf("resumed after hot restart: %d session(s) in %v", n, time.Since(st.At).Round(time.Millisecond))
	return nil
}

func (s *Server) snapPath(key string) string { return filepath.Join(s.dir, "terms", key+".snap") }
func (s *Server) metaPath(key string) string { return filepath.Join(s.dir, "terms", key+".json") }

// savedMeta goes with the snapshot on disk, for relaunching after a restart.
type savedMeta struct {
	Dir     string    `json:"dir"`
	FgArgv  []string  `json:"fg_argv,omitempty"`
	FgCwd   string    `json:"fg_cwd,omitempty"`
	SavedAt time.Time `json:"saved_at"`
}

// writeAtomic replaces path in one go: temporary file with a unique name
// (two simultaneous saves do not step on each other), fsync, rename.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func (s *Server) save(se *sess, force bool) { s.saveSnap(se, force, nil) }

// saveSnap writes the snapshot snap (taken now if nil) and the session
// metadata to disk. Returns the saved version number.
func (s *Server) saveSnap(se *sess, force bool, snap []byte) uint64 {
	se.saveMu.Lock()
	defer se.saveMu.Unlock()
	if se.ephemeral.Load() || se.gone {
		return se.savedSeq
	}
	seq := se.s.Term.Seq()
	if !force && seq == se.savedSeq {
		return seq
	}
	if snap == nil {
		snap = se.s.Term.Snapshot()
	}
	argv, fgCwd := se.s.Foreground()
	m := savedMeta{Dir: se.s.Cwd(), FgArgv: argv, FgCwd: fgCwd, SavedAt: time.Now()}
	data, _ := json.Marshal(m)
	if writeAtomic(s.snapPath(se.key), snap) == nil && writeAtomic(s.metaPath(se.key), data) == nil {
		se.savedSeq = seq
	}
	return se.savedSeq
}

func (s *Server) forget(key string) {
	os.Remove(s.snapPath(key))
	os.Remove(s.metaPath(key))
}

// drop erases the save of a closed session, so that a save in progress or
// to come does not recreate it.
func (s *Server) drop(se *sess) {
	se.saveMu.Lock()
	se.gone = true
	s.forget(se.key)
	se.saveMu.Unlock()
}

// loop refreshes the metadata of attached sessions, saves to disk
// regularly and stops the daemon once it has nothing left to do.
func (s *Server) loop() {
	tick := time.NewTicker(time.Second)
	lastSave := time.Now()
	for range tick.C {
		list := s.list()
		s.mu.Lock()
		idle := len(s.sessions) == 0 && s.conns == 0 && time.Since(s.idleSince) > idleTimeout
		s.mu.Unlock()
		if idle {
			s.shutdown(false)
		}
		s.refresh(list)
		if time.Since(lastSave) >= saveEvery {
			lastSave = time.Now()
			for _, se := range list {
				s.save(se, false)
			}
		}
	}
}

// refresh reads the foreground of the sessions and sends their metadata to
// the attached windows when it changes.
func (s *Server) refresh(list []*sess) {
	for _, se := range list {
		// A program that announced work in progress (OSC 7501) handed control
		// back to the shell without clearing it: its status goes away with
		// it, so the next window does not reload it.
		fg := se.s.ForegroundName()
		se.metaMu.Lock()
		left := shell.LeftForShell(se.lastFg, fg)
		se.lastFg = fg
		se.metaMu.Unlock()
		if left {
			se.s.Term.DropStatus(vt.PSWorking, vt.PSBlocked, vt.PSIdle)
		}
		c := se.att.Load()
		if c == nil {
			continue
		}
		m := Meta{Name: fg, Cwd: se.s.Cwd(), FgCwd: se.s.ForegroundCwd()}
		if se.swapMeta(m) {
			c.sendJSON(MsgMeta, se.key, m)
		}
	}
}

// shutdown stops the daemon. With kill, every session is ended (a program
// that ignores SIGHUP is finished off after killGrace) and forgotten;
// otherwise they are saved. The lock is only held to copy the list, so a
// stuck session cannot prevent the shutdown.
func (s *Server) shutdown(kill bool) {
	if !s.shutting.CompareAndSwap(false, true) {
		return
	}
	list := s.list()
	if kill {
		var wg sync.WaitGroup
		for _, se := range list {
			wg.Add(1)
			go func() {
				defer wg.Done()
				se.s.Kill(killGrace)
				s.drop(se)
			}()
		}
		wg.Wait()
	} else {
		for _, se := range list {
			s.save(se, true)
		}
	}
	if s.ln != nil {
		s.ln.Close()
	}
	os.Remove(s.sock)
	if s.removePid != nil {
		s.removePid()
	}
	os.Exit(0)
}

func (s *Server) serve(nc net.Conn) {
	// The socket directory is private, but still check who is at the other
	// end: only the user themselves drives their terminals.
	if uc, ok := nc.(*net.UnixConn); ok {
		if uid := peerUID(uc); uid != os.Getuid() {
			log.Printf("connection refused: uid %d", uid)
			nc.Close()
			return
		}
	}
	r := bufio.NewReaderSize(nc, 64*1024)
	first, err := r.Peek(1)
	if err != nil {
		nc.Close()
		return
	}
	if first[0] == '{' {
		s.serveCLI(nc, r)
		return
	}
	var c *conn
	c = makeConn(nc, func(key string) { s.resync(c, key) })
	go c.writeLoop()
	s.mu.Lock()
	s.gui = c
	s.conns++
	s.mu.Unlock()
	for {
		f, err := readFrame(r)
		if errors.Is(err, errFrameTooBig) {
			log.Printf("frame %d too large for %q, skipped", f.typ, f.key)
			continue
		}
		if err != nil {
			break
		}
		s.handle(c, f)
	}
	c.close()
	s.mu.Lock()
	for key, se := range s.sessions {
		if se.att.CompareAndSwap(c, nil) && se.ephemeral.Load() {
			// Spare of a window that is gone (even through a crash): it will
			// never serve anyone.
			delete(s.sessions, key)
			se.s.Close()
		}
	}
	if s.gui == c {
		s.gui = nil
	}
	s.conns--
	s.idleSince = time.Now()
	s.mu.Unlock()
}

func (s *Server) get(key string) *sess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[key]
}

// handle processes a frame from the window. Nothing here waits for a
// program: a single goroutine serves all the window's sessions, and a stuck
// pane must not cut off input to the others.
func (s *Server) handle(c *conn, f frame) {
	switch f.typ {
	case MsgHello:
		// Only a window that sends it gets the reply: an older window never
		// sees this frame type.
		c.sendJSON(MsgHello, "", Hello{Proto: protoVersion, Pid: os.Getpid()})
	case MsgCreate:
		var req CreateReq
		if json.Unmarshal(f.data, &req) == nil {
			s.create(c, req)
		}
	case MsgAttach:
		var req AttachReq
		_ = json.Unmarshal(f.data, &req)
		s.attach(c, f.key, req.Cols, req.Rows)
	case MsgInput:
		if se := s.get(f.key); se != nil {
			s.inputFrom(se, c, f.data)
		}
	case MsgResize:
		var req ResizeReq
		if json.Unmarshal(f.data, &req) == nil {
			if se := s.get(f.key); se != nil {
				se.setGuiSize(req.Cols, req.Rows)
				if se.isWindowOwner() {
					s.resize(se, req)
				}
			}
		}
	case MsgClose:
		if se := s.get(f.key); se != nil {
			s.mu.Lock()
			if s.sessions[f.key] == se {
				delete(s.sessions, f.key)
			}
			s.mu.Unlock()
			se.s.Close()
			s.drop(se)
			se.notifyExited()
		}
	case MsgAdopt:
		if se := s.get(f.key); se != nil {
			se.ephemeral.Store(false)
		}
	case MsgDetach:
		if se := s.get(f.key); se != nil {
			se.att.CompareAndSwap(c, nil)
		}
	case MsgColors:
		var col Colors
		if json.Unmarshal(f.data, &col) == nil {
			s.mu.Lock()
			s.colors = &col
			for _, se := range s.sessions {
				applyColors(se.s.Term, &col)
			}
			s.mu.Unlock()
		}
	case MsgKillAll:
		s.shutdown(true)
	case MsgUpgrade:
		go s.upgrade()
	}
}

func applyColors(t *vt.Terminal, c *Colors) {
	pal := c.Palette
	t.SetReportColors(c.FG, c.BG, c.Cursor, func(i int) (uint8, uint8, uint8) {
		return pal[i][0], pal[i][1], pal[i][2]
	})
}

// relaunchable reports whether the foreground command can safely be
// relaunched after a restart (editors, viewers…).
func relaunchable(argv, allowed []string) []string {
	if len(argv) == 0 {
		return nil
	}
	name := strings.TrimPrefix(filepath.Base(argv[0]), "-")
	if !slices.Contains(allowed, name) {
		return nil
	}
	out := append([]string{name}, argv[1:]...)
	if name == "claude" {
		out = append([]string{name}, claudeResumeArgs(argv[1:])...)
		if !slices.ContainsFunc(out[1:], func(a string) bool {
			return a == "-c" || a == "--continue" || a == "-r" || a == "--resume"
		}) {
			out = append(out, "--continue") // resumes the directory's conversation
		}
	}
	return out
}

// claudeValueFlags are the claude options followed by a value.
var claudeValueFlags = map[string]bool{
	"--model": true, "--fallback-model": true, "--permission-mode": true, "--add-dir": true,
	"--allowedTools": true, "--allowed-tools": true, "--disallowedTools": true, "--disallowed-tools": true,
	"--mcp-config": true, "--append-system-prompt": true, "--system-prompt": true, "--settings": true,
	"--setting-sources": true, "--agent": true, "--agents": true, "--session-id": true, "--effort": true,
	"--output-format": true, "--input-format": true, "--max-turns": true, "--name": true, "-n": true,
	"--plugin-dir": true, "--betas": true,
}

// claudeResumeArgs keeps the options of a claude command to relaunch it
// after a restart, without the initial prompt (it would be sent again as a
// new message), -p or --worktree: the resume happens in the directory
// where the session was running, the worktree itself.
func claudeResumeArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--print" || strings.HasPrefix(a, "--worktree="):
		case a == "-w" || a == "--worktree":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case (a == "-r" || a == "--resume") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-"):
			out = append(out, a, args[i+1])
			i++
		case claudeValueFlags[a] && i+1 < len(args):
			out = append(out, a, args[i+1])
			i++
		case !strings.HasPrefix(a, "-"):
			// The initial prompt.
		default:
			out = append(out, a)
		}
	}
	return out
}

// relaunchCmd builds the line typed into the shell to relaunch argv in cwd
// (empty: in dir, the shell's directory). It is typed as keystrokes, so an
// argument with a control character (a booby-trapped file name,
// "a^Ccommand\r") would cancel the line and then run the rest. Nothing is
// relaunched in that case.
func relaunchCmd(argv []string, cwd, dir string) string {
	if len(argv) == 0 {
		return ""
	}
	for _, a := range append([]string{cwd}, argv...) {
		if !typable(a) {
			log.Printf("relaunch refused: control character in %q", a)
			return ""
		}
	}
	cmd := shellQuote(argv)
	if cwd != "" && cwd != dir {
		cmd = "cd " + shellQuote([]string{cwd}) + " && " + cmd
	}
	return cmd
}

// typable reports whether a string can be typed safely: valid UTF-8
// (readline reads a lone byte above 0x7f as Meta + key), with no C0
// control character, DEL or C1 (0x9b is the 8-bit CSI).
func typable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

func shellQuote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`;&|<>()*?[]{}~!#") {
			q[i] = a
			continue
		}
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func (s *Server) create(c *conn, req CreateReq) {
	if s.get(req.Key) != nil {
		s.attach(c, req.Key, req.Cols, req.Rows)
		return
	}
	cols, rows := max(req.Cols, 2), max(req.Rows, 1)
	term := vt.New(cols, rows, req.Scrollback)
	dir := req.Dir
	var relaunch []string
	var relaunchDir string
	if req.Restore {
		if data, err := os.ReadFile(s.snapPath(req.Key)); err == nil && term.LoadSnapshot(data) == nil {
			term.RestoreAfterRestart(time.Now())
			var m savedMeta
			if raw, err := os.ReadFile(s.metaPath(req.Key)); err == nil && json.Unmarshal(raw, &m) == nil {
				if m.Dir != "" {
					dir = m.Dir
				}
				relaunch, relaunchDir = relaunchable(m.FgArgv, req.Relaunch), m.FgCwd
			}
		}
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir, _ = os.UserHomeDir()
	}
	se := &sess{key: req.Key, guiCols: cols, guiRows: rows, watchers: make(map[remote.Watcher]struct{})}
	se.ephemeral.Store(req.Ephemeral)
	sh, err := shell.Start(shell.Options{
		Argv: req.Argv, Dir: dir, Env: req.Env, Cols: cols, Rows: rows,
		Scrollback: req.Scrollback, Key: req.Key, Term: term,
		// term rather than se.s: reading starts in Start, before se.s is
		// assigned.
		Tap: func(b []byte) { s.tap(se, term, b) },
	})
	if err != nil {
		c.send(frame{typ: MsgNotFound, key: req.Key})
		return
	}
	se.s = sh
	s.mu.Lock()
	if s.colors != nil {
		applyColors(term, s.colors)
	}
	s.sessions[req.Key] = se
	s.mu.Unlock()
	if cmd := relaunchCmd(relaunch, relaunchDir, dir); cmd != "" {
		// Leading space: with HIST_IGNORE_SPACE, the relaunch stays out of
		// the shell history.
		sh.Write([]byte(" " + cmd + "\r"))
	}
	go s.watch(se)
	s.attach(c, req.Key, cols, rows)
}

// tap forwards output to the attached window and keeps the notifications
// received while nobody is watching. It is called under the session's
// read lock, from Start or Adopt on, so it only touches the terminal it
// receives, never se.s.
func (s *Server) tap(se *sess, term *vt.Terminal, b []byte) {
	c := se.att.Load()
	if c != nil {
		c.sendOutput(se.key, b)
	}
	for _, ev := range term.TakeEvents() {
		if c != nil || (ev.Kind != vt.EvBell && ev.Kind != vt.EvNotify) {
			continue
		}
		msg := strings.TrimSpace(ev.Title + " " + ev.Body)
		if msg == "" {
			msg = "bell"
		}
		se.pending.Store(&msg)
	}
	se.notifyChanged()
}

// snapshotFrame returns a terminal's snapshot, brought under the maximum
// frame size (oldest history dropped if needed).
func snapshotFrame(t *vt.Terminal) []byte {
	snap := t.Snapshot()
	if fit := fitSnapshot(snap, maxFrame); fit != nil {
		if len(fit) < len(snap) {
			log.Printf("snapshot of %d bytes trimmed to %d", len(snap), len(fit))
		}
		return fit
	}
	// Unknown format or oversized screen: a blank terminal of the right size
	// is better than a frame the window would reject.
	cols, rows := sizeOf(t)
	return vt.New(cols, rows, 0).Snapshot()
}

func (s *Server) attach(c *conn, key string, cols, rows int) {
	se := s.get(key)
	if se == nil {
		c.send(frame{typ: MsgNotFound, key: key})
		return
	}
	se.s.Atomic(func() {
		// Snapshot and stream subscription under the same lock as the PTY
		// reader: not a single byte is missed or duplicated.
		if old := se.att.Swap(c); old != nil && old != c {
			old.send(frame{typ: MsgNotFound, key: key})
		}
		c.unstale(key)
		c.send(frame{typ: MsgSnapshot, key: key, data: snapshotFrame(se.s.Term)})
	})
	if p := se.pending.Swap(nil); p != nil {
		c.send(frame{typ: MsgPending, key: key, data: []byte(*p)})
	}
	m := Meta{Name: se.s.ForegroundName(), Cwd: se.s.Cwd(), FgCwd: se.s.ForegroundCwd()}
	se.swapMeta(m)
	c.sendJSON(MsgMeta, key, m)
	s.mu.Lock()
	s.gui = c
	s.mu.Unlock()
	if cols > 0 && rows > 0 {
		se.setGuiSize(cols, rows)
	}
	if se.isWindowOwner() {
		tc, tr := sizeOf(se.s.Term)
		if cols > 0 && rows > 0 && (cols != tc || rows != tr) {
			s.resize(se, ResizeReq{Cols: cols, Rows: rows})
		}
	}
}

// resync sends a fresh snapshot of a session whose output overflowed c's
// queue, in its place in the stream.
func (s *Server) resync(c *conn, key string) {
	se := s.get(key)
	if se == nil || se.att.Load() != c {
		c.unstale(key)
		return
	}
	se.s.Atomic(func() {
		c.unstale(key)
		c.send(frame{typ: MsgSnapshot, key: key, data: snapshotFrame(se.s.Term)})
	})
}

func sizeOf(t *vt.Terminal) (int, int) {
	t.Lock()
	defer t.Unlock()
	return t.Size()
}

func (s *Server) resize(se *sess, req ResizeReq) {
	se.s.Atomic(func() {
		se.s.Resize(max(req.Cols, 2), max(req.Rows, 1), req.PxW, req.PxH)
		if c := se.att.Load(); c != nil {
			c.sendJSON(MsgResized, se.key, req)
		}
	})
	se.notifyChanged()
}

// watch waits for the process to end. A grace period avoids erasing saves
// when everything stops at once (computer shutdown). A program that exited
// on its own with success (:q, exit) does not need it: on shutdown, shells
// and editors die from a signal or with an error.
func (s *Server) watch(se *sess) {
	<-se.s.Done()
	end := time.Now().Add(exitGrace)
	if !se.s.Succeeded(exitGrace) {
		time.Sleep(time.Until(end))
	}
	// During a handover, the session goes along with the others. If the
	// handover fails, pick up here: an exited session does not stay in the table.
	for s.upgrading.Load() && !s.shutting.Load() {
		time.Sleep(100 * time.Millisecond)
	}
	if s.shutting.Load() {
		return
	}
	s.mu.Lock()
	cur, ok := s.sessions[se.key]
	if ok && cur == se {
		delete(s.sessions, se.key)
	}
	if len(s.sessions) == 0 {
		s.idleSince = time.Now()
	}
	s.mu.Unlock()
	if ok && cur == se {
		if c := se.att.Load(); c != nil {
			c.send(frame{typ: MsgExited, key: se.key})
		}
		s.drop(se)
	}
	se.notifyExited()
}

// serveCLI handles a JSON request from the CLI: relayed to the window if
// it is open, kept pending otherwise (notifications). The connection has a
// deadline and the request a maximum size, so a client that sends nothing,
// or never stops sending, does not hold on to a daemon goroutine.
func (s *Server) serveCLI(nc net.Conn, r *bufio.Reader) {
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(s.cliTimeout))
	line, err := readLine(r, maxCLILine)
	if errors.Is(err, errLineTooLong) {
		fmt.Fprintln(nc, "error: request too long")
		return
	}
	if err != nil && (len(line) == 0 || !errors.Is(err, io.EOF)) {
		return
	}
	var req Request
	if json.Unmarshal(line, &req) != nil {
		fmt.Fprintln(nc, "error: invalid request")
		return
	}
	switch req.Cmd {
	case "ls":
		// The list is copied under the lock, the processes are read afterwards.
		for _, se := range s.list() {
			state := "detached"
			switch {
			case se.ephemeral.Load():
				state = "spare"
			case se.att.Load() != nil:
				state = "attached"
			}
			fmt.Fprintf(nc, "%s\t%s\t%s\t%s\n", se.key, state, se.s.ForegroundName(), se.s.Cwd())
		}
		fmt.Fprintln(nc, "ok")
		return
	case "kill-server":
		fmt.Fprintln(nc, "ok")
		go s.shutdown(true)
		return
	case "restart-server":
		fmt.Fprintln(nc, "ok")
		go s.upgrade()
		return
	case "capture", "send-keys":
		s.servePane(nc, req)
		return
	}
	s.mu.Lock()
	gui := s.gui
	se := s.sessions[req.Pane]
	s.mu.Unlock()
	switch {
	case req.Cmd == "restart" && gui == nil:
		// No window open: only the daemon restarts.
		go s.upgrade()
	case gui != nil:
		gui.sendJSON(MsgRequest, req.Pane, req)
	case req.Cmd == "notify" && se != nil:
		msg := strings.Join(req.Args, " ")
		se.pending.Store(&msg)
	}
	fmt.Fprintln(nc, "ok")
}

var errLineTooLong = errors.New("line too long")

// readLine reads a line of at most max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max {
			return nil, errLineTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// servePane reads the screen of a session (capture) or sends keys to it
// (send-keys). Each screen line is prefixed with "|" so that an "ok" line
// on screen is not taken for the end of the response.
func (s *Server) servePane(nc net.Conn, req Request) {
	se, err := s.findSession(req.Pane)
	if err != nil {
		fmt.Fprintln(nc, "error: "+err.Error())
		return
	}
	term := se.s.Terminal()
	if req.Cmd == "capture" {
		for _, l := range term.ScreenText() {
			fmt.Fprintln(nc, "|"+l)
		}
	} else {
		b, err := keys.EncodeTokens(req.Args, term.Modes())
		if err != nil {
			fmt.Fprintln(nc, "error: "+err.Error())
			return
		}
		se.s.Write(b)
	}
	fmt.Fprintln(nc, "ok")
}

// findSession finds a session by its key (neonmux ls) or by the name of
// its foreground program, if it is unique.
func (s *Server) findSession(id string) (*sess, error) {
	if id == "" {
		return nil, fmt.Errorf("specify the pane: its key (neonmux ls) or the name of its program")
	}
	if se := s.get(id); se != nil {
		return se, nil
	}
	var found []*sess
	for _, se := range s.list() {
		if !se.ephemeral.Load() && se.s.ForegroundName() == id {
			found = append(found, se)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no pane \"%s\" (neonmux ls lists the panes)", id)
	case 1:
		return found[0], nil
	}
	var ks []string
	for _, se := range found {
		ks = append(ks, se.key)
	}
	slices.Sort(ks)
	return nil, fmt.Errorf("several panes run \"%s\": %s", id, strings.Join(ks, ", "))
}
