package games

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// version of games.json. Missions are known by a stable id, never by their
// place in a list, so that new ones can come without breaking old files.
const version = 1

// Store holds the records of the games. It lives in games.json, next to the
// layout.
type Store struct {
	Version int                   `json:"version"`
	Typing  TypingRecords         `json:"typing"`
	Kata    map[string]KataRecord `json:"kata"`
	path    string
}

// TypingRecords holds the last mode chosen and the best result per mode.
type TypingRecords struct {
	Mode string                `json:"mode,omitempty"`
	Best map[string]TypingBest `json:"best,omitempty"`
}

type TypingBest struct {
	WPM      float64   `json:"wpm"`
	Accuracy float64   `json:"accuracy"`
	At       time.Time `json:"at"`
}

// KataRecord is the best result of a mission: stars and keys count the
// gesture, time is a record of its own.
type KataRecord struct {
	Stars      int   `json:"stars"`
	BestKeys   int   `json:"best_keys"`
	BestMillis int64 `json:"best_ms"`
	Wins       int   `json:"wins"`
}

// Load reads the records. A missing file gives empty records; a corrupt
// one is put aside (games.json.bad) rather than overwritten unseen.
func Load(path string) *Store {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err == nil && json.Unmarshal(data, s) != nil {
		_ = os.Rename(path, path+".bad")
		s = &Store{path: path}
	}
	s.Version = version
	if s.Kata == nil {
		s.Kata = map[string]KataRecord{}
	}
	if s.Typing.Best == nil {
		s.Typing.Best = map[string]TypingBest{}
	}
	return s
}

// Save writes the records. The caller may ignore the error: the games go
// on, only the records are not kept.
func (s *Store) Save() error {
	if s.path == "" {
		return errors.New("games: no file")
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Stars grades a mission: three at the par or below, two up to one and a
// half times the par, one beyond.
func Stars(keys, par int) int {
	switch {
	case keys <= par:
		return 3
	case 2*keys <= 3*par:
		return 2
	}
	return 1
}

// RecordTyping keeps the best speed of a mode. It reports a new best.
func (s *Store) RecordTyping(mode string, wpm, acc float64, at time.Time) bool {
	if b, ok := s.Typing.Best[mode]; ok && wpm <= b.WPM {
		return false
	}
	s.Typing.Best[mode] = TypingBest{WPM: wpm, Accuracy: acc, At: at}
	return true
}

// RecordKata counts a mission won and keeps its best stars, keys and time.
// It returns the stars of this try and whether its time is a new best.
func (s *Store) RecordKata(id string, keys, par int, d time.Duration) (stars int, newBestTime bool) {
	stars = Stars(keys, par)
	r, seen := s.Kata[id]
	ms := d.Milliseconds()
	newBestTime = !seen || r.BestMillis == 0 || ms < r.BestMillis
	if newBestTime {
		r.BestMillis = ms
	}
	if !seen || keys < r.BestKeys {
		r.BestKeys = keys
	}
	r.Stars = max(r.Stars, stars)
	r.Wins++
	s.Kata[id] = r
	return stars, newBestTime
}
