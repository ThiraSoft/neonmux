package shell

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
	"golang.org/x/sys/unix"
)

// Same limits as vt (unexported there): a longer APC is dropped by vt, so it
// is not worth rewriting; a larger file could not become an image.
const (
	maxAPC        = 4 << 20
	maxImageBytes = 32 << 20
	// gfxChunk is the base64 size of each rewritten chunk, the largest the
	// protocol allows (a multiple of 4).
	gfxChunk = 4096
)

const (
	gfGround  = iota
	gfPrefix  // held is "ESC" or "ESC _": maybe a graphics command
	gfAPC     // inside ESC _ G, held until ST
	gfAPCEsc  // ESC seen inside ESC _ G
	gfSkip    // ESC _ G longer than maxAPC: passed through until its end
	gfSkipEsc // ESC seen while skipping
)

// Errors of a file transmission, answered as kitty does: EBADF for any
// failure to read (kitty never says more, so that programs cannot probe the
// filesystem), EINVAL for bad S/O keys or a size beyond the limit.
var (
	errGfxRefused = errors.New("EBADF")
	errGfxInval   = errors.New("EINVAL")
)

// gfxKeep is the output and held buffer capacity kept from one call to the
// next: an inlined 32 MiB image must not stay allocated afterwards.
const gfxKeep = 64 << 10

// gfxReply is an error answer of the filter, to send to the program once the
// terminal has parsed out[:at]: it keeps its place among vt's replies.
type gfxReply struct {
	at  int
	msg []byte
}

// gfxFilter sits between the daemon's PTY and both its terminal and the
// stream to the window. It turns kitty graphics transmissions by file (t=f),
// temporary file (t=t) and shared memory (t=s) into direct ones (t=d): the
// window replays the stream and never reads a file, which with t=t is
// already gone and with t=f may have changed.
//
// Everything else goes through byte for byte. A graphics command cut between
// two reads is held until the next one; its "ESC" and "ESC _" prefix is
// passed on right away (so a lone ESC is never held back) and counted in
// emitted.
//
// The scanner only knows ESC _ G … ESC \, aborted by CAN, SUB or another ESC,
// as vt does. Exotic forms vt also accepts (control bytes between ESC and _)
// are passed through untouched: the daemon's terminal then answers EBADF and
// the window shows nothing, as before this filter.
type gfxFilter struct {
	state   int
	held    []byte
	emitted int // bytes of held already passed on
	out     []byte
	// replies of the last Filter call, in output order. A refused command is
	// answered here and dropped from the stream.
	replies []gfxReply
}

// Filter returns the bytes to hand to the terminal and the window in place of
// in. The result is valid until the next call.
func (f *gfxFilter) Filter(in []byte) []byte {
	f.replies = f.replies[:0]
	if cap(f.out) > gfxKeep {
		f.out = nil
	}
	if f.state == gfGround && !bytes.Contains(in, []byte("\x1b_")) &&
		(len(in) == 0 || in[len(in)-1] != 0x1b) {
		return in
	}
	f.out = f.out[:0]
	for i := 0; i < len(in); {
		switch f.state {
		case gfGround:
			j := bytes.IndexByte(in[i:], 0x1b)
			if j < 0 {
				f.out = append(f.out, in[i:]...)
				i = len(in)
				break
			}
			f.out = append(f.out, in[i:i+j]...)
			f.held = append(f.held[:0], 0x1b)
			f.emitted = 0
			f.state = gfPrefix
			i += j + 1
		case gfPrefix:
			b := in[i]
			switch {
			case len(f.held) == 1 && b == '_', len(f.held) == 2 && b == 'G':
				f.held = append(f.held, b)
				i++
				if len(f.held) == 3 {
					f.state = gfAPC
				}
			default:
				f.flushHeld(len(f.held))
				f.state = gfGround // b is scanned again from the ground
			}
		case gfAPC:
			j := bytes.IndexAny(in[i:], "\x1b\x18\x1a")
			end := len(in)
			if j >= 0 {
				end = i + j
			}
			f.held = append(f.held, in[i:end]...)
			i = end
			if len(f.held) > maxAPC+3 {
				f.flushHeld(len(f.held))
				f.state = gfSkip
				break
			}
			if j < 0 {
				break
			}
			b := in[i]
			i++
			if b == 0x1b {
				f.held = append(f.held, b)
				f.state = gfAPCEsc
				break
			}
			f.held = append(f.held, b) // CAN or SUB: aborted, passed on as is
			f.flushHeld(len(f.held))
			f.state = gfGround
		case gfAPCEsc:
			if in[i] == '\\' {
				i++
				f.held = append(f.held, '\\')
				rw, reply := rewriteGfx(f.held)
				if rw == nil {
					// Dropped, but a prefix already out must be closed: it
					// becomes an empty APC, which vt ignores.
					if f.emitted > 0 {
						f.out = append(f.out, "\x1b_\x1b\\"[f.emitted:]...)
					}
					if reply != nil {
						f.replies = append(f.replies, gfxReply{at: len(f.out), msg: reply})
					}
				} else {
					f.out = append(f.out, rw[f.emitted:]...)
				}
				f.state = gfGround
				break
			}
			// Aborted by another escape sequence: pass the APC on, keep the
			// ESC as the start of what follows.
			f.flushHeld(len(f.held) - 1)
			f.held = append(f.held[:0], 0x1b)
			f.emitted = 0
			f.state = gfPrefix
		case gfSkip:
			j := bytes.IndexAny(in[i:], "\x1b\x18\x1a")
			if j < 0 {
				f.out = append(f.out, in[i:]...)
				i = len(in)
				break
			}
			b := in[i+j]
			f.out = append(f.out, in[i:i+j+1]...)
			i += j + 1
			if b == 0x1b {
				f.state = gfSkipEsc
			} else {
				f.state = gfGround
			}
		case gfSkipEsc:
			if in[i] == '\\' {
				f.out = append(f.out, '\\')
				i++
				f.state = gfGround
				break
			}
			// The ESC, already passed on, starts a new sequence.
			f.held = append(f.held[:0], 0x1b)
			f.emitted = 1
			f.state = gfPrefix
		}
	}
	// A prefix is passed on at once: it costs nothing to send it early.
	if f.state == gfPrefix {
		f.out = append(f.out, f.held[f.emitted:]...)
		f.emitted = len(f.held)
	}
	if f.state == gfGround || f.state == gfSkip || f.state == gfSkipEsc {
		f.held = f.held[:0]
		if cap(f.held) > gfxKeep {
			f.held = nil // do not keep a 4 MiB buffer for nothing
		}
	}
	return f.out
}

// writeTerm hands out to term and sends each of the filter's replies once
// term has parsed the bytes before it, so that replies to the program keep
// the order of its commands.
func writeTerm(term *vt.Terminal, send func([]byte), out []byte, replies []gfxReply) {
	prev := 0
	for _, r := range replies {
		if r.at > prev {
			term.Write(out[prev:r.at])
		}
		if r.msg != nil {
			send(r.msg)
		}
		prev = r.at
	}
	if len(out) > prev {
		term.Write(out[prev:])
	}
}

// flushHeld passes on held[:n] as is (minus what is already out).
func (f *gfxFilter) flushHeld(n int) {
	f.out = append(f.out, f.held[f.emitted:n]...)
	f.emitted = 0
}

// rewriteGfx returns the command to pass on for a complete graphics sequence
// seq (ESC _ G … ESC \): seq itself unless it is a transmission from a file
// or shared memory, which is read here and turned into direct chunks. When
// that fails, out is nil (the command is dropped) and reply is the answer
// for the program, nil if the command asks for none.
func rewriteGfx(seq []byte) (out, reply []byte) {
	body := seq[3 : len(seq)-2]
	ctrl, payload, _ := bytes.Cut(body, []byte(";"))
	var (
		kept       [][]byte
		medium     byte = 'd'
		action     byte = 't'
		size, off  int64
		badKey     bool
		id, num, p uint64
		quiet      uint64
	)
	for pair := range bytes.SplitSeq(ctrl, []byte(",")) {
		k, v, ok := bytes.Cut(pair, []byte("="))
		if !ok || len(k) != 1 {
			kept = append(kept, pair)
			continue
		}
		var err error
		switch k[0] {
		case 't':
			if len(v) == 1 {
				medium = v[0]
			}
			continue
		case 'a':
			if len(v) == 1 {
				action = v[0]
			}
		case 'S', 'O':
			n, perr := strconv.ParseUint(string(v), 10, 63)
			if perr != nil {
				badKey = true
			}
			if k[0] == 'S' {
				size = int64(n)
			} else {
				off = int64(n)
			}
			continue
		case 'm':
			continue // the rewrite chunks on its own
		case 'i':
			id, err = strconv.ParseUint(string(v), 10, 32)
		case 'I':
			num, err = strconv.ParseUint(string(v), 10, 32)
		case 'p':
			p, err = strconv.ParseUint(string(v), 10, 32)
		case 'q':
			quiet, err = strconv.ParseUint(string(v), 10, 8)
		}
		if err != nil {
			badKey = true
		}
		kept = append(kept, pair)
	}
	if (medium != 'f' && medium != 't' && medium != 's') ||
		(action != 't' && action != 'T' && action != 'q') {
		return seq, nil
	}
	var err error
	name, ok := decodeB64(payload)
	switch {
	case !ok && len(bytes.TrimSpace(payload)) == 0:
		err = errGfxRefused // no name: nothing to open
	case !ok, len(name) > 2048, badKey, size > maxImageBytes:
		err = errGfxInval // as kitty: bad keys, bad or too long a name
	default:
		var data []byte
		data, err = readGfxDeadline(medium, string(name), size, off)
		if err == nil {
			return inlineGfx(bytes.Join(kept, []byte(",")), data), nil
		}
	}
	return nil, gfxErrReply(uint32(id), uint32(num), uint32(p), quiet, err.Error())
}

// gfxErrReply formats an error answer as vt's gReply does, or returns nil
// when the command asks for none (no id, or q=2).
func gfxErrReply(id, num, p uint32, quiet uint64, msg string) []byte {
	if (id == 0 && num == 0) || quiet >= 2 {
		return nil
	}
	b := []byte("\x1b_Gi=")
	b = strconv.AppendUint(b, uint64(id), 10)
	if num != 0 {
		b = append(b, ",I="...)
		b = strconv.AppendUint(b, uint64(num), 10)
	}
	if p != 0 {
		b = append(b, ",p="...)
		b = strconv.AppendUint(b, uint64(p), 10)
	}
	b = append(b, ';')
	b = append(b, msg...)
	return append(b, "\x1b\\"...)
}

// inlineGfx writes data as a direct transmission with the given keys, in
// chunks of gfxChunk base64 bytes. Later chunks only carry m, as the protocol
// requires.
func inlineGfx(keys, data []byte) []byte {
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
	base64.StdEncoding.Encode(enc, data)
	n := (len(enc) + gfxChunk - 1) / gfxChunk
	out := make([]byte, 0, len(enc)+len(keys)+n*12+8)
	if n <= 1 {
		out = append(out, "\x1b_G"...)
		out = append(out, keys...)
		if len(enc) > 0 {
			out = append(out, ';')
			out = append(out, enc...)
		}
		return append(out, "\x1b\\"...)
	}
	for c := range n {
		chunk := enc[c*gfxChunk : min((c+1)*gfxChunk, len(enc))]
		out = append(out, "\x1b_G"...)
		if c == 0 {
			out = append(out, keys...)
			if len(keys) > 0 {
				out = append(out, ',')
			}
		}
		if c == n-1 {
			out = append(out, "m=0;"...)
		} else {
			out = append(out, "m=1;"...)
		}
		out = append(out, chunk...)
		out = append(out, "\x1b\\"...)
	}
	return out
}

func decodeB64(p []byte) ([]byte, bool) {
	clean := bytes.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, p)
	if len(clean) == 0 {
		return nil, false
	}
	out, err := base64.StdEncoding.DecodeString(string(clean))
	if err != nil {
		out, err = base64.RawStdEncoding.DecodeString(string(clean))
	}
	return out, err == nil && len(out) > 0
}

// File reads run under the session's feed lock, so that the inline copy
// comes out exactly where the command was. A file on a stuck mount must not
// hold that lock: past gfxReadTimeout the command is answered EBADF and the
// read is left to finish on its own. At most cap(gfxReaders) such reads can
// be pending; beyond that, commands are refused at once. Variables for tests.
var (
	gfxReadTimeout = 500 * time.Millisecond
	gfxReaders     = make(chan struct{}, 4)
	gfxRead        = readGfxSource
)

func readGfxDeadline(medium byte, name string, size, off int64) ([]byte, error) {
	select {
	case gfxReaders <- struct{}{}:
	default:
		return nil, errGfxRefused
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-gfxReaders }()
		data, err := gfxRead(medium, name, size, off)
		done <- result{data, err}
	}()
	timer := time.NewTimer(gfxReadTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.data, r.err
	case <-timer.C:
		return nil, errGfxRefused
	}
}

// readGfxSource reads the data of a t=f, t=t or t=s transmission, at most
// size bytes (0: up to the end) from off.
func readGfxSource(medium byte, name string, size, off int64) ([]byte, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return nil, errGfxRefused
	}
	if medium == 's' {
		// POSIX shared memory is /dev/shm/<name> on Linux. Elsewhere it
		// needs shm_open, so cgo: refused.
		name = strings.TrimPrefix(name, "/")
		if runtime.GOOS != "linux" || name == "" || strings.ContainsRune(name, '/') || name == "." || name == ".." {
			return nil, errGfxRefused
		}
		path := "/dev/shm/" + name
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() {
			return nil, errGfxRefused
		}
		data, err := readRegular(path, size, off)
		os.Remove(path) // read, then unlinked, as shm_unlink would
		return data, err
	}
	// The daemon's working directory is not the program's: only absolute
	// paths mean something here.
	if !filepath.IsAbs(name) {
		return nil, errGfxRefused
	}
	// Checked on the resolved path before opening it: merely opening some
	// files has side effects.
	real, err := filepath.EvalSymlinks(name)
	if err != nil || sensitivePath(real) {
		return nil, errGfxRefused
	}
	var sfs unix.Statfs_t
	if unix.Statfs(real, &sfs) != nil || refusedFS(&sfs) {
		return nil, errGfxRefused
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return nil, errGfxRefused
	}
	data, err := readRegular(real, size, off)
	// kitty's rule: a temporary file is deleted only inside a temporary
	// directory and with this marker in its path.
	if medium == 't' && inTempDir(real) && strings.Contains(real, "tty-graphics-protocol") {
		os.Remove(real)
	}
	return data, err
}

// readRegular opens path without following a last-moment symlink nor
// blocking on a FIFO swapped in, checks it is still a regular file on a
// local filesystem, then reads at most maxImageBytes. Like kitty, it answers
// EBADF when less than size bytes (or nothing) could be read.
func readRegular(path string, size, off int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errGfxRefused
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var sfs unix.Statfs_t
	if unix.Fstatfs(fd, &sfs) != nil || refusedFS(&sfs) {
		return nil, errGfxRefused
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errGfxRefused
	}
	limit := int64(maxImageBytes + 1)
	if size > 0 {
		limit = min(size, limit)
	}
	data, err := io.ReadAll(io.NewSectionReader(f, off, limit))
	switch {
	case err != nil, len(data) == 0, size > 0 && int64(len(data)) < size:
		return nil, errGfxRefused
	case len(data) > maxImageBytes:
		return nil, errGfxInval // kitty: data size too large
	}
	return data, nil
}

// sensitivePath reports a path under /proc, /sys or /dev (but /dev/shm):
// reading there can have side effects or leak secrets.
func sensitivePath(p string) bool {
	for _, dir := range []string{"/proc", "/sys", "/dev"} {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return !strings.HasPrefix(p, "/dev/shm/")
		}
	}
	return false
}

// gfxTempDirs lists the temporary directories where t=t files may be
// deleted (a variable for tests).
var gfxTempDirs = func() []string { return []string{os.TempDir(), "/tmp", "/dev/shm"} }

func inTempDir(p string) bool {
	for _, dir := range gfxTempDirs() {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dir = r
		}
		if strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
			return true
		}
	}
	return false
}
