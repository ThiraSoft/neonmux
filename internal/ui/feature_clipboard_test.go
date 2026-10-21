package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeClipboard replaces the clipboard tools with script (empty: no tool
// installed).
func fakeClipboard(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("PATH", dir)
	if script != "" {
		c, p := clipboardCmds()
		for _, name := range []string{c[0], p[0]} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// Without a clipboard tool, the explorer no longer says "copied".
func TestCopyWithoutToolSaysSo(t *testing.T) {
	fakeClipboard(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)
	a := featureApp()
	ctx := &Ctx{App: a, Theme: a.theme}
	e := NewExplorer(dir)
	e.Update(ctx)
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	e.Chars(ctx, []rune{'y'})
	drain(t, a, 3*time.Second, func() bool { return lastToast(a) != "" })
	if msg := lastToast(a); strings.Contains(msg, "copied") || !strings.Contains(msg, "not found") {
		t.Fatalf("toast %q", msg)
	}
}

// Without a success message (copy on select), a successful copy stays
// silent and only a failure is reported.
func TestCopyNotifyQuietUnlessError(t *testing.T) {
	fakeClipboard(t, "/bin/cat > /dev/null\n")
	a := featureApp()
	a.copyNotify("x", "")
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(3 * time.Second):
		t.Fatal("copy never finished")
	}
	if len(a.toasts) != 0 {
		t.Fatalf("unexpected toast %q", lastToast(a))
	}
	fakeClipboard(t, "")
	a.copyNotify("x", "")
	drain(t, a, 3*time.Second, func() bool { return lastToast(a) != "" })
	if msg := lastToast(a); !strings.Contains(msg, "not found") {
		t.Fatalf("toast %q", msg)
	}
}

// A frozen clipboard owner no longer leaves a command stuck forever: it is
// killed after the timeout, and the failure is reported.
func TestPasteTimesOut(t *testing.T) {
	defer func(d time.Duration) { clipboardTimeout = d }(clipboardTimeout)
	clipboardTimeout = time.Second // a new script takes ~300 ms to start on macOS
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeClipboard(t, "echo $$ > "+pidFile+"\nexec /bin/sleep 30\n")
	failed := make(chan error, 1)
	pasteText(func(string) { t.Error("unexpected paste") }, func(err error) { failed <- err })
	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), "not responding") {
			t.Errorf("error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error after the timeout")
	}
	data, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 {
		t.Fatal("tool never started")
	}
	time.Sleep(100 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("the stuck command is still running")
	}
}
