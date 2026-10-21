package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// Client is the window's connection to the daemon. If the connection drops
// (hot restart, daemon crash), it reconnects on its own and reattaches to
// its sessions. If the daemon does not come back in time, the client
// declares itself lost (Lost) and keeps trying in the background.
type Client struct {
	sock    string
	spawn   func() error
	wait    time.Duration // reconnectWait at the time of Connect
	closed  chan struct{}
	drained chan struct{}
	once    sync.Once
	mu      sync.Mutex
	conn    net.Conn
	remotes map[string]*Remote
	colors  *Colors

	qmu    sync.Mutex
	q      []frame // send queue, kept from one connection to the next
	qbytes int
	ready  chan struct{} // wakes writeLoop (one slot)

	lost        atomic.Bool  // daemon lost: nothing is sent anymore
	serverProto atomic.Int32 // protocol announced by the daemon (0: old daemon)

	OnWake    func()        // a session received data
	OnRequest func(Request) // CLI request relayed by the daemon
}

// reconnectWait is how long the daemon has to come back after a
// disconnect before its sessions are declared lost.
var reconnectWait = 5 * time.Second

// clientBudget caps the input queued for the daemon. Beyond it, input is
// dropped rather than blocking the Ebiten thread.
const clientBudget = 16 << 20

func dial(sock string, spawn func() error, wait time.Duration) (net.Conn, error) {
	c, err := net.Dial("unix", sock)
	if err == nil {
		return c, nil
	}
	if spawn != nil {
		if err := spawn(); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if c, err = net.Dial("unix", sock); err == nil {
			return c, nil
		}
	}
	return nil, err
}

// Connect connects to the daemon, starting it if it is not running.
func Connect(sock string, spawn func() error) (*Client, error) {
	c, err := dial(sock, spawn, 3*time.Second)
	if err != nil {
		return nil, err
	}
	cl := &Client{sock: sock, spawn: spawn, wait: reconnectWait, conn: c, ready: make(chan struct{}, 1),
		closed: make(chan struct{}), drained: make(chan struct{}), remotes: map[string]*Remote{}}
	cl.hello()
	go cl.run(c)
	return cl, nil
}

// Spawn starts `exe server` detached from the window (new Unix session), so
// it survives neonmux and the terminal that launched it being closed.
func Spawn(exe string) error {
	if Forcing() {
		return errors.New("forced restart in progress")
	}
	cmd := exec.Command(exe, "server")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	log, err := os.OpenFile(StateDir()+"/server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		cmd.Stdout, cmd.Stderr = log, log
		// The daemon got its own copy of the descriptor.
		defer log.Close()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (cl *Client) isClosed() bool {
	select {
	case <-cl.closed:
		return true
	default:
		return false
	}
}

// send queues a frame without ever blocking the caller (the Ebiten
// thread). If the daemon is lost, the frame is dropped. Beyond clientBudget,
// input is dropped; control frames (create, close, detach…) are small and
// always go through.
func (cl *Client) send(f frame) {
	if cl.isClosed() || cl.lost.Load() {
		return
	}
	cl.qmu.Lock()
	if f.typ == MsgInput && cl.qbytes+len(f.data) > clientBudget {
		cl.qmu.Unlock()
		return
	}
	cl.q = append(cl.q, f)
	cl.qbytes += len(f.data)
	cl.qmu.Unlock()
	select {
	case cl.ready <- struct{}{}:
	default:
	}
}

func (cl *Client) sendJSON(typ byte, key string, v any) {
	data, _ := json.Marshal(v)
	cl.send(frame{typ: typ, key: key, data: data})
}

// hello announces the protocol version. An older daemon ignores the frame
// and does not answer, so ServerProto stays at 0.
func (cl *Client) hello() {
	cl.sendJSON(MsgHello, "", Hello{Proto: protoVersion, Pid: os.Getpid()})
}

// run serves one connection after another: read, write, then reconnect
// if the disconnect was not requested.
func (cl *Client) run(c net.Conn) {
	short := 0
	for {
		stopW := make(chan struct{})
		wDone := make(chan struct{})
		go cl.writeLoop(c, stopW, wDone)
		t0 := time.Now()
		cl.readLoop(c)
		close(stopW)
		<-wDone
		c.Close()
		if cl.isClosed() {
			return
		}
		cl.serverProto.Store(0)
		// A connection dropped right after it was established (a daemon
		// that refuses, or crashes on every attach): wait a bit longer each
		// time instead of reconnecting in a loop.
		if time.Since(t0) < time.Second {
			short = min(short+1, 6)
			select {
			case <-cl.closed:
				return
			case <-time.After(100 * time.Millisecond << short):
			}
		} else {
			short = 0
		}
		nc, err := dial(cl.sock, cl.spawn, cl.wait)
		if err != nil {
			cl.lostAll()
			if nc = cl.retry(); nc == nil {
				return
			}
		}
		c = nc
		cl.mu.Lock()
		cl.conn = c
		cl.mu.Unlock()
		cl.lost.Store(false)
		cl.reattach()
	}
}

// retry keeps trying to reach the daemon in the background, doubling the
// delay after each failure (30s at most). Returns nil if the client is closed.
func (cl *Client) retry() net.Conn {
	delay := time.Second
	for {
		select {
		case <-cl.closed:
			return nil
		case <-time.After(delay):
		}
		if c, err := dial(cl.sock, cl.spawn, 2*time.Second); err == nil {
			return c
		}
		delay = min(2*delay, 30*time.Second)
	}
}

// reattach sends the theme again and reattaches to each session at its
// current size. A session the new daemon does not know will show up as "Missing".
func (cl *Client) reattach() {
	cl.hello()
	cl.mu.Lock()
	colors := cl.colors
	remotes := make([]*Remote, 0, len(cl.remotes))
	for _, rm := range cl.remotes {
		remotes = append(remotes, rm)
	}
	cl.mu.Unlock()
	if colors != nil {
		cl.sendJSON(MsgColors, "", colors)
	}
	for _, rm := range remotes {
		cols, rows := sizeOf(rm.term)
		cl.sendJSON(MsgAttach, rm.key, AttachReq{Cols: cols, Rows: rows})
	}
	if cl.OnWake != nil {
		cl.OnWake()
	}
}

// lostAll declares the daemon lost: sessions end (Done closed, Lost true)
// and are forgotten, and the send queue is emptied. If the daemon comes
// back, only sessions created afterwards are served.
func (cl *Client) lostAll() {
	cl.lost.Store(true)
	cl.qmu.Lock()
	cl.q, cl.qbytes = nil, 0
	cl.qmu.Unlock()
	cl.mu.Lock()
	for key, rm := range cl.remotes {
		rm.lost.Store(true)
		rm.finish()
		delete(cl.remotes, key)
	}
	cl.mu.Unlock()
	if cl.OnWake != nil {
		cl.OnWake()
	}
}

func (cl *Client) writeLoop(c net.Conn, stop, done chan struct{}) {
	defer close(done)
	w := bufio.NewWriterSize(c, 64*1024)
	for {
		select {
		case <-cl.ready:
			if !cl.flush(w) {
				c.Close() // reading stops and reconnection takes over
				return
			}
		case <-stop:
			return
		case <-cl.closed:
			// The last frames (closing the spare, detaches, restart
			// request) go out before the connection is closed.
			cl.flush(w)
			cl.signalDrained()
			return
		}
	}
}

// flush writes the whole queue. On error, whatever was not sent stays at
// the head of the queue for the next connection.
func (cl *Client) flush(w *bufio.Writer) bool {
	for {
		cl.qmu.Lock()
		batch := cl.q
		cl.q = nil
		cl.qmu.Unlock()
		if len(batch) == 0 {
			return w.Flush() == nil
		}
		for i, f := range batch {
			err := writeFrame(w, f)
			cl.qmu.Lock()
			cl.qbytes -= len(f.data)
			if err != nil && !errors.Is(err, errFrameTooBig) {
				rest := batch[i+1:]
				for _, r := range rest {
					cl.qbytes += len(r.data)
				}
				cl.q = append(rest, cl.q...)
				cl.qmu.Unlock()
				return false
			}
			cl.qmu.Unlock()
		}
	}
}

func (cl *Client) signalDrained() {
	select {
	case <-cl.drained:
	default:
		close(cl.drained)
	}
}

func (cl *Client) remote(key string) *Remote {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.remotes[key]
}

func (cl *Client) readLoop(c net.Conn) {
	r := bufio.NewReaderSize(c, 256*1024)
	for {
		f, err := readFrame(r)
		if errors.Is(err, errFrameTooBig) {
			// Skipped without disconnecting: reconnecting would get the same one again.
			continue
		}
		if err != nil {
			return
		}
		if f.typ == MsgHello {
			var h Hello
			if json.Unmarshal(f.data, &h) == nil {
				cl.serverProto.Store(int32(h.Proto))
			}
			continue
		}
		if f.typ == MsgRequest {
			var req Request
			if json.Unmarshal(f.data, &req) == nil && cl.OnRequest != nil {
				cl.OnRequest(req)
			}
			continue
		}
		rm := cl.remote(f.key)
		if rm == nil {
			continue
		}
		switch f.typ {
		case MsgSnapshot:
			if rm.term.LoadSnapshot(f.data) == nil {
				rm.ready.Store(true)
				rm.missing.Store(false)
			}
		case MsgOutput:
			rm.term.Write(f.data)
		case MsgResized:
			var req ResizeReq
			if json.Unmarshal(f.data, &req) == nil {
				rm.term.Resize(req.Cols, req.Rows)
				rm.term.SetPixelSize(req.PxW, req.PxH)
			}
		case MsgMeta:
			var m Meta
			if json.Unmarshal(f.data, &m) == nil {
				rm.meta.Store(&m)
			}
		case MsgPending:
			msg := string(f.data)
			rm.pending.Store(&msg)
		case MsgNotFound:
			rm.missing.Store(true)
		case MsgExited:
			rm.finish()
		}
		if cl.OnWake != nil {
			cl.OnWake()
		}
	}
}

// Close closes the connection (sessions keep running in the daemon) after
// sending whatever was left in the queue, within 300 ms.
func (cl *Client) Close() {
	cl.once.Do(func() {
		close(cl.closed)
		select {
		case <-cl.drained:
		case <-time.After(300 * time.Millisecond):
		}
		cl.mu.Lock()
		if cl.conn != nil {
			cl.conn.Close()
		}
		cl.mu.Unlock()
	})
}

// Alive reports whether the connection to the daemon is usable: neither
// closed nor lost.
func (cl *Client) Alive() bool { return !cl.isClosed() && !cl.lost.Load() }

// Lost reports that the daemon is lost: it did not come back within the
// reconnect delay. The sessions of that moment are over (Done closed,
// Remote.Lost true), nothing is sent anymore, and Create or Attach return
// a session that is already lost. The client keeps retrying in the
// background with a growing delay: if the daemon comes back, Lost turns
// false again and Alive true.
func (cl *Client) Lost() bool { return cl.lost.Load() }

// ServerProto returns the daemon's protocol version: 0 for a daemon older
// than MsgHello, or as long as it has not answered.
func (cl *Client) ServerProto() int { return int(cl.serverProto.Load()) }

// SetColors sends the theme colors to the daemon (and sends them again
// after a reconnect).
func (cl *Client) SetColors(c Colors) {
	cl.mu.Lock()
	cl.colors = &c
	cl.mu.Unlock()
	cl.sendJSON(MsgColors, "", c)
}

// KillAll ends every session and stops the daemon.
func (cl *Client) KillAll() { cl.send(frame{typ: MsgKillAll}) }

// Upgrade asks the daemon for a hot restart on the binary on disk.
func (cl *Client) Upgrade() { cl.send(frame{typ: MsgUpgrade}) }

func (cl *Client) newRemote(key string, cols, rows, scrollback int) *Remote {
	rm := &Remote{cl: cl, key: key, term: vt.New(cols, rows, scrollback), done: make(chan struct{})}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.lost.Load() {
		// Daemon lost: the session is lost from the start.
		rm.lost.Store(true)
		rm.finish()
		return rm
	}
	cl.remotes[key] = rm
	return rm
}

// Create starts a session in the daemon (or restores it if Restore).
func (cl *Client) Create(req CreateReq) *Remote {
	rm := cl.newRemote(req.Key, max(req.Cols, 2), max(req.Rows, 1), req.Scrollback)
	cl.sendJSON(MsgCreate, req.Key, req)
	return rm
}

// Attach attaches to an existing session. If it no longer exists,
// Missing turns true and the caller can create it again.
func (cl *Client) Attach(key string, cols, rows, scrollback int) *Remote {
	rm := cl.newRemote(key, max(cols, 2), max(rows, 1), scrollback)
	cl.sendJSON(MsgAttach, key, AttachReq{Cols: cols, Rows: rows})
	return rm
}

// Remote is a daemon session as seen from the window. Its terminal
// interprets the same stream as the daemon's, without ever answering the
// program: only the daemon does that.
type Remote struct {
	cl      *Client
	key     string
	term    *vt.Terminal
	done    chan struct{}
	once    sync.Once
	ready   atomic.Bool
	missing atomic.Bool
	lost    atomic.Bool // lost with the daemon: nothing is sent anymore
	meta    atomic.Pointer[Meta]
	pending atomic.Pointer[string]
}

func (r *Remote) finish() { r.once.Do(func() { close(r.done) }) }

func (r *Remote) Terminal() *vt.Terminal { return r.term }
func (r *Remote) Key() string            { return r.key }
func (r *Remote) Done() <-chan struct{}  { return r.done }

// Missing reports that the daemon does not know this session (or no longer does).
func (r *Remote) Missing() bool { return r.missing.Load() }

// Ready reports that the initial snapshot has arrived.
func (r *Remote) Ready() bool { return r.ready.Load() }

// Lost reports that the session was lost with the daemon (Client.Lost)
// rather than ended by its program. Its terminal keeps the last screen.
func (r *Remote) Lost() bool { return r.lost.Load() }

// Exited reports that the session has ended.
func (r *Remote) Exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// Write sends input to the program. It never blocks.
func (r *Remote) Write(b []byte) {
	if len(b) > 0 && !r.Exited() {
		r.cl.send(frame{typ: MsgInput, key: r.key, data: append([]byte(nil), b...)})
	}
}

// Resize requests a resize. The local terminal changes size when the
// daemon confirms it, in its place in the stream.
func (r *Remote) Resize(cols, rows, pxW, pxH int) {
	if r.lost.Load() {
		return
	}
	r.cl.sendJSON(MsgResize, r.key, ResizeReq{Cols: cols, Rows: rows, PxW: pxW, PxH: pxH})
}

// ForegroundName returns the foreground process (updated by the daemon).
func (r *Remote) ForegroundName() string {
	if m := r.meta.Load(); m != nil {
		return m.Name
	}
	return ""
}

// ForegroundCwd returns the directory of the foreground program.
func (r *Remote) ForegroundCwd() string {
	if m := r.meta.Load(); m != nil {
		return m.FgCwd
	}
	return ""
}

// Cwd returns the shell's current directory.
func (r *Remote) Cwd() string {
	if m := r.meta.Load(); m != nil {
		return m.Cwd
	}
	return ""
}

// TakePending returns the notification received while detached.
func (r *Remote) TakePending() string {
	if p := r.pending.Swap(nil); p != nil {
		return *p
	}
	return ""
}

// Adopt turns a spare session into a real (saved) session.
func (r *Remote) Adopt() {
	if !r.lost.Load() {
		r.cl.send(frame{typ: MsgAdopt, key: r.key})
	}
}

// Close ends the process. It does nothing on a lost session, whose key may
// already be in use by a session recreated in a new daemon.
func (r *Remote) Close() {
	if r.lost.Load() {
		return
	}
	r.cl.send(frame{typ: MsgClose, key: r.key})
	r.forget()
}

// Detach leaves the session without killing it.
func (r *Remote) Detach() {
	if r.lost.Load() {
		return
	}
	r.cl.send(frame{typ: MsgDetach, key: r.key})
	r.forget()
}

func (r *Remote) forget() {
	r.cl.mu.Lock()
	if r.cl.remotes[r.key] == r {
		delete(r.cl.remotes, r.key)
	}
	r.cl.mu.Unlock()
}

// ErrNoServer is returned when the daemon cannot be reached.
var ErrNoServer = errors.New("neonmux daemon unreachable")
