package wiki

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Endpoint asks for one random article of the main namespace, as plain
// text (TextExtracts).
const Endpoint = "https://en.wikipedia.org/w/api.php?action=query&format=json&formatversion=2&generator=random&grnnamespace=0&prop=extracts&explaintext=1"

// UserAgent identifies neonmux, as Wikimedia's policy asks of every client.
const UserAgent = "neonmux/0.1 (https://github.com/ThiraSoft/neonmux)"

// ErrUnreachable reports that neither Wikipedia nor the cache had an
// article to give.
var ErrUnreachable = errors.New("Wikipedia unreachable")

const (
	fetchTimeout = 5 * time.Second
	// fetchTries is the number of random articles tried before giving up
	// when they have no usable text (lists, stubs).
	fetchTries = 3
)

// Source hands out random articles: from Wikipedia, one fetched ahead so
// that the next game starts right away, and from the disk cache when the
// network is gone. Nothing runs until a game asks.
type Source struct {
	dir      string
	endpoint string
	client   *http.Client

	mu       sync.Mutex
	ready    *Article      // fetched ahead, not served yet
	inflight chan struct{} // closed when the fetch ahead ends
	aheadErr error         // the fetch ahead failed: go straight to the cache
	last     string        // title served last
}

// NewSource returns a source that keeps its articles in cacheDir.
func NewSource(cacheDir string) *Source {
	return &Source{dir: cacheDir, endpoint: Endpoint, client: &http.Client{Timeout: fetchTimeout}}
}

// Next returns an article: the one fetched ahead if there is one (waiting
// for it if it is on its way), otherwise a new one, otherwise one from the
// cache.
func (s *Source) Next(ctx context.Context) (Article, error) {
	s.mu.Lock()
	if ch := s.inflight; ch != nil {
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return Article{}, ctx.Err()
		}
		s.mu.Lock()
	}
	if a := s.ready; a != nil {
		s.ready = nil
		s.last = a.Title
		s.mu.Unlock()
		return *a, nil
	}
	failed := s.aheadErr != nil
	s.aheadErr = nil
	s.mu.Unlock()

	if !failed {
		if a, err := s.fetch(ctx); err == nil {
			s.mu.Lock()
			s.last = a.Title
			s.mu.Unlock()
			return a, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.cached(); ok {
		s.last = a.Title
		return a, nil
	}
	return Article{}, ErrUnreachable
}

// Prefetch fetches the next article in the background, unless one is
// already there or on its way.
func (s *Source) Prefetch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready != nil || s.inflight != nil {
		return
	}
	ch := make(chan struct{})
	s.inflight = ch
	go func() {
		a, err := s.fetch(context.Background())
		s.mu.Lock()
		if err == nil {
			s.ready = &a
		}
		s.aheadErr = err
		s.inflight = nil
		s.mu.Unlock()
		close(ch)
	}()
}

// fetch asks Wikipedia for random articles until one has text, and keeps
// it in the cache.
func (s *Source) fetch(ctx context.Context) (Article, error) {
	var err error
	for range fetchTries {
		var a Article
		if a, err = s.fetchOne(ctx); err == nil {
			s.remember(a)
			return a, nil
		}
		if !errors.Is(err, ErrNoText) {
			return Article{}, err
		}
	}
	return Article{}, err
}

func (s *Source) fetchOne(ctx context.Context) (Article, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return Article{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return Article{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Article{}, fmt.Errorf("wikipedia: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Article{}, err
	}
	return ParseResponse(data)
}
