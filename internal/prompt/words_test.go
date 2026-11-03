package prompt

import (
	"reflect"
	"testing"
)

var allOn = Opts{SkipCode: true, ShortPaths: true, StripMarkdown: true, Pauses: true}

func texts(ws []Word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.T
	}
	return out
}

func TestPrepareSkipsCode(t *testing.T) {
	ws := Prepare("voici:\n```go\na\nb\n```\nfin.", allOn)
	if got, want := texts(ws), []string{"voici:", "[code · 2 lines]", "fin."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
	if !ws[1].M || ws[1].W != 2 {
		t.Fatalf("marker %+v", ws[1])
	}
	if ws[2].W != 2 {
		t.Fatalf("sentence end weight %v", ws[2].W)
	}
	if ws[0].W != 1.5 {
		t.Fatalf("colon weight %v", ws[0].W)
	}
}

func TestPrepareTable(t *testing.T) {
	ws := Prepare("avant\n| a | b |\n|---|---|\n| 1 | 2 |\napres", allOn)
	if got, want := texts(ws), []string{"avant", "[table]", "apres"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
	if !ws[1].M {
		t.Fatal("table is not a marker")
	}
}

func TestPrepareMarkdown(t *testing.T) {
	ws := Prepare("## Titre\n- **gras** et [lien](http://x.y) `code`\n> cite _ital_\n---", allOn)
	want := []string{"Titre", "gras", "et", "lien", "code", "cite", "ital"}
	if got := texts(ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
}

func TestPrepareKeepsSnakeCase(t *testing.T) {
	ws := Prepare("snake_case and 3 * 4", allOn)
	if got, want := texts(ws), []string{"snake_case", "and", "3", "*", "4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
}

func TestPrepareShortPaths(t *testing.T) {
	ws := Prepare("/home/ricardo/dev/neonmux/internal/ui/agent.go, ui/a.go https://example.com/a/b/c/d/e/f", allOn)
	if got, want := texts(ws), []string{"agent.go,", "ui/a.go", "example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
}

func TestPrepareOptsOff(t *testing.T) {
	ws := Prepare("**gras** fin.\n```\nx y\n```", Opts{})
	if got, want := texts(ws), []string{"**gras**", "fin.", "```", "x", "y", "```"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
	for _, w := range ws {
		if w.W != 1 || w.M {
			t.Fatalf("word %+v with pauses off", w)
		}
	}
}

func TestPrepareLongWordWeight(t *testing.T) {
	ws := Prepare("anticonstitutionnellement", allOn)
	if len(ws) != 1 || ws[0].W != 1.3 {
		t.Fatalf("got %+v", ws)
	}
}

func TestAnchor(t *testing.T) {
	cases := map[string]int{"a": 0, "le": 1, "chats": 1, "maison": 2, "mangeoire": 2, "abcdefghij": 3, "abcdefghijklm": 3, "abcdefghijklmn": 4, "été": 1,
		"(bonjour": 3, "maison,": 2, "chats.": 1, "«maison»": 3, "\"le\"": 2, "...": 1, "—": 0, "l'eau": 1}
	for w, want := range cases {
		if got := Anchor(w); got != want {
			t.Errorf("Anchor(%q) = %d, want %d", w, got, want)
		}
	}
}
