package ui

import (
	"math/rand/v2"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/mega"
)

// The mini game hidden in the city: over that background, with nothing in
// front of it (zen mode or no pane), typing "game" starts it. It takes the
// keyboard until escape: zqsd or the arrows move, space jumps, j shoots,
// i opens the weapons. Programs keep running behind it.

const megaWord = "game"

type megaRun struct {
	g    *mega.Game
	keys map[ebiten.Key]string // keys held, by their name in the layout
}

// megaWatch reads the letters typed over the city and starts the game on
// the secret word. It reports that it started it.
func (a *App) megaWatch(ev KeyEvent) bool {
	bare := a.zen || a.visibleCount() == 0 && a.palette == nil && a.renaming == nil && a.explorer == nil && a.mode == ""
	if !bare || a.rview != nil || a.bg == nil || a.bg.Kind != "city" || ev.Mods&^ModShift != 0 || ev.Repeat || len(ev.Name) != 1 {
		if !ev.Repeat {
			a.megaTyped = ""
		}
		return false
	}
	a.megaTyped += ev.Name
	if !strings.HasPrefix(megaWord, a.megaTyped) {
		a.megaTyped = ""
		if strings.HasPrefix(megaWord, ev.Name) {
			a.megaTyped = ev.Name
		}
		return false
	}
	if a.megaTyped != megaWord {
		return false
	}
	a.megaTyped = ""
	a.mega = &megaRun{g: mega.New(rand.Uint64()), keys: map[ebiten.Key]string{}}
	a.needRedraw = true
	return true
}

// megaKey routes a key to the game. Escape closes the weapons, then the
// game; the quit shortcut still quits.
func (a *App) megaKey(ev KeyEvent) {
	m := a.mega
	if !ev.Repeat {
		m.keys[ev.Key] = ev.Name
	}
	if ev.Name == "escape" && ev.Mods == 0 {
		if !m.g.Back() {
			a.mega = nil
		}
		a.needRedraw = true
		return
	}
	if ev.Mods&^ModShift != 0 {
		if act := a.bindings[Combo{Mods: ev.Mods, Name: ev.Name}]; act == "quit" {
			a.do(act)
		}
		return
	}
	m.g.Key(ev.Name, ev.Repeat)
}

// megaUpdate moves the game forward and the city with its camera.
func (a *App) megaUpdate(dt float64) {
	m := a.mega
	if m == nil {
		return
	}
	for k := range m.keys {
		if !ebiten.IsKeyPressed(k) {
			delete(m.keys, k)
		}
	}
	m.g.SetHeld(func(name string) bool {
		for _, n := range m.keys {
			if n == name {
				return true
			}
		}
		return false
	})
	s := mega.Scale(a.h)
	m.g.SetView(float64(a.w) / float64(s))
	m.g.Paused = !windowFocused()
	m.g.Update(dt)
	if dx := m.g.PanX(); dx != 0 {
		a.bg.Pan(dx * float64(s))
	}
	a.needRedraw = true
}

// megaFrame is the rate the game asks for.
func (a *App) megaFrame() int {
	if a.mega == nil {
		return 0
	}
	return 60
}

// megaDraw paints the game over the background.
func (a *App) megaDraw(ctx *Ctx, dst *ebiten.Image) {
	a.mega.g.Draw(ctx.P, a.uiFont, a.theme, a.w, a.h)
	ctx.P.Flush(dst)
}
