package ui

import (
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/sfx"
)

func TestActionSounds(t *testing.T) {
	for act, want := range map[string]sfx.Sound{
		"files.open": sfx.PaneOpen, "focus.left": sfx.Focus,
		"ws.3": sfx.Workspace, "ws.move.2": sfx.Move, "view.zen": sfx.Toggle,
	} {
		if got, ok := actionSound(act); !ok || got != want {
			t.Errorf("%s: %v %v, want %v", act, got, ok, want)
		}
	}
	for _, silent := range []string{"theme.next", "term.new", "pane.close"} {
		if _, ok := actionSound(silent); ok {
			t.Errorf("%s should stay silent in actionSound", silent)
		}
	}
}

func TestPaneLifecycleSounds(t *testing.T) {
	var count int
	sfx.SetOutput(func([]byte) { count++ })
	defer sfx.SetOutput(nil)

	a, _ := coreApp(t, 0)
	// While not ready, addPane and retire stay silent (startup)
	a.ready = false
	p1 := NewTermPane(a.newID(), nil, "", nil)
	a.addPane(p1)
	if count != 0 {
		t.Fatalf("expected 0 sounds when not ready, got %d", count)
	}
	a.closePane(0)
	if count != 0 {
		t.Fatalf("expected 0 sounds when not ready, got %d", count)
	}

	// When ready, addPane plays PaneOpen
	a.ready = true
	p2 := NewTermPane(a.newID(), nil, "", nil)
	a.addPane(p2)
	if count != 1 {
		t.Fatalf("expected 1 sound after addPane, got %d", count)
	}

	// Sleep >30ms to bypass sfx debounce
	time.Sleep(35 * time.Millisecond)

	// closePane plays PaneClose
	a.closePane(0)
	if count != 2 {
		t.Fatalf("expected 2 sounds after closePane, got %d", count)
	}

	// Parked pane close stays silent
	time.Sleep(35 * time.Millisecond)
	p3 := NewTermPane(a.newID(), nil, "", nil)
	p3.Base().parked = true
	a.panes = append(a.panes, p3)
	a.closePane(len(a.panes) - 1)
	if count != 2 {
		t.Fatalf("expected parked pane close to stay silent, got count %d", count)
	}

	// Away pane close stays silent
	time.Sleep(35 * time.Millisecond)
	p4 := NewTermPane(a.newID(), nil, "", nil)
	p4.Base().away = 2
	a.panes = append(a.panes, p4)
	a.closePane(len(a.panes) - 1)
	if count != 2 {
		t.Fatalf("expected away pane close to stay silent, got count %d", count)
	}
}
