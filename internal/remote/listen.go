package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	rescanInterval = 30 * time.Second
	// A phone that stops answering (background, lost wifi) is dropped after
	// this, which ends its serveConn and unwatches its session.
	quicIdleTimeout = 30 * time.Second
	quicKeepAlive   = 10 * time.Second
)

// Where the WebTransport sockets may listen and where connections may come
// from: RFC 1918, Tailscale's CGNAT range and Tailscale's IPv6 prefix.
var privatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

func eligible(a netip.Addr, allowLoopback bool) bool {
	a = a.Unmap()
	if allowLoopback && a.IsLoopback() {
		return true
	}
	for _, p := range privatePrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// interfaceAddrs lists the addresses of the interfaces that are up, except
// container and VM bridges.
func interfaceAddrs() []netip.Addr {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		// Best effort, to keep container bridges out of the QR code: this
		// list is not a security boundary (eligible and the source check
		// are).
		n := ifc.Name
		if strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") ||
			strings.HasPrefix(n, "veth") || strings.HasPrefix(n, "virbr") {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					out = append(out, ip.Unmap())
				}
			}
		}
	}
	return out
}

// serving is what one Enable started.
type serving struct {
	ctx    context.Context // canceled by close, parent of every connection
	cancel context.CancelFunc
	wt     *webtransport.Server
	page   *http.Server
	tls    *tls.Config
	quic   *quic.Config
	socks  map[netip.Addr]*socket // touched under lifeMu only
	stop   chan struct{}

	// QUIC connections that have not opened a session yet.
	waitMu  sync.Mutex
	waiting map[*quic.Conn]*time.Timer
}

type quicConnKey struct{}

type socket struct {
	udp *net.UDPConn
	tr  *quic.Transport
	ln  *quic.EarlyListener
}

func (k *socket) close() {
	_ = k.ln.Close()
	_ = k.tr.Close()
	_ = k.udp.Close()
}

func (r *Remote) newServing() *serving {
	s := &serving{
		socks:   make(map[netip.Addr]*socket),
		stop:    make(chan struct{}),
		waiting: make(map[*quic.Conn]*time.Timer),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.tls = http3.ConfigureTLSConfig(&tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			c := r.certs.Current(time.Now())
			if len(c.Certificate) == 0 {
				return nil, errors.New("no valid certificate")
			}
			return &c, nil
		},
	})
	s.quic = &quic.Config{
		MaxIdleTimeout:                   quicIdleTimeout,
		KeepAlivePeriod:                  quicKeepAlive,
		HandshakeIdleTimeout:             5 * time.Second,
		MaxIncomingStreams:               8,
		MaxIncomingUniStreams:            8,
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	}
	mux := http.NewServeMux()
	h3 := &http3.Server{
		Handler:        mux,
		TLSConfig:      s.tls,
		QUICConfig:     s.quic,
		MaxHeaderBytes: 16 << 10,
		ConnContext: func(ctx context.Context, c *quic.Conn) context.Context {
			return context.WithValue(ctx, quicConnKey{}, c)
		},
	}
	s.wt = &webtransport.Server{H3: h3, CheckOrigin: func(req *http.Request) bool {
		return r.originOK(req.Header.Get("Origin"))
	}}
	mux.HandleFunc("/nmx", func(w http.ResponseWriter, req *http.Request) { r.handleSession(s, w, req) })
	return s
}

// close stops everything one Enable started. Connections are closed first,
// while their sockets can still carry the CONNECTION_CLOSE.
func (s *serving) close() {
	close(s.stop)
	s.cancel()
	_ = s.wt.Close()
	for a, k := range s.socks {
		k.close()
		delete(s.socks, a)
	}
	if s.page != nil {
		_ = s.page.Close()
	}
}

func (r *Remote) rescanLoop(s *serving) {
	t := time.NewTicker(rescanInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			r.rescan()
		}
	}
}

// rescan extends the certificate series if needed, opens a socket on every
// new eligible address and closes the sockets of vanished ones.
func (r *Remote) rescan() {
	r.lifeMu.Lock()
	changed := r.rescanLocked()
	r.lifeMu.Unlock()
	if changed {
		r.notify()
	}
}

func (r *Remote) rescanLocked() bool {
	r.mu.Lock()
	s := r.srv
	r.mu.Unlock()
	if s == nil {
		return false
	}
	var errs []string
	if _, err := r.certs.Extend(time.Now()); err != nil {
		errs = append(errs, "certificates: "+err.Error())
	}

	want := map[netip.Addr]bool{}
	for _, a := range r.cfg.listAddrs() {
		a = a.Unmap()
		if eligible(a, r.cfg.allowLoopback) {
			want[a] = true
		}
	}
	changed := false
	for a, k := range s.socks {
		if !want[a] {
			k.close()
			delete(s.socks, a)
			changed = true
		}
	}
	for a := range want {
		if s.socks[a] != nil {
			continue
		}
		if err := r.openSocket(s, a); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		changed = true
	}

	addrs := make([]netip.AddrPort, 0, len(s.socks))
	for a, k := range s.socks {
		addrs = append(addrs, netip.AddrPortFrom(a, uint16(k.udp.LocalAddr().(*net.UDPAddr).Port)))
	}
	slices.SortFunc(addrs, func(x, y netip.AddrPort) int { return x.Compare(y) })
	msg := strings.Join(errs, "; ")
	r.mu.Lock()
	if r.errMsg != msg {
		r.errMsg = msg
		changed = true
	}
	r.addrs = addrs
	r.mu.Unlock()
	return changed
}

func (r *Remote) openSocket(s *serving, a netip.Addr) error {
	r.mu.Lock()
	port := r.port
	r.mu.Unlock()
	udp, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(a, uint16(port))))
	if err != nil {
		return err
	}
	if port == 0 {
		// An ephemeral port (tests): every address shares the first one.
		r.mu.Lock()
		r.port = udp.LocalAddr().(*net.UDPAddr).Port
		r.mu.Unlock()
	}
	tr := &quic.Transport{Conn: udp}
	ln, err := tr.ListenEarly(s.tls, s.quic)
	if err != nil {
		_ = tr.Close()
		_ = udp.Close()
		return err
	}
	k := &socket{udp: udp, tr: tr, ln: ln}
	s.socks[a] = k
	go r.acceptLoop(s, k)
	return nil
}

func (r *Remote) acceptLoop(s *serving, k *socket) {
	for {
		c, err := k.ln.Accept(s.ctx)
		if err != nil {
			return
		}
		peer := netip.Addr{}
		if ua, ok := c.RemoteAddr().(*net.UDPAddr); ok {
			peer = ua.AddrPort().Addr().Unmap()
		}
		if !eligible(peer, r.cfg.allowLoopback) {
			r.drop(peer, "nonprivate")
			_ = c.CloseWithError(0, "")
			continue
		}
		if r.limit.banned(peer) || !r.limit.startQUIC(peer) {
			r.drop(peer, "ratelimited")
			_ = c.CloseWithError(0, "")
			continue
		}
		// A connection must open its session soon, or it goes.
		timer := time.AfterFunc(r.cfg.connectTimeout, func() { _ = c.CloseWithError(0, "") })
		s.waitMu.Lock()
		s.waiting[c] = timer
		s.waitMu.Unlock()
		context.AfterFunc(c.Context(), func() {
			r.limit.endQUIC(peer)
			s.connected(c)
		})
		go func() { _ = s.wt.ServeQUICConn(c) }()
	}
}

// connected stops the CONNECT deadline of c.
func (s *serving) connected(c *quic.Conn) {
	s.waitMu.Lock()
	t := s.waiting[c]
	delete(s.waiting, c)
	s.waitMu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// drop records a connection refused before its handshake. Nothing is
// counted for non-private sources: they never get through anyway.
func (r *Remote) drop(peer netip.Addr, outcome string) {
	if r.limit.shouldLog(peer, outcome) {
		r.logLine("?", peer, outcome)
	}
}

// originOK accepts the origins of paired devices, and any https origin
// while a pairing token can still be used (the origin is then recorded with
// the device).
func (r *Remote) originOK(origin string) bool {
	if origin == "" {
		return false
	}
	for _, d := range r.devices.List() {
		if d.Origin == origin {
			return true
		}
	}
	return strings.HasPrefix(origin, "https://") && r.pairingOpen(time.Now())
}

// handleSession checks a WebTransport CONNECT request and runs the wire
// protocol on a stream the server opens.
func (r *Remote) handleSession(s *serving, w http.ResponseWriter, req *http.Request) {
	ap, err := netip.ParseAddrPort(req.RemoteAddr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	peer := ap.Addr().Unmap()
	if !eligible(peer, r.cfg.allowLoopback) {
		r.drop(peer, "nonprivate")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.limit.banned(peer) {
		r.drop(peer, "ratelimited")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	origin := req.Header.Get("Origin")
	if !r.originOK(origin) {
		r.limit.fail(peer)
		r.logLine("?", peer, "refused")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !r.limit.startPre(peer) {
		r.drop(peer, "ratelimited")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	ctx, cancel := context.WithCancel(s.ctx)
	lc := &liveConn{peer: peer, cancel: cancel, pre: true}
	r.liveMu.Lock()
	if r.liveClosed {
		r.liveMu.Unlock()
		cancel()
		r.limit.endPre(peer)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	r.live[lc] = struct{}{}
	r.liveWG.Add(1)
	r.liveMu.Unlock()

	sess, err := s.wt.Upgrade(w, req)
	if err != nil {
		r.endLive(lc, connResult{outcome: outcomeRefused}, ctx.Err() != nil)
		cancel()
		r.liveWG.Done()
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if c, ok := req.Context().Value(quicConnKey{}).(*quic.Conn); ok {
		s.connected(c)
	}
	go r.runSession(ctx, cancel, lc, sess, origin)
}

func (r *Remote) runSession(ctx context.Context, cancel context.CancelFunc, lc *liveConn, sess *webtransport.Session, origin string) {
	defer r.liveWG.Done()
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = sess.CloseWithError(0, "") })
	defer stop()

	var res connResult
	octx, ocancel := context.WithTimeout(ctx, 10*time.Second)
	str, err := sess.OpenStreamSync(octx)
	ocancel()
	if err == nil {
		res = r.serveConn(context.WithValue(ctx, liveKey{}, lc), newWTStream(str), origin, lc.peer)
	}
	cut := ctx.Err() != nil
	r.endLive(lc, res, cut)
	if !cut {
		// Give the phone a moment to read the last frame and close itself.
		t := time.NewTimer(2 * time.Second)
		select {
		case <-sess.Context().Done():
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
	_ = sess.CloseWithError(0, "")
}

// wtStream adapts a WebTransport stream to serveConn: Close may be called
// from any goroutine, even while a Write is blocked on a stalled phone.
type wtStream struct {
	str    *webtransport.Stream
	wmu    sync.Mutex
	closed atomic.Bool
	once   sync.Once
}

func newWTStream(str *webtransport.Stream) *wtStream { return &wtStream{str: str} }

func (s *wtStream) Read(p []byte) (int, error) { return s.str.Read(p) }

func (s *wtStream) Write(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed.Load() {
		return 0, net.ErrClosed
	}
	return s.str.Write(p)
}

func (s *wtStream) Close() error {
	s.once.Do(func() {
		s.closed.Store(true)
		if s.wmu.TryLock() {
			// No write in flight: end the stream cleanly so the last frame
			// (an Error, say) reaches the phone.
			_ = s.str.Close()
			s.wmu.Unlock()
		} else {
			s.str.CancelWrite(0)
		}
		s.str.CancelRead(0)
		// CancelRead does not wake a Read waiting for the session to close
		// after the phone went away; a past deadline does.
		_ = s.str.SetReadDeadline(time.Now())
	})
	return nil
}
