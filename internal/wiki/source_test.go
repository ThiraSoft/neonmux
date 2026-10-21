package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

// fakeWiki serves the answers in turn (the last one again once they run
// out) and counts the requests.
type fakeWiki struct {
	srv   *httptest.Server
	hits  atomic.Int32
	agent atomic.Value
}

func newFakeWiki(t *testing.T, answers ...func(n int) []byte) *fakeWiki {
	t.Helper()
	f := &fakeWiki{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.hits.Add(1)) - 1
		f.agent.Store(r.Header.Get("User-Agent"))
		_, _ = w.Write(answers[min(n, len(answers)-1)](n))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func serve(t *testing.T, name string) func(int) []byte {
	data := fixture(t, name)
	return func(int) []byte { return data }
}

// numbered serves the plain article under a new title on every request.
func numbered(t *testing.T) func(int) []byte {
	var page map[string]any
	if err := json.Unmarshal(fixture(t, "plain"), &page); err != nil {
		t.Fatal(err)
	}
	return func(n int) []byte {
		p := page["query"].(map[string]any)["pages"].([]any)[0].(map[string]any)
		p["title"] = "Town " + strconv.Itoa(n)
		data, _ := json.Marshal(page)
		return data
	}
}

func testSource(t *testing.T, f *fakeWiki, dir string) *Source {
	s := NewSource(dir)
	s.endpoint = f.srv.URL
	return s
}

func cacheFiles(t *testing.T, dir string) int {
	entries, _ := os.ReadDir(dir)
	return len(entries)
}

func TestSourceSendsUserAgent(t *testing.T) {
	f := newFakeWiki(t, serve(t, "plain"))
	if _, err := testSource(t, f, t.TempDir()).Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.agent.Load(); got != UserAgent {
		t.Errorf("User-Agent %q", got)
	}
}

func TestSourceCachesArticles(t *testing.T) {
	dir := t.TempDir()
	f := newFakeWiki(t, serve(t, "plain"))
	a, err := testSource(t, f, dir).Next(context.Background())
	if err != nil || a.Title != "Marrowby" {
		t.Fatalf("%q, %v", a.Title, err)
	}
	if n := cacheFiles(t, dir); n != 1 {
		t.Errorf("%d files in the cache", n)
	}
}

func TestSourceFallsBackToCache(t *testing.T) {
	dir := t.TempDir()
	f := newFakeWiki(t, serve(t, "plain"))
	s := testSource(t, f, dir)
	if _, err := s.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.srv.Close()
	a, err := s.Next(context.Background())
	if err != nil || a.Title != "Marrowby" {
		t.Errorf("offline: %q, %v; want the cached article", a.Title, err)
	}
}

func TestSourceUnreachable(t *testing.T) {
	f := newFakeWiki(t, serve(t, "plain"))
	f.srv.Close()
	_, err := testSource(t, f, t.TempDir()).Next(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want ErrUnreachable", err)
	}
}

func TestSourceSkipsArticleWithoutText(t *testing.T) {
	f := newFakeWiki(t, serve(t, "excluded"), serve(t, "plain"))
	a, err := testSource(t, f, t.TempDir()).Next(context.Background())
	if err != nil || a.Title != "Marrowby" {
		t.Errorf("%q, %v; want the second article", a.Title, err)
	}
}

func TestSourceEvictsOldest(t *testing.T) {
	dir := t.TempDir()
	f := newFakeWiki(t, numbered(t))
	s := testSource(t, f, dir)
	for range 52 {
		if _, err := s.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := cacheFiles(t, dir); n != maxCached {
		t.Errorf("%d files in the cache, want %d", n, maxCached)
	}
}

func TestSourcePrefetch(t *testing.T) {
	f := newFakeWiki(t, serve(t, "plain"))
	s := testSource(t, f, t.TempDir())
	s.Prefetch()
	s.Prefetch() // one request at a time
	if _, err := s.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestSourceWithUnwritableCache(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFakeWiki(t, serve(t, "plain"))
	if _, err := testSource(t, f, filepath.Join(file, "cache")).Next(context.Background()); err != nil {
		t.Errorf("a cache that cannot be written must not stop the game: %v", err)
	}
}
