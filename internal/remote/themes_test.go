package remote

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// /themes.json gives the PWA every theme with its palette, so the picker
// works off a connection (home screen); the service worker caches it.
func TestWebThemesJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	pageHandler("test").ServeHTTP(rec, httptest.NewRequest("GET", "/themes.json", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET /themes.json: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var list []struct {
		Name   string       `json:"name"`
		Label  string       `json:"label"`
		FG     [3]uint8     `json:"fg"`
		BG     [3]uint8     `json:"bg"`
		Cursor [3]uint8     `json:"cursor"`
		Ansi   [16][3]uint8 `json:"ansi"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != len(theme.All) {
		t.Fatalf("%d themes, want %d", len(list), len(theme.All))
	}
	for i, th := range theme.All {
		e := list[i]
		if e.Name != th.Name || e.Label != th.Label || e.FG != theme.RGB(th.FG) || e.BG != theme.RGB(th.BG) ||
			e.Cursor != theme.RGB(th.Cursor) || e.Ansi[1] != theme.RGB(th.Palette(1)) || e.Ansi[15] != theme.RGB(th.Palette(15)) {
			t.Fatalf("theme %d = %+v, want %s", i, e, th.Name)
		}
	}
	sw, _ := webFiles.ReadFile("web/sw.js")
	if !regexp.MustCompile(`'/themes\.json'`).Match(sw) {
		t.Error("/themes.json not cached by the service worker")
	}
}
