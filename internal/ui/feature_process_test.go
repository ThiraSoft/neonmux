package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A restart that fails does not close the window: the binary is missing
// (checked before anything is detached) or cannot be executed.
func TestRestartFailureKeepsWindow(t *testing.T) {
	a := featureApp()
	a.restartWith(filepath.Join(t.TempDir(), "absent"), nil)
	if a.quit || a.shutdown || !strings.Contains(lastToast(a), "restart failed") {
		t.Fatalf("missing binary: quit %v, shutdown %v, toast %q", a.quit, a.shutdown, lastToast(a))
	}
	garbage := filepath.Join(t.TempDir(), "neonmux")
	os.WriteFile(garbage, []byte("\x00\x01not an executable"), 0o755)
	a.toasts = nil
	a.restartWith(garbage, nil)
	if a.quit || !strings.Contains(lastToast(a), "restart failed") {
		t.Fatalf("exec failed: quit %v, toast %q", a.quit, lastToast(a))
	}
}

// The log handed to the detached process is closed here, whether the start
// succeeds or not.
func TestStartDetachedClosesLog(t *testing.T) {
	for _, argv := range [][]string{{"/bin/sh", "-c", "exit 0"}, {filepath.Join(t.TempDir(), "absent")}} {
		f, err := os.CreateTemp(t.TempDir(), "force.log")
		if err != nil {
			t.Fatal(err)
		}
		startDetached(exec.Command(argv[0], argv[1:]...), f)
		if _, err := f.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
			t.Errorf("%v: descriptor still open (%v)", argv, err)
			f.Close()
		}
	}
}

// The o key in the file explorer waits for the opener: no more zombies.
func TestExplorerOpenLeavesNoZombie(t *testing.T) {
	bin := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	os.WriteFile(filepath.Join(bin, opener), []byte("#!/bin/sh\necho $$ > "+pidFile+"\n"), 0o755)
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)
	a := featureApp()
	ctx := &Ctx{App: a, Theme: a.theme}
	e := NewExplorer(dir)
	e.Update(ctx)
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	e.Chars(ctx, []rune{'o'})
	var pid string
	for deadline := time.Now().Add(3 * time.Second); pid == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		data, _ := os.ReadFile(pidFile)
		pid = strings.TrimSpace(string(data))
	}
	if pid == "" {
		t.Fatal("opener never started")
	}
	time.Sleep(300 * time.Millisecond)
	out, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", pid).Output()
	if strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
		t.Fatalf("process %s left as a zombie", pid)
	}
}
