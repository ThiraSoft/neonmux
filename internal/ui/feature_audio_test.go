package ui

import (
	"image"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A slow file (a NAS, here a named pipe that has no writer yet) no longer
// freezes the window: go-mp3 reads the whole file to compute its length,
// now off the main thread.
func TestAudioOpensInBackground(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow.mp3")
	if err := syscall.Mkfifo(slow, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	unblock := func() {
		if f, err := os.OpenFile(slow, os.O_WRONLY, 0); err == nil {
			f.Write([]byte("not an mp3"))
			f.Close()
		}
	}
	a := featureApp()
	p := NewAudioPane(1, slow)
	done := make(chan struct{})
	go func() {
		p.Layout(&Ctx{App: a}, image.Rect(0, 0, 400, 300))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		unblock()
		t.Fatal("Layout stuck opening the file")
	}
	if p.Info() != "loading…" {
		t.Errorf("info %q", p.Info())
	}
	go unblock()
	drain(t, a, 5*time.Second, func() bool { return !p.opening })
	if p.err == nil || p.player != nil {
		t.Fatalf("expected an error: %v", p.err)
	}
}

// Track changed while opening: the result for the old one is dropped.
func TestAudioStaleOpenIgnored(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "a.wav")
	if err := syscall.Mkfifo(slow, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	bad := filepath.Join(dir, "b.wav")
	os.WriteFile(bad, []byte("not a wav"), 0o644)
	a := featureApp()
	p := NewAudioPane(1, slow)
	p.open(a)
	p.path = bad
	p.open(a)
	drain(t, a, 5*time.Second, func() bool { return !p.opening })
	if p.err == nil {
		t.Fatal("expected an error from b.wav")
	}
	errB := p.err
	// Opening a.wav finishes afterwards: nothing changes.
	if f, err := os.OpenFile(slow, os.O_WRONLY, 0); err == nil {
		f.Write([]byte("RIFF"))
		f.Close()
	}
	time.Sleep(200 * time.Millisecond)
	for len(a.posted) > 0 {
		(<-a.posted)()
	}
	if p.err != errB || p.path != bad || p.opening {
		t.Fatalf("stale result applied: %v", p.err)
	}
}
