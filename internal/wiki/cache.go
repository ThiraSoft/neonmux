package wiki

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// maxCached is the number of articles kept on disk to play offline.
const maxCached = 50

// DefaultCacheDir is where the articles already fetched are kept.
func DefaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "neonmux", "wikipedia")
}

// remember writes the article to the cache and trims it. Errors are
// ignored: without a cache, the games only lose the offline mode.
func (s *Source) remember(a Article) {
	if os.MkdirAll(s.dir, 0o700) != nil {
		return
	}
	data, err := json.Marshal(a)
	if err != nil {
		return
	}
	sum := sha1.Sum([]byte(a.Title))
	path := filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil || os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		return
	}
	s.trim()
}

// trim removes the oldest articles beyond maxCached.
func (s *Source) trim() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, file{e.Name(), info.ModTime()})
		}
	}
	if len(files) <= maxCached {
		return
	}
	slices.SortFunc(files, func(x, y file) int {
		if c := x.mod.Compare(y.mod); c != 0 {
			return c
		}
		return strings.Compare(x.name, y.name)
	})
	for _, f := range files[:len(files)-maxCached] {
		_ = os.Remove(filepath.Join(s.dir, f.name))
	}
}

// cached returns an article of the cache at random, if possible another
// one than the article served last.
func (s *Source) cached() (Article, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return Article{}, false
	}
	rand.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	var fallback *Article
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		var a Article
		if err != nil || json.Unmarshal(data, &a) != nil || len(a.Sections) == 0 {
			continue
		}
		if a.Title != s.last {
			return a, true
		}
		fallback = &a
	}
	if fallback != nil {
		return *fallback, true
	}
	return Article{}, false
}
