package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// spawnFixture puts a fake systemctl on PATH and returns the fake daemon
// binary and the two files that tell which of them ran.
func spawnFixture(t *testing.T, enabled bool) (exe, systemctlLog, exeRan string) {
	t.Helper()
	dir := t.TempDir()
	systemctlLog = filepath.Join(dir, "systemctl.log")
	exeRan = filepath.Join(dir, "exe.ran")
	status := "1"
	if enabled {
		status = "0"
	}
	fake := "#!/bin/sh\necho \"$@\" >> '" + systemctlLog + "'\ncase \"$*\" in *is-enabled*) exit " + status + ";; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(dir, "neonmux")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\ntouch '"+exeRan+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("NEONMUX_STATE", "")
	t.Setenv("NEONMUX_SERVER_SOCK", "")
	return exe, systemctlLog, exeRan
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func appears(path string) bool {
	for range 100 {
		if exists(path) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// With the user service enabled, the daemon belongs to it: started by hand,
// it would run outside the service, which would not restart it after a crash.
func TestSpawnGoesThroughEnabledService(t *testing.T) {
	exe, log, ran := spawnFixture(t, true)
	if err := Spawn(exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "start") || !strings.Contains(string(b), "neonmux.service") {
		t.Errorf("systemctl calls: %q", b)
	}
	time.Sleep(100 * time.Millisecond)
	if exists(ran) {
		t.Error("the binary was started as well as the service")
	}
}

func TestSpawnWithoutServiceStartsTheBinary(t *testing.T) {
	exe, _, ran := spawnFixture(t, false)
	if err := Spawn(exe); err != nil {
		t.Fatal(err)
	}
	if !appears(ran) {
		t.Error("the binary was not started")
	}
}

// An isolated instance (tests, side-by-side builds) never touches the
// user's service.
func TestSpawnIsolatedInstanceIgnoresService(t *testing.T) {
	exe, log, ran := spawnFixture(t, true)
	t.Setenv("NEONMUX_SERVER_SOCK", filepath.Join(t.TempDir(), "s.sock"))
	if err := Spawn(exe); err != nil {
		t.Fatal(err)
	}
	if !appears(ran) {
		t.Error("the binary was not started")
	}
	if exists(log) {
		b, _ := os.ReadFile(log)
		t.Errorf("systemctl was called: %q", b)
	}
}
