package prompt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

type fakeSrc struct {
	key, agent string
	st         vt.ProgStatus
	has        bool
}

func (f *fakeSrc) Key() string                   { return f.key }
func (f *fakeSrc) Status() (vt.ProgStatus, bool) { return f.st, f.has }
func (f *fakeSrc) Pgid() int                     { return 42 }
func (f *fakeSrc) Agent() string                 { return f.agent }
func (f *fakeSrc) Cwd() string                   { return "/p" }
func (f *fakeSrc) set(s vt.ProgState, kind string) {
	f.st.State, f.st.Kind, f.has = s, kind, true
	f.st.Gen++
}

type fakeReaders struct {
	claude []Reply // successive answers, an empty ID means none
	calls  int
	stops  []Stop
	agy    Reply
	agyN   int
	db     string
}

func (f *fakeReaders) readers() Readers {
	return Readers{
		ClaudePath: func(int) (string, bool) { return "t.jsonl", true },
		Claude: func(_ string, s Stop, _ bool) (Reply, bool) {
			f.calls++
			f.stops = append(f.stops, s)
			if len(f.claude) == 0 {
				return Reply{}, false
			}
			r := f.claude[0]
			if len(f.claude) > 1 {
				f.claude = f.claude[1:]
			}
			return r, r.ID != ""
		},
		AgyDB: func(int) (string, bool) { return f.db, f.db != "" },
		Agy: func(string, bool) (Reply, bool) {
			f.agyN++
			return f.agy, f.agy.ID != ""
		},
	}
}

func setup(t *testing.T) (*Hub, *fakeReaders, *Detector, *fakeSrc) {
	h := NewHub(allOn)
	h.Open("window")
	fr := &fakeReaders{}
	d := NewDetector(h, fr.readers(), false)
	src := &fakeSrc{key: "k", agent: "claude"}
	src.set(vt.PSWorking, "")
	return h, fr, d, src
}

func ticks(d *Detector, src Source, n int) {
	for range n {
		d.Tick([]Source{src}, time.Now())
	}
}

func TestDetectDoneMakesTrack(t *testing.T) {
	h, fr, d, src := setup(t)
	ticks(d, src, 1)
	fr.claude = []Reply{{ID: "a1", Text: "Fini."}}
	src.set(vt.PSDone, "")
	ticks(d, src, 2)
	q := h.Queue("window")
	if len(q.Tracks) != 1 || q.Tracks[0].Words[0].T != "Fini." || q.Tracks[0].Agent != "claude" || q.Tracks[0].Cwd != "/p" {
		t.Fatalf("queue %+v", q)
	}
	if fr.stops[0] != StopDone {
		t.Fatalf("stop %v", fr.stops[0])
	}
}

func TestDetectRetryLateFlush(t *testing.T) {
	h, fr, d, src := setup(t)
	ticks(d, src, 1)
	fr.claude = []Reply{{}, {}, {ID: "a1", Text: "Fini."}}
	src.set(vt.PSDone, "")
	ticks(d, src, 6)
	if n := len(h.Queue("window").Tracks); n != 1 {
		t.Fatalf("%d tracks", n)
	}
	if fr.calls != 3 {
		t.Fatalf("%d reads", fr.calls)
	}
}

func TestDetectGivesUp(t *testing.T) {
	_, fr, d, src := setup(t)
	ticks(d, src, 1)
	src.set(vt.PSDone, "")
	ticks(d, src, 10)
	if fr.calls != 4 {
		t.Fatalf("%d reads, want 4", fr.calls)
	}
}

func TestDetectPermissionNoTrack(t *testing.T) {
	h, fr, d, src := setup(t)
	ticks(d, src, 1)
	src.set(vt.PSBlocked, "permission")
	ticks(d, src, 5)
	if fr.calls != 0 || len(h.Queue("window").Tracks) != 0 {
		t.Fatalf("%d reads", fr.calls)
	}
	fr.claude = []Reply{{ID: "q1", Text: "Quelle couleur ?"}}
	src.set(vt.PSBlocked, "question")
	ticks(d, src, 2)
	if len(h.Queue("window").Tracks) != 1 || fr.stops[0] != StopQuestion {
		t.Fatalf("question: %d tracks, stops %v", len(h.Queue("window").Tracks), fr.stops)
	}
}

func TestDetectDedupe(t *testing.T) {
	h, fr, d, src := setup(t)
	ticks(d, src, 1)
	fr.claude = []Reply{{ID: "a1", Text: "Fini."}}
	src.set(vt.PSDone, "")
	ticks(d, src, 2)
	src.set(vt.PSIdle, "")
	ticks(d, src, 2)
	if n := len(h.Queue("window").Tracks); n != 1 {
		t.Fatalf("%d tracks", n)
	}
}

func TestDetectClosedNoWork(t *testing.T) {
	h, fr, d, src := setup(t)
	h.Close("window")
	ticks(d, src, 1)
	src.set(vt.PSDone, "")
	ticks(d, src, 3)
	if fr.calls != 0 {
		t.Fatalf("%d reads while closed", fr.calls)
	}
}

func TestDetectOldStateNotReplayed(t *testing.T) {
	h, fr, d, src := setup(t)
	h.Close("window")
	src.set(vt.PSDone, "")
	ticks(d, src, 2)
	h.Open("window")
	fr.claude = []Reply{{ID: "a1", Text: "vieux"}}
	ticks(d, src, 3)
	if n := len(h.Queue("window").Tracks); n != 0 {
		t.Fatalf("%d tracks", n)
	}
}

func TestDetectAgyMtime(t *testing.T) {
	h, fr, d, _ := setup(t)
	db := filepath.Join(t.TempDir(), "c.db")
	os.WriteFile(db, []byte("x"), 0o600)
	fr.db = db
	src := &fakeSrc{key: "g", agent: "agy"}
	fr.agy = Reply{ID: "old", Text: "ancienne"}
	ticks(d, src, 1)
	d.inflight.Wait()
	ticks(d, src, 2)
	if n := len(h.Queue("window").Tracks); n != 0 || fr.agyN != 1 {
		t.Fatalf("baseline: %d tracks, %d reads", n, fr.agyN)
	}
	fr.agy = Reply{ID: "new", Text: "Fini."}
	later := time.Now().Add(time.Minute)
	os.Chtimes(db, later, later)
	ticks(d, src, 1)
	d.inflight.Wait()
	ticks(d, src, 2)
	if n := len(h.Queue("window").Tracks); n != 1 || fr.agyN != 2 {
		t.Fatalf("after change: %d tracks, %d reads", n, fr.agyN)
	}
}

// A slow agy read (sqlite3 up to 2 s) never holds the tick.
func TestDetectAgyReadOffTheTick(t *testing.T) {
	h, fr, d, _ := setup(t)
	db := filepath.Join(t.TempDir(), "c.db")
	os.WriteFile(db, []byte("x"), 0o600)
	release := make(chan struct{})
	rs := fr.readers()
	rs.AgyDB = func(int) (string, bool) { return db, true }
	rs.Agy = func(string, bool) (Reply, bool) { <-release; return Reply{ID: "r", Text: "Fini."}, true }
	d.r = rs
	src := &fakeSrc{key: "g", agent: "agy"}
	start := time.Now()
	ticks(d, src, 1)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("the tick waited for sqlite3")
	}
	close(release)
	d.inflight.Wait()
	ticks(d, src, 1) // baseline taken
	later := time.Now().Add(time.Minute)
	os.Chtimes(db, later, later)
	rs.Agy = func(string, bool) (Reply, bool) { return Reply{ID: "r2", Text: "Suite."}, true }
	d.r = rs
	ticks(d, src, 1)
	d.inflight.Wait()
	ticks(d, src, 1)
	if n := len(h.Queue("window").Tracks); n != 1 {
		t.Fatalf("%d tracks", n)
	}
}
