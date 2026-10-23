package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

const testOrigin = "https://arch.example.ts.net"

// freePort returns a port that is free on 127.0.0.1 for both UDP and TCP
// at the time of the call.
func freePort(t *testing.T) int {
	t.Helper()
	for range 50 {
		u, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := u.LocalAddr().(*net.UDPAddr).Port
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		u.Close()
		if err != nil {
			continue
		}
		l.Close()
		return port
	}
	t.Fatal("no free port")
	return 0
}

type testRemoteOpts struct {
	idleOff time.Duration
	addrs   func() []netip.Addr
}

func newTestServer(t *testing.T, o testRemoteOpts) (*Remote, Config) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote")
	cfg := Config{
		Dir:           dir,
		Name:          "test-mac",
		Port:          freePort(t),
		PagePort:      freePort(t),
		IdleOff:       o.idleOff,
		Version:       "v9.9.9-test",
		allowLoopback: true,
		listAddrs:     o.addrs,
	}
	if cfg.listAddrs == nil {
		cfg.listAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }
	}
	r, err := New(cfg, newFakeHost())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(r.Close)
	return r, cfg
}

func currentHash(r *Remote) [32]byte {
	c := r.certs.Current(time.Now())
	return sha256.Sum256(c.Certificate[0])
}

// dialRemote opens a WebTransport session pinned to hash and returns the
// stream the server opens for the wire protocol.
func dialRemote(ctx context.Context, addr string, hash [32]byte, origin string) (*webtransport.Session, *webtransport.Stream, error) {
	tc := &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 || sha256.Sum256(s.PeerCertificates[0].Raw) != hash {
				return errors.New("certificate hash mismatch")
			}
			return nil
		},
	}
	cl := &webtransport.Transport{
		TLSClientConfig: tc,
		QUICConfig:      &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
	}
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	_, sess, err := cl.Dial(ctx, "https://"+addr+"/nmx", hdr)
	if err != nil {
		return nil, nil, err
	}
	str, err := sess.AcceptStream(ctx)
	if err != nil {
		sess.CloseWithError(0, "")
		return nil, nil, err
	}
	return sess, str, nil
}

func listenAddr(t *testing.T, r *Remote) string {
	t.Helper()
	st := r.State()
	if len(st.Addrs) == 0 {
		t.Fatalf("no listening address: %+v", st)
	}
	return st.Addrs[0]
}

func readChallenge(t *testing.T, rw io.Reader) ChallengeMsg {
	t.Helper()
	typ, payload, err := readFrame(rw)
	if err != nil {
		t.Fatalf("reading challenge: %v", err)
	}
	if typ != wChallenge {
		t.Fatalf("expected Challenge, got %d", typ)
	}
	var ch ChallengeMsg
	if err := json.Unmarshal(payload, &ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

// addTestDevice pairs a device with a fresh key, returning what is needed
// to authenticate as it.
type testDevice struct {
	dev  Device
	auth func(t *testing.T, nonce []byte) AuthMsg
}

func addTestDevice(t *testing.T, r *Remote, id string) testDevice {
	t.Helper()
	key, pub := makeTestP256Key(t)
	credID := []byte("cred-" + id)
	dev := Device{ID: id, Name: "phone-" + id, CredID: credID, PubKey: pub, Origin: testOrigin}
	if err := r.devices.Add(dev); err != nil {
		t.Fatal(err)
	}
	return testDevice{dev: dev, auth: func(t *testing.T, nonce []byte) AuthMsg {
		cd := makeTestClientDataJSON(t, "webauthn.get", testOrigin, nonce)
		ad := makeTestAuthData(t, testOrigin, 0x01|0x04, 1)
		sig := signAssertion(t, key, ad, cd)
		_, sessPub := makeTestP256Key(t)
		return AuthMsg{
			Assertion:  AssertionWire{CredID: credID, AuthData: ad, ClientData: cd, Sig: sig},
			SessionPub: sessPub,
		}
	}}
}

func badAuth(t *testing.T, d testDevice, nonce []byte) AuthMsg {
	m := d.auth(t, nonce)
	m.Assertion.Sig = bytes.Clone(m.Assertion.Sig)
	m.Assertion.Sig[len(m.Assertion.Sig)-1] ^= 0xff
	return m
}

func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDisabledListensNowhere(t *testing.T) {
	r, cfg := newTestServer(t, testRemoteOpts{})
	if r.State().Enabled {
		t.Fatal("enabled right after New")
	}
	u, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		t.Fatalf("UDP port taken while disabled: %v", err)
	}
	u.Close()
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.PagePort), time.Second); err == nil {
		c.Close()
		t.Fatal("page port accepts connections while disabled")
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "active")); !os.IsNotExist(err) {
		t.Fatalf("active file present while disabled: %v", err)
	}
}

func TestEnableServesHandshake(t *testing.T) {
	r, cfg := newTestServer(t, testRemoteOpts{})
	addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "active")); err != nil {
		t.Fatalf("active file missing after Enable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.CloseWithError(0, "")
	ch := readChallenge(t, str)
	if ch.Name != "test-mac" || len(ch.Nonce) != 32 || ch.Proto != 1 {
		t.Fatalf("bad challenge: %+v", ch)
	}

	r.Disable()
	if _, err := os.Stat(filepath.Join(cfg.Dir, "active")); !os.IsNotExist(err) {
		t.Fatalf("active file still there after Disable: %v", err)
	}
	u, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		t.Fatalf("UDP port still taken after Disable: %v", err)
	}
	u.Close()
}

func TestOriginRefused(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if sess, _, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), "https://evil.example"); err == nil {
		sess.CloseWithError(0, "")
		t.Fatal("unknown origin accepted with no pairing open")
	}
}

func TestPinnedHashMismatchFails(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrong := currentHash(r)
	wrong[0] ^= 1
	if sess, _, err := dialRemote(ctx, listenAddr(t, r), wrong, testOrigin); err == nil {
		sess.CloseWithError(0, "")
		t.Fatal("dial succeeded with a wrong pinned hash")
	}
}

func TestIdleOff(t *testing.T) {
	r, cfg := newTestServer(t, testRemoteOpts{idleOff: 200 * time.Millisecond})
	start := time.Now()
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle off", 3*time.Second, func() bool { return !r.State().Enabled })
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("disabled too early: %v", el)
	}
	if _, err := os.Stat(filepath.Join(cfg.Dir, "active")); !os.IsNotExist(err) {
		t.Fatalf("active file still there after idle off: %v", err)
	}
}

func TestRescanPicksNewAddress(t *testing.T) {
	var mu sync.Mutex
	a := netip.MustParseAddr("127.0.0.1")
	b := netip.MustParseAddr("127.0.0.2")
	cur := []netip.Addr{a}
	lister := func() []netip.Addr {
		mu.Lock()
		defer mu.Unlock()
		return append([]netip.Addr(nil), cur...)
	}
	r, cfg := newTestServer(t, testRemoteOpts{addrs: lister})
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	ap := func(ip netip.Addr) string { return netip.AddrPortFrom(ip, uint16(cfg.Port)).String() }
	if got := r.State().Addrs; len(got) != 1 || got[0] != ap(a) {
		t.Fatalf("Addrs = %v, want [%s]", got, ap(a))
	}

	mu.Lock()
	cur = []netip.Addr{a, b}
	mu.Unlock()
	r.rescan()
	if got := r.State().Addrs; len(got) != 2 || got[0] != ap(a) || got[1] != ap(b) {
		t.Fatalf("Addrs = %v, want [%s %s]", got, ap(a), ap(b))
	}

	// A vanished address loses its socket.
	mu.Lock()
	cur = []netip.Addr{b}
	mu.Unlock()
	r.rescan()
	if got := r.State().Addrs; len(got) != 1 || got[0] != ap(b) {
		t.Fatalf("Addrs = %v, want [%s]", got, ap(b))
	}
	u, err := net.ListenPacket("udp", ap(a))
	if err != nil {
		t.Fatalf("socket on the vanished address still open: %v", err)
	}
	u.Close()
}

func TestRescanSkipsIneligible(t *testing.T) {
	lister := func() []netip.Addr {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::")}
	}
	r, _ := newTestServer(t, testRemoteOpts{addrs: lister})
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	if got := r.State().Addrs; len(got) != 0 {
		t.Fatalf("listening on ineligible addresses: %v", got)
	}
	if !r.State().Enabled {
		t.Fatal("Enable must succeed with no eligible address")
	}
}

func TestEligibleAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"10.1.2.3": true, "172.16.0.1": true, "172.31.255.255": true, "172.32.0.1": false,
		"192.168.1.1": true, "100.64.0.1": true, "100.127.255.255": true, "100.128.0.1": false,
		"fd7a:115c:a1e0::1": true, "fd7a:115c:a1e1::1": false, "fd00::1": false,
		"127.0.0.1": false, "8.8.8.8": false, "0.0.0.0": false, "::": false, "::ffff:192.168.1.1": true,
		"::ffff:8.8.8.8": false, "fe80::1": false, "fc00::1": false,
	} {
		if got := eligible(netip.MustParseAddr(s), false); got != want {
			t.Errorf("eligible(%s) = %v, want %v", s, got, want)
		}
	}
	if !eligible(netip.MustParseAddr("127.0.0.1"), true) {
		t.Error("loopback refused with allowLoopback")
	}
}

func TestPagePolicy(t *testing.T) {
	r, cfg := newTestServer(t, testRemoteOpts{})
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.PagePort)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	const csp = "default-src 'self'; script-src 'self'; connect-src 'self' https:; img-src 'self' blob: data:; style-src 'self'; font-src 'self'; media-src 'self' blob:; frame-ancestors 'none'"
	if got := resp.Header.Get("Content-Security-Policy"); got != csp {
		t.Fatalf("CSP = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control on / = %q", got)
	}

	resp, err = http.Get(base + "/fonts/IosevkaTerm-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(body, gfx.IosevkaTTF(0)) || len(body) == 0 {
		t.Fatalf("font: status %d, %d bytes", resp.StatusCode, len(body))
	}
	resp, err = http.Get(base + "/fonts/IosevkaTerm-BoldItalic.ttf")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(body, gfx.IosevkaTTF(gfx.StyleBold|gfx.StyleItalic)) {
		t.Fatal("bold italic font differs")
	}

	resp, err = http.Get(base + "/version.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version string `json:"version"`
		Proto   int    `json:"proto"`
	}
	err = json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if err != nil || v.Version != "v9.9.9-test" || v.Proto != 1 {
		t.Fatalf("version.json = %+v, %v", v, err)
	}

	r.Disable()
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.PagePort), time.Second); err == nil {
		c.Close()
		t.Fatal("page port still open after Disable")
	}
}

// authOK runs a full passkey handshake on a new session.
func authOK(t *testing.T, r *Remote, d testDevice) (*webtransport.Session, *webtransport.Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch := readChallenge(t, str)
	if err := writeFrame(str, wAuth, d.auth(t, ch.Nonce)); err != nil {
		t.Fatal(err)
	}
	typ, _, err := readFrame(str)
	if err != nil || typ != wAuthed {
		t.Fatalf("expected Authed, got %d, %v", typ, err)
	}
	return sess, str
}

// authBad runs a handshake with a broken signature and waits for the end.
func authBad(t *testing.T, r *Remote, d testDevice) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.CloseWithError(0, "")
	ch := readChallenge(t, str)
	if err := writeFrame(str, wAuth, badAuth(t, d, ch.Nonce)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := readFrame(str); err != nil {
			return
		}
	}
}

func TestLogRecordsOutcomes(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	sess, _ := authOK(t, r, d)
	defer sess.CloseWithError(0, "")
	waitFor(t, "connected device", 2*time.Second, func() bool {
		c := r.State().Connected
		return len(c) == 1 && c[0] == "phone-d1"
	})
	authBad(t, r, d)
	waitFor(t, "two log lines", 3*time.Second, func() bool { return len(r.Log(10)) == 2 })
	lines := r.Log(10)
	if f := strings.Fields(lines[0]); len(f) != 4 || f[1] != "phone-d1" || f[2] != "127.0.0.1" || f[3] != "ok" {
		t.Fatalf("first line %q", lines[0])
	}
	if f := strings.Fields(lines[1]); len(f) != 4 || f[1] != "?" || f[2] != "127.0.0.1" || f[3] != "refused" {
		t.Fatalf("second line %q", lines[1])
	}
	if got := r.Log(1); len(got) != 1 || got[0] != lines[1] {
		t.Fatalf("Log(1) = %v", got)
	}
}

func TestLogTrimmed(t *testing.T) {
	r, cfg := newTestServer(t, testRemoteOpts{})
	for i := range 2001 {
		r.logLine("?", netip.MustParseAddr("192.168.1.2"), fmt.Sprintf("refused%d", i))
	}
	data, err := os.ReadFile(filepath.Join(cfg.Dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 2000 || len(lines) < 1000 {
		t.Fatalf("log has %d lines", len(lines))
	}
	if !strings.HasSuffix(lines[len(lines)-1], "refused2000") {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
	st, _ := os.Stat(filepath.Join(cfg.Dir, "log"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %o", st.Mode().Perm())
	}
}

func TestRateLimit(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		authBad(t, r, d)
	}
	waitFor(t, "5 refused", 3*time.Second, func() bool { return len(r.Log(10)) >= 5 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err == nil {
		defer sess.CloseWithError(0, "")
		if _, _, err := readFrame(str); err == nil {
			t.Fatal("6th connection got a Challenge")
		}
	}
	waitFor(t, "ratelimited line", 3*time.Second, func() bool {
		l := r.Log(1)
		return len(l) == 1 && strings.HasSuffix(l[0], " ratelimited")
	})
}

func TestPreAuthPerIPCap(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var held []*webtransport.Session
	defer func() {
		for _, s := range held {
			s.CloseWithError(0, "")
		}
	}()
	for i := range 4 {
		sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		held = append(held, sess)
		readChallenge(t, str)
	}
	sess, str, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err == nil {
		defer sess.CloseWithError(0, "")
		if _, _, err := readFrame(str); err == nil {
			t.Fatal("5th pre-auth connection from one IP got a Challenge")
		}
	}

	// Freeing a slot lets the next one in.
	held[0].CloseWithError(0, "")
	held = held[1:]
	waitFor(t, "slot released", 3*time.Second, func() bool {
		r.limit.mu.Lock()
		defer r.limit.mu.Unlock()
		return r.limit.pre[netip.MustParseAddr("127.0.0.1")] == 3
	})
	sess2, str2, err := dialRemote(ctx, listenAddr(t, r), currentHash(r), testOrigin)
	if err != nil {
		t.Fatalf("dial after release: %v", err)
	}
	held = append(held, sess2)
	readChallenge(t, str2)
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(func() time.Time { return now })
	ip := netip.MustParseAddr("192.168.1.9")
	other := netip.MustParseAddr("192.168.1.10")
	for range 4 {
		l.fail(ip)
	}
	if l.banned(ip) {
		t.Fatal("banned after 4 failures")
	}
	l.fail(ip)
	if !l.banned(ip) || l.banned(other) {
		t.Fatal("5 failures must ban this IP only")
	}
	now = now.Add(61 * time.Second)
	if l.banned(ip) {
		t.Fatal("still banned after a minute")
	}

	// Pre-auth caps: 4 per IP, 16 in total.
	for i := range 4 {
		if !l.startPre(ip) {
			t.Fatalf("startPre %d refused", i)
		}
	}
	if l.startPre(ip) {
		t.Fatal("5th pre-auth slot for one IP granted")
	}
	for i := range 12 {
		if !l.startPre(netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})) {
			t.Fatalf("pre-auth slot %d refused", i)
		}
	}
	if l.startPre(other) {
		t.Fatal("17th pre-auth slot granted")
	}
	l.endPre(ip)
	if !l.startPre(other) {
		t.Fatal("slot not released")
	}

	// At most 4 authenticated connections.
	for i := range 4 {
		if !l.startAuthed() {
			t.Fatalf("authed %d refused", i)
		}
	}
	if l.startAuthed() {
		t.Fatal("5th authenticated connection granted")
	}
	l.endAuthed()
	if !l.startAuthed() {
		t.Fatal("authed slot not released")
	}
}

func TestRevokeCutsConnection(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	sess, str := authOK(t, r, d)
	defer sess.CloseWithError(0, "")
	if err := r.Revoke("d1"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := readFrame(str); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked device's connection still open")
	}
	waitFor(t, "no connected device", 2*time.Second, func() bool { return len(r.State().Connected) == 0 })
	if len(r.State().Devices) != 0 {
		t.Fatal("device still listed")
	}
}

func TestDisableClosesConnectionsAndResumes(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	sess, str := authOK(t, r, d)
	defer sess.CloseWithError(0, "")
	r.Disable()
	// Disable returns once every connection handler has ended.
	if n := len(r.State().Connected); n != 0 {
		t.Fatalf("%d connections after Disable", n)
	}
	r.resumes.mu.Lock()
	n := len(r.resumes.sessions)
	r.resumes.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d resume keys survive Disable", n)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := readFrame(str); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("connection still open after Disable")
	}
}

func TestStartPairingState(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	changed := make(chan struct{}, 16)
	r.OnChange(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	ps, err := r.StartPairing()
	if err != nil {
		t.Fatal(err)
	}
	st := r.State()
	if !st.Enabled || st.Pairing == nil || st.Pairing.Payload != ps.Payload || st.Pairing.Code != "" {
		t.Fatalf("state after StartPairing: %+v", st)
	}
	if !strings.HasPrefix(ps.Payload, "nmx1:") {
		t.Fatalf("payload %q", ps.Payload)
	}
	p, err := decodePayload(ps.Payload)
	if err != nil {
		t.Fatal(err)
	}
	h := currentHash(r)
	if p.Name != "test-mac" || len(p.Addrs) != 1 || p.Addrs[0] != "127.0.0.1" || p.Port != r.cfg.Port ||
		!bytes.Equal(p.Hash, h[:]) || len(p.Token) != 16 {
		t.Fatalf("payload content %+v", p)
	}
	select {
	case <-changed:
	default:
		t.Fatal("no change notification")
	}

	// A pairing allows any https origin to open a session.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, str, err := dialRemote(ctx, listenAddr(t, r), h, "https://other.example.ts.net")
	if err != nil {
		t.Fatalf("dial during pairing: %v", err)
	}
	defer sess.CloseWithError(0, "")
	readChallenge(t, str)

	// Even while the pairing is open, only https origins get in.
	if sess, _, err := dialRemote(ctx, listenAddr(t, r), h, "http://other.example.ts.net"); err == nil {
		sess.CloseWithError(0, "")
		t.Fatal("non-https origin accepted during pairing")
	}

	// Cancelling closes it.
	r.ConfirmPairing(false)
	if r.State().Pairing != nil {
		t.Fatal("pairing still shown after cancel")
	}
	if sess, _, err := dialRemote(ctx, listenAddr(t, r), h, "https://other.example.ts.net"); err == nil {
		sess.CloseWithError(0, "")
		t.Fatal("unknown origin accepted after the pairing closed")
	}
}

func TestRegenerateForgetsEverything(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	old := currentHash(r)
	sess, str := authOK(t, r, d)
	defer sess.CloseWithError(0, "")
	if err := r.Regenerate(); err != nil {
		t.Fatal(err)
	}
	if currentHash(r) == old {
		t.Fatal("certificate unchanged")
	}
	if len(r.State().Devices) != 0 {
		t.Fatal("devices survive Regenerate")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := readFrame(str); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("connection survives Regenerate")
	}
}

func decodePayload(s string) (qrPayload, error) {
	var p qrPayload
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "nmx1:"))
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}

func TestAuthedSlotReleased(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	// One more than the cap, one after another: each must free its slot.
	for i := range maxAuthed + 1 {
		sess, _ := authOK(t, r, d)
		sess.CloseWithError(0, "")
		waitFor(t, fmt.Sprintf("slot %d released", i), 3*time.Second, func() bool {
			return r.limit.authedCount() == 0
		})
	}
}

func TestIdleOffWaitsForConnectedPhone(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{idleOff: 200 * time.Millisecond})
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	sess, _ := authOK(t, r, d)
	time.Sleep(500 * time.Millisecond)
	if !r.State().Enabled {
		t.Fatal("idle off fired with a phone connected")
	}
	sess.CloseWithError(0, "")
	waitFor(t, "idle off after the phone left", 3*time.Second, func() bool { return !r.State().Enabled })
}

// dialQUIC opens a bare QUIC connection that never sends CONNECT.
func dialQUIC(ctx context.Context, addr string) (*quic.Conn, error) {
	tc := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}
	return quic.DialAddr(ctx, addr, tc, &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
		KeepAlivePeriod:                  time.Second,
	})
}

func closedWithin(c *quic.Conn, d time.Duration) bool {
	select {
	case <-c.Context().Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestQUICPerIPCap(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	r.cfg.connectTimeout = time.Minute
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var conns []*quic.Conn
	defer func() {
		for _, c := range conns {
			c.CloseWithError(0, "")
		}
	}()
	for i := range maxQUICPerIP {
		c, err := dialQUIC(ctx, listenAddr(t, r))
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	extra, err := dialQUIC(ctx, listenAddr(t, r))
	if err == nil {
		conns = append(conns, extra)
		if !closedWithin(extra, 2*time.Second) {
			t.Fatalf("QUIC connection %d from one IP kept open", maxQUICPerIP+1)
		}
	}
	for i, c := range conns[:maxQUICPerIP] {
		if closedWithin(c, 0) {
			t.Fatalf("connection %d under the cap was closed", i)
		}
	}
	waitFor(t, "ratelimited line", 3*time.Second, func() bool {
		l := r.Log(1)
		return len(l) == 1 && strings.HasSuffix(l[0], " ratelimited")
	})

	// Closing one frees its slot.
	conns[0].CloseWithError(0, "")
	waitFor(t, "slot released", 3*time.Second, func() bool {
		r.limit.mu.Lock()
		defer r.limit.mu.Unlock()
		return r.limit.quic[netip.MustParseAddr("127.0.0.1")] == maxQUICPerIP-1
	})
	c, err := dialQUIC(ctx, listenAddr(t, r))
	if err != nil {
		t.Fatal(err)
	}
	conns = append(conns, c)
	if closedWithin(c, 500*time.Millisecond) {
		t.Fatal("connection refused after a slot was freed")
	}
}

func TestQUICWithoutConnectClosed(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	r.cfg.connectTimeout = 300 * time.Millisecond
	d := addTestDevice(t, r, "d1")
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dialQUIC(ctx, listenAddr(t, r))
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseWithError(0, "")
	if !closedWithin(c, 3*time.Second) {
		t.Fatal("QUIC connection without CONNECT kept open")
	}
	// A real session outlives the deadline.
	sess, str := authOK(t, r, d)
	defer sess.CloseWithError(0, "")
	time.Sleep(600 * time.Millisecond)
	if err := writeFrame(str, wList, nil); err != nil {
		t.Fatal(err)
	}
	if typ, _, err := readFrame(str); err != nil || typ != wSessions {
		t.Fatalf("session closed by the CONNECT deadline: %d, %v", typ, err)
	}
}

func TestNonPrivateNotTracked(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(func() time.Time { return now })
	r := &Remote{}
	r.limit = l
	r.cfg.Dir = t.TempDir()
	r.logCount = -1
	for i := range 50 {
		r.drop(netip.AddrFrom4([4]byte{8, 8, 8, byte(i)}), "nonprivate")
	}
	l.mu.Lock()
	n := len(l.failures) + len(l.bans) + len(l.logged)
	l.mu.Unlock()
	if n > 1 {
		t.Fatalf("limiter tracks %d entries for public sources", n)
	}
	if got := r.Log(10); len(got) != 1 || !strings.HasSuffix(got[0], " nonprivate") {
		t.Fatalf("log = %v", got)
	}
}
