package games

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

func TestClockPauses(t *testing.T) {
	var c Clock
	if c.Started() || c.Running() || c.Elapsed(at(5)) != 0 {
		t.Fatal("a new clock is stopped at zero")
	}
	c.Resume(at(0))
	c.Resume(at(1)) // already running: no effect
	c.Pause(at(2))
	c.Pause(at(3))
	if c.Running() || !c.Started() || c.Elapsed(at(9)) != 2*time.Second {
		t.Errorf("paused: elapsed %v", c.Elapsed(at(9)))
	}
	c.Resume(at(10))
	if got := c.Elapsed(at(11)); got != 3*time.Second {
		t.Errorf("elapsed %v, want 3s", got)
	}
}

func TestStars(t *testing.T) {
	for _, c := range []struct{ keys, par, want int }{
		{5, 5, 3}, {4, 5, 3}, {7, 5, 2}, {8, 5, 1}, {6, 4, 2}, {30, 4, 1},
	} {
		if got := Stars(c.keys, c.par); got != c.want {
			t.Errorf("Stars(%d, %d) = %d, want %d", c.keys, c.par, got, c.want)
		}
	}
}

func TestRecordKataKeepsTheBest(t *testing.T) {
	s := Load(filepath.Join(t.TempDir(), "games.json"))
	stars, best := s.RecordKata("obj.empty-parens", 8, 5, 4*time.Second)
	if stars != 1 || !best {
		t.Errorf("first win: %d stars, best %v", stars, best)
	}
	if stars, best = s.RecordKata("obj.empty-parens", 5, 5, 6*time.Second); stars != 3 || best {
		t.Errorf("second win: %d stars, best %v", stars, best)
	}
	s.RecordKata("obj.empty-parens", 7, 5, 3*time.Second)
	r := s.Kata["obj.empty-parens"]
	if r.Stars != 3 || r.BestKeys != 5 || r.BestMillis != 3000 || r.Wins != 3 {
		t.Errorf("record %+v", r)
	}
}

func TestRecordTyping(t *testing.T) {
	s := Load(filepath.Join(t.TempDir(), "games.json"))
	if !s.RecordTyping("time 30", 62, 95, t0) || s.RecordTyping("time 30", 60, 99, t0) || !s.RecordTyping("time 30", 70, 90, t0) {
		t.Error("only a higher WPM is a new best")
	}
	if b := s.Typing.Best["time 30"]; b.WPM != 70 || b.Accuracy != 90 {
		t.Errorf("best %+v", b)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "games.json")
	s := Load(path)
	s.Typing.Mode = "words 50"
	s.RecordKata("move.word", 4, 4, time.Second)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved file: %v %v", info, err)
	}
	got := Load(path)
	if got.Version != 1 || got.Typing.Mode != "words 50" || got.Kata["move.word"].Stars != 3 {
		t.Errorf("loaded %+v", got)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "games.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Load(path)
	if s.Version != 1 || len(s.Kata) != 0 {
		t.Errorf("corrupt file: %+v", s)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Error("the corrupt file must be kept aside as games.json.bad")
	}
}

func TestSaveUnwritableKeepsPlaying(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Load(filepath.Join(file, "games.json"))
	s.RecordKata("move.word", 3, 3, time.Second)
	if err := s.Save(); err == nil {
		t.Error("Save under a regular file must fail")
	}
	if s.Kata["move.word"].Wins != 1 {
		t.Error("the records stay in memory")
	}
}
