package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// watchTest watches path and returns the channel that receives each reload
// request.
func watchTest(t *testing.T, path string) chan struct{} {
	t.Helper()
	ch := make(chan struct{}, 16)
	stop, err := watchConfigFile(path, func() { ch <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ch
}

func expectReload(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: no reload", what)
	}
	// Events grouped by the debounce only count once.
	time.Sleep(250 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}
}

// saveByRename saves the way most editors do: a temporary file, then a
// rename.
func saveByRename(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp~"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func realTempDir(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A direct write and a save by rename both always reload.
func TestWatchConfigWriteAndRename(t *testing.T) {
	dir := realTempDir(t)
	p := filepath.Join(dir, "config.json")
	os.WriteFile(p, []byte(`{}`), 0o644)
	ch := watchTest(t, p)
	os.WriteFile(p, []byte(`{"typing_fx": true}`), 0o644)
	expectReload(t, ch, "write")
	saveByRename(t, p, `{"typing_fx": false}`)
	expectReload(t, ch, "rename")
}

// config.json as a symbolic link (stow, home-manager): changing the target,
// in another directory, reloads too.
func TestWatchConfigFollowsSymlinkTarget(t *testing.T) {
	dir, dots := realTempDir(t), realTempDir(t)
	target := filepath.Join(dots, "neonmux.json")
	os.WriteFile(target, []byte(`{}`), 0o644)
	p := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	ch := watchTest(t, p)
	saveByRename(t, target, `{"typing_fx": true}`)
	expectReload(t, ch, "target renamed")
	os.WriteFile(target, []byte(`{"typing_fx": false}`), 0o644)
	expectReload(t, ch, "target written")
}

// Directory deleted then recreated: watching resumes.
func TestWatchConfigDirRecreated(t *testing.T) {
	defer func(d time.Duration) { configRetry = d }(configRetry)
	configRetry = 100 * time.Millisecond
	root := realTempDir(t)
	dir := filepath.Join(root, "neonmux")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "config.json")
	os.WriteFile(p, []byte(`{}`), 0o644)
	ch := watchTest(t, p)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(p, []byte(`{"typing_fx": true}`), 0o644)
	expectReload(t, ch, "directory recreated")
	// And the following changes come through normally.
	time.Sleep(300 * time.Millisecond)
	saveByRename(t, p, `{"typing_fx": false}`)
	expectReload(t, ch, "after resuming")
}
