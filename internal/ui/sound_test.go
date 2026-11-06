package ui

import (
	"testing"

	"github.com/ThiraSoft/neonmux/internal/sfx"
)

func TestActionSounds(t *testing.T) {
	for act, want := range map[string]sfx.Sound{
		"term.new": sfx.PaneOpen, "pane.close": sfx.PaneClose, "focus.left": sfx.Focus,
		"ws.3": sfx.Workspace, "ws.move.2": sfx.Move, "view.zen": sfx.Toggle,
	} {
		if got, ok := actionSound(act); !ok || got != want {
			t.Errorf("%s: %v %v, want %v", act, got, ok, want)
		}
	}
	if _, ok := actionSound("theme.next"); ok {
		t.Error("theme.next should stay silent")
	}
}
