package wiki

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Section is a part of an article: the introduction has no title.
type Section struct {
	Title      string   `json:"title"`
	Paragraphs []string `json:"paragraphs"`
}

// Article is a random article, already normalized and stripped of what is
// not prose.
type Article struct {
	Title    string    `json:"title"`
	Sections []Section `json:"sections"`
}

// ErrNoText reports an article with nothing left to play on: only lists,
// references, or text in another script.
var ErrNoText = errors.New("no usable text")

// minSectionWords drops the stubs: a two-line section makes a poor game.
const minSectionWords = 30

var heading = regexp.MustCompile(`^(={2,})\s*(.*?)\s*={2,}$`)

// skipped holds the sections that are not prose (lowercase).
var skipped = map[string]bool{
	"see also": true, "references": true, "notes": true, "external links": true,
	"further reading": true, "bibliography": true, "sources": true, "citations": true,
	"footnotes": true, "notes and references": true, "works cited": true,
}

// response is the part of the answer of the TextExtracts API that we read
// (formatversion=2).
type response struct {
	Query struct {
		Pages []struct {
			Title   string `json:"title"`
			Extract string `json:"extract"`
		} `json:"pages"`
	} `json:"query"`
}

// ParseResponse reads an answer of the API: the article split into
// sections, each paragraph normalized. Subsections are sections of their
// own.
func ParseResponse(data []byte) (Article, error) {
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return Article{}, err
	}
	if len(r.Query.Pages) == 0 {
		return Article{}, ErrNoText
	}
	page := r.Query.Pages[0]
	title, _ := Normalize(page.Title)
	a := Article{Title: title}
	cur := Section{}
	keep := func() {
		if !skipped[strings.ToLower(cur.Title)] && countWords(cur.Paragraphs) >= minSectionWords {
			a.Sections = append(a.Sections, cur)
		}
	}
	for line := range strings.SplitSeq(page.Extract, "\n") {
		line = strings.TrimSpace(line)
		if m := heading.FindStringSubmatch(line); m != nil {
			keep()
			t, _ := Normalize(m[2])
			cur = Section{Title: t}
			continue
		}
		if line == "" {
			continue
		}
		if p, ok := Normalize(line); ok && p != "" {
			cur.Paragraphs = append(cur.Paragraphs, p)
		}
	}
	keep()
	if len(a.Sections) == 0 {
		return Article{}, ErrNoText
	}
	return a, nil
}

func countWords(ps []string) int {
	n := 0
	for _, p := range ps {
		n += len(strings.Fields(p))
	}
	return n
}

// Words returns every word of the article, in order.
func (a Article) Words() []string {
	var w []string
	for _, s := range a.Sections {
		for _, p := range s.Paragraphs {
			w = append(w, strings.Fields(p)...)
		}
	}
	return w
}
