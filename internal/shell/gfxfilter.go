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

var errGfxRefused = errors.New("graphics file refused")

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
}

// Filter returns the bytes to hand to the terminal and the window in place of
// in. The result is valid until the next call.
func (f *gfxFilter) Filter(in []byte) []byte {
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
				rw := rewriteGfx(f.held)
				f.out = append(f.out, rw[f.emitted:]...)
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
		if cap(f.held) > 64<<10 {
			f.held = nil // do not keep a 4 MiB buffer for nothing
		}
	}
	return f.out
}

// flushHeld passes on held[:n] as is (minus what is already out).
func (f *gfxFilter) flushHeld(n int) {
	f.out = append(f.out, f.held[f.emitted:n]...)
	f.emitted = 0
}

// rewriteGfx returns the command to pass on for a complete graphics sequence
// seq (ESC _ G … ESC \): seq itself unless it is a transmission from a file
// or shared memory, which is read here and turned into direct chunks.
func rewriteGfx(seq []byte) []byte {
	body := seq[3 : len(seq)-2]
	ctrl, payload, _ := bytes.Cut(body, []byte(";"))
	var (
		kept       [][]byte
		medium     byte = 'd'
		action     byte = 't'
		size, off  int64
		badSizeOff bool
	)
	for pair := range bytes.SplitSeq(ctrl, []byte(",")) {
		k, v, ok := bytes.Cut(pair, []byte("="))
		if !ok || len(k) != 1 {
			kept = append(kept, pair)
			continue
		}
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
			n, err := strconv.ParseInt(string(v), 10, 64)
			if err != nil || n < 0 {
				badSizeOff = true
			}
			if k[0] == 'S' {
				size = n
			} else {
				off = n
			}
			continue
		case 'm':
			continue // the rewrite chunks on its own
		}
		kept = append(kept, pair)
	}
	if (medium != 'f' && medium != 't' && medium != 's') ||
		(action != 't' && action != 'T' && action != 'q') {
		return seq
	}
	keys := bytes.Join(kept, []byte(","))
	var data []byte
	err := errGfxRefused
	if name, ok := decodeB64(payload); ok && !badSizeOff {
		data, err = readGfxSource(medium, string(name), size, off)
	}
	if err != nil {
		// Same command without its path nor its m key: the daemon's terminal
		// answers EBADF to a medium other than t=d, once, and the window's
		// terminal never answers.
		out := append([]byte("\x1b_G"), keys...)
		if len(keys) > 0 {
			out = append(out, ',')
		}
		out = append(out, "t="...)
		out = append(out, medium)
		return append(out, "\x1b\\"...)
	}
	return inlineGfx(keys, data)
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
// blocking on a FIFO swapped in, checks it is still a regular file, then
// reads at most maxImageBytes.
func readRegular(path string, size, off int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errGfxRefused
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errGfxRefused
	}
	limit := int64(maxImageBytes + 1)
	if size > 0 {
		limit = min(size, limit)
	}
	data, err := io.ReadAll(io.NewSectionReader(f, off, limit))
	if err != nil || len(data) > maxImageBytes {
		return nil, errGfxRefused
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

func inTempDir(p string) bool {
	for _, dir := range []string{os.TempDir(), "/tmp", "/dev/shm"} {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dir = r
		}
		if strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
			return true
		}
	}
	return false
}
