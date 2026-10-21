package kata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThiraSoft/neonmux/internal/wiki"
)

// loadFixtures returns every excerpt of the synthetic articles.
func loadFixtures(t testing.TB) []Text {
	t.Helper()
	files, _ := filepath.Glob("testdata/*.json")
	var texts []Text
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var a wiki.Article
		if err := json.Unmarshal(data, &a); err != nil {
			t.Fatal(f, err)
		}
		texts = append(texts, Excerpts(a)...)
	}
	if len(texts) == 0 {
		t.Fatal("no fixture excerpt")
	}
	return texts
}

func TestWrap(t *testing.T) {
	s := strings.Repeat("word ", 20) + "end."
	lines := Wrap(s, 72)
	if len(lines) != 2 {
		t.Fatalf("%d lines: %q", len(lines), lines)
	}
	for _, l := range lines {
		if len(l) > 72 || strings.HasPrefix(l, " ") || strings.HasSuffix(l, " ") {
			t.Errorf("line %q", l)
		}
	}
	if strings.Join(lines, " ") != s {
		t.Error("wrapping must keep every word")
	}
	long := strings.Repeat("x", 80)
	if got := Wrap("a "+long+" b", 72); len(got) != 3 || got[1] != long {
		t.Errorf("a word longer than the width stays alone: %q", got)
	}
}

func TestExcerpts(t *testing.T) {
	for _, x := range loadFixtures(t) {
		if len(x.Lines) < minLines || len(x.Lines) > maxLines {
			t.Errorf("%s: %d lines", x.Title, len(x.Lines))
		}
		if x.Lines[0] == "" || x.Lines[len(x.Lines)-1] == "" {
			t.Errorf("%s: blank first or last line", x.Title)
		}
		for i, l := range x.Lines {
			if len(l) > width && strings.Contains(l, " ") {
				t.Errorf("%s: line %d too long: %q", x.Title, i, l)
			}
			if i > 0 && l == "" && x.Lines[i-1] == "" {
				t.Errorf("%s: two blank lines in a row", x.Title)
			}
		}
	}
}
