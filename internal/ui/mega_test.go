package ui

import (
	"testing"
	"time"
)

func typeWord(a *App, w string) {
	ctx := a.frameCtx(time.Now())
	for _, r := range w {
		a.handleKey(ctx, KeyEvent{Name: string(r)})
	}
}

// TestMegaStartsOverTheCity: the secret word starts the game in zen mode
// over the city only, and escape leaves it.
func TestMegaStartsOverTheCity(t *testing.T) {
	a, _ := coreApp(t, 1)
	a.zen = true
	a.bg.Kind = "none"
	typeWord(a, "game")
	if a.mega != nil {
		t.Fatal("started over another background")
	}
	a.bg.Kind = "city"
	typeWord(a, "gagame")
	if a.mega == nil {
		t.Fatal("the word did not start the game")
	}
	ctx := a.frameCtx(time.Now())
	a.handleKey(ctx, KeyEvent{Name: "i"})
	a.handleKey(ctx, KeyEvent{Name: "escape"})
	if a.mega == nil {
		t.Fatal("escape in the weapons left the game")
	}
	a.handleKey(ctx, KeyEvent{Name: "escape"})
	if a.mega != nil || !a.zen {
		t.Fatal("escape must leave the game, back to zen mode")
	}
}

// TestMegaNotWithPanes: a terminal in front receives the letters.
func TestMegaNotWithPanes(t *testing.T) {
	a, _ := coreApp(t, 1)
	a.bg.Kind = "city"
	typeWord(a, "game")
	if a.mega != nil {
		t.Fatal("started while a terminal is on screen")
	}
}
