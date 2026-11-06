package ui

import (
	"strings"

	"github.com/hajimehoshi/ebiten/v2/audio"

	"github.com/ThiraSoft/neonmux/internal/sfx"
)

// The 8-bit synth (internal/sfx) plays through the audio context of the
// audio panes. The window blips softly on pane actions, the games make
// their own little noises. "sound" in the config, or mod+s, turns it off.

// startSound plugs the synth into the audio output, unless muted.
func (a *App) startSound() {
	if muted {
		return
	}
	var playing []*audio.Player
	sfx.SetOutput(func(pcm []byte) {
		if !a.cfg.Sound {
			return
		}
		// Players are kept until they end: one dropped mid-sound would be
		// cut by the garbage collector.
		j := 0
		for _, p := range playing {
			if p.IsPlaying() {
				playing[j] = p
				j++
			}
		}
		playing = playing[:j]
		if len(playing) >= 8 {
			return
		}
		p := audioContext().NewPlayerFromBytes(pcm)
		p.Play()
		playing = append(playing, p)
	})
}

// actionSound is the blip of a window action, if it has one.
func actionSound(action string) (sfx.Sound, bool) {
	switch {
	case action == "term.new" || action == "agent.new" || action == "browser.open" || action == "files.open" || action == "diff.open":
		return sfx.PaneOpen, true
	case action == "pane.close":
		return sfx.PaneClose, true
	case strings.HasPrefix(action, "focus."):
		return sfx.Focus, true
	case strings.HasPrefix(action, "swap.") || strings.HasPrefix(action, "ws.move.") || strings.HasPrefix(action, "float.") || action == "pane.park":
		return sfx.Move, true
	case strings.HasPrefix(action, "ws."):
		return sfx.Workspace, true
	case action == "pane.zoom" || action == "pane.float" || action == "view.zen" || action == "view.braille" || action == "layout.next" || action == "sound.toggle":
		return sfx.Toggle, true
	}
	return 0, false
}

func (a *App) toggleSound() {
	a.cfg.Sound = !a.cfg.Sound
	a.saveConfig()
	if a.cfg.Sound {
		a.toast("sound on", a.theme.Accent2)
	} else {
		a.toast("sound off", a.theme.Accent2)
	}
}
