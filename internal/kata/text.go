package kata

import (
	"strings"

	"github.com/ThiraSoft/neonmux/internal/wiki"
)

const (
	width    = 72 // columns of a mission text
	minLines = 6
	maxLines = 20
)

// Pos is a place in a mission text: 0-based line and column (bytes: the
// texts are ASCII).
type Pos struct {
	Line, Col int
}

// Text is an excerpt of an article that missions are cut from.
type Text struct {
	Title string
	Lines []string
}

// Wrap cuts a paragraph into lines of at most width columns, at spaces. A
// word longer than the width stays alone on its line.
func Wrap(paragraph string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(paragraph) {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// Excerpts cuts every section of an article into texts of 6 to 20 lines,
// made of whole paragraphs separated by a blank line. A paragraph longer
// than 20 lines keeps its first 20.
func Excerpts(a wiki.Article) []Text {
	var out []Text
	for _, s := range a.Sections {
		var cur []string
		emit := func() {
			if len(cur) >= minLines {
				out = append(out, Text{Title: a.Title, Lines: cur})
			}
			cur = nil
		}
		for _, p := range s.Paragraphs {
			block := Wrap(p, width)
			if len(block) > maxLines {
				block = block[:maxLines]
			}
			need := len(block)
			if len(cur) > 0 {
				need++
			}
			if len(cur)+need > maxLines {
				emit()
			}
			if len(cur) > 0 {
				cur = append(cur, "")
			}
			cur = append(cur, block...)
		}
		emit()
	}
	return out
}
