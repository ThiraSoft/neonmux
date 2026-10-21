package kata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// allStates lists every place of the cursor, plus a few desired columns
// beyond the end of each line (after j or k from a longer line, or $).
func allStates(lines []string) []State {
	var out []State
	for l, s := range lines {
		last := max(len(s)-1, 0)
		for c := 0; c <= last; c++ {
			out = append(out, State{Pos{l, c}, c})
		}
		for _, w := range []int{40, 70, WantEOL} {
			if w == WantEOL || w > last {
				out = append(out, State{Pos{l, last}, w})
			}
		}
	}
	return out
}

var testedMotions = []string{
	"h", "j", "k", "l", "w", "b", "e", "W", "B", "E", "0", "^", "$", "gg", "G", "+", "-",
	"{", "}", "%", "*", "#", "3w", "2j", "5k", "fe", "t,", "F.", "3b", "2e", "4l", "2}", "T ", "3G", "2fa", "2W",
}

func edgeFor(buf []string, s State, keys string) (Edge, bool) {
	for _, e := range Moves(buf, s) {
		if e.Keys == keys {
			return e, true
		}
	}
	return Edge{}, false
}

func TestMotionsMatchNvim(t *testing.T) {
	needNvim(t)
	for _, x := range loadFixtures(t) {
		states := allStates(x.Lines)
		ref := nvimMotions(t, x.Lines, states, testedMotions)
		bad := 0
		for _, m := range testedMotions {
			for i, s := range states {
				e, ok := edgeFor(x.Lines, s, m)
				if !ok {
					continue // not offered from there: nothing to compare
				}
				if e.To != ref[m][i] && bad < 25 {
					bad++
					t.Errorf("%s: %q from %v: go %v, nvim %v (line %q)", x.Title, m, s, e.To, ref[m][i], x.Lines[s.Line])
				}
			}
		}
	}
}

func TestMovesAreOffered(t *testing.T) {
	buf := []string{"The cat (a tabby) sat on the mat.", "", "Then the cat slept."}
	s := State{Pos{0, 0}, 0}
	for _, k := range []string{"w", "e", "$", "fc", "tc", "2fa", "j", "}", "G", "3w"} {
		if _, ok := edgeFor(buf, s, k); !ok {
			t.Errorf("%q not offered from (0,0)", k)
		}
	}
	if _, ok := edgeFor(buf, s, "h"); ok {
		t.Error("h cannot move from column 0")
	}
	if e, ok := edgeFor(buf, State{Pos{0, 8}, 8}, "%"); !ok || e.To.Pos != (Pos{0, 16}) {
		t.Errorf("%% from '(' gives %v %v", e.To, ok)
	}
}

// uniformCost is a plain, slow search used to check Shortest.
func uniformCost(buf []string, from Pos, goal func(Pos) bool) int {
	start := State{from, from.Col}
	dist := map[State]int{start: 0}
	done := map[State]bool{}
	for {
		best, bd := State{}, -1
		for s, d := range dist {
			if !done[s] && (bd < 0 || d < bd) {
				best, bd = s, d
			}
		}
		if bd < 0 {
			return -1
		}
		if goal(best.Pos) {
			return bd
		}
		done[best] = true
		for _, e := range movesWithSearch(buf, best, goal) {
			nd := bd + KeyCount(e.Keys)
			if d, ok := dist[e.To]; !ok || nd < d {
				dist[e.To] = nd
			}
		}
	}
}

func TestShortestIsShortest(t *testing.T) {
	buf := []string{"One fish, two fish (red fish) and blue fish.", "", "Then a cat came by the river bank."}
	for _, goal := range []Pos{{0, 20}, {2, 23}, {0, 43}, {2, 5}, {0, 4}} {
		g := goal
		isGoal := func(p Pos) bool { return p == g }
		r, ok := Shortest(buf, Pos{0, 0}, isGoal)
		want := uniformCost(buf, Pos{0, 0}, isGoal)
		if !ok || r.Count() != want {
			t.Errorf("goal %v: Shortest %q (%d), uniform cost %d", goal, r.Keys(), r.Count(), want)
		}
	}
}

func TestShortestPlaysInNvim(t *testing.T) {
	needNvim(t)
	rng := rand.New(rand.NewPCG(1, 2))
	texts := loadFixtures(t)
	for i := range 40 {
		x := texts[i%len(texts)]
		pick := func() Pos {
			for {
				l := rng.IntN(len(x.Lines))
				if n := len(x.Lines[l]); n > 0 {
					return Pos{l, rng.IntN(n)}
				}
			}
		}
		from, goal := pick(), pick()
		r, ok := Shortest(x.Lines, from, func(p Pos) bool { return p == goal })
		if !ok {
			t.Errorf("%v → %v: no path", from, goal)
			continue
		}
		_, at, _ := playInNvim(t, x.Lines, from, r.Keys())
		if at != goal {
			t.Errorf("%s: %v → %v with %q lands on %v", x.Title, from, goal, r.Keys(), at)
		}
	}
}

func TestShortestIsFast(t *testing.T) {
	var buf []string
	for i := range 20 {
		buf = append(buf, fmt.Sprintf("line %02d: the quick brown fox jumps over the lazy dog, again and again.", i))
	}
	start := time.Now()
	r, ok := Shortest(buf, Pos{0, 0}, func(p Pos) bool { return p == Pos{19, 47} })
	if el := time.Since(start); !ok || el > 300*time.Millisecond {
		t.Errorf("%q in %v", r.Keys(), el)
	}
	if !slices.ContainsFunc(r, func(s Step) bool { return s.Explain != "" }) {
		t.Error("every step is explained")
	}
}
