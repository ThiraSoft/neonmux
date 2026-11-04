package ui

import (
	"encoding/json"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// driveSync runs a drive command and returns its answer, running the
// script frames until it comes.
func driveSync(t *testing.T, a *App, cmd string, args ...string) []string {
	t.Helper()
	var got []string
	a.drive(Request{Cmd: cmd, Args: args, Cwd: t.TempDir()}, func(l []string) { got = l })
	now := time.Now()
	for i := 0; got == nil && i < 200; i++ {
		now = now.Add(20 * time.Millisecond)
		a.runScript(a.frameCtx(now))
	}
	if got == nil {
		t.Fatalf("%s: no answer", cmd)
	}
	return got
}

func TestDriveState(t *testing.T) {
	a, ts := coreApp(t, 2)
	ts[1].name = "api"
	lines := driveSync(t, a, "state")
	if lines[len(lines)-1] != "ok" {
		t.Fatalf("answer %q", lines)
	}
	var s driveState
	if err := json.Unmarshal([]byte(lines[0]), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Panes) != 2 || s.Layout != "tiles" || s.Workspace != 1 {
		t.Fatalf("state %+v", s)
	}
	p0, p1 := s.Panes[0], s.Panes[1]
	if !p0.Focused || p1.Focused || p0.N != 1 || p1.N != 2 || p1.Name != "api" {
		t.Errorf("panes %+v %+v", p0, p1)
	}
	if p0.Kind != "term" || p0.Program != "zsh" || p0.Rect == nil || p0.Cells == nil || p0.Cursor == nil || p0.Dir == "" {
		t.Errorf("terminal %+v", p0)
	}
}

func TestDriveAction(t *testing.T) {
	a, _ := coreApp(t, 2)
	if l := driveSync(t, a, "action", "nope"); !strings.HasPrefix(l[0], "error: unknown action") {
		t.Errorf("unknown action: %q", l)
	}
	if l := driveSync(t, a, "action", "pane.zoom"); l[0] != "ok" || !a.zoomed {
		t.Errorf("pane.zoom: %q, zoomed %v", l, a.zoomed)
	}
	l := driveSync(t, a, "actions")
	joined := strings.Join(l, "\n")
	for _, want := range []string{"pane.zoom\t", "ws.3\t", "copy\t"} {
		if !strings.Contains(joined, want) {
			t.Errorf("actions lacks %q", want)
		}
	}
}

func TestDriveFocus(t *testing.T) {
	a, ts := coreApp(t, 3)
	ts[2].name = "logs"
	if l := driveSync(t, a, "focus", "logs"); l[0] != "ok" || a.focused() != ts[2] {
		t.Errorf("by name: %q", l)
	}
	if l := driveSync(t, a, "focus", "2"); l[0] != "ok" || a.focused() != ts[1] {
		t.Errorf("by number: %q", l)
	}
	if l := driveSync(t, a, "focus", "zsh"); !strings.Contains(l[0], "several panes") {
		t.Errorf("ambiguous: %q", l)
	}
	if l := driveSync(t, a, "focus", "nothing"); !strings.HasPrefix(l[0], "error: no pane") {
		t.Errorf("missing: %q", l)
	}
}

// neonmux key goes through the shortcuts like the keyboard, and answers
// once everything is typed.
func TestDriveKey(t *testing.T) {
	a, ts := coreApp(t, 2)
	a.mod = ModAlt
	a.buildBindings()
	be := ts[0].s.(*fakeBackend)
	if l := driveSync(t, a, "key", "text:echo hi", "enter"); l[0] != "ok" {
		t.Fatalf("answer %q", l)
	}
	if got := be.sent(); !strings.Contains(got, "echo hi\r") {
		t.Errorf("sent %q", got)
	}
	if l := driveSync(t, a, "key", "mod+f"); l[0] != "ok" || !a.zoomed {
		t.Errorf("mod+f: %q, zoomed %v", l, a.zoomed)
	}
	// A bad token types nothing at all.
	before := be.sent()
	for _, bad := range []string{"ctrl+nope+z", "ctrl+nope", "wait:soon"} {
		if l := driveSync(t, a, "key", "text:x", bad); !strings.HasPrefix(l[0], "error: bad") {
			t.Errorf("%s: %q", bad, l)
		}
	}
	for _, good := range []string{"enter", "f5", "pageup", "escape", "ctrl+shift+v", "mod+shift+h", "up", "space", "backspace"} {
		if err := a.checkToken(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	if be.sent() != before || len(a.script) != 0 {
		t.Error("a sequence with a bad token must type nothing")
	}
	if l := driveSync(t, a, "key", "wait:1h"); !strings.HasPrefix(l[0], "error: bad wait") {
		t.Errorf("an endless wait must be refused: %q", l)
	}
}

// Keys the CLI gave up on are never typed later.
func TestDriveKeyExpires(t *testing.T) {
	a, ts := coreApp(t, 1)
	be := ts[0].s.(*fakeBackend)
	var got []string
	a.drive(Request{Cmd: "key", Args: []string{"text:a", "text:b"}}, func(l []string) { got = l })
	a.keyJobs[0].deadline = time.Now().Add(-time.Second)
	a.runScript(a.frameCtx(time.Now()))
	if len(got) == 0 || !strings.HasPrefix(got[0], "error: too slow") || len(a.keyJobs) != 0 {
		t.Errorf("answer %q, %d jobs left", got, len(a.keyJobs))
	}
	if be.sent() != "" {
		t.Errorf("expired keys typed: %q", be.sent())
	}
}

// A shot nobody draws for is answered and forgotten.
func TestDriveShotStale(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	a, _ := coreApp(t, 1)
	var got []string
	a.drive(Request{Cmd: "shot", Cwd: t.TempDir()}, func(l []string) { got = l })
	a.dropStaleShots(time.Now().Add(driveTimeout + time.Second))
	if len(a.shots) != 0 || len(got) == 0 || !strings.HasPrefix(got[0], "error") {
		t.Errorf("answer %q, %d shots left", got, len(a.shots))
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "neonmux-shot-*")); len(left) != 0 {
		t.Errorf("empty shot file left: %v", left)
	}
}

// A link planted where the shot goes is not followed.
func TestDriveShotNoFollow(t *testing.T) {
	defer func(f func(interface{ ReadPixels([]byte) }, image.Rectangle) *image.RGBA) { readScreen = f }(readScreen)
	readScreen = func(_ interface{ ReadPixels([]byte) }, b image.Rectangle) *image.RGBA { return image.NewRGBA(b) }
	a, _ := coreApp(t, 1)
	dir := t.TempDir()
	target := filepath.Join(dir, "precious")
	os.WriteFile(target, []byte("keep"), 0o644)
	os.Symlink(target, filepath.Join(dir, "s.png"))
	done := make(chan []string, 1)
	a.drive(Request{Cmd: "shot", Args: []string{"s.png"}, Cwd: dir}, func(l []string) { done <- l })
	a.takeShots(fakeScreen{image.Rect(0, 0, a.w, a.h)})
	if l := <-done; !strings.HasPrefix(l[0], "error") {
		t.Errorf("answer %q", l)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Error("the link target was overwritten")
	}
}

func TestDriveShot(t *testing.T) {
	defer func(f func(interface{ ReadPixels([]byte) }, image.Rectangle) *image.RGBA) { readScreen = f }(readScreen)
	readScreen = func(_ interface{ ReadPixels([]byte) }, b image.Rectangle) *image.RGBA { return image.NewRGBA(b) }
	a, ts := coreApp(t, 2)
	dir := t.TempDir()
	done := make(chan []string, 2)
	a.drive(Request{Cmd: "shot", Args: []string{"w.png"}, Cwd: dir}, func(l []string) { done <- l })
	a.drive(Request{Cmd: "shot", Args: []string{"-p", "2", filepath.Join(dir, "p.png")}, Cwd: dir}, func(l []string) { done <- l })
	if !a.needRedraw || len(a.shots) != 2 {
		t.Fatal("a shot must ask for a frame")
	}
	a.takeShots(fakeScreen{image.Rect(0, 0, a.w, a.h)})
	for range 2 {
		l := <-done
		if len(l) != 2 || l[1] != "ok" {
			t.Fatalf("answer %q", l)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "w.png")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("a screenshot must be private: %v %v", st.Mode().Perm(), err)
	}
	whole := decodeSize(t, filepath.Join(dir, "w.png"))
	pane := decodeSize(t, filepath.Join(dir, "p.png"))
	if whole != image.Pt(a.w, a.h) || pane != ts[1].target.Size() {
		t.Errorf("sizes %v %v", whole, pane)
	}
}

type fakeScreen struct{ b image.Rectangle }

func (f fakeScreen) ReadPixels([]byte)       {}
func (f fakeScreen) Bounds() image.Rectangle { return f.b }

func decodeSize(t *testing.T, path string) image.Point {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img.Bounds().Size()
}

// The window socket answers the drive commands itself.
func TestDriveOverSocket(t *testing.T) {
	sock := shortSock(t)
	a, _ := coreApp(t, 1)
	stop, err := StartIPC(a, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv("NEONMUX_WINDOW_SOCK", sock)
	res := make(chan []string, 1)
	go func() {
		l, err := SendWindow(Request{Cmd: "state"})
		if err != nil {
			l = []string{err.Error()}
		}
		res <- l
	}()
	var got []string
	for got == nil {
		select {
		case fn := <-a.posted:
			fn()
		case got = <-res:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out")
		}
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "{") {
		t.Errorf("state over the socket: %q", got)
	}
}

// A socket planted in a directory that others can write to, or that is
// not ours, is never picked.
func TestWindowSocketSkipsSharedDir(t *testing.T) {
	t.Setenv("NEONMUX_WINDOW_SOCK", "")
	t.Setenv("NEONMUX_SOCK", "")
	dir, err := os.MkdirTemp("", "ws")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("NEONMUX_SERVER_SOCK", filepath.Join(dir, "server.sock"))
	l, err := net.Listen("unix", filepath.Join(dir, "gui-"+strconv.Itoa(os.Getpid())+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	os.Chmod(dir, 0o700)
	if s, err := WindowSocket(); err != nil || filepath.Dir(s) != dir {
		t.Fatalf("private dir: %q %v", s, err)
	}
	os.Chmod(dir, 0o777)
	defer os.Chmod(dir, 0o700)
	if s, err := WindowSocket(); err == nil && filepath.Dir(s) == dir {
		t.Fatalf("a world-writable directory must be skipped, got %q", s)
	}
}
