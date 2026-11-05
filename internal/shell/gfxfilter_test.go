package shell

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
	"golang.org/x/sys/unix"
)

// testPNG returns a noisy 64x64 PNG: large enough to need several 4096-byte
// base64 chunks once inlined.
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	r := rand.New(rand.NewSource(1))
	for y := range 64 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// apc wraps a kitty graphics command.
func apc(keys, payload string) string {
	if payload == "" {
		return "\x1b_G" + keys + "\x1b\\"
	}
	return "\x1b_G" + keys + ";" + payload + "\x1b\\"
}

// replay feeds out into a fresh terminal that records its replies, like the
// daemon's own terminal.
func replay(t *testing.T, out []byte) (*vt.Terminal, *bytes.Buffer) {
	t.Helper()
	term := vt.New(80, 24, 10)
	var replies bytes.Buffer
	term.SetReply(func(b []byte) { replies.Write(b) })
	term.Write(out)
	return term, &replies
}

// feed runs parts through f and joins the results (each is only valid until
// the next call).
func feed(f *gfxFilter, parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, f.Filter(p)...)
	}
	return out
}

// daemon runs parts through f into a terminal the way readLoop does, and
// returns it with every reply to the program (the filter's and vt's).
func daemon(t *testing.T, f *gfxFilter, parts ...[]byte) (*vt.Terminal, *bytes.Buffer) {
	t.Helper()
	term := vt.New(80, 24, 10)
	var replies bytes.Buffer
	send := func(b []byte) { replies.Write(b) }
	term.SetReply(send)
	for _, p := range parts {
		writeTerm(term, send, f.Filter(p), f.replies)
	}
	return term, &replies
}

func imageData(t *testing.T, term *vt.Terminal, id uint32) []byte {
	t.Helper()
	term.Lock()
	defer term.Unlock()
	img, ok := term.Image(id)
	if !ok {
		return nil
	}
	return img.Data
}

func TestFilterPassThrough(t *testing.T) {
	in := "hello \x1b[31mred\x1b[0m \x1b]0;title\x07 é\x1b" +
		apc("a=T,f=100,i=3", base64.StdEncoding.EncodeToString(testPNG(t))[:100]) +
		"\x1b_Xother apc\x1b\\" + "\x1b_Gi=4,a=q;AAAA\x18" + "\x1b_Gi=5\x1bc" +
		apc("a=d,d=a", "") + "end\x1b"
	var f gfxFilter
	if got := string(f.Filter([]byte(in))); got != in {
		t.Fatalf("one call: got %q", got)
	}
	for cut := range len(in) + 1 {
		var f gfxFilter
		got := string(feed(&f, []byte(in[:cut]), []byte(in[cut:])))
		if got != in {
			t.Fatalf("cut at %d: got %q", cut, got)
		}
	}
	// Byte by byte.
	var f2 gfxFilter
	var out []byte
	for i := range len(in) {
		out = append(out, f2.Filter([]byte{in[i]})...)
	}
	if string(out) != in {
		t.Fatalf("byte by byte: got %q", out)
	}
}

func TestFilterOversizeAPCPassesThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(path, testPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file transmission past maxAPC is not rewritten: vt drops it anyway.
	in := apc("a=t,t=f,i=1,f=100", b64(path)+strings.Repeat("A", maxAPC))
	var f gfxFilter
	var out []byte
	for i := 0; i < len(in); i += 64 << 10 {
		out = append(out, f.Filter([]byte(in[i:min(i+64<<10, len(in))]))...)
	}
	out = append(out, f.Filter([]byte("x"))...)
	if string(out) != in+"x" {
		t.Fatal("oversize sequence not passed through as is")
	}
}

func TestFilterFile(t *testing.T) {
	data := testPNG(t)
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	direct, _ := replay(t, []byte(apc("a=t,f=100,i=7", base64.StdEncoding.EncodeToString(data))))
	want := imageData(t, direct, 7)
	if want == nil {
		t.Fatal("direct transmission stored nothing")
	}

	in := []byte("before" + apc("a=t,t=f,f=100,i=7,q=0", b64(path)) + "after")
	for _, cut := range []int{0, 7, 9, len(in) / 2, len(in) - 6, len(in)} {
		var f gfxFilter
		out := feed(&f, in[:cut], in[cut:])
		if bytes.Contains(out, []byte("t=f")) {
			t.Fatalf("cut %d: t=f left in the stream", cut)
		}
		if !bytes.HasPrefix(out, []byte("before")) || !bytes.HasSuffix(out, []byte("after")) {
			t.Fatalf("cut %d: surrounding text lost", cut)
		}
		term, replies := replay(t, out)
		if got := imageData(t, term, 7); !bytes.Equal(got, want) {
			t.Fatalf("cut %d: image differs (%d bytes, want %d)", cut, len(got), len(want))
		}
		if replies.String() != "\x1b_Gi=7;OK\x1b\\" {
			t.Fatalf("cut %d: replies %q", cut, replies)
		}
	}

	// S and O: read only part of a larger file.
	padded := filepath.Join(t.TempDir(), "padded.bin")
	if err := os.WriteFile(padded, append(append([]byte("junk"), data...), "trailer"...), 0o600); err != nil {
		t.Fatal(err)
	}
	var f gfxFilter
	keys := "a=t,t=f,f=100,i=8,O=4,S=" + strconv.Itoa(len(data))
	term, replies := daemon(t, &f, []byte(apc(keys, b64(padded))))
	if got := imageData(t, term, 8); !bytes.Equal(got, want) {
		t.Fatalf("S/O: image differs (%d bytes), replies %q", len(got), replies)
	}
}

func TestFilterTempFileDeleted(t *testing.T) {
	data := testPNG(t)
	tmpDir, other := t.TempDir(), t.TempDir()
	saved := gfxTempDirs
	gfxTempDirs = func() []string { return []string{tmpDir} }
	t.Cleanup(func() { gfxTempDirs = saved })
	write := func(dir, name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for i, c := range []struct {
		path    string
		deleted bool
	}{
		{write(tmpDir, "tty-graphics-protocol-a.png"), true},
		{write(tmpDir, "neonmux-plain.png"), false},          // no marker
		{write(other, "tty-graphics-protocol-b.png"), false}, // not a temporary directory
	} {
		var f gfxFilter
		id := uint32(i + 1)
		term, _ := daemon(t, &f, []byte(apc("a=t,t=t,f=100,i="+strconv.Itoa(int(id)), b64(c.path))))
		if imageData(t, term, id) == nil {
			t.Errorf("%s: not transmitted", c.path)
		}
		_, err := os.Stat(c.path)
		if gone := os.IsNotExist(err); gone != c.deleted {
			t.Errorf("%s: deleted %v, want %v", c.path, gone, c.deleted)
		}
	}
}

func TestFilterErrorCodes(t *testing.T) {
	data := testPNG(t)
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	big := strconv.Itoa(maxImageBytes + 1)
	for _, c := range []struct{ keys, want string }{
		{"S=abc", "EINVAL"},
		{"O=-1", "EINVAL"},
		{"S=" + big, "EINVAL"},
		{"O=" + strconv.Itoa(len(data)), "EBADF"},    // nothing left to read
		{"S=" + strconv.Itoa(len(data)+1), "EBADF"},  // shorter than S
		{"q=2,O=" + strconv.Itoa(len(data)), ""},     // errors silenced
		{"I=4,p=2,O=" + strconv.Itoa(len(data)), ""}, // checked below
	} {
		var f gfxFilter
		keys := "a=T,t=f,f=100,i=3," + c.keys
		want := ""
		if c.want != "" {
			want = "\x1b_Gi=3;" + c.want + "\x1b\\"
		}
		if strings.HasPrefix(c.keys, "I=") {
			want = "\x1b_Gi=3,I=4,p=2;EBADF\x1b\\"
		}
		_, replies := daemon(t, &f, []byte(apc(keys, b64(path))))
		if replies.String() != want {
			t.Errorf("%s: replies %q, want %q", c.keys, replies, want)
		}
	}
	// No name, an undecodable one.
	for _, c := range []struct{ payload, want string }{{"", "EBADF"}, {"!!!", "EINVAL"}} {
		var f gfxFilter
		_, replies := daemon(t, &f, []byte(apc("a=t,t=f,i=3", c.payload)))
		if want := "\x1b_Gi=3;" + c.want + "\x1b\\"; replies.String() != want {
			t.Errorf("payload %q: replies %q, want %q", c.payload, replies, want)
		}
	}
}

// A refused command is dropped: the reply keeps its place among vt's, and a
// prefix already passed on is closed so that what follows is still text.
func TestFilterRefusalOrder(t *testing.T) {
	in := []byte("\x1b_Ga=q,i=1,s=1,v=1;AAAAAA==\x1b\\" + apc("a=t,t=f,i=2", b64("/proc/self/environ")) +
		"\x1b_Ga=q,i=3,s=1,v=1;AAAAAA==\x1b\\after")
	second := bytes.Index(in[1:], []byte("\x1b_G")) + 1
	for _, cut := range []int{len(in), second + 1, second + 2, second + 3} {
		var f gfxFilter
		term, replies := daemon(t, &f, in[:cut], in[cut:])
		want := "\x1b_Gi=1;OK\x1b\\\x1b_Gi=2;EBADF\x1b\\\x1b_Gi=3;OK\x1b\\"
		if replies.String() != want {
			t.Errorf("cut %d: replies %q", cut, replies)
		}
		var g gfxFilter
		window, _ := replay(t, feed(&g, in[:cut], in[cut:]))
		for _, term := range []*vt.Terminal{term, window} {
			line := term.ScreenText()[0]
			if !strings.HasPrefix(line, "after") {
				t.Errorf("cut %d: screen %q", cut, line)
			}
		}
	}
}

// A file on a stuck mount never holds the session for long, and stuck reads
// do not pile up.
func TestFilterReadDeadline(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	savedRead, savedTimeout := gfxRead, gfxReadTimeout
	gfxRead = func(byte, string, int64, int64) ([]byte, error) {
		calls.Add(1)
		<-release
		return nil, errGfxRefused
	}
	gfxReadTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		close(release)
		for len(gfxReaders) > 0 {
			time.Sleep(time.Millisecond)
		}
		gfxRead, gfxReadTimeout = savedRead, savedTimeout
	})
	cmd := []byte(apc("a=t,t=f,i=1", b64("/mnt/stuck/img.png")))
	for n := range cap(gfxReaders) + 2 {
		var f gfxFilter
		start := time.Now()
		_, replies := daemon(t, &f, cmd)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("command %d held the filter %v", n, d)
		}
		if replies.String() != "\x1b_Gi=1;EBADF\x1b\\" {
			t.Fatalf("command %d: replies %q", n, replies)
		}
	}
	if got := calls.Load(); got != int32(cap(gfxReaders)) {
		t.Fatalf("%d reads started, want %d", got, cap(gfxReaders))
	}
}

func TestFilterRefusedFilesystems(t *testing.T) {
	refused := "/proc"
	if runtime.GOOS == "darwin" {
		refused = "/dev"
	}
	var st unix.Statfs_t
	if err := unix.Statfs(refused, &st); err != nil {
		t.Fatal(err)
	}
	if !refusedFS(&st) {
		t.Errorf("%s accepted", refused)
	}
	if err := unix.Statfs(t.TempDir(), &st); err != nil {
		t.Fatal(err)
	}
	if refusedFS(&st) {
		t.Skip("the temporary directory is itself on a refused filesystem")
	}
}

func TestFilterReleasesBuffers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	var f gfxFilter
	if out := f.Filter([]byte(apc("a=t,t=f,f=100,i=1", b64(path)))); len(out) < 1<<20 {
		t.Fatalf("not inlined: %d bytes", len(out))
	}
	f.Filter([]byte("x"))
	f.Filter([]byte("\x1b_Ga=d\x1b\\"))
	if c := cap(f.out); c > gfxKeep {
		t.Fatalf("output buffer kept: %d bytes", c)
	}
	if c := cap(f.held); c > gfxKeep {
		t.Fatalf("held buffer kept: %d bytes", c)
	}
}

func TestFilterRefusesSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/dev/zero", link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/proc/self/environ", "/dev/zero", dir, link, fifo, "relative.png", filepath.Join(dir, "missing")} {
		var f gfxFilter
		in := []byte(apc("a=T,t=f,f=100,i=9", b64(path)))
		if out := f.Filter(in); len(out) != 0 {
			t.Errorf("%s: refused command left %q in the stream", path, out)
		}
		var g gfxFilter
		term, replies := daemon(t, &g, in)
		if replies.String() != "\x1b_Gi=9;EBADF\x1b\\" {
			t.Errorf("%s: replies %q", path, replies)
		}
		if imageData(t, term, 9) != nil {
			t.Errorf("%s: image stored", path)
		}
	}
	// Nothing read from the FIFO: opening it for reading would have blocked
	// the test, reading /dev/zero would have produced 32 MiB.
}

func TestFilterSharedMemory(t *testing.T) {
	data := testPNG(t)
	name := "neonmux-test-" + strconv.Itoa(os.Getpid())
	if runtime.GOOS != "linux" {
		var f gfxFilter
		_, replies := daemon(t, &f, []byte(apc("a=t,t=s,f=100,i=4", b64(name))))
		if replies.String() != "\x1b_Gi=4;EBADF\x1b\\" {
			t.Fatalf("replies %q", replies)
		}
		return
	}
	shm := "/dev/shm/" + name
	if err := os.WriteFile(shm, data, 0o600); err != nil {
		t.Skip("no /dev/shm:", err)
	}
	t.Cleanup(func() { os.Remove(shm) })
	var f gfxFilter
	term, replies := daemon(t, &f, []byte(apc("a=t,t=s,f=100,i=4", b64("/"+name))))
	if got := imageData(t, term, 4); !bytes.Equal(got, data) {
		t.Fatalf("shared memory not transmitted, replies %q", replies)
	}
	if _, err := os.Stat(shm); !os.IsNotExist(err) {
		t.Fatal("shared memory object not unlinked")
	}
	// A name that escapes /dev/shm is refused.
	_, replies = daemon(t, &f, []byte(apc("a=t,t=s,f=100,i=5", b64("../../etc/passwd"))))
	if replies.String() != "\x1b_Gi=5;EBADF\x1b\\" {
		t.Fatalf("escaping name: replies %q", replies)
	}
}

// The daemon's session inlines the file both for its own terminal and for
// the stream it sends to the window.
func TestFilterSession(t *testing.T) {
	data := testPNG(t)
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var tapped []byte
	script := "printf '\\033_Ga=t,t=f,f=100,i=6;" + b64(path) + "\\033\\\\done\\n'; sleep 5"
	s, err := Start(Options{
		Argv: []string{"/bin/sh", "-c", script}, Dir: os.TempDir(), Cols: 80, Rows: 24, Scrollback: 10,
		Tap: func(b []byte) { mu.Lock(); tapped = append(tapped, b...); mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-s.Pid(), syscall.SIGKILL) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := bytes.Contains(tapped, []byte("done"))
		mu.Unlock()
		if got {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no output")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := imageData(t, s.Term, 6); !bytes.Equal(got, data) {
		t.Fatal("daemon terminal has no image")
	}
	mu.Lock()
	window, _ := replay(t, tapped)
	mu.Unlock()
	if got := imageData(t, window, 6); !bytes.Equal(got, data) {
		t.Fatal("window stream has no image")
	}
}
