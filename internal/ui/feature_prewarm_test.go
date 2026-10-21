package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// idleSession is a live session that does nothing.
type idleSession struct{ done chan struct{} }

func (s idleSession) Terminal() *vt.Terminal { return nil }
func (s idleSession) Key() string            { return "" }
func (s idleSession) Write([]byte)           {}
func (s idleSession) Resize(_, _, _, _ int)  {}
func (s idleSession) Done() <-chan struct{}  { return s.done }
func (s idleSession) ForegroundName() string { return "" }
func (s idleSession) ForegroundCwd() string  { return "" }
func (s idleSession) Cwd() string            { return "" }
func (s idleSession) Close()                 {}
func (s idleSession) Detach()                {}

// The spare is checked on every frame: the rc files' dates (nine stat
// calls, 30 allocations, a network round trip per stat on an NFS $HOME)
// are only read again every rcCheckEvery.
func TestSpareRcCheckThrottled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := filepath.Join(home, ".zshrc")
	os.WriteFile(rc, []byte("# rc"), 0o644)
	now := time.Now()
	p := NewTermPane(1, nil, "", nil)
	p.s = idleSession{done: make(chan struct{})}
	sp := &spareShell{p: p, born: now, rcMod: rcModTime()}
	if sp.stale(now) {
		t.Fatal("fresh spare considered stale")
	}
	if n := testing.AllocsPerRun(100, func() { sp.stale(now.Add(time.Second)) }); n != 0 {
		t.Errorf("%v allocations per frame", n)
	}
	later := now.Add(time.Hour)
	os.Chtimes(rc, later, later)
	if sp.stale(now.Add(time.Second)) {
		t.Error("dates read again before the delay")
	}
	if !sp.stale(now.Add(rcCheckEvery + time.Second)) {
		t.Error("modified rc file not noticed")
	}
}
