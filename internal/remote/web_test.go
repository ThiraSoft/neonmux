package remote

import (
	"bytes"
	"io/fs"
	"net/http/httptest"
	"regexp"
	"strconv"
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
	regexp.MustCompile(`\.href\s*=([^=]|$)`),
	regexp.MustCompile(`\.src\s*=([^=]|$)`),
	regexp.MustCompile(`style\.cssText`),
	regexp.MustCompile(`\blocation\s*=([^=]|$)`),
}

// The only URL assignments allowed, all literal, per file.
var allowedSinks = map[string][]string{
	"web/pair.js": {"s.src = '/vendor/jsQR.js'"},
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
		for _, ok := range allowedSinks[path] {
			if !bytes.Contains(b, []byte(ok)) {
				t.Errorf("%s: allowed %q not found, update allowedSinks", path, ok)
			}
			b = bytes.ReplaceAll(b, []byte(ok), nil)
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

// wire.js mirrors the message types and the payload limit of wire.go.
func TestWebWireConstants(t *testing.T) {
	b, err := webFiles.ReadFile("web/wire.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)export const T = Object\.freeze\(\{(.*?)\}\);`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no T map in wire.js")
	}
	js := map[string]int{}
	for _, kv := range regexp.MustCompile(`(\w+):\s*(\d+)`).FindAllSubmatch(m[1], -1) {
		n, _ := strconv.Atoi(string(kv[2]))
		js[string(kv[1])] = n
	}
	goTypes := map[string]uint8{
		"Hello": wHello, "Pair": wPair, "Auth": wAuth, "Resume": wResume, "List": wList, "Watch": wWatch,
		"Size": wSize, "Scroll": wScroll, "Text": wText, "Keys": wKeys, "Unwatch": wUnwatch,
		"Challenge": wChallenge, "PairCode": wPairCode, "Authed": wAuthed, "Sessions": wSessions,
		"Theme": wTheme, "Frame": wFrame, "Exited": wExited, "Error": wError,
		"Wheel": wWheel, "SetTheme": wSetTheme, "NewTerm": wNewTerm, "NewTermDone": wNewTermDone,
	}
	if len(js) != len(goTypes) {
		t.Errorf("wire.js has %d types, wire.go %d", len(js), len(goTypes))
	}
	for name, v := range goTypes {
		if js[name] != int(v) {
			t.Errorf("type %s: wire.js %d, wire.go %d", name, js[name], v)
		}
	}

	mp := regexp.MustCompile(`export const MAX_PAYLOAD = 1 << (\d+);`).FindSubmatch(b)
	if mp == nil {
		t.Fatal("no MAX_PAYLOAD = 1 << n in wire.js")
	}
	if sh, _ := strconv.Atoi(string(mp[1])); 1<<sh != maxPayloadSize {
		t.Errorf("MAX_PAYLOAD 1<<%d, wire.go %d", sh, maxPayloadSize)
	}
	pr := regexp.MustCompile(`export const PROTO = (\d+);`).FindSubmatch(b)
	if pr == nil || string(pr[1]) != strconv.Itoa(wireProto) {
		t.Errorf("PROTO in wire.js differs from wireProto %d", wireProto)
	}
}

// DEV_FILES (app.js, hashed for a development build) lists every app file
// the service worker caches, except jsQR, fonts and icons, plus sw.js.
func TestWebDevFilesInSW(t *testing.T) {
	list := func(file, re string) map[string]bool {
		b, err := webFiles.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(re).FindSubmatch(b)
		if m == nil {
			t.Fatalf("no list in %s", file)
		}
		out := map[string]bool{}
		for _, p := range regexp.MustCompile(`'(/[^']*)'`).FindAllSubmatch(m[1], -1) {
			out[string(p[1])] = true
		}
		return out
	}
	sw := list("web/sw.js", `(?s)const FILES = \[(.*?)\];`)
	dev := list("web/app.js", `(?s)const DEV_FILES = \[(.*?)\];`)
	for f := range sw {
		skip := strings.HasPrefix(f, "/vendor/") || strings.HasPrefix(f, "/fonts/") || strings.HasSuffix(f, ".png")
		if !skip && !dev[f] {
			t.Errorf("%s cached by sw.js but missing from DEV_FILES", f)
		}
	}
	for f := range dev {
		if !sw[f] && f != "/sw.js" {
			t.Errorf("%s in DEV_FILES but not cached by sw.js", f)
		}
	}
}

// Every button of the key row maps (app.js KEYS) to a key name the daemon
// accepts, and the row has backspace.
func TestWebKeyRow(t *testing.T) {
	js, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const KEYS = \{([^}]*)\};`).FindSubmatch(js)
	if m == nil {
		t.Fatal("no KEYS map in app.js")
	}
	keyMap := map[string]string{}
	for _, kv := range regexp.MustCompile(`(\w+):\s*'([^']*)'`).FindAllSubmatch(m[1], -1) {
		keyMap[string(kv[1])] = string(kv[2])
	}
	for id, name := range keyMap {
		if !isValidKeyName(name) {
			t.Errorf("KEYS.%s = %q: not a key the daemon accepts", id, name)
		}
	}
	buttons := regexp.MustCompile(`data-key="([^"]+)"`).FindAllSubmatch(html, -1)
	if len(buttons) == 0 {
		t.Fatal("no key row in index.html")
	}
	seen := map[string]bool{}
	for _, b := range buttons {
		id := string(b[1])
		if _, ok := keyMap[id]; !ok {
			t.Errorf("button data-key=%q has no entry in KEYS", id)
		}
		seen[keyMap[id]] = true
	}
	if !seen["backspace"] {
		t.Error("no backspace button in the key row")
	}
	if !bytes.Contains(html, []byte(`aria-label="backspace">⌫</button>`)) {
		t.Error(`backspace button: want label ⌫ and aria-label "backspace"`)
	}
}
