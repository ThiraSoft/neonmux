package kata

import "math/rand/v2"

// Chapter 1, Moving: the text stays as it is, the cursor must reach a
// highlighted place. The par is the true shortest path among the modeled
// motions.

func init() {
	register(
		Def{ID: "move.word", Name: "Reach the highlighted word", Gesture: "w", Chapter: 1, Gen: genMoveWord},
		Def{ID: "move.letter", Name: "Jump to the highlighted letter", Gesture: "f", Chapter: 1, Gen: genMoveLetter},
		Def{ID: "move.paragraph", Name: "Reach the next paragraph", Gesture: "}", Chapter: 1, Gen: genMoveParagraph},
		Def{ID: "move.far", Name: "Reach the highlighted word far below", Gesture: "/", Chapter: 1, Gen: genMoveFar},
		Def{ID: "move.bracket", Name: "Jump to the closing parenthesis", Gesture: "%", Chapter: 1, Gen: genMoveBracket},
		Def{ID: "move.same-word", Name: "Reach the next use of this word", Gesture: "*", Chapter: 1, Gen: genMoveSameWord},
	)
}

// wordSpans returns the keyword runs of a line, as [start, end).
func wordSpans(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); {
		if !isKeyword(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isKeyword(s[j]) {
			j++
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

// paragraphs returns the runs of non-blank lines, as [start, end).
func paragraphs(lines []string) [][2]int {
	var out [][2]int
	for i := 0; i < len(lines); {
		if lines[i] == "" {
			i++
			continue
		}
		j := i
		for j < len(lines) && lines[j] != "" {
			j++
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.IntN(len(xs))] }

// moving builds a moving mission toward goal.
func moving(lines []string, from, goal Pos, hl Span, instruction string) (Mission, bool) {
	nav, ok := Shortest(lines, from, func(p Pos) bool { return p == goal })
	if !ok || len(nav) == 0 {
		return Mission{}, false
	}
	return Mission{
		Instruction: instruction, Start: lines, Cursor: from, Goal: &goal, Highlight: &hl,
		Candidates: []Recipe{nav},
	}, true
}

type spot struct {
	pos Pos
	end int
}

func genMoveWord(t Text, r *rand.Rand) (Mission, bool) {
	var spots []spot
	for l, s := range t.Lines {
		ws := wordSpans(s)
		for k := 3; k < len(ws) && k <= 8; k++ {
			spots = append(spots, spot{Pos{l, ws[k][0]}, ws[k][1]})
		}
	}
	if len(spots) == 0 {
		return Mission{}, false
	}
	s := pick(r, spots)
	return moving(t.Lines, Pos{s.pos.Line, 0}, s.pos, Span{s.pos.Line, s.pos.Col, s.end},
		"Move the cursor to the start of the highlighted word.")
}

func genMoveLetter(t Text, r *rand.Rand) (Mission, bool) {
	var spots []Pos
	for l, s := range t.Lines {
		for c := 10; c < len(s); c++ {
			if isKeyword(s[c]) && isKeyword(s[c-1]) && s[c] >= 'a' && s[c] <= 'z' {
				spots = append(spots, Pos{l, c})
			}
		}
	}
	if len(spots) == 0 {
		return Mission{}, false
	}
	g := pick(r, spots)
	return moving(t.Lines, Pos{g.Line, 0}, g, Span{g.Line, g.Col, g.Col + 1}, "Jump to the highlighted letter.")
}

func genMoveParagraph(t Text, r *rand.Rand) (Mission, bool) {
	ps := paragraphs(t.Lines)
	if len(ps) < 2 {
		return Mission{}, false
	}
	i := r.IntN(len(ps) - 1)
	l := ps[i][0] + r.IntN(ps[i][1]-ps[i][0])
	from := Pos{l, r.IntN(len(t.Lines[l]))}
	goal := Pos{ps[i+1][0], 0}
	return moving(t.Lines, from, goal, Span{goal.Line, 0, 1}, "Move to the start of the next paragraph.")
}

func genMoveFar(t Text, r *rand.Rand) (Mission, bool) {
	var spots []spot
	for l := 5; l < len(t.Lines); l++ {
		for _, w := range wordSpans(t.Lines[l]) {
			spots = append(spots, spot{Pos{l, w[0]}, w[1]})
		}
	}
	if len(spots) == 0 {
		return Mission{}, false
	}
	s := pick(r, spots)
	return moving(t.Lines, Pos{0, 0}, s.pos, Span{s.pos.Line, s.pos.Col, s.end}, "Reach the highlighted word.")
}

func genMoveBracket(t Text, r *rand.Rand) (Mission, bool) {
	var froms, goals []Pos
	for l, s := range t.Lines {
		for c := 0; c < len(s); c++ {
			if s[c] != '(' {
				continue
			}
			// Only where % works as modeled: the mission teaches %.
			to, ok := matchBracket(t.Lines, Pos{l, c})
			if ok && (to.Line > l || to.Col-c >= 10) && percentSafe(t.Lines, l, to.Line) {
				froms, goals = append(froms, Pos{l, c}), append(goals, to)
			}
		}
	}
	if len(froms) == 0 {
		return Mission{}, false
	}
	i := r.IntN(len(froms))
	g := goals[i]
	return moving(t.Lines, froms[i], g, Span{g.Line, g.Col, g.Col + 1}, "Jump to the parenthesis that closes this one.")
}

func genMoveSameWord(t Text, r *rand.Rand) (Mission, bool) {
	type use struct{ from, to Pos }
	var uses []use
	for l, s := range t.Lines {
		for _, w := range wordSpans(s) {
			word := s[w[0]:w[1]]
			if len(word) < 4 {
				continue
			}
			if ps := wholeWords(t.Lines, word); len(ps) >= 2 {
				from := Pos{l, w[0]}
				uses = append(uses, use{from, nextMatch(ps, from)})
			}
		}
	}
	if len(uses) == 0 {
		return Mission{}, false
	}
	u := pick(r, uses)
	end := u.to.Col
	for end < len(t.Lines[u.to.Line]) && isKeyword(t.Lines[u.to.Line][end]) {
		end++
	}
	return moving(t.Lines, u.from, u.to, Span{u.to.Line, u.to.Col, end}, "Reach the next use of the word under the cursor.")
}
