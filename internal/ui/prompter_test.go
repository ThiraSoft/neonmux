package ui

import (
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/prompt"
)

func track(seq uint64, words ...string) prompt.Track {
	tr := prompt.Track{Seq: seq, Key: "k", Agent: "claude"}
	for _, w := range words {
		tr.Words = append(tr.Words, prompt.Word{T: w, W: 1})
	}
	return tr
}

func queue(trs ...prompt.Track) prompt.Queue { return prompt.Queue{Open: true, Tracks: trs} }

// 300 wpm: one word of weight 1 lasts 200 ms.
const word = 200 * time.Millisecond

func TestPlayerNoAutoAdvance(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a", "b"), track(2, "c")))
	p.toggle()
	for range 10 {
		p.advance(word)
	}
	if !p.ended || p.playing || p.cur().Seq != 1 || p.pos != 1 {
		t.Fatalf("ended %v playing %v seq %d pos %d", p.ended, p.playing, p.cur().Seq, p.pos)
	}
}

func TestPlayerReplayAtEnd(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a", "b"), track(2, "c")))
	p.toggle()
	p.advance(10 * word)
	p.toggle()
	if !p.playing || p.ended || p.pos != 0 || p.cur().Seq != 1 {
		t.Fatalf("playing %v ended %v pos %d seq %d", p.playing, p.ended, p.pos, p.cur().Seq)
	}
}

func TestPlayerNextPlays(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a"), track(2, "c", "d")))
	dismiss, ok := p.next()
	if !ok || dismiss != 1 || p.cur().Seq != 2 || !p.playing || p.pos != 0 {
		t.Fatalf("dismiss %d %v seq %d playing %v", dismiss, ok, p.cur().Seq, p.playing)
	}
	// The daemon confirms with the queue without track 1.
	p.setQueue(queue(track(2, "c", "d")))
	if p.cur().Seq != 2 || !p.playing {
		t.Fatal("lost the track after the confirmation")
	}
	dismiss, ok = p.next()
	if !ok || dismiss != 2 || p.cur().Seq != 0 || p.playing {
		t.Fatalf("last next: %d %v", dismiss, ok)
	}
}

func TestPlayerMergeKeepsPosition(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a", "b", "c")))
	p.toggle()
	p.advance(word)
	p.setQueue(queue(track(1, "a", "b", "c"), track(2, "d")))
	if p.pos != 1 || p.cur().Seq != 1 || !p.playing {
		t.Fatalf("pos %d seq %d", p.pos, p.cur().Seq)
	}
	p.setQueue(queue(track(2, "d")))
	if p.pos != 0 || p.cur().Seq != 2 || p.playing {
		t.Fatalf("after the current track went: pos %d seq %d playing %v", p.pos, p.cur().Seq, p.playing)
	}
}

func TestPlayerTiming(t *testing.T) {
	p := newPlayer(300)
	tr := track(1, "a", "b", "c")
	tr.Words[0].W = 2
	p.setQueue(queue(tr))
	p.toggle()
	if p.advance(399 * time.Millisecond) {
		t.Fatal("moved before 400 ms")
	}
	if !p.advance(time.Millisecond) || p.pos != 1 {
		t.Fatalf("pos %d at 400 ms", p.pos)
	}
	if d := p.untilNext(); d != word {
		t.Fatalf("next word in %v", d)
	}
}

func TestPlayerSpeedClamp(t *testing.T) {
	p := newPlayer(300)
	p.speed(+1)
	if p.wpm != 325 {
		t.Fatalf("wpm %d", p.wpm)
	}
	for range 100 {
		p.speed(+1)
	}
	if p.wpm != 1000 {
		t.Fatalf("wpm %d", p.wpm)
	}
	for range 100 {
		p.speed(-1)
	}
	if p.wpm != 100 {
		t.Fatalf("wpm %d", p.wpm)
	}
}

func TestPlayerStep(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a", "b", "c")))
	p.step(-1)
	p.step(+1)
	p.step(+5)
	if p.pos != 2 {
		t.Fatalf("pos %d", p.pos)
	}
}

func TestPlayerView(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a", "b", "c", "d", "e", "f")))
	p.step(+1)
	before, cur, after, progress := p.view(3)
	if len(before) != 1 || cur.T != "b" || len(after) != 3 || after[2].T != "e" {
		t.Fatalf("before %v cur %v after %v", before, cur, after)
	}
	if progress != 1.0/5 {
		t.Fatalf("progress %v", progress)
	}
}

// After a suspend, the clock jumps: the player moves one word, not to the end.
func TestPlayerLongGapMovesOneWord(t *testing.T) {
	p := newPlayer(300)
	p.setQueue(queue(track(1, "a", "b", "c", "d")))
	p.toggle()
	p.advance(30 * time.Second)
	if p.ended || p.pos != 1 {
		t.Fatalf("pos %d ended %v after a 30 s gap", p.pos, p.ended)
	}
}
