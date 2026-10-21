package ui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
}

func selectName(t *testing.T, e *Explorer, name string) {
	t.Helper()
	i := slices.IndexFunc(e.view, func(en entry) bool { return en.name == name })
	if i < 0 {
		t.Fatalf("%s not in %v", name, e.view)
	}
	e.sel = i
}

func names(e *Explorer) []string {
	var out []string
	for _, en := range e.view {
		out = append(out, en.name)
	}
	return out
}

// A named pipe no longer freezes the preview: os.Open waited there for a
// writer forever, in the middle of Draw.
func TestExplorerPreviewFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	e := NewExplorer(dir)
	e.Update(&Ctx{})
	selectName(t, e, "pipe")
	done := make(chan struct{})
	go func() {
		e.loadPreview(&Ctx{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Unblock the test goroutine before failing.
		if f, err := os.OpenFile(filepath.Join(dir, "pipe"), os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("preview stuck on a named pipe")
	}
	if !strings.Contains(e.prev.info, "named pipe") {
		t.Errorf("info %q", e.prev.info)
	}
}

// The directory is read in the background; a stale result (directory left
// in the meantime) is ignored.
func TestExplorerLoadsInBackground(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
		os.WriteFile(filepath.Join(root, d, "in-"+d+".txt"), []byte(d), 0o644)
	}
	a := featureApp()
	ctx := &Ctx{App: a}
	e := NewExplorer(root)
	e.Update(ctx)
	if len(e.view) != 0 || !e.listing {
		t.Fatalf("directory read synchronously: %v", names(e))
	}
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	if got := names(e); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("root: %v", got)
	}
	selectName(t, e, "a")
	e.enter(ctx)
	e.cd(a, filepath.Join(root, "b")) // the user moves on right away
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	time.Sleep(50 * time.Millisecond)
	for len(a.posted) > 0 {
		(<-a.posted)()
	}
	if got := names(e); e.dir != filepath.Join(root, "b") || !slices.Equal(got, []string{"in-b.txt"}) {
		t.Fatalf("%s: %v", e.dir, got)
	}
	// Going up selects the directory we came from again.
	e.cd(a, root)
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	if en, _ := e.current(); en.name != "b" {
		t.Errorf("selection %q, want b", en.name)
	}
}

// The preview is read in the background; only the one for the selected
// item is kept.
func TestExplorerPreviewInBackground(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "one.txt"), []byte("first"), 0o644)
	os.WriteFile(filepath.Join(dir, "two.txt"), []byte("second"), 0o644)
	a := featureApp()
	ctx := &Ctx{App: a}
	e := NewExplorer(dir)
	e.Update(ctx)
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	selectName(t, e, "one.txt")
	e.loadPreview(ctx)
	selectName(t, e, "two.txt")
	e.loadPreview(ctx)
	drain(t, a, 2*time.Second, func() bool { return len(e.prev.lines) > 0 })
	time.Sleep(50 * time.Millisecond)
	for len(a.posted) > 0 {
		(<-a.posted)()
	}
	if !strings.HasSuffix(e.prev.path, "two.txt") || e.prev.lines[0] != "second" {
		t.Fatalf("preview %s: %v", e.prev.path, e.prev.lines)
	}
}

// Thumbnails go through a single worker, which only decodes the last image
// requested; a thumbnail that arrives after the selection changed is
// dropped.
func TestExplorerThumbsOnlyLatest(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.png", "b.png", "c.png"} {
		writePNG(t, filepath.Join(dir, n), 64, 64)
	}
	a := featureApp()
	ctx := &Ctx{App: a}
	e := NewExplorer(dir)
	e.Update(ctx)
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	for _, n := range []string{"a.png", "b.png", "c.png"} {
		selectName(t, e, n)
		e.loadPreview(ctx)
	}
	drain(t, a, 2*time.Second, func() bool { return e.prev.thumb != nil })
	time.Sleep(100 * time.Millisecond)
	for len(a.posted) > 0 {
		(<-a.posted)()
	}
	if len(e.thumbs) != 1 || e.thumbs[filepath.Join(dir, "c.png")] == nil {
		t.Fatalf("thumbnails kept: %v", e.thumbs)
	}
}

// latestWorker only keeps the last job requested while it is busy.
func TestLatestWorker(t *testing.T) {
	var w latestWorker
	var mu sync.Mutex
	var ran []int
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	w.do(func() {
		close(started)
		<-release
		mu.Lock()
		ran = append(ran, 0)
		mu.Unlock()
	})
	<-started
	for i := 1; i <= 10; i++ {
		w.do(func() {
			mu.Lock()
			ran = append(ran, i)
			mu.Unlock()
			if i == 10 {
				close(finished)
			}
		})
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the last job did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(ran, []int{0, 10}) {
		t.Fatalf("jobs run: %v", ran)
	}
}
