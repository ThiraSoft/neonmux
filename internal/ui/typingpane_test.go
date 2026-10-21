package ui

import (
	"context"
	"image"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/games"
	"github.com/ThiraSoft/neonmux/internal/wiki"
)

// fakeArticles hands out the same article, without any network.
type fakeArticles struct {
	art   wiki.Article
	err   error
	asked atomic.Int32
}

func (f *fakeArticles) Next(context.Context) (wiki.Article, error) {
	f.asked.Add(1)
	return f.art, f.err
}
func (f *fakeArticles) Prefetch() {}

var catArticle = wiki.Article{Title: "Cats", Sections: []wiki.Section{{Paragraphs: []string{
	strings.Repeat("the cat sat on the mat while the dog slept by the warm fire. ", 6),
}}}}

func typingApp(t *testing.T) (*App, *TypingPane, *fakeArticles) {
	t.Helper()
	a, _ := coreApp(t, 1)
	src := &fakeArticles{art: catArticle}
	a.wikiSrc = src
	a.gameStore = games.Load(filepath.Join(t.TempDir(), "games.json"))
	a.showTyping()
	for _, p := range a.panes {
		if tp, ok := p.(*TypingPane); ok {
			return a, tp, src
		}
	}
	t.Fatal("no typing pane")
	return nil, nil, nil
}

func TestTypingRequestsText(t *testing.T) {
	a, p, src := typingApp(t)
	p.Update(a.frameCtx(time.Now()))
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("no answer posted")
	}
	if p.test == nil || src.asked.Load() != 1 || p.title != "Cats" {
		t.Errorf("text %v, asked %d", p.test != nil, src.asked.Load())
	}
}

func TestTypingModeKeys(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, catArticle, nil)
	ctx := a.frameCtx(time.Now())
	p.Key(ctx, KeyEvent{Name: "right"})
	if p.mode.String() != "time 60" || a.gameStore.Typing.Mode != "time 60" {
		t.Fatalf("mode %v, stored %q", p.mode, a.gameStore.Typing.Mode)
	}
	p.Chars(ctx, []rune("t"))
	p.Key(ctx, KeyEvent{Name: "right"})
	if p.mode.String() != "time 60" {
		t.Error("the mode cannot change once the test has started")
	}
}

func TestTypingTypes(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, catArticle, nil)
	p.Chars(a.frameCtx(time.Now()), []rune("the "))
	if p.test.Cur != 1 {
		t.Errorf("cur %d after a typed word", p.test.Cur)
	}
	p.Key(a.frameCtx(time.Now()), KeyEvent{Name: "backspace", Mods: ModAlt})
	if p.test.Typed[1] != "" {
		t.Error("alt+backspace clears the word")
	}
}

func TestTypingSuspendPauses(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, catArticle, nil)
	now := time.Now()
	p.Chars(a.frameCtx(now), []rune("t"))
	p.suspend(now.Add(time.Second))
	if p.test.Remaining(now.Add(time.Second)) != p.test.Remaining(now.Add(9*time.Second)) {
		t.Error("the clock must stop while the game is away")
	}
}

func TestTypingPausesWhenWindowUnfocused(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, catArticle, nil)
	defer func(f func() bool) { windowFocused = f }(windowFocused)
	windowFocused = func() bool { return false }
	now := time.Now()
	p.Chars(a.frameCtx(now), []rune("t"))
	p.Update(a.frameCtx(now.Add(time.Second)))
	if p.test.Remaining(now.Add(time.Second)) != p.test.Remaining(now.Add(9*time.Second)) {
		t.Error("the clock must stop while the window is not focused")
	}
}

// The explorer opened over the game takes the keyboard: the clock stops.
func TestTypingPausesUnderExplorer(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, catArticle, nil)
	defer func(f func() bool) { windowFocused = f }(windowFocused)
	windowFocused = func() bool { return true }
	now := time.Now()
	p.Chars(a.frameCtx(now), []rune("t"))
	a.openExplorer(t.TempDir())
	p.Update(a.frameCtx(now.Add(time.Second)))
	if p.test.Remaining(now.Add(time.Second)) != p.test.Remaining(now.Add(9*time.Second)) {
		t.Error("the clock must stop while the explorer is open")
	}
}

func TestTypingFailsWithoutText(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, wiki.Article{}, wiki.ErrUnreachable)
	if lastToast(a) != "Wikipedia unreachable" {
		t.Errorf("toast %q", lastToast(a))
	}
	if slices.Contains(a.panes, Pane(p)) {
		t.Error("the pane closes")
	}
}

func TestLateTextAfterCloseIgnored(t *testing.T) {
	a, p, _ := typingApp(t)
	a.closePane(slices.Index(a.panes, Pane(p)))
	n := len(a.toasts)
	p.gotText(a, catArticle, nil)
	p.gotText(a, wiki.Article{}, wiki.ErrUnreachable)
	if p.test != nil || len(a.toasts) != n {
		t.Error("an answer for a closed pane is dropped")
	}
}

func TestTypingResultsRecorded(t *testing.T) {
	a, p, _ := typingApp(t)
	p.gotText(a, catArticle, nil)
	now := time.Now()
	p.Chars(a.frameCtx(now), []rune("the cat "))
	p.Update(a.frameCtx(now.Add(31 * time.Second)))
	if p.result == nil || !p.newBest {
		t.Fatal("time is up: results and a first best")
	}
	if _, ok := a.gameStore.Typing.Best["time 30"]; !ok {
		t.Error("the best result is kept")
	}
	p.Key(a.frameCtx(now.Add(32*time.Second)), KeyEvent{Name: "enter"})
	if p.result != nil || p.test != nil {
		t.Error("enter on the results asks for a new text")
	}
}

func TestTypingDrawStates(t *testing.T) {
	a, p, _ := typingApp(t)
	screen := ebiten.NewImage(a.w, a.h)
	draw := func(state string) {
		t.Helper()
		ctx := a.frameCtx(time.Now())
		for _, r := range []image.Rectangle{p.content, image.Rect(10, 10, 60, 40)} {
			p.Draw(ctx, screen, r, true)
		}
		ctx.P.Flush(screen)
	}
	draw("loading")
	p.gotText(a, catArticle, nil)
	draw("ready")
	p.Chars(a.frameCtx(time.Now()), []rune("thx cat sat on"))
	draw("running")
	p.Update(a.frameCtx(time.Now().Add(time.Minute)))
	draw("results")
}

func TestPaletteHasTypingGame(t *testing.T) {
	a, _ := coreApp(t, 1)
	if !slices.ContainsFunc(a.commands(), func(c command) bool {
		return c.label == "Game: typing test" && c.action == "game.typing"
	}) {
		t.Error("palette entry missing")
	}
}

// Two tabs in a row ask for two texts: the older answer, arriving late, must
// not restart a test the player has started.
func TestTypingStaleAnswerDropped(t *testing.T) {
	a, p, _ := typingApp(t)
	old := p.gen
	p.newText(a)
	p.apply(a, p.gen, false, catArticle, nil)
	p.Chars(a.frameCtx(time.Now()), []rune("the "))
	p.apply(a, old, false, catArticle, nil)
	if p.test == nil || p.test.Cur != 1 {
		t.Error("a stale answer restarted the test")
	}
}

// A failed request for more text waits before trying again, instead of
// asking on every frame.
func TestTypingMoreBacksOff(t *testing.T) {
	a, p, src := typingApp(t)
	short := wiki.Article{Title: "Short", Sections: []wiki.Section{{Paragraphs: []string{"one two three four five six seven eight nine ten"}}}}
	p.apply(a, p.gen, false, short, nil)
	src.err = wiki.ErrUnreachable
	now := time.Now()
	p.Update(a.frameCtx(now))
	if !p.more {
		t.Fatal("too few words: more text is asked for")
	}
	p.apply(a, p.gen, true, wiki.Article{}, wiki.ErrUnreachable)
	p.Update(a.frameCtx(now.Add(100 * time.Millisecond)))
	if p.more {
		t.Error("right after a failure, no new request")
	}
	p.Update(a.frameCtx(now.Add(time.Minute)))
	if !p.more {
		t.Error("later, it tries again")
	}
}
