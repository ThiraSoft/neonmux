package kata

import (
	"slices"
	"strings"
)

// Navigation: the fewest keys to bring the cursor into a goal region,
// with the motions of Moves plus a search (/prefix<CR>) straight to a goal.

// maxPrefix is the longest search typed toward a goal.
const maxPrefix = 8

// searchSafe reports characters that a search pattern takes literally
// (magic mode): others like . * [ ~ \ / are left out of prefixes.
func searchSafe(c byte) bool {
	return isKeyword(c) || strings.IndexByte(` ,'"-:;!?()`, c) >= 0
}

// searcher finds, for a goal, the shortest search that lands on it.
type searcher struct {
	buf   []string
	goals []Pos
	// For each goal, the prefixes taken from it, shortest first, with the
	// places where each one matches.
	prefixes [][]prefix
}

type prefix struct {
	text string
	at   []Pos
}

func newSearcher(buf []string, goal func(Pos) bool) *searcher {
	s := &searcher{buf: buf}
	for l, line := range buf {
		for c := 0; c < len(line); c++ {
			if !goal(Pos{l, c}) {
				continue
			}
			var ps []prefix
			for n := 1; n <= maxPrefix && c+n <= len(line); n++ {
				if !searchSafe(line[c+n-1]) {
					break
				}
				text := line[c : c+n]
				ps = append(ps, prefix{text, occurrences(buf, text)})
			}
			if len(ps) > 0 {
				s.goals = append(s.goals, Pos{l, c})
				s.prefixes = append(s.prefixes, ps)
			}
		}
	}
	return s
}

func occurrences(buf []string, text string) []Pos {
	var out []Pos
	for l, s := range buf {
		for i := 0; ; {
			j := strings.Index(s[i:], text)
			if j < 0 {
				break
			}
			out = append(out, Pos{l, i + j})
			i += j + 1
		}
	}
	return out
}

// edges returns, for each goal, the shortest search from p that lands on
// it (the first match after the cursor, wrapping around).
func (s *searcher) edges(p Pos) []Edge {
	var out []Edge
	for i, g := range s.goals {
		if g == p {
			continue
		}
		for _, pf := range s.prefixes[i] {
			k, _ := slices.BinarySearchFunc(pf.at, p, func(a, b Pos) int {
				if before(a, b) {
					return -1
				}
				if a == b {
					return 0
				}
				return 1
			})
			if k < len(pf.at) && pf.at[k] == p {
				k++
			}
			next := pf.at[0]
			if k < len(pf.at) {
				next = pf.at[k]
			}
			if next == g {
				out = append(out, Edge{"/" + pf.text + "<CR>", "search for '" + pf.text + "'", State{g, g.Col}})
				break
			}
		}
	}
	return out
}

// movesWithSearch is Moves plus the searches toward the goal.
func movesWithSearch(buf []string, st State, goal func(Pos) bool) []Edge {
	return append(Moves(buf, st), newSearcher(buf, goal).edges(st.Pos)...)
}

// maxCost bounds the search: no mission needs that many keys to move.
const maxCost = 40

// Shortest returns the fewest keys that bring the cursor from from to a
// place where goal is true (Dijkstra, the cost of a motion being its keys).
func Shortest(buf []string, from Pos, goal func(Pos) bool) (Recipe, bool) {
	start := State{from, from.Col}
	if goal(from) {
		return Recipe{}, true
	}
	srch := newSearcher(buf, goal)
	type link struct {
		from State
		edge Edge
	}
	dist := map[State]int{start: 0}
	prev := map[State]link{}
	buckets := make([][]State, maxCost+1)
	buckets[0] = []State{start}
	for cost := 0; cost <= maxCost; cost++ {
		for i := 0; i < len(buckets[cost]); i++ {
			s := buckets[cost][i]
			if dist[s] != cost {
				continue
			}
			if goal(s.Pos) {
				var r Recipe
				for s != start {
					l := prev[s]
					r = append(r, Step{l.edge.Keys, l.edge.Explain})
					s = l.from
				}
				slices.Reverse(r)
				return r, true
			}
			for _, e := range append(Moves(buf, s), srch.edges(s.Pos)...) {
				nd := cost + KeyCount(e.Keys)
				if d, ok := dist[e.To]; (ok && d <= nd) || nd > maxCost {
					continue
				}
				dist[e.To] = nd
				prev[e.To] = link{s, e}
				buckets[nd] = append(buckets[nd], e.To)
			}
		}
	}
	return nil, false
}
