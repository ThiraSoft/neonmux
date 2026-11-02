package prompt

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	maxTracks    = 50       // per device, the oldest goes first
	maxText      = 64 << 10 // bytes of text in one track
	maxQueueJSON = 900 << 10
)

// Track is one reply of an agent.
type Track struct {
	Seq   uint64    `json:"seq"`
	Key   string    `json:"key"`   // session of the agent
	Agent string    `json:"agent"` // claude, agy
	Cwd   string    `json:"cwd"`
	At    time.Time `json:"at"`
	Words []Word    `json:"words"`
	size  int       // marshalled size
}

// Queue is what a device receives: always its whole queue.
type Queue struct {
	Open   bool    `json:"open"`
	Tracks []Track `json:"tracks"`
}

type device struct {
	tracks []Track
	size   int
}

// Hub keeps one queue per device with an open prompter ("window", or a
// phone's device id). A device that disconnects keeps its queue: only Close
// drops it.
type Hub struct {
	mu     sync.Mutex
	opts   Opts
	seq    uint64
	nextW  int
	open   map[string]*device
	watchs map[string]map[int]func() // watchers, even of devices not open
}

func NewHub(o Opts) *Hub {
	return &Hub{opts: o, open: map[string]*device{}, watchs: map[string]map[int]func(){}}
}

// Add queues a reply for every open device.
func (h *Hub) Add(key, agent, cwd, text string) {
	cut := false
	if len(text) > maxText {
		text = text[:maxText]
		if i := strings.LastIndexAny(text, " \n\t"); i > 0 {
			text = text[:i]
		}
		cut = true
	}
	words := Prepare(text, h.opts)
	if cut {
		words = append(words, Word{T: "[cut]", A: Anchor("[cut]"), W: 2, M: true})
	}
	if len(words) == 0 {
		return
	}
	h.mu.Lock()
	if len(h.open) == 0 {
		h.mu.Unlock()
		return
	}
	h.seq++
	tr := Track{Seq: h.seq, Key: key, Agent: agent, Cwd: cwd, At: time.Now(), Words: words}
	data, _ := json.Marshal(tr)
	tr.size = len(data) + 1
	var notify []func()
	for dev, d := range h.open {
		d.tracks = append(d.tracks, tr)
		d.size += tr.size
		for len(d.tracks) > 1 && (len(d.tracks) > maxTracks || d.size > maxQueueJSON) {
			d.size -= d.tracks[0].size
			d.tracks = d.tracks[1:]
		}
		notify = append(notify, h.watchersLocked(dev)...)
	}
	h.mu.Unlock()
	call(notify)
}

// Open starts the device's queue, or keeps it if it is already open.
func (h *Hub) Open(dev string) {
	h.mu.Lock()
	if _, ok := h.open[dev]; !ok {
		h.open[dev] = &device{}
	}
	n := h.watchersLocked(dev)
	h.mu.Unlock()
	call(n)
}

// Close drops the device's queue.
func (h *Hub) Close(dev string) {
	h.mu.Lock()
	delete(h.open, dev)
	n := h.watchersLocked(dev)
	h.mu.Unlock()
	call(n)
}

// Dismiss removes a track the device has left.
func (h *Hub) Dismiss(dev string, seq uint64) {
	h.mu.Lock()
	d := h.open[dev]
	if d == nil {
		h.mu.Unlock()
		return
	}
	for i, tr := range d.tracks {
		if tr.Seq == seq {
			d.size -= tr.size
			d.tracks = append(d.tracks[:i:i], d.tracks[i+1:]...)
			break
		}
	}
	n := h.watchersLocked(dev)
	h.mu.Unlock()
	call(n)
}

// Queue returns a copy of the device's queue.
func (h *Hub) Queue(dev string) Queue {
	h.mu.Lock()
	defer h.mu.Unlock()
	d := h.open[dev]
	if d == nil {
		return Queue{Tracks: []Track{}}
	}
	return Queue{Open: true, Tracks: append([]Track{}, d.tracks...)}
}

// AnyOpen reports whether some device has an open prompter.
func (h *Hub) AnyOpen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.open) > 0
}

// Watch calls fn after every change of the device's queue. fn must not
// block; it is called without the hub's lock.
func (h *Hub) Watch(dev string, fn func()) (cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextW++
	id := h.nextW
	if h.watchs[dev] == nil {
		h.watchs[dev] = map[int]func(){}
	}
	h.watchs[dev][id] = fn
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.watchs[dev], id)
		if len(h.watchs[dev]) == 0 {
			delete(h.watchs, dev)
		}
	}
}

func (h *Hub) watchersLocked(dev string) []func() {
	var out []func()
	for _, fn := range h.watchs[dev] {
		out = append(out, fn)
	}
	return out
}

func call(fns []func()) {
	for _, fn := range fns {
		fn()
	}
}
