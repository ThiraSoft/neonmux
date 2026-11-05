package mega

import (
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Arcade is a game hidden in a background. Typing "game" over the
// background starts it (internal/ui/mega.go).
type Arcade interface {
	// Key handles a key press, by its name in the layout.
	Key(name string, repeat bool)
	// Back handles escape: false means leave the game.
	Back() bool
	// SetHeld gives the keys being held.
	SetHeld(func(string) bool)
	// Step moves the game forward by dt seconds on a W x H screen.
	Step(dt float64, W, H int, paused bool)
	// Draw paints the game over the background.
	Draw(P *gfx.Painter, f *gfx.Font, th *theme.Theme, W, H int)
}

// Panner is a game whose camera moves the background, in screen pixels.
type Panner interface{ PanScreen() float64 }

var arcades = map[string]func(seed uint64) Arcade{
	"city":    func(s uint64) Arcade { return New(s) },
	"stars":   func(s uint64) Arcade { return newStars(s) },
	"lava":    func(s uint64) Arcade { return newLava(s) },
	"circuit": func(s uint64) Arcade { return newSnake(s) },
}

// Has reports whether the background has a game.
func Has(kind string) bool { return arcades[kind] != nil }

// ForBackground starts the game of a background, nil if it has none.
func ForBackground(kind string, seed uint64) Arcade {
	if mk := arcades[kind]; mk != nil {
		return mk(seed)
	}
	return nil
}

// Step implements Arcade for the run and gun.
func (g *Game) Step(dt float64, W, H int, paused bool) {
	g.scale = Scale(H)
	g.SetView(float64(W) / float64(g.scale))
	g.Paused = paused
	g.Update(dt)
}

// PanScreen implements Panner: the city follows the camera.
func (g *Game) PanScreen() float64 { return g.PanX() * float64(max(1, g.scale)) }

// held keys shared by the games.
func left(h func(string) bool) bool  { return h("q") || h("left") || h("h") }
func right(h func(string) bool) bool { return h("d") || h("right") || h("l") }
func up(h func(string) bool) bool    { return h("z") || h("up") || h("k") }
func down(h func(string) bool) bool  { return h("s") || h("down") }

// best keeps the session's best scores, by game.
var best = map[string]int{}

func record(game string, score int) int {
	best[game] = max(best[game], score)
	return best[game]
}
