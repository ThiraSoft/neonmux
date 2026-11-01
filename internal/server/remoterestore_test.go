package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

const (
	restoredKey = "00112233aabbccdd"
	goneKey     = "99887766aabbccdd" // in the layout, without a snapshot
)

// saveTerminal leaves what a stopped daemon leaves on disk for key.
func saveTerminal(t *testing.T, s *Server, key string, cols, rows int, meta savedMeta) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(s.dir, "terms"), 0o700); err != nil {
		t.Fatal(err)
	}
	term := vt.New(cols, rows, 100)
	term.Write([]byte("before the reboot\r\n"))
	if err := os.WriteFile(s.snapPath(key), term.Snapshot(), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(s.metaPath(key), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLayoutFile(t *testing.T, s *Server, panes ...layoutPane) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"panes": panes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "layout.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// With remote control on, a daemon that starts with nothing to do stays up
// for the phone. Off, it stops as before.
func TestRemoteKeepsIdleDaemon(t *testing.T) {
	s, _ := inProcess(t, testRemote(t))
	s.mu.Lock()
	s.conns = 0
	s.idleSince = time.Now().Add(-2 * idleTimeout)
	s.mu.Unlock()
	if !s.idle() {
		t.Fatal("remote off: the daemon should stop")
	}
	if err := s.rem.Enable(); err != nil {
		t.Fatal(err)
	}
	if s.idle() {
		t.Fatal("remote on: the daemon stopped under the phone")
	}
	s.rem.Disable()
	if !s.idle() {
		t.Fatal("remote off again: the daemon should stop")
	}
}

// A real start with remote control on brings the window's terminals back
// from disk, at their saved size and in their saved directory, for a phone
// to find. A window opening later attaches to them.
func TestRemoteStartRestoresLayout(t *testing.T) {
	dir := t.TempDir()
	s, _ := inProcess(t, testRemote(t), phoneShell(t), func(s *Server) { s.relaunch = []string{"echo"} })
	saveTerminal(t, s, restoredKey, 90, 20, savedMeta{Dir: dir, FgArgv: []string{"echo", "relaunched-ok"}})
	saveTerminal(t, s, "not-a-key", 90, 20, savedMeta{Dir: dir})
	writeLayoutFile(t, s,
		layoutPane{Key: restoredKey, Name: "work", Path: "/nonexistent"},
		layoutPane{Key: goneKey, Path: dir},
		layoutPane{Key: "not-a-key", Path: dir},
		layoutPane{Path: dir}, // an image, a browser...
	)

	s.startRemote(false, true)
	if !s.rem.State().Enabled {
		t.Fatal("remote not on")
	}
	se := s.get(restoredKey)
	if se == nil {
		t.Fatal("the terminal did not come back")
	}
	t.Cleanup(func() { se.s.Close() })
	if len(s.list()) != 1 {
		t.Fatalf("%d sessions, want only the one with a valid key and a snapshot", len(s.list()))
	}
	if c, r := sizeOf(se.s.Term); c != 90 || r != 20 {
		t.Fatalf("size %dx%d, want the saved 90x20", c, r)
	}
	if !strings.Contains(termText(se.s.Term), "before the reboot") {
		t.Fatal("the saved screen is gone")
	}
	waitFor(t, "the command relaunched", 5*time.Second, func() bool {
		return strings.Count(termText(se.s.Term), "relaunched-ok") >= 2 // typed, then printed
	})
	want, _ := filepath.EvalSymlinks(dir)
	waitFor(t, "the saved directory", 5*time.Second, func() bool {
		got, _ := filepath.EvalSymlinks(se.s.Cwd())
		return got == want
	})
	list := (&remoteHost{s: s}).Sessions()
	if len(list) != 1 || list[0].Name != "work" {
		t.Fatalf("phone list %+v", list)
	}
	if s.orphans[restoredKey] {
		t.Fatal("already in the layout: nothing for a window to adopt")
	}

	// Run again (a second start path), nothing doubles.
	s.restoreLayout()
	if s.get(restoredKey) != se {
		t.Fatal("the running terminal was replaced")
	}
}

// Remote off, or a hot restart: the window stays the one that restores.
func TestRemoteStartLeavesLayoutAlone(t *testing.T) {
	s, _ := inProcess(t, testRemote(t), phoneShell(t))
	saveTerminal(t, s, restoredKey, 90, 20, savedMeta{})
	writeLayoutFile(t, s, layoutPane{Key: restoredKey})
	s.startRemote(false, false)
	if s.get(restoredKey) != nil {
		t.Fatal("restored with remote control off")
	}
	if err := os.WriteFile(filepath.Join(s.remCfg.Dir, "active"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.startRemote(true, true)
	if s.get(restoredKey) != nil {
		t.Fatal("restored over a hot restart")
	}
}
