package remote

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// phoneKeys are what a phone holds for one subscription.
type phoneKeys struct {
	ua        *ecdh.PrivateKey
	auth      []byte
	vapidPriv []byte
	vapidPub  []byte
}

func newPhoneKeys(t *testing.T) phoneKeys {
	t.Helper()
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return phoneKeys{ua: ua, auth: auth, vapidPriv: v.Bytes(), vapidPub: v.PublicKey().Bytes()}
}

func (k phoneKeys) msg(endpoint string) PushSubMsg {
	return PushSubMsg{Endpoint: endpoint, P256DH: k.ua.PublicKey().Bytes(), Auth: k.auth, VapidPub: k.vapidPub, VapidPriv: k.vapidPriv}
}

// decryptPush does the phone's side of RFC 8291, apart from encryptPush.
func decryptPush(t *testing.T, body []byte, k phoneKeys) []byte {
	t.Helper()
	if len(body) < 86 || binary.BigEndian.Uint32(body[16:20]) != 4096 || body[20] != 65 {
		t.Fatalf("bad header: %x", body[:min(len(body), 86)])
	}
	salt, asPub, ct := body[:16], body[21:86], body[86:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := k.ua.ECDH(as)
	if err != nil {
		t.Fatal(err)
	}
	ikm, _ := hkdf.Key(sha256.New, shared, k.auth, "WebPush: info\x00"+string(k.ua.PublicKey().Bytes())+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("no last-record delimiter: %x", plain)
	}
	return plain[:len(plain)-1]
}

func TestEncryptPushRoundTrip(t *testing.T) {
	k := newPhoneKeys(t)
	body, err := encryptPush([]byte("a secret"), k.ua.PublicKey().Bytes(), k.auth)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "a secret") {
		t.Fatal("plain text in the body")
	}
	if got := decryptPush(t, body, k); string(got) != "a secret" {
		t.Fatalf("got %q", got)
	}
}

func TestVapidAuth(t *testing.T) {
	k := newPhoneKeys(t)
	now := time.Now()
	h, err := vapidAuth("https://web.push.apple.com/abc?x=1", k.vapidPub, k.vapidPriv, now)
	if err != nil {
		t.Fatal(err)
	}
	jwt, pub, ok := strings.Cut(strings.TrimPrefix(h, "vapid t="), ", k=")
	if !ok || pub != base64.RawURLEncoding.EncodeToString(k.vapidPub) {
		t.Fatalf("header %q", h)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Aud != "https://web.push.apple.com" || c.Sub != pushSubject || c.Exp <= now.Unix() || c.Exp > now.Add(24*time.Hour).Unix() {
		t.Fatalf("claims %+v", c)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature of %d bytes", len(sig))
	}
	pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), k.vapidPub)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pk, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("bad signature")
	}
}

func TestPushSubChecks(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	k, other := newPhoneKeys(t), newPhoneKeys(t)
	good := "https://web.push.apple.com/x"
	if err := r.setPush("d", k.msg(good)); err != nil {
		t.Fatal(err)
	}
	bad := map[string]PushSubMsg{
		"http":      k.msg("http://web.push.apple.com/x"),
		"other":     k.msg("https://127.0.0.1/x"),
		"lookalike": k.msg("https://web.push.apple.com.evil.example/x"),
		"userinfo":  k.msg("https://web.push.apple.com@evil.example/x"),
		"long":      k.msg("https://web.push.apple.com/" + strings.Repeat("x", maxEndpoint)),
	}
	wrong := k.msg(good)
	wrong.VapidPub = other.vapidPub
	bad["foreign vapid key"] = wrong
	short := k.msg(good)
	short.Auth = k.auth[:8]
	bad["short secret"] = short
	broken := k.msg(good)
	broken.P256DH = []byte{1, 2, 3}
	bad["bad key"] = broken
	for name, m := range bad {
		if r.setPush("x", m) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(r.push.all()) != 1 {
		t.Fatalf("subs: %+v", r.push.all())
	}
	if err := r.setPush("d", PushSubMsg{}); err != nil || len(r.push.all()) != 0 {
		t.Fatalf("stop: %v %+v", err, r.push.all())
	}
}

func TestPushStoreOnDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st := loadPushStore(dir)
	if err := st.set("d", pushSub{Endpoint: "e"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "push.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if got := loadPushStore(dir).all()["d"].Endpoint; got != "e" {
		t.Fatalf("reloaded %q", got)
	}
	st.remove("d", "not this one")
	if len(st.all()) != 1 {
		t.Fatal("removed another subscription")
	}
	st.remove("d", "e")
	if len(loadPushStore(dir).all()) != 0 {
		t.Fatal("still on disk")
	}
}

// pushServer stands for the push service.
type pushServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
	status int
}

func newPushServer(t *testing.T) *pushServer {
	t.Helper()
	p := &pushServer{status: http.StatusCreated}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, b)
		p.heads = append(p.heads, req.Header.Clone())
		st := p.status
		p.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pushServer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

type activeHost struct {
	*fakeHost
	active bool
}

func (h *activeHost) DesktopActive() bool { return h.active }

func blocked(key string, since time.Time) SessionInfo {
	return SessionInfo{Key: key, Name: "claude", Agent: &AgentInfo{State: "blocked", Kind: "permission", Msg: "rm -rf", Since: since}}
}

func addDevice(t *testing.T, r *Remote, id string) {
	t.Helper()
	p, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&p.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.devices.Add(Device{ID: id, Name: id, CredID: []byte(id), PubKey: der, Origin: testOrigin}); err != nil {
		t.Fatal(err)
	}
}

func TestPushScan(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	host := &activeHost{fakeHost: r.host.(*fakeHost)}
	r.host = host
	srv := newPushServer(t)
	r.pushClient = srv.Client()
	r.pushHostOK = func(string) bool { return true }
	addDevice(t, r, "phone")
	k := newPhoneKeys(t)
	if err := r.setPush("phone", k.msg(srv.URL+"/sub")); err != nil {
		t.Fatal(err)
	}
	set := func(s ...SessionInfo) {
		host.mu.Lock()
		host.sessions = s
		host.mu.Unlock()
	}
	ctx := context.Background()
	t0 := time.Now()
	sc := &pushScan{}

	set(blocked("old", t0))
	r.scanPush(ctx, sc)
	set(blocked("old", t0), blocked("new", t0))
	r.scanPush(ctx, sc)
	waitFor(t, "one notification", 3*time.Second, func() bool { return srv.count() == 1 })
	r.scanPush(ctx, sc) // still waiting: nothing new

	srv.mu.Lock()
	body, head := srv.bodies[0], srv.heads[0]
	srv.mu.Unlock()
	var got pushPayload
	if err := json.Unmarshal(decryptPush(t, body, k), &got); err != nil {
		t.Fatal(err)
	}
	if got.Key != "new" || got.Device != "phone" || got.Title != "test-mac" || !strings.Contains(got.Body, "needs a permission") {
		t.Fatalf("payload %+v", got)
	}
	if head.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(head.Get("Authorization"), "vapid t=") || head.Get("TTL") == "" || len(head.Get("Topic")) != 32 {
		t.Fatalf("headers %v", head)
	}

	// Waiting again later is news; someone at the machine sees it there.
	host.active = true
	set(blocked("new", t0.Add(time.Minute)))
	r.scanPush(ctx, sc)
	host.active = false

	// A phone with the app open sees it on screen.
	lc := &liveConn{authed: true, deviceID: "phone"}
	r.liveMu.Lock()
	r.live[lc] = struct{}{}
	r.liveMu.Unlock()
	set(blocked("new", t0.Add(2*time.Minute)))
	r.scanPush(ctx, sc)
	r.liveMu.Lock()
	delete(r.live, lc)
	r.liveMu.Unlock()

	time.Sleep(100 * time.Millisecond)
	if srv.count() != 1 {
		t.Fatalf("%d notifications, want still 1", srv.count())
	}

	// The service says the subscription is gone: it is forgotten.
	srv.mu.Lock()
	srv.status = http.StatusGone
	srv.mu.Unlock()
	set(blocked("new", t0.Add(3*time.Minute)))
	r.scanPush(ctx, sc)
	waitFor(t, "subscription forgotten", 3*time.Second, func() bool { return len(r.push.all()) == 0 })
}

func TestPushForgottenWithDevice(t *testing.T) {
	r, _ := newTestServer(t, testRemoteOpts{})
	addDevice(t, r, "phone")
	k := newPhoneKeys(t)
	if err := r.setPush("phone", k.msg("https://web.push.apple.com/x")); err != nil {
		t.Fatal(err)
	}
	if err := r.Revoke("phone"); err != nil {
		t.Fatal(err)
	}
	if len(r.push.all()) != 0 {
		t.Fatal("subscription kept after revoke")
	}
}

// A handshake is counted as soon as it starts, even one that goes no further.
func TestHandshakesCounted(t *testing.T) {
	r, cfg := newTestServer(t, testRemoteOpts{})
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := r.Handshakes(); n != 0 {
		t.Fatalf("%d before any", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialQUIC(ctx, fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseWithError(0, "")
	waitFor(t, "handshake counted", 3*time.Second, func() bool { n, _, _ := r.Handshakes(); return n >= 1 })
	if _, at, peer := r.Handshakes(); at.IsZero() || !peer.IsLoopback() {
		t.Fatalf("at %v peer %v", at, peer)
	}
}
