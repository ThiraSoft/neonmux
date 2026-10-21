package wiki

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseResponseSections(t *testing.T) {
	a, err := ParseResponse(fixture(t, "plain"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "Marrowby" {
		t.Errorf("title %q", a.Title)
	}
	var titles []string
	for _, s := range a.Sections {
		titles = append(titles, s.Title)
	}
	// Trivia is too short, See also and References are not prose.
	if want := []string{"", "History", "Early years"}; !slices.Equal(titles, want) {
		t.Fatalf("sections %q, want %q", titles, want)
	}
	intro := a.Sections[0].Paragraphs
	if len(intro) != 2 || !strings.HasPrefix(intro[0], "Marrowby is a small fictional harbour town") {
		t.Errorf("intro %q: the pronunciation must be gone", intro)
	}
}

func TestParseResponseNoText(t *testing.T) {
	if _, err := ParseResponse(fixture(t, "excluded")); !errors.Is(err, ErrNoText) {
		t.Errorf("err = %v, want ErrNoText", err)
	}
	if _, err := ParseResponse([]byte(`{"query":{"pages":[]}}`)); !errors.Is(err, ErrNoText) {
		t.Errorf("no page: err = %v, want ErrNoText", err)
	}
	if _, err := ParseResponse([]byte(`not json`)); err == nil {
		t.Error("garbage must fail")
	}
}

func TestArticleWords(t *testing.T) {
	a, err := ParseResponse(fixture(t, "two"))
	if err != nil {
		t.Fatal(err)
	}
	w := a.Words()
	if len(w) != 49 || w[0] != "Alpha" || w[len(w)-1] != "twentyfive." {
		t.Errorf("words %d: %q … %q", len(w), w[0], w[len(w)-1])
	}
}
