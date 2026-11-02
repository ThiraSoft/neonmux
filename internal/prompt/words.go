// Package prompt turns the replies of agents (Claude Code, agy) into tracks
// of timed words for the prompter: one word at a time, on an anchor
// (docs/superpowers/specs/2026-11-02-prompteur-design.md).
//
// Nothing here draws: the daemon detects the end of a turn, reads the reply
// in the agent's transcript, prepares its words and keeps a queue per
// device; the window and the phone only play it.
package prompt

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Opts are the prompter_* switches of the config.
type Opts struct {
	SkipCode      bool // code blocks and tables become a marker
	ShortPaths    bool // a long path becomes its file name, a URL its host
	StripMarkdown bool // bold, headings, links, backticks… go
	Pauses        bool // longer on punctuation and long words
}

// Word is one step of a track.
type Word struct {
	T string  `json:"t"`           // text
	A int     `json:"a"`           // anchor: index of the rune on the anchor
	W float32 `json:"w"`           // duration, in words at the current speed
	M bool    `json:"m,omitempty"` // marker of skipped content ([code · 3 lines])
}

var (
	linkRe   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	ruleRe   = regexp.MustCompile(`^(?:-{3,}|\*{3,}|_{3,})$`)
	bulletRe = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)
)

// Prepare cuts text into words, according to o.
func Prepare(text string, o Opts) []Word {
	var out []Word
	marker := func(s string) {
		out = append(out, Word{T: s, A: Anchor(s), W: 2, M: true})
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if o.SkipCode {
			if fence := fenceOf(line); fence != "" {
				n := 0
				for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
					n++
				}
				marker(fmt.Sprintf("[code · %d lines]", n))
				continue
			}
			if strings.HasPrefix(line, "|") && i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|") {
				for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|") {
					i++
				}
				marker("[table]")
				continue
			}
		}
		if o.StripMarkdown {
			if ruleRe.MatchString(line) {
				continue
			}
			line = strings.TrimLeft(line, ">")
			line = strings.TrimSpace(line)
			if h := strings.TrimLeft(line, "#"); h != line && (h == "" || h[0] == ' ') {
				line = strings.TrimSpace(h)
			}
			line = bulletRe.ReplaceAllString(line, "")
			line = linkRe.ReplaceAllString(line, "$1")
		}
		for _, tok := range strings.Fields(line) {
			if o.StripMarkdown {
				tok = stripToken(tok)
				if tok == "" {
					continue
				}
			}
			if o.ShortPaths {
				tok = shortPath(tok)
			}
			out = append(out, Word{T: tok, A: Anchor(tok), W: weight(tok, o.Pauses)})
		}
	}
	return out
}

func fenceOf(line string) string {
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(line, f) {
			return f
		}
	}
	return ""
}

// stripToken removes the markdown marks of a word, but keeps an underscore
// inside it (snake_case) and a lone operator (3 * 4).
func stripToken(tok string) string {
	if tok == "*" || tok == "_" {
		return tok
	}
	tok = strings.ReplaceAll(tok, "**", "")
	tok = strings.ReplaceAll(tok, "`", "")
	if strings.HasPrefix(tok, "__") || strings.HasSuffix(tok, "__") {
		tok = strings.Trim(tok, "_")
	}
	return strings.Trim(tok, "*_")
}

// shortPath keeps the file name of a long path and the host of a URL.
func shortPath(tok string) string {
	if i := strings.Index(tok, "://"); i > 0 {
		host := tok[i+3:]
		if j := strings.IndexAny(host, "/?#"); j >= 0 {
			host = host[:j]
		}
		if host != "" {
			return host
		}
		return tok
	}
	if !strings.Contains(tok, "/") || strings.HasPrefix(tok, "http") || utf8.RuneCountInString(tok) <= 20 {
		return tok
	}
	core := strings.TrimRight(tok, ".,;:!?)")
	tail := tok[len(core):]
	segs := strings.Split(core, "/")
	for k := len(segs) - 1; k >= 0; k-- {
		if segs[k] != "" {
			return segs[k] + tail
		}
	}
	return tok
}

func weight(tok string, pauses bool) float32 {
	if !pauses {
		return 1
	}
	w := float32(1)
	switch tok[len(tok)-1] {
	case '.', '!', '?':
		w = 2
	case ',', ';', ':':
		w = 1.5
	}
	if utf8.RuneCountInString(tok) > 8 {
		w *= 1.3
	}
	return w
}

// Anchor returns the rune of word the eye fixes: a little before the
// middle, further in for long words.
func Anchor(word string) int {
	switch n := utf8.RuneCountInString(word); {
	case n <= 1:
		return 0
	case n <= 5:
		return 1
	case n <= 9:
		return 2
	case n <= 13:
		return 3
	default:
		return 4
	}
}
