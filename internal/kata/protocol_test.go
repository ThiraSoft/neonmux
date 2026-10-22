package kata

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/vt"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseMsg(t *testing.T) {
	cases := map[string]Msg{
		"kata=ready":      {Kind: "ready"},
		"kata=missing":    {Kind: "missing"},
		"kata=old:0.9.5":  {Kind: "old", Version: "0.9.5"},
		"kata=loaded:x.y": {Kind: "loaded", ID: "x.y"},
		"kata=keys:12":    {Kind: "keys", Keys: 12},
		"kata=badpar:a.b": {Kind: "badpar", ID: "a.b"},
		"kata=done:" + b64(`{"keys":7,"seq":"f(ci(x<Esc>"}`): {Kind: "done", Keys: 7, Seq: "f(ci(x<Esc>"},
		"kata=quit:" + b64(`{"keys":2,"seq":":q"}`):          {Kind: "quit", Keys: 2, Seq: ":q"},
	}
	for body, want := range cases {
		if got, ok := ParseMsg(body); !ok || got != want {
			t.Errorf("ParseMsg(%q) = %+v, %v", body, got, ok)
		}
	}
	for _, bad := range []string{"", "fx=confetti", "kata=", "kata=keys:x", "kata=done:!!", "kata=what"} {
		if _, ok := ParseMsg(bad); ok {
			t.Errorf("ParseMsg(%q) must fail", bad)
		}
	}
}

func TestLaunchScriptQuotes(t *testing.T) {
	dir := "/tmp/it's a dir"
	s := LaunchScript(dir)
	out, err := exec.Command("sh", "-c", "printf %s "+quote1(dir)).Output()
	if err != nil || string(out) != dir {
		t.Errorf("quoting: %q %v", out, err)
	}
	if !strings.Contains(s, "kata=missing") || !strings.Contains(s, "exec nvim --clean") || !strings.Contains(s, quote1(dir+"/harness.lua")) {
		t.Errorf("script %q", s)
	}
}

// harness starts nvim with the harness in a PTY and returns a function
// that waits for the next kata message of a kind.
type harness struct {
	t   *testing.T
	s   *shell.Session
	dir string
	got []Msg
}

func startHarness(t *testing.T) *harness {
	t.Helper()
	needNvim(t)
	dir := t.TempDir()
	if err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	s, err := shell.Start(shell.Options{
		Argv: []string{"nvim", "--clean", "-n", "-i", "NONE", "-u", filepath.Join(dir, "harness.lua")},
		Dir:  dir, Env: []string{"NEONMUX_KATA_DIR=" + dir}, Cols: 100, Rows: 30, Scrollback: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := &harness{t: t, s: s, dir: dir}
	h.wait("ready")
	return h
}

func (h *harness) wait(kind string) Msg {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range h.s.Terminal().TakeEvents() {
			if ev.Kind != vt.EvFX {
				continue
			}
			if m, ok := ParseMsg(ev.Body); ok {
				h.got = append(h.got, m)
			}
		}
		for i, m := range h.got {
			if m.Kind == kind {
				h.got = h.got[i+1:]
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no %q message (got %+v)", kind, h.got)
	return Msg{}
}

// expectNo fails if a message of that kind comes within d.
func (h *harness) expectNo(kind string, d time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, ev := range h.s.Terminal().TakeEvents() {
			if m, ok := ParseMsg(ev.Body); ok && ev.Kind == vt.EvFX {
				if m.Kind == kind {
					h.t.Fatalf("unexpected %q: %+v", kind, m)
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var parensMission = Mission{
	ID: "obj.change-parens", Start: []string{"a (b c) d"}, Target: []string{"a (x) d"}, Cursor: Pos{0, 0},
	Par: Recipe{{"f(", ""}, {"ci(x<Esc>", ""}}, Highlight: &Span{0, 2, 7},
}

func TestHarnessRound(t *testing.T) {
	h := startHarness(t)
	if err := WriteMission(h.dir, parensMission); err != nil {
		t.Fatal(err)
	}
	if m := h.wait("loaded"); m.ID != "obj.change-parens" {
		t.Errorf("loaded %q", m.ID)
	}
	h.s.Write([]byte("f(ci(x\x1b"))
	m := h.wait("done")
	if m.Keys != 7 || m.Seq != "f(ci(x<Esc>" {
		t.Errorf("done %+v", m)
	}
}

func TestHarnessBadPar(t *testing.T) {
	h := startHarness(t)
	bad := parensMission
	bad.Par = Recipe{{"x", ""}}
	if err := WriteMission(h.dir, bad); err != nil {
		t.Fatal(err)
	}
	if m := h.wait("badpar"); m.ID != "obj.change-parens" {
		t.Errorf("badpar %+v", m)
	}
}

func TestHarnessIgnoresFocusEvents(t *testing.T) {
	h := startHarness(t)
	if err := WriteMission(h.dir, parensMission); err != nil {
		t.Fatal(err)
	}
	h.wait("loaded")
	h.s.Write([]byte("\x1b[I"))
	time.Sleep(200 * time.Millisecond)
	h.s.Write([]byte("\x1b[O"))
	time.Sleep(200 * time.Millisecond)
	h.s.Write([]byte("l"))
	if m := h.wait("keys"); m.Keys != 1 {
		t.Errorf("first counted key: %d, focus events must not count", m.Keys)
	}
}

func TestHarnessQuit(t *testing.T) {
	h := startHarness(t)
	if err := WriteMission(h.dir, parensMission); err != nil {
		t.Fatal(err)
	}
	h.wait("loaded")
	h.s.Write([]byte(":q!\r"))
	if m := h.wait("quit"); m.Keys != 4 {
		t.Errorf("quit %+v", m)
	}
}

func TestPrepareWritesHarness(t *testing.T) {
	dir := t.TempDir()
	if err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "harness.lua"))
	if err != nil || !strings.Contains(string(data), "kata=") {
		t.Errorf("harness.lua: %v", err)
	}
}

// The check of the par must not leave the solution within reach: the last
// inserted text (ctrl+a in insert mode) and the command-line history.
func TestHarnessForgetsInsertedText(t *testing.T) {
	h := startHarness(t)
	if err := WriteMission(h.dir, parensMission); err != nil {
		t.Fatal(err)
	}
	h.wait("loaded")
	h.s.Write([]byte("f(ci(\x01\x1b"))
	h.expectNo("done", 1500*time.Millisecond)
}

func TestHarnessForgetsHistory(t *testing.T) {
	h := startHarness(t)
	m := parensMission
	m.Par = Recipe{{":s/b c/x/<CR>", ""}}
	if err := WriteMission(h.dir, m); err != nil {
		t.Fatal(err)
	}
	h.wait("loaded")
	h.s.Write([]byte(":\x1b[A\x1b[A\x1b[A\r"))
	h.expectNo("done", 1500*time.Millisecond)
}

// The login shell may be fish or nushell: it only runs "exec /bin/sh file",
// and the POSIX script in that file reports a missing nvim.
func TestLaunchCommandWithoutNvim(t *testing.T) {
	dir := t.TempDir()
	if err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	for _, sh := range []string{"sh", "zsh", "bash", "fish"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		// An empty PATH: nvim may well live in /usr/bin. The scripts call
		// /bin/sh by its path and only use shell builtins.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "-c", LaunchCommand(dir))
		cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + dir}
		out, err := cmd.Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 127 || !strings.Contains(string(out), "kata=missing") {
			t.Errorf("%s: %q, %v", sh, out, err)
		}
	}
}
