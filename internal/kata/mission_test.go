package kata

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/games"
)

var idForm = regexp.MustCompile(`^[a-z]+\.[a-z0-9-]+$`)

func TestCatalogShape(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Chapters {
		defs := DefsOf(c.N)
		if len(defs) < 5 {
			t.Errorf("chapter %d has %d missions, want at least 5", c.N, len(defs))
		}
		for _, d := range defs {
			if seen[d.ID] || !idForm.MatchString(d.ID) || d.Name == "" || d.Gesture == "" {
				t.Errorf("mission %+v", d)
			}
			seen[d.ID] = true
			if got, ok := Find(d.ID); !ok || got.ID != d.ID {
				t.Errorf("Find(%q)", d.ID)
			}
		}
	}
}

func TestOpenChapters(t *testing.T) {
	st := games.Load(t.TempDir() + "/games.json")
	if o := Open(st); !o[0] || slices.Contains(o[1:], true) {
		t.Fatalf("empty records: %v", o)
	}
	ids := DefsOf(1)
	for _, d := range ids[:4] {
		st.RecordKata(d.ID, 9, 3, time.Second)
	}
	for range 5 {
		st.RecordKata(ids[0].ID, 3, 3, time.Second)
	}
	if Open(st)[1] {
		t.Error("4 different missions are not enough")
	}
	st.RecordKata(ids[4].ID, 9, 3, time.Second)
	if !Open(st)[1] {
		t.Error("5 different missions with a star open chapter 2")
	}
}

// checkMission verifies what every mission must hold, whatever its kind.
func checkMission(t *testing.T, m Mission) {
	t.Helper()
	if m.ID == "" || m.Name == "" || m.Instruction == "" || len(m.Start) == 0 || len(m.Candidates) == 0 {
		t.Fatalf("incomplete mission %+v", m)
	}
	for _, c := range m.Candidates {
		if c.Count() < m.Par.Count() {
			t.Errorf("%s: par %q is not the shortest candidate (%q)", m.ID, m.Par.Keys(), c.Keys())
		}
	}
	if m.Par.Count() < 1 {
		t.Errorf("%s: empty par", m.ID)
	}
	if h := m.Highlight; h != nil && (h.Line >= len(m.Start) || h.End > len(m.Start[h.Line]) || h.Col >= h.End) {
		t.Errorf("%s: highlight %+v out of the text", m.ID, *h)
	}
}

// generated returns the missions a def makes on the fixtures.
func generated(t *testing.T, d Def) []Mission {
	t.Helper()
	var out []Mission
	for i, x := range loadFixtures(t) {
		for seed := range 3 {
			r := rand.New(rand.NewPCG(uint64(i), uint64(seed)))
			if m, ok := Generate(d, []Text{x}, r); ok {
				out = append(out, m)
			}
		}
	}
	if len(out) == 0 {
		t.Errorf("%s: no fixture text fits", d.ID)
	}
	return out
}

func TestChapter1Missions(t *testing.T) {
	needNvim(t)
	for _, d := range DefsOf(1) {
		var jobs []job
		var goals []Pos
		for _, m := range generated(t, d) {
			checkMission(t, m)
			if m.Goal == nil || !slices.Equal(m.Start, m.Target) {
				t.Fatalf("%s: a moving mission has a goal and keeps the text", m.ID)
			}
			for _, c := range m.Candidates {
				jobs, goals = append(jobs, jobFor(m, c)), append(goals, *m.Goal)
			}
		}
		for i, r := range playAll(t, jobs) {
			at := Pos{r.Cursor[0], r.Cursor[1]}
			if !slices.Equal(r.Lines, jobs[i].Lines) || at != goals[i] || r.Mode != "n" {
				t.Errorf("%s: %q lands on %v (goal %v)", d.ID, jobs[i].Keys, at, goals[i])
			}
		}
	}
}

// TestEditMissions plays every candidate of every editing mission in nvim:
// it must give exactly the target text, back in normal mode.
func TestEditMissions(t *testing.T) {
	needNvim(t)
	for ch := 2; ch <= len(Chapters); ch++ {
		for _, d := range DefsOf(ch) {
			ms := generated(t, d)
			var jobs []job
			var want [][]string
			for _, m := range ms {
				checkMission(t, m)
				if slices.Equal(m.Start, m.Target) || m.Goal != nil {
					t.Errorf("%s: an editing mission changes the text", m.ID)
				}
				for _, c := range m.Candidates {
					jobs, want = append(jobs, jobFor(m, c)), append(want, m.Target)
				}
			}
			for i, r := range playAll(t, jobs) {
				if !slices.Equal(r.Lines, want[i]) || r.Mode != "n" || r.Err != "" {
					t.Errorf("%s: %q from %v gives (mode %s, err %q)\n%s\nwant\n%s", d.ID, jobs[i].Keys, jobs[i].Cursor, r.Mode, r.Err,
						strings.Join(r.Lines, "\n"), strings.Join(want[i], "\n"))
				}
			}
		}
	}
}

func TestYearRules(t *testing.T) {
	d, _ := Find("count.shift-years")
	for _, x := range loadFixtures(t) {
		r := rand.New(rand.NewPCG(1, 1))
		if m, ok := Generate(d, []Text{x}, r); ok && strings.Contains(strings.Join(m.Start, " "), "1914-1918") {
			t.Errorf("%q: a year after a dash would read as negative", x.Title)
		}
	}
}

func TestSentenceRulesSkipAbbreviations(t *testing.T) {
	d, ok := Find("obj.delete-sentence")
	if !ok {
		t.Fatal("missing obj.delete-sentence")
	}
	for _, m := range generated(t, d) {
		h := m.Highlight
		if h == nil {
			t.Fatal("the sentence is highlighted")
		}
		if s := m.Start[h.Line][h.Col:h.End]; strings.Contains(s, "Mr.") || strings.HasPrefix(s, "Smith") {
			t.Errorf("picked %q", s)
		}
	}
}

func TestMissionInstructionsAreEnglish(t *testing.T) {
	for _, d := range Defs() {
		if strings.ContainsAny(d.Name, "éèàç") {
			t.Errorf("%s: %q", d.ID, d.Name)
		}
	}
}

// The bracket mission is solvable with %: a line with an apostrophe (a
// possessive) must not push the par to f).
func TestBracketMissionUsesPercent(t *testing.T) {
	d, _ := Find("move.bracket")
	buf := []string{"The harbour's old quay (built of grey granite in the time of the salt", "traders) still stands."}
	m, ok := Generate(d, []Text{{Title: "t", Lines: buf}}, rand.New(rand.NewPCG(1, 1)))
	if !ok || m.Par.Keys() != "%" {
		t.Errorf("par %q, want %%", m.Par.Keys())
	}
}

// Other numbers (a decimal, a model name) do not stop the years mission:
// only a 4-digit number it would leave alone does.
func TestShiftYearsIgnoresOtherNumbers(t *testing.T) {
	d, _ := Find("count.shift-years")
	buf := []string{"The tower was built in 1820 and stands 2.5 m above the A380 hangar.", "", "It was restored in 1956 by the town."}
	if _, ok := Generate(d, []Text{{Title: "t", Lines: buf}}, rand.New(rand.NewPCG(1, 1))); !ok {
		t.Error("two plain years: the mission fits")
	}
	buf[2] = "It was restored in 1956-1958 by the town."
	if _, ok := Generate(d, []Text{{Title: "t", Lines: buf}}, rand.New(rand.NewPCG(1, 1))); ok {
		t.Error("1958 after a dash would stay as it is: the mission does not fit")
	}
}
