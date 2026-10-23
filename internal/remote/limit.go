package remote

import (
	"net/netip"
	"sync"
	"time"
)

const (
	maxFailures    = 5           // failed handshakes per source IP ...
	failureWindow  = time.Minute // ... within this window, then
	banDuration    = time.Minute // every connection from it is dropped for this long
	maxPreAuth     = 16          // connections not yet authenticated, in total
	maxPreAuthIP   = 4           // ... and per source IP
	maxAuthed      = 4           // authenticated connections at once
	maxQUIC        = 32          // QUIC connections, in total
	maxQUICPerIP   = 8           // ... and per source IP
	logQuietPeriod = time.Minute // one log line per IP and outcome for dropped connections
)

// limiter bounds what an address can cost the daemon before it proves who
// it is: failed handshakes, connections waiting for their handshake, and the
// number of phones connected at once.
type limiter struct {
	mu       sync.Mutex
	now      func() time.Time
	failures map[netip.Addr][]time.Time
	bans     map[netip.Addr]time.Time
	pre      map[netip.Addr]int
	preTotal int
	quic     map[netip.Addr]int
	quicAll  int
	authed   int
	logged   map[string]time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{
		now:      now,
		failures: make(map[netip.Addr][]time.Time),
		bans:     make(map[netip.Addr]time.Time),
		pre:      make(map[netip.Addr]int),
		quic:     make(map[netip.Addr]int),
		logged:   make(map[string]time.Time),
	}
}

// fail records a failed handshake from ip and bans it after maxFailures
// within failureWindow.
func (l *limiter) fail(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if now.Sub(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	if len(recent) >= maxFailures {
		l.bans[ip] = now.Add(banDuration)
		delete(l.failures, ip)
	} else {
		l.failures[ip] = recent
	}
	l.pruneLocked(now)
}

func (l *limiter) banned(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.bans[ip]
	if !ok {
		return false
	}
	if !l.now().Before(until) {
		delete(l.bans, ip)
		return false
	}
	return true
}

// pruneLocked keeps the maps from growing with addresses seen long ago.
func (l *limiter) pruneLocked(now time.Time) {
	if len(l.failures)+len(l.bans)+len(l.logged) < 1024 {
		return
	}
	for ip, ts := range l.failures {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= failureWindow {
			delete(l.failures, ip)
		}
	}
	for ip, until := range l.bans {
		if !now.Before(until) {
			delete(l.bans, ip)
		}
	}
	for k, t := range l.logged {
		if now.Sub(t) >= logQuietPeriod {
			delete(l.logged, k)
		}
	}
}

// startPre takes a pre-authentication slot for ip, or reports that the caps
// are reached.
func (l *limiter) startPre(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.preTotal >= maxPreAuth || l.pre[ip] >= maxPreAuthIP {
		return false
	}
	l.pre[ip]++
	l.preTotal++
	return true
}

func (l *limiter) endPre(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pre[ip] > 0 {
		l.pre[ip]--
		l.preTotal--
	}
	if l.pre[ip] == 0 {
		delete(l.pre, ip)
	}
}

// startQUIC takes a slot for a QUIC connection from ip, or reports that
// the caps are reached. It bounds connections that never send CONNECT.
func (l *limiter) startQUIC(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.quicAll >= maxQUIC || l.quic[ip] >= maxQUICPerIP {
		return false
	}
	l.quic[ip]++
	l.quicAll++
	return true
}

func (l *limiter) endQUIC(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.quic[ip] > 0 {
		l.quic[ip]--
		l.quicAll--
	}
	if l.quic[ip] == 0 {
		delete(l.quic, ip)
	}
}

func (l *limiter) startAuthed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.authed >= maxAuthed {
		return false
	}
	l.authed++
	return true
}

func (l *limiter) endAuthed() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.authed > 0 {
		l.authed--
	}
}

func (l *limiter) authedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.authed
}

// shouldLog reports whether a dropped connection from ip deserves a log
// line, so a flood writes one line per minute instead of one per packet.
// Non-private sources share one entry: they can be many and never pass.
func (l *limiter) shouldLog(ip netip.Addr, outcome string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	k := ip.String() + " " + outcome
	if outcome == "nonprivate" {
		k = outcome
	}
	if t, ok := l.logged[k]; ok && now.Sub(t) < logQuietPeriod {
		return false
	}
	l.logged[k] = now
	l.pruneLocked(now)
	return true
}
