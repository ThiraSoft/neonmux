package kata

import "testing"

func TestKeyCount(t *testing.T) {
	for keys, want := range map[string]int{
		"ci(x<Esc>":       5,
		"<C-v>3jI> <Esc>": 7,
		":g/a/d<CR>":      7,
		"<lt>":            1,
		"10<C-a>":         3,
		"":                0,
		"a<b":             3, // an unclosed bracket is a plain character
	} {
		if got := KeyCount(keys); got != want {
			t.Errorf("KeyCount(%q) = %d, want %d", keys, got, want)
		}
	}
}

func TestRecipe(t *testing.T) {
	r := Recipe{{Keys: "f(", Explain: "next '(' on the line"}, {Keys: "ci(", Explain: "change inside the parentheses"}, {Keys: "x<Esc>"}}
	if r.Keys() != "f(ci(x<Esc>" || r.Count() != 7 {
		t.Errorf("keys %q, count %d", r.Keys(), r.Count())
	}
}
