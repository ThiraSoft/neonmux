package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ThiraSoft/neonmux/internal/server"
)

// absFS mimics the file system of ebiten.DroppedFiles: its entries know
// their real path (ebiten.AbsPather).
type absFS struct {
	root string
	fs.FS
}

type absEntry struct {
	fs.DirEntry
	abs string
}

func (e absEntry) AbsPath() string { return e.abs }

func (a absFS) ReadDir(name string) ([]fs.DirEntry, error) {
	ents, err := fs.ReadDir(a.FS, name)
	for i, e := range ents {
		ents[i] = absEntry{e, filepath.Join(a.root, name, e.Name())}
	}
	return ents, err
}

// Dropped files open where they are: no more copy into a shared
// /tmp/neonmux-demo, and no full read in Update.
func TestDroppedFilesOpenInPlace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "photo.png"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "album"), 0o755)
	os.WriteFile(filepath.Join(dir, "album", "song.mp3"), []byte("x"), 0o644)
	got := droppedPaths(absFS{dir, os.DirFS(dir)})
	want := []string{filepath.Join(dir, "album", "song.mp3"), filepath.Join(dir, "photo.png")}
	if !slices.Equal(got, want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
}

// The demo assets go into the private (0700) instance directory, never
// into a directory with a fixed name in the shared /tmp.
func TestDemoDirIsPrivate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("NEONMUX_SERVER_SOCK", filepath.Join(tmp, "inst", "server.sock"))
	d := demoDir()
	if d == "" || !strings.HasPrefix(d, server.InstanceDir()+string(filepath.Separator)) {
		t.Fatalf("directory %q outside %s", d, server.InstanceDir())
	}
	if st, err := os.Stat(server.InstanceDir()); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("instance directory: %v %v", st.Mode(), err)
	}
	if p := demoImage(); !strings.HasPrefix(p, d) {
		t.Errorf("image %q", p)
	}
}
