package remote

import (
	"errors"
	"sync"
	"time"
)

// A phone may open terminals (NewTerm). Even authenticated, a stolen phone
// must not flood the machine with shells: one creation at a time per
// device, and at most newTermMax per device over newTermWindow.
const (
	newTermMax    = 5
	newTermWindow = time.Minute
)

var (
	errNewTermBusy    = errors.New("new terminal: busy")
	errNewTermTooMany = errors.New("new terminal: too many")
)

// newTerms counts the phones' terminal creations, per device ID: a phone
// that reconnects keeps its count.
type newTerms struct {
	mu    sync.Mutex
	busy  map[string]bool
	times map[string][]time.Time
}

// start records a creation by dev at now, or says why it is refused.
func (n *newTerms) start(dev string, now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.busy == nil {
		n.busy = make(map[string]bool)
		n.times = make(map[string][]time.Time)
	}
	if n.busy[dev] {
		return errNewTermBusy
	}
	recent := n.times[dev][:0]
	for _, t := range n.times[dev] {
		if now.Sub(t) < newTermWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= newTermMax {
		n.times[dev] = recent
		return errNewTermTooMany
	}
	n.times[dev] = append(recent, now)
	n.busy[dev] = true
	return nil
}

// end marks dev's creation finished, failed or not.
func (n *newTerms) end(dev string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.busy, dev)
}
