package ui

import (
	"slices"
	"time"

	"github.com/ThiraSoft/neonmux/internal/prompt"
)

// player is the prompter's reading state over the queue the daemon sends:
// the current track, the word on the anchor, the clock. It never moves to
// the next track by itself (docs/superpowers/specs/2026-11-02-prompteur-design.md).
type player struct {
	tracks    []prompt.Track
	curSeq    uint64 // 0: no track
	pos       int    // word on the anchor
	playing   bool
	ended     bool          // the last word went by: play replays the track
	acc       time.Duration // time spent on the current word
	wpm       int
	dismissed map[uint64]bool // left here, until the daemon's queue drops them
}

func newPlayer(wpm int) *player {
	return &player{wpm: min(max(wpm, minWPM), maxWPM), dismissed: map[uint64]bool{}}
}

// setQueue takes the daemon's queue. The current track and its position
// stay if it is still there.
func (p *player) setQueue(q prompt.Queue) {
	p.tracks = p.tracks[:0]
	present := map[uint64]bool{}
	for _, tr := range q.Tracks {
		present[tr.Seq] = true
		if !p.dismissed[tr.Seq] && len(tr.Words) > 0 {
			p.tracks = append(p.tracks, tr)
		}
	}
	for seq := range p.dismissed {
		if !present[seq] {
			delete(p.dismissed, seq)
		}
	}
	if p.index() >= 0 {
		return
	}
	p.pos, p.playing, p.ended, p.acc = 0, false, false, 0
	p.curSeq = 0
	if len(p.tracks) > 0 {
		p.curSeq = p.tracks[0].Seq
	}
}

func (p *player) index() int {
	if p.curSeq == 0 {
		return -1
	}
	return slices.IndexFunc(p.tracks, func(tr prompt.Track) bool { return tr.Seq == p.curSeq })
}

// cur returns the current track, a zero Track if there is none.
func (p *player) cur() prompt.Track {
	if i := p.index(); i >= 0 {
		return p.tracks[i]
	}
	return prompt.Track{}
}

// toggle is play/pause; at the end of a track, it plays it again.
func (p *player) toggle() {
	if p.index() < 0 {
		return
	}
	if p.ended {
		p.pos, p.ended, p.acc, p.playing = 0, false, 0, true
		return
	}
	p.playing = !p.playing
}

// step moves d words, without changing play or pause.
func (p *player) step(d int) {
	tr := p.cur()
	if len(tr.Words) == 0 {
		return
	}
	p.pos = min(max(p.pos+d, 0), len(tr.Words)-1)
	p.acc = 0
	if d < 0 {
		p.ended = false
	}
}

// speed changes the speed by d steps.
func (p *player) speed(d int) {
	p.wpm = min(max(p.wpm+d*stepWPM, minWPM), maxWPM)
}

// next leaves the current track (its Seq is to be dismissed) and plays the
// one after it.
func (p *player) next() (dismiss uint64, ok bool) {
	i := p.index()
	if i < 0 {
		return 0, false
	}
	dismiss = p.curSeq
	p.dismissed[dismiss] = true
	p.tracks = slices.Delete(p.tracks, i, i+1)
	p.pos, p.ended, p.acc = 0, false, 0
	if i < len(p.tracks) {
		p.curSeq, p.playing = p.tracks[i].Seq, true
	} else if len(p.tracks) > 0 {
		p.curSeq, p.playing = p.tracks[0].Seq, true
	} else {
		p.curSeq, p.playing = 0, false
	}
	return dismiss, true
}

func (p *player) dur(w prompt.Word) time.Duration {
	weight := float64(w.W)
	if weight <= 0 {
		weight = 1
	}
	return time.Duration(float64(time.Minute) / float64(p.wpm) * weight)
}

// advance moves the clock by dt. It returns true if the display changed.
func (p *player) advance(dt time.Duration) bool {
	tr := p.cur()
	if !p.playing || len(tr.Words) == 0 {
		return false
	}
	// A suspend or a sleeping window comes back with a long gap: one word
	// at most, never a jump to the end.
	p.acc += min(dt, p.dur(tr.Words[p.pos]))
	changed := false
	for p.acc >= p.dur(tr.Words[p.pos]) {
		if p.pos == len(tr.Words)-1 {
			p.ended, p.playing, p.acc = true, false, 0
			return true
		}
		p.acc -= p.dur(tr.Words[p.pos])
		p.pos++
		changed = true
	}
	return changed
}

// untilNext is how long the current word still stays.
func (p *player) untilNext() time.Duration {
	tr := p.cur()
	if !p.playing || len(tr.Words) == 0 {
		return 0
	}
	return p.dur(tr.Words[p.pos]) - p.acc
}

// view returns the words around the anchor and how far the track is read
// (0 to 1).
func (p *player) view(ctx int) (before []prompt.Word, cur prompt.Word, after []prompt.Word, progress float64) {
	ws := p.cur().Words
	if len(ws) == 0 {
		return nil, prompt.Word{}, nil, 0
	}
	before = ws[max(0, p.pos-ctx):p.pos]
	after = ws[p.pos+1 : min(len(ws), p.pos+1+ctx)]
	progress = 1
	if len(ws) > 1 && !p.ended {
		progress = float64(p.pos) / float64(len(ws)-1)
	}
	return before, ws[p.pos], after, progress
}
