package prompt

import (
	"os"
	"slices"
	"sync"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// Source is a terminal that may run an agent.
type Source interface {
	Key() string
	Status() (vt.ProgStatus, bool) // OSC 7501 status
	Pgid() int                     // foreground process group
	Agent() string                 // foreground program: claude, agy, anything else is ignored
	Cwd() string
}

// Readers find and read transcripts; tests replace them.
type Readers struct {
	ClaudePath func(pgid int) (string, bool)
	Claude     func(path string, s Stop, intermediate bool) (Reply, bool)
	AgyDB      func(pgid int) (string, bool)
	Agy        func(db string, intermediate bool) (Reply, bool)
}

// DefaultReaders reads the real transcripts of the user's home.
func DefaultReaders() Readers {
	home, _ := os.UserHomeDir()
	return Readers{
		ClaudePath: func(pgid int) (string, bool) { return ClaudeTranscript(home, pgid) },
		Claude:     ClaudeReply,
		AgyDB:      AgyConversation,
		Agy:        AgyReply,
	}
}

const (
	claudeTries = 4  // the read on the tick after the change, then 3 more
	agyRebind   = 5  // ticks between two lookups of agy's conversation
	keptIDs     = 16 // replies remembered per terminal, against doubles
)

type watched struct {
	st      vt.ProgStatus
	had     bool
	pending bool
	stop    Stop
	tries   int
	ids     []string
	// agy
	pgid    int
	db      string
	bindAge int
	mtime   time.Time
	seen    bool
	agyBusy bool       // a read runs (sqlite3 can take up to 2 s)
	agyRes  *agyResult // its result, taken at the next tick
}

type agyResult struct {
	r  Reply
	ok bool
}

// Detector turns the end of an agent's turn into a track of the hub. It is
// ticked about once a second (the daemon's loop, or the window without a
// daemon).
type Detector struct {
	hub   *Hub
	r     Readers
	inter bool
	terms map[string]*watched

	mu       sync.Mutex     // agyBusy and agyRes, written by the reads
	inflight sync.WaitGroup // agy reads running
}

func NewDetector(h *Hub, r Readers, intermediate bool) *Detector {
	return &Detector{hub: h, r: r, inter: intermediate, terms: map[string]*watched{}}
}

// Tick looks at every terminal once.
func (d *Detector) Tick(srcs []Source, now time.Time) {
	open := d.hub.AnyOpen()
	live := map[string]bool{}
	for _, src := range srcs {
		key := src.Key()
		live[key] = true
		w := d.terms[key]
		if w == nil {
			w = &watched{}
			d.terms[key] = w
		}
		switch src.Agent() {
		case "claude":
			d.claude(src, w, open)
		case "agy":
			d.agy(src, w, open)
		default:
			w.pending, w.db, w.seen = false, "", false
			w.st, w.had = src.Status()
		}
	}
	for key := range d.terms {
		if !live[key] {
			delete(d.terms, key)
		}
	}
}

func (d *Detector) claude(src Source, w *watched, open bool) {
	st, ok := src.Status()
	changed := ok && w.had && (st.Gen != w.st.Gen || st.State != w.st.State)
	prev := w.st.State
	w.st, w.had = st, ok
	if !open {
		w.pending = false
		return
	}
	if changed {
		switch st.State {
		case vt.PSDone, vt.PSError:
			w.pending, w.stop, w.tries = true, StopDone, claudeTries
		case vt.PSIdle:
			if prev == vt.PSWorking {
				w.pending, w.stop, w.tries = true, StopDone, claudeTries
			}
		case vt.PSBlocked:
			if st.Kind != "permission" {
				w.pending, w.stop, w.tries = true, StopQuestion, claudeTries
			}
		default:
			w.pending = false
		}
		return // read on the next tick: the transcript may lag the status
	}
	if !w.pending {
		return
	}
	w.tries--
	if w.tries <= 0 {
		w.pending = false
	}
	path, ok := d.r.ClaudePath(src.Pgid())
	if !ok {
		return
	}
	if r, ok := d.r.Claude(path, w.stop, d.inter); ok {
		w.pending = false
		d.add(src, w, r)
	}
}

func (d *Detector) agy(src Source, w *watched, open bool) {
	d.mu.Lock()
	res, busy := w.agyRes, w.agyBusy
	w.agyRes = nil
	d.mu.Unlock()
	if res != nil {
		if !w.seen {
			// The reply already there when the prompter (or this agy) appeared.
			w.seen = true
			if res.ok {
				w.ids = append(w.ids, res.r.ID)
			}
		} else if res.ok && open {
			d.add(src, w, res.r)
		}
	}
	if !open {
		w.seen, w.db = false, ""
		return
	}
	if busy {
		return
	}
	pgid := src.Pgid()
	if w.db == "" || pgid != w.pgid || w.bindAge >= agyRebind {
		w.pgid, w.bindAge = pgid, 0
		db, ok := d.r.AgyDB(pgid)
		if !ok {
			w.db = ""
			return
		}
		if db != w.db {
			w.db, w.mtime = db, time.Time{}
		}
	}
	w.bindAge++
	m := mtime(w.db)
	if m.Equal(w.mtime) {
		return
	}
	w.mtime = m
	// The read runs off the tick: the daemon's loop and the window's frame
	// never wait for sqlite3.
	d.mu.Lock()
	w.agyBusy = true
	d.mu.Unlock()
	d.inflight.Add(1)
	go func(db string) {
		defer d.inflight.Done()
		r, ok := d.r.Agy(db, d.inter)
		d.mu.Lock()
		w.agyBusy, w.agyRes = false, &agyResult{r, ok}
		d.mu.Unlock()
	}(w.db)
}

func mtime(db string) time.Time {
	var m time.Time
	for _, p := range []string{db, db + "-wal"} {
		if st, err := os.Stat(p); err == nil && st.ModTime().After(m) {
			m = st.ModTime()
		}
	}
	return m
}

func (d *Detector) add(src Source, w *watched, r Reply) {
	if slices.Contains(w.ids, r.ID) {
		return
	}
	w.ids = append(w.ids, r.ID)
	if len(w.ids) > keptIDs {
		w.ids = w.ids[1:]
	}
	d.hub.Add(src.Key(), src.Agent(), src.Cwd(), r.Text)
}
