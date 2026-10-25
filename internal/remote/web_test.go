package remote

import (
	"io/fs"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The PWA draws terminal output on a canvas and puts daemon text in the DOM
// through textContent only: none of these may appear in the embedded files.
var forbiddenSinks = []*regexp.Regexp{
	regexp.MustCompile(`innerHTML`),
	regexp.MustCompile(`outerHTML`),
	regexp.MustCompile(`insertAdjacentHTML`),
	regexp.MustCompile(`document\.write`),
	regexp.MustCompile(`\beval\s*\(`),
	regexp.MustCompile(`\bnew\s+Function\b`),
	regexp.MustCompile(`setAttribute\(\s*['"]on`),
	regexp.MustCompile(`setAttribute\(\s*['"]style`),
	regexp.MustCompile(`srcdoc`),
}

func TestWebNoDOMSinks(t *testing.T) {
	n := 0
	err := fs.WalkDir(webFiles, "web", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".js") && !strings.HasSuffix(path, ".mjs") && !strings.HasSuffix(path, ".html") {
			return nil
		}
		n++
		b, err := webFiles.ReadFile(path)
		if err != nil {
			return err
		}
		for _, re := range forbiddenSinks {
			if loc := re.FindIndex(b); loc != nil {
				t.Errorf("%s: forbidden %q at byte %d", path, re.String(), loc[0])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatalf("only %d web files checked", n)
	}
}

// The page's CSP (pageCSP) has no 'unsafe-inline': no inline script, style
// or event handler attribute in the HTML.
func TestWebHTMLNoInline(t *testing.T) {
	scriptTag := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	inlineAttr := regexp.MustCompile(`(?i)<[^>]*\s(on[a-z]+|style)\s*=`)
	for _, name := range []string{"web/index.html", "web/selftest.html"} {
		b, err := webFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		scripts := scriptTag.FindAllStringSubmatch(s, -1)
		if len(scripts) == 0 {
			t.Errorf("%s: no script", name)
		}
		for _, m := range scripts {
			if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
				t.Errorf("%s: inline script %q", name, m[0])
			}
		}
		if regexp.MustCompile(`(?i)<style`).MatchString(s) {
			t.Errorf("%s: inline <style>", name)
		}
		if m := inlineAttr.FindString(s); m != "" {
			t.Errorf("%s: inline attribute in %q", name, m)
		}
		if regexp.MustCompile(`(?i)javascript:`).MatchString(s) {
			t.Errorf("%s: javascript: URL", name)
		}
	}
}

// Every file the service worker caches is served, scripts with a
// JavaScript type (modules need it).
func TestWebServesPWAFiles(t *testing.T) {
	h := pageHandler("test")
	sw, err := webFiles.ReadFile("web/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	list := regexp.MustCompile(`(?s)const FILES = \[(.*?)\];`).FindSubmatch(sw)
	if list == nil {
		t.Fatal("no FILES in sw.js")
	}
	paths := regexp.MustCompile(`'(/[^']*)'`).FindAllSubmatch(list[1], -1)
	if len(paths) < 15 {
		t.Fatalf("only %d files in sw.js", len(paths))
	}
	for _, p := range append(paths, [][]byte{nil, []byte("/sw.js?v=1.2.3")}) {
		path := string(p[1])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Errorf("GET %s: %d", path, rec.Code)
			continue
		}
		if strings.HasSuffix(strings.SplitN(path, "?", 2)[0], ".js") && !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
			t.Errorf("GET %s: Content-Type %q", path, rec.Header().Get("Content-Type"))
		}
	}
}
