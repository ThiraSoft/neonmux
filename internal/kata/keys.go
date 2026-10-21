// Package kata holds the vim katas without their screen: the texts of the
// missions, a model of vim's motions to find the shortest way to a place,
// the catalog of missions with their reference recipes, and the Lua harness
// that runs inside Neovim.
package kata

import "strings"

// Keys are written in vim's notation, as keytrans() writes them: <Esc>,
// <CR>, <C-a>… count for one key each, <lt> is a "<".

// KeyCount returns the number of keys of a sequence, VimGolf-style.
func KeyCount(keys string) int {
	n := 0
	for i := 0; i < len(keys); i++ {
		if keys[i] == '<' {
			if j := specialEnd(keys[i:]); j > 0 {
				i += j
			}
		}
		n++
	}
	return n
}

// specialEnd returns the index of the ">" closing a special key at the start
// of s ("<C-a>", "<Esc>"), or 0 when s does not start with one.
func specialEnd(s string) int {
	for j := 1; j < len(s) && j < 16; j++ {
		c := s[j]
		switch {
		case c == '>':
			if j == 1 {
				return 0
			}
			return j
		case c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
		default:
			return 0
		}
	}
	return 0
}

// Step is one gesture of a recipe, with what it does in plain words.
type Step struct {
	Keys    string
	Explain string
}

// Recipe is a way to solve a mission.
type Recipe []Step

// Keys returns the whole sequence.
func (r Recipe) Keys() string {
	var b strings.Builder
	for _, s := range r {
		b.WriteString(s.Keys)
	}
	return b.String()
}

// Count returns the number of keys of the recipe.
func (r Recipe) Count() int { return KeyCount(r.Keys()) }
