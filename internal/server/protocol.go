// Package server contains the daemon that keeps terminals alive while the
// window is closed, and the client the window uses to attach to it.
//
// The daemon owns the PTYs and one vt.Terminal per session. On attach, it
// sends a full snapshot followed by the program's raw stream, which the
// window's terminal interprets identically. Resizes travel in the same
// stream so both sides stay in sync.
package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// Frame types.
const (
	MsgCreate   byte = iota + 1 // window → daemon: CreateReq
	MsgAttach                   // window → daemon: AttachReq
	MsgInput                    // window → daemon: bytes to write to the PTY
	MsgResize                   // window → daemon: ResizeReq
	MsgClose                    // window → daemon: ends the session
	MsgDetach                   // window → daemon: detaches without killing
	MsgColors                   // window → daemon: theme colors (OSC 10/11 replies)
	MsgKillAll                  // window → daemon: ends everything
	MsgSnapshot                 // daemon → window: full terminal state
	MsgOutput                   // daemon → window: program output
	MsgResized                  // daemon → window: new size, in its place in the stream
	MsgExited                   // daemon → window: the process has exited
	MsgMeta                     // daemon → window: foreground process, directory
	MsgNotFound                 // daemon → window: unknown session
	MsgPending                  // daemon → window: notification received while detached
	MsgRequest                  // daemon → window: CLI request (neonmux open, notify…)
	MsgAdopt                    // window → daemon: the spare becomes a real session
	MsgUpgrade                  // window → daemon: hot restart on the binary on disk
	// Added with protoVersion 2. A recent window sends it on every
	// connection and a recent daemon answers it. Both sides ignore unknown
	// types: an older daemon does not answer (ServerProto stays at 0), and
	// an older window never sends it, so it receives nothing.
	MsgHello // both directions: Hello
	// Added with protoVersion 3. The daemon only sends MsgRemoteState to a
	// window whose hello said 3 or more, and only obeys MsgRemote from it.
	MsgRemote      // window → daemon: RemoteCmd
	MsgRemoteState // daemon → window: remote.State
)

// protoVersion is the protocol version spoken by this binary. 1: the
// version without MsgHello. 3: remote control.
const protoVersion = 3

// RemoteCmd drives the remote control server from the window. Op is one of
// on, off, pair, confirm, reject, revoke (ID: the device) and regen.
type RemoteCmd struct {
	Op string `json:"op"`
	ID string `json:"id,omitempty"`
}

// Hello announces the protocol version and the sender's PID.
type Hello struct {
	Proto int `json:"proto"`
	Pid   int `json:"pid"`
}

const maxFrame = 64 << 20

// errFrameTooBig reports a frame larger than maxFrame. readFrame has
// already skipped its data, so the stream stays aligned and reading can
// go on instead of dropping the connection (the client would reconnect
// and receive the same frame, in a loop).
var errFrameTooBig = errors.New("frame too large")

type frame struct {
	typ  byte
	key  string
	data []byte
}

// Header: type, key length, key, data length (u32).
func writeFrame(w *bufio.Writer, f frame) error {
	if len(f.key) > 255 {
		return errors.New("session key too long")
	}
	if len(f.data) > maxFrame {
		return errFrameTooBig // nothing is written, so the stream stays aligned
	}
	hdr := make([]byte, 0, 6+len(f.key))
	hdr = append(hdr, f.typ, byte(len(f.key)))
	hdr = append(hdr, f.key...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(f.data)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(f.data)
	return err
}

func readFrame(r *bufio.Reader) (frame, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return frame{}, err
	}
	key := make([]byte, h[1])
	if _, err := io.ReadFull(r, key); err != nil {
		return frame{}, err
	}
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return frame{}, err
	}
	n := binary.LittleEndian.Uint32(l[:])
	if n > maxFrame {
		if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
			return frame{}, err
		}
		return frame{typ: h[0], key: string(key)}, errFrameTooBig
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return frame{}, err
	}
	return frame{typ: h[0], key: string(key), data: data}, nil
}

// CreateReq starts a session (or restores it from disk).
type CreateReq struct {
	Key        string   `json:"key"`
	Argv       []string `json:"argv,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Env        []string `json:"env,omitempty"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	Scrollback int      `json:"scrollback"`
	Restore    bool     `json:"restore,omitempty"`   // reload the saved history
	Relaunch   []string `json:"relaunch,omitempty"`  // commands relaunched after a restart
	Ephemeral  bool     `json:"ephemeral,omitempty"` // spare shell, never saved
}

// AttachReq attaches the window to an existing session.
type AttachReq struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// ResizeReq changes the size of a session.
type ResizeReq struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	PxW  int `json:"pxw,omitempty"`
	PxH  int `json:"pxh,omitempty"`
}

// Colors carries the theme colors for OSC 10/11/12/4 replies.
type Colors struct {
	FG      [3]uint8      `json:"fg"`
	BG      [3]uint8      `json:"bg"`
	Cursor  [3]uint8      `json:"cursor"`
	Palette [256][3]uint8 `json:"palette"`
}

// Meta describes the state of a session for the pane header.
type Meta struct {
	Name  string `json:"name"`
	Cwd   string `json:"cwd"`
	FgCwd string `json:"fg_cwd,omitempty"` // directory of the foreground program
}

// Request is a CLI command (`neonmux open`, `notify`, `fx`…).
type Request struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
	Pane string   `json:"pane"`
	Cwd  string   `json:"cwd"`
}

// SocketPath returns the daemon socket, private to the user: $TMPDIR on
// macOS, $XDG_RUNTIME_DIR on Linux (never a shared /tmp).
func SocketPath() string {
	if p := os.Getenv("NEONMUX_SERVER_SOCK"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" && runtime.GOOS == "linux" {
		return filepath.Join(d, "neonmux", "server.sock")
	}
	dir := "neonmux"
	if runtime.GOOS != "darwin" {
		dir += "-" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(os.TempDir(), dir, "server.sock")
}

// StateDir returns the directory for persistent data.
func StateDir() string {
	if d := os.Getenv("NEONMUX_STATE"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "neonmux")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "neonmux")
}

// privateDir creates the socket directory if needed and checks that it
// really is ours: on Linux without XDG_RUNTIME_DIR it lives in the shared
// /tmp, where another user may have created it first (or put a link
// there). It must be a real directory, owned by our uid, with mode 0700.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%s is not a directory", dir)
	case int(st.Uid) != os.Getuid():
		return fmt.Errorf("%s belongs to uid %d, not to us", dir, st.Uid)
	case st.Mode&0o777 != 0o700:
		return fmt.Errorf("%s has mode %o, it must be 700", dir, st.Mode&0o777)
	}
	return nil
}

// snapMagic is the header of vt.Terminal.Snapshot snapshots.
const snapMagic = "NMX1"

// fitSnapshot brings a snapshot under limit bytes by dropping the oldest
// history, since the reader rejects frames larger than maxFrame bytes. The
// screen, cursors and recent lines are kept. Parsing follows the layout of
// vt.Terminal.Snapshot: size, alternate screen flag, both grids, then the
// history (maximum size, total, line count and lines). Returns nil if the
// snapshot cannot fit (unknown format, or the screen alone is too big).
func fitSnapshot(snap []byte, limit int) []byte {
	if len(snap) <= limit {
		return snap
	}
	if !bytes.HasPrefix(snap, []byte(snapMagic)) {
		return nil
	}
	b := snap[len(snapMagic):]
	pos := func() int { return len(snap) - len(b) }
	ok := true
	uvar := func() uint64 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			ok = false
			return 0
		}
		b = b[n:]
		return v
	}
	skip := func() {
		l := uvar()
		if !ok || uint64(len(b)) < l {
			ok = false
			return
		}
		b = b[l:]
	}
	uvar() // columns
	rows := uvar()
	if !ok || len(b) < 1 || rows > 4096 {
		return nil
	}
	b = b[1:]
	for i := uint64(0); i < 2*rows && ok; i++ {
		skip()
	}
	uvar() // maximum history size
	if _, n := binary.Varint(b); n > 0 {
		b = b[n:] // total lines pushed, kept so absolute indexes stay correct
	} else {
		ok = false
	}
	head := pos()
	n := uvar()
	if !ok || n > uint64(len(b)) {
		return nil
	}
	offs := make([]int, n+1)
	for i := uint64(0); i < n && ok; i++ {
		offs[i] = pos()
		skip()
	}
	if !ok {
		return nil
	}
	offs[n] = pos()
	tail := snap[offs[n]:]
	fixed := head + binary.MaxVarintLen64 + len(tail)
	if fixed > limit {
		return nil
	}
	drop := 0
	for uint64(drop) < n && fixed+offs[n]-offs[drop] > limit {
		drop++
	}
	out := make([]byte, 0, fixed+offs[n]-offs[drop])
	out = append(out, snap[:head]...)
	out = binary.AppendUvarint(out, n-uint64(drop))
	out = append(out, snap[offs[drop]:offs[n]]...)
	return append(out, tail...)
}
