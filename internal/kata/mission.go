package kata

import (
	"math/rand/v2"
	"slices"
	"sort"

	"github.com/ThiraSoft/neonmux/internal/games"
)

// Span is a stretch of a line (End excluded), highlighted in the mission.
type Span struct {
	Line, Col, End int
}

// Mission is one kata: a text, where the cursor starts, what the text (or
// the cursor) must become, and the reference recipes. The par is the
// shortest of them on this text.
type Mission struct {
	ID          string
	Chapter     int
	Name        string
	Instruction string
	Title       string // the article the text comes from
	Start       []string
	Target      []string
	Cursor      Pos
	Goal        *Pos // moving missions: where the cursor must end
	Highlight   *Span
	Candidates  []Recipe
	Par         Recipe
	Practice    bool // a try again after the solution was shown: not recorded
}

// Def is a mission of the catalog. Gen builds it on a text, or reports that
// the text does not suit it.
type Def struct {
	ID      string
	Name    string
	Gesture string // shown in the list: the gesture the mission teaches
	Chapter int
	Gen     func(t Text, r *rand.Rand) (Mission, bool)
}

// Chapter is a group of missions around one family of gestures.
type Chapter struct {
	N    int
	Name string
}

var Chapters = []Chapter{
	{1, "Moving"}, {2, "Typos"}, {3, "Operators"}, {4, "Text objects"}, {5, "Repeat"},
	{6, "Numbers and counts"}, {7, "Moving text"}, {8, "Visual block and macros"}, {9, "Ex commands"},
}

// unlockAfter is the number of different missions of a chapter to win
// before the next one opens.
const unlockAfter = 5

var registry []Def

// register adds missions to the catalog (each chapter's file, at init).
func register(defs ...Def) {
	registry = append(registry, defs...)
	sort.SliceStable(registry, func(i, j int) bool { return registry[i].Chapter < registry[j].Chapter })
}

// Defs returns the catalog, chapter by chapter.
func Defs() []Def { return slices.Clone(registry) }

// DefsOf returns the missions of chapter n.
func DefsOf(n int) []Def {
	var out []Def
	for _, d := range registry {
		if d.Chapter == n {
			out = append(out, d)
		}
	}
	return out
}

func Find(id string) (Def, bool) {
	for _, d := range registry {
		if d.ID == id {
			return d, true
		}
	}
	return Def{}, false
}

// Open tells which chapters are open (index n-1): the first one, then each
// one whose previous chapter has 5 different missions with a star.
func Open(st *games.Store) []bool {
	open := make([]bool, len(Chapters))
	open[0] = true
	for i := 1; i < len(Chapters); i++ {
		won := 0
		for _, d := range DefsOf(Chapters[i-1].N) {
			if st.Kata[d.ID].Stars > 0 {
				won++
			}
		}
		open[i] = open[i-1] && won >= unlockAfter
	}
	return open
}

// Generate builds the mission on one of the texts, tried in random order.
func Generate(d Def, texts []Text, r *rand.Rand) (Mission, bool) {
	for _, i := range r.Perm(len(texts)) {
		m, ok := d.Gen(texts[i], r)
		if !ok {
			continue
		}
		m.ID, m.Chapter, m.Name, m.Title = d.ID, d.Chapter, d.Name, texts[i].Title
		if m.Target == nil {
			m.Target = m.Start
		}
		if finish(&m) {
			return m, true
		}
	}
	return Mission{}, false
}

// finish sets the par: the shortest candidate.
func finish(m *Mission) bool {
	m.Candidates = slices.DeleteFunc(m.Candidates, func(r Recipe) bool { return len(r) == 0 })
	if len(m.Candidates) == 0 {
		return false
	}
	m.Par = slices.MinFunc(m.Candidates, func(a, b Recipe) int { return a.Count() - b.Count() })
	return true
}

// navThen is the shortest way into region, then the gesture.
func navThen(buf []string, from Pos, region func(Pos) bool, keys, explain string) Recipe {
	nav, ok := Shortest(buf, from, region)
	if !ok {
		return nil
	}
	return append(nav, Step{keys, explain})
}
