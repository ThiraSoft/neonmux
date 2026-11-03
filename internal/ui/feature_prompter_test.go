package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// runPosted runs what the hub's watch posted for the main thread.
func runPosted(a *App) {
	for len(a.posted) > 0 {
		(<-a.posted)()
	}
}

func openPrompter(t *testing.T) (*App, *PrompterPane) {
	t.Helper()
	configEnv(t, "")
	a, _ := keysApp(t, 1)
	press(a, a.hintFor("prompter"))
	runPosted(a)
	pp := prompterOf(a)
	if pp == nil {
		t.Fatal("no prompter pane")
	}
	if a.focused() != Pane(pp) {
		t.Fatal("the prompter does not have the focus")
	}
	return a, pp
}

func TestPrompterOpens(t *testing.T) {
	a, _ := openPrompter(t)
	if a.cfg.Bindings != nil && a.hintFor("prompter") == "" {
		t.Fatal("no shortcut")
	}
	if !a.localHub.AnyOpen() {
		t.Fatal("the local queue is not open (no daemon)")
	}
}

func TestPrompterKeys(t *testing.T) {
	a, pp := openPrompter(t)
	a.localHub.Add("k", "claude", "/p", "un deux trois.")
	a.localHub.Add("k", "claude", "/p", "suite")
	runPosted(a)
	if pp.pl.cur().Seq == 0 {
		t.Fatal("no current track")
	}
	press(a, "space")
	if !pp.pl.playing {
		t.Fatal("space did not play")
	}
	press(a, "enter")
	if pp.pl.playing {
		t.Fatal("enter did not pause")
	}
	press(a, "right")
	if pp.pl.pos != 1 {
		t.Fatalf("pos %d after right", pp.pl.pos)
	}
	press(a, "left")
	if pp.pl.pos != 0 {
		t.Fatalf("pos %d after left", pp.pl.pos)
	}
	press(a, "up")
	if pp.pl.wpm != 325 || a.cfg.PrompterWPM != 325 {
		t.Fatalf("wpm %d, config %d", pp.pl.wpm, a.cfg.PrompterWPM)
	}
	data, _ := os.ReadFile(os.Getenv("NEONMUX_CONFIG"))
	if !strings.Contains(string(data), `"prompter_wpm": 325`) {
		t.Fatalf("config file: %s", data)
	}
	press(a, "down")
	if pp.pl.wpm != 300 {
		t.Fatalf("wpm %d", pp.pl.wpm)
	}
	press(a, "n")
	runPosted(a)
	if n := len(a.localHub.Queue("window").Tracks); n != 1 {
		t.Fatalf("%d tracks after n", n)
	}
	if !pp.pl.playing || pp.pl.cur().Words[0].T != "suite" {
		t.Fatal("n did not play the next track")
	}
}

func TestPrompterCloseDropsQueue(t *testing.T) {
	a, _ := openPrompter(t)
	a.localHub.Add("k", "claude", "", "un")
	runPosted(a)
	press(a, a.hintFor("pane.close"))
	if prompterOf(a) != nil {
		t.Fatal("still there")
	}
	if a.localHub.AnyOpen() {
		t.Fatal("queue still open after closing the pane")
	}
}

func TestPrompterPersist(t *testing.T) {
	a, _ := openPrompter(t)
	st := a.layoutState()
	found := false
	for _, ps := range st.Panes {
		found = found || ps.Kind == "prompter"
	}
	if !found {
		t.Fatalf("layout %+v", st.Panes)
	}
}

func TestPrompterDraws(t *testing.T) {
	a, pp := openPrompter(t)
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	a.localHub.Add("k", "claude", "/home/me/proj", "Le correctif tient en deux points: le daemon garde la session vivante.")
	a.localHub.Add("k", "agy", "", "suite")
	runPosted(a)
	pp.pl.toggle()
	settle(a, screen)
	if len(pp.buttons) == 0 {
		t.Fatal("no buttons laid out")
	}
}

// A prompter restored from layout.json opens its queue at its first frame.
func TestPrompterRestoredOpens(t *testing.T) {
	configEnv(t, "")
	a, _ := keysApp(t, 1)
	pp := NewPrompterPane(a.newID(), DefaultConfig())
	a.panes = append(a.panes, pp)
	pp.Update(a.frameCtx(time.Now()))
	if a.localHub == nil || !a.localHub.AnyOpen() {
		t.Fatal("not opened")
	}
}

// prompter_context changed in the config reaches an open prompter.
func TestPrompterContextReload(t *testing.T) {
	a, pp := openPrompter(t)
	nc := a.cfg
	nc.PrompterContext = 0
	a.applyConfig(nc)
	if pp.ctxWords != 0 {
		t.Fatalf("context %d after reload", pp.ctxWords)
	}
}

// prompter_hold: space plays while it is held, its release pauses.
func TestPrompterHoldSpace(t *testing.T) {
	a, pp := openPrompter(t)
	nc := a.cfg
	nc.PrompterHold = true
	nc.PrompterBlankMs = 40
	a.applyConfig(nc)
	if !pp.hold || pp.pl.blank != 40*time.Millisecond {
		t.Fatalf("hold %v blank %v after reload", pp.hold, pp.pl.blank)
	}
	down := true
	old := spaceDown
	spaceDown = func() bool { return down }
	defer func() { spaceDown = old }()
	a.localHub.Add("k", "claude", "/p", "un deux trois quatre cinq six.")
	runPosted(a)
	press(a, "space")
	if !pp.pl.playing {
		t.Fatal("space held does not play")
	}
	now := time.Now()
	ctx := a.frameCtx(now)
	if w := pp.NextWake(ctx); w.Sub(now) > holdPoll {
		t.Fatalf("next wake in %v while held", w.Sub(now))
	}
	pp.Update(ctx)
	if !pp.pl.playing {
		t.Fatal("paused while held")
	}
	down = false
	pp.Update(ctx)
	if pp.pl.playing || pp.held {
		t.Fatal("release did not pause")
	}
	press(a, "enter")
	if !pp.pl.playing {
		t.Fatal("enter still toggles")
	}
}

// prompter.play reads the next reply from another pane, the focus stays.
func TestPrompterPlayNextFromAnyPane(t *testing.T) {
	a, pp := openPrompter(t)
	a.localHub.Add("k", "claude", "/p", "un deux.")
	a.localHub.Add("k", "claude", "/p", "trois quatre.")
	runPosted(a)
	a.goTo(a.panes[0])
	other := a.focused()
	if other == Pane(pp) {
		t.Fatal("setup: the prompter still has the focus")
	}
	first := pp.pl.cur().Seq
	a.do("prompter.play")
	if !pp.pl.playing || pp.pl.cur().Seq != first || a.focused() != other {
		t.Fatalf("fresh track: playing %v seq %d focus moved %v", pp.pl.playing, pp.pl.cur().Seq, a.focused() != other)
	}
	a.do("prompter.play")
	if !pp.pl.playing || pp.pl.cur().Seq == first {
		t.Fatalf("next track: playing %v seq %d", pp.pl.playing, pp.pl.cur().Seq)
	}
	a.do("prompter.play")
	if pp.pl.playing {
		t.Fatal("last track: the key did not pause")
	}
}

// The track number blinks while replies wait after the current one.
func TestPrompterBlinksWhilePending(t *testing.T) {
	a, pp := openPrompter(t)
	a.localHub.Add("k", "claude", "/p", "un deux.")
	runPosted(a)
	now := time.Now()
	ctx := a.frameCtx(now)
	if pp.pending() {
		t.Fatal("pending with one track")
	}
	a.localHub.Add("k", "claude", "/p", "trois.")
	runPosted(a)
	if !pp.pending() {
		t.Fatal("not pending with two tracks")
	}
	if w := pp.NextWake(ctx); w.Sub(now) > pendingBlink {
		t.Fatalf("next wake in %v", w.Sub(now))
	}
	pp.lit = !blinkLit(now)
	if !pp.Update(ctx) {
		t.Fatal("phase change not redrawn")
	}
}

// prompter_blink false: no blink, no wakeup for it.
func TestPrompterBlinkOff(t *testing.T) {
	a, pp := openPrompter(t)
	nc := a.cfg
	nc.PrompterBlink = false
	a.applyConfig(nc)
	a.localHub.Add("k", "claude", "/p", "un.")
	a.localHub.Add("k", "claude", "/p", "deux.")
	runPosted(a)
	if !pp.pending() || pp.blinking() {
		t.Fatalf("pending %v blinking %v", pp.pending(), pp.blinking())
	}
}
