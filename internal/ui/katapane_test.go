package ui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/games"
	"github.com/ThiraSoft/neonmux/internal/kata"
)

// kataBench is a kata pane whose Neovim is a fake terminal: the test writes
// the harness's messages into it and reads what the pane sends.
type kataBench struct {
	a     *App
	p     *KataPane
	terms []*fakeBackend
}

func newKataBench(t *testing.T) *kataBench {
	t.Helper()
	a, _ := coreApp(t, 1)
	a.wikiSrc = &fakeArticles{err: errors.New("no network in tests")}
	a.gameStore = games.Load(filepath.Join(t.TempDir(), "games.json"))
	b := &kataBench{a: a}
	old := newKataTerm
	newKataTerm = func(a *App, p *KataPane) *TermPane {
		tp := NewTermPane(a.newID(), nil, "", nil)
		be := newFakeBackend(100, 30)
		tp.s, tp.t, tp.cols, tp.rows = be, be.term, 100, 30
		b.terms = append(b.terms, be)
		return tp
	}
	t.Cleanup(func() { newKataTerm = old })
	a.showKata()
	for _, p := range a.panes {
		if kp, ok := p.(*KataPane); ok {
			b.p = kp
		}
	}
	if b.p == nil {
		t.Fatal("no kata pane")
	}
	t.Cleanup(b.p.Close)
	return b
}

var testMission = kata.Mission{
	ID: "obj.empty-parens", Chapter: 4, Name: "Empty the parentheses", Instruction: "Empty the parentheses, keeping them.",
	Title: "Test", Start: []string{"a (b c) d"}, Target: []string{"a () d"}, Cursor: kata.Pos{},
	Par: kata.Recipe{{Keys: "f(", Explain: "next '('"}, {Keys: "di(", Explain: "delete inside the parentheses"}},
}

// say writes a harness message into the current fake terminal and lets the
// pane read it.
func (b *kataBench) say(msg string) {
	be := b.terms[len(b.terms)-1]
	fmt.Fprintf(be.term, "\x1b]7777;kata=%s\x07", msg)
	b.p.Update(b.a.frameCtx(time.Now()))
}

// gone reports a pane that the app is closing (or has closed).
func (b *kataBench) gone() bool { return b.p.Done() || !slices.Contains(b.a.panes, Pane(b.p)) }

func payload(keys int, seq string) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, `{"keys":%d,"seq":%q}`, keys, seq))
}

// play starts testMission and lets the harness load it.
func (b *kataBench) play() {
	b.p.gotMission(b.a, b.p.gen, testMission, nil)
	b.say("ready")
	b.say("loaded:" + testMission.ID)
}

func TestKataDoneRecords(t *testing.T) {
	b := newKataBench(t)
	b.play()
	if b.p.screen != kataPlaying {
		t.Fatalf("screen %v", b.p.screen)
	}
	b.say("done:" + payload(5, "f(di("))
	if b.p.screen != kataEnd || b.p.end == nil || b.p.end.stars != 3 {
		t.Fatalf("end %+v", b.p.end)
	}
	if r := b.a.gameStore.Kata[testMission.ID]; r.Stars != 3 || r.Wins != 1 {
		t.Errorf("record %+v", r)
	}
}

func TestKataPracticeNotRecorded(t *testing.T) {
	b := newKataBench(t)
	b.play()
	b.say("done:" + payload(9, "xxxxxxxxx"))
	before := b.a.gameStore.Kata[testMission.ID]
	b.p.Key(b.a.frameCtx(time.Now()), KeyEvent{Name: "r"})
	if b.p.mission == nil || !b.p.mission.Practice || b.p.screen != kataPlaying {
		t.Fatal("r tries the same text again")
	}
	b.say("loaded:" + testMission.ID)
	b.say("done:" + payload(5, "f(di("))
	if after := b.a.gameStore.Kata[testMission.ID]; after != before {
		t.Errorf("a practice try changed the record: %+v → %+v", before, after)
	}
}

func TestQuitIsGiveUp(t *testing.T) {
	b := newKataBench(t)
	b.play()
	b.say("quit:" + payload(3, ":q!"))
	close(b.terms[0].done)
	b.p.Update(b.a.frameCtx(time.Now()))
	if b.p.screen != kataEnd || !b.p.end.gaveUp || b.p.term != nil {
		t.Fatalf("give up: screen %v end %+v", b.p.screen, b.p.end)
	}
	if _, ok := b.a.gameStore.Kata[testMission.ID]; ok {
		t.Error("a give up is not recorded")
	}
	b.p.gotMission(b.a, b.p.gen, testMission, nil)
	if len(b.terms) != 2 {
		t.Error("the next mission starts a new nvim")
	}
}

func TestKataMissingNvim(t *testing.T) {
	b := newKataBench(t)
	b.p.gotMission(b.a, b.p.gen, testMission, nil)
	b.say("missing")
	if lastToast(b.a) != "Vim kata needs Neovim" || !b.gone() {
		t.Errorf("toast %q", lastToast(b.a))
	}
}

func TestKataOldNvim(t *testing.T) {
	b := newKataBench(t)
	b.p.gotMission(b.a, b.p.gen, testMission, nil)
	b.say("old:0.9.5")
	if lastToast(b.a) != "Vim kata needs Neovim 0.10 or later (found 0.9.5)" {
		t.Errorf("toast %q", lastToast(b.a))
	}
}

func TestKataFailedStart(t *testing.T) {
	b := newKataBench(t)
	b.p.gotMission(b.a, b.p.gen, testMission, nil)
	close(b.terms[0].done)
	b.p.Update(b.a.frameCtx(time.Now()))
	if lastToast(b.a) != "Vim kata: Neovim failed to start" || !b.gone() {
		t.Errorf("toast %q", lastToast(b.a))
	}
}

func TestKataKeysGoToNvim(t *testing.T) {
	b := newKataBench(t)
	ctx := b.a.frameCtx(time.Now())
	b.p.Key(ctx, KeyEvent{Name: "j"})
	if b.p.sel != 1 {
		t.Errorf("j moves in the chapter list: sel %d", b.p.sel)
	}
	b.play()
	b.p.Chars(ctx, []rune("dw"))
	if !strings.Contains(b.terms[0].sent(), "dw") {
		t.Errorf("sent %q", b.terms[0].sent())
	}
}

func TestKataSuspendKeepsNvimState(t *testing.T) {
	b := newKataBench(t)
	b.play()
	now := time.Now()
	b.p.Chars(b.a.frameCtx(now), []rune("f"))
	b.p.suspend(now.Add(time.Second))
	if b.p.clock.Running() || b.p.term == nil || b.p.term.Done() {
		t.Error("away: the clock stops, nvim stays")
	}
}

// The explorer opened over the game takes the keyboard: the clock stops.
func TestKataPausesUnderExplorer(t *testing.T) {
	b := newKataBench(t)
	defer func(f func() bool) { windowFocused = f }(windowFocused)
	windowFocused = func() bool { return true }
	b.play()
	now := time.Now()
	b.p.Chars(b.a.frameCtx(now), []rune("f"))
	b.a.openExplorer(t.TempDir())
	b.p.Update(b.a.frameCtx(now.Add(time.Second)))
	if b.p.clock.Running() {
		t.Error("the clock must stop while the explorer is open")
	}
}

func TestNoSuitableText(t *testing.T) {
	b := newKataBench(t)
	b.p.chapter, b.p.screen = 4, kataLoading // chosen from the list of chapter 4
	b.p.gotMission(b.a, b.p.gen, kata.Mission{}, errNoSuitableText)
	if lastToast(b.a) != "No suitable text found, try again" || b.p.screen != kataMissions {
		t.Errorf("toast %q, screen %v", lastToast(b.a), b.p.screen)
	}
}

func TestClosedChaptersStayClosed(t *testing.T) {
	b := newKataBench(t)
	ctx := b.a.frameCtx(time.Now())
	b.p.Key(ctx, KeyEvent{Name: "j"})
	b.p.Key(ctx, KeyEvent{Name: "enter"})
	if b.p.screen != kataChapters {
		t.Error("chapter 2 is closed with no records")
	}
	b.p.Key(ctx, KeyEvent{Name: "k"})
	b.p.Key(ctx, KeyEvent{Name: "enter"})
	if b.p.screen != kataMissions || b.p.chapter != 1 {
		t.Errorf("chapter 1 opens: screen %v chapter %d", b.p.screen, b.p.chapter)
	}
}

func TestKataDrawScreens(t *testing.T) {
	b := newKataBench(t)
	screen := ebiten.NewImage(b.a.w, b.a.h)
	draw := func() {
		ctx := b.a.frameCtx(time.Now())
		b.p.Draw(ctx, screen, b.p.content, true)
		b.p.Draw(ctx, screen, b.p.content.Inset(b.p.content.Dx()/3), true)
		ctx.P.Flush(screen)
	}
	draw()
	b.p.Key(b.a.frameCtx(time.Now()), KeyEvent{Name: "enter"})
	draw()
	b.play()
	draw()
	b.say("done:" + payload(4, "f(di("))
	draw()
}

func TestPaletteHasKataGame(t *testing.T) {
	a, _ := coreApp(t, 1)
	if !slices.ContainsFunc(a.commands(), func(c command) bool {
		return c.label == "Game: vim kata" && c.action == "game.kata"
	}) {
		t.Error("palette entry missing")
	}
}
