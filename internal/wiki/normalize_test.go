package wiki

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"It’s “fine” — really… ok", `It's "fine" - really... ok`},
		{"Zürich café Ærø Straße", "Zurich cafe AEro Strasse"},
		{"London (/ˈlʌndən/ LUN-dən) is a city", "London is a city"},
		{"Tokyo (東京, Tōkyō) is big", "Tokyo is big"},
		{"a \t b  c", "a b c"},
		{"3×4 is −5 «x»", `3x4 is -5 "x"`},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if got != c.want || !ok {
			t.Errorf("Normalize(%q) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}

func TestNormalizeRejectsForeignScript(t *testing.T) {
	if _, ok := Normalize("Москва is the capital of Russia"); ok {
		t.Error("a paragraph with Cyrillic outside parentheses must be rejected")
	}
}

func TestNormalizeKeepsPlainParentheses(t *testing.T) {
	got, ok := Normalize("The river (about 40 km long) flows east.")
	if !ok || got != "The river (about 40 km long) flows east." {
		t.Errorf("got %q, %v", got, ok)
	}
}
