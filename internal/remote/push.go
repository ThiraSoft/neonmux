package remote

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Web Push: a notification on the phone when an agent starts waiting, while
// no connection is open (iOS cuts it as soon as the app leaves the screen).
// Each phone opts in from the app. The payload is encrypted for the phone
// (RFC 8291), so the push service, Apple's on an iPhone, carries it without
// reading it. It learns only that a notification was sent, and when.

const (
	pushInterval = 2 * time.Second // how often the sessions are looked at
	pushTimeout  = 10 * time.Second
	pushTTL      = time.Hour // a notification older than this is not worth delivering
	maxPushBody  = 180       // runes
	maxEndpoint  = 2048
	// pushSubject identifies the sender to the push service (RFC 8292).
	pushSubject = "https://github.com/ThiraSoft/neonmux"
)

// pushSub is one phone's subscription, with the VAPID key the phone made for
// it. The key is the phone's: a subscription is bound to one key, and a phone
// paired with several machines holds a single subscription.
type pushSub struct {
	Endpoint  string `json:"endpoint"`
	P256DH    []byte `json:"p256dh"`
	Auth      []byte `json:"auth"`
	VapidPub  []byte `json:"vapid_pub"`
	VapidPriv []byte `json:"vapid_priv"`
}

// pushStore keeps the subscriptions by device, in Dir/push.json. A nil store
// holds nothing and refuses nothing it is asked to forget.
type pushStore struct {
	mu   sync.Mutex
	path string
	subs map[string]pushSub
}

// loadPushStore never fails: an unreadable file only means no notifications
// until the phone connects again, and sends its subscription.
func loadPushStore(dir string) *pushStore {
	st := &pushStore{path: filepath.Join(dir, "push.json"), subs: map[string]pushSub{}}
	if data, err := os.ReadFile(st.path); err == nil {
		if json.Unmarshal(data, &st.subs) != nil || st.subs == nil {
			st.subs = map[string]pushSub{}
		}
	}
	return st
}

func (st *pushStore) saveLocked() error {
	data, err := json.Marshal(st.subs)
	if err != nil {
		return err
	}
	return atomicWriteFile(st.path, data, 0o600)
}

func (st *pushStore) set(device string, s pushSub) error {
	if st == nil {
		return errors.New("push: no store")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.subs[device] = s
	return st.saveLocked()
}

// remove forgets device's subscription. With endpoint set, only if it is
// still that one: the phone may have sent a new one meanwhile.
func (st *pushStore) remove(device, endpoint string) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	cur, ok := st.subs[device]
	if !ok || (endpoint != "" && cur.Endpoint != endpoint) {
		return
	}
	delete(st.subs, device)
	_ = st.saveLocked()
}

func (st *pushStore) clear() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.subs = map[string]pushSub{}
	_ = st.saveLocked()
}

func (st *pushStore) all() map[string]pushSub {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]pushSub, len(st.subs))
	for k, v := range st.subs {
		out[k] = v
	}
	return out
}

// PushSubMsg is what a phone sends once authenticated: its subscription, or
// an empty Endpoint to stop.
type PushSubMsg struct {
	Endpoint  string   `json:"endpoint"`
	P256DH    b64Bytes `json:"p256dh"`
	Auth      b64Bytes `json:"auth"`
	VapidPub  b64Bytes `json:"vapid_pub"`
	VapidPriv b64Bytes `json:"vapid_priv"`
}

// applePushHost accepts Apple's push service only: the daemon posts to this
// URL, and it should not be a way to reach anything else from the machine.
func applePushHost(host string) bool {
	return host == "web.push.apple.com" || strings.HasSuffix(host, ".push.apple.com")
}

// checkSub validates a subscription before anything is kept.
func (r *Remote) checkSub(m PushSubMsg) (pushSub, error) {
	u, err := url.Parse(m.Endpoint)
	if err != nil || len(m.Endpoint) > maxEndpoint || u.Scheme != "https" || u.User != nil || !r.pushHostOK(u.Hostname()) {
		return pushSub{}, errors.New("push: bad endpoint")
	}
	if _, err := ecdh.P256().NewPublicKey(m.P256DH); err != nil {
		return pushSub{}, errors.New("push: bad key")
	}
	if len(m.Auth) != 16 {
		return pushSub{}, errors.New("push: bad secret")
	}
	priv, err := ecdh.P256().NewPrivateKey(m.VapidPriv)
	if err != nil || !bytes.Equal(priv.PublicKey().Bytes(), m.VapidPub) {
		return pushSub{}, errors.New("push: bad vapid key")
	}
	return pushSub{Endpoint: m.Endpoint, P256DH: m.P256DH, Auth: m.Auth, VapidPub: m.VapidPub, VapidPriv: m.VapidPriv}, nil
}

// setPush records what an authenticated phone sent about its notifications.
func (r *Remote) setPush(device string, m PushSubMsg) error {
	if m.Endpoint == "" {
		r.push.remove(device, "")
		return nil
	}
	s, err := r.checkSub(m)
	if err != nil {
		return err
	}
	return r.push.set(device, s)
}

// encryptPush encrypts plain for the subscription keys, as RFC 8291 says
// (aes128gcm, one record).
func encryptPush(plain, p256dh, auth []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(p256dh)
	if err != nil {
		return nil, err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	ikm, err := hkdf.Key(sha256.New, shared, auth, "WebPush: info\x00"+string(p256dh)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 86+len(plain)+17)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	// 0x02: the delimiter of the last record.
	return gcm.Seal(out, nonce, append(append([]byte(nil), plain...), 2), nil), nil
}

// vapidAuth is the Authorization header for endpoint (RFC 8292).
func vapidAuth(endpoint string, pub, priv []byte, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), priv)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": pushSubject,
	})
	if err != nil {
		return "", err
	}
	signed := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sr, ss, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	sr.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	return "vapid t=" + signed + "." + enc.EncodeToString(sig) + ", k=" + enc.EncodeToString(pub), nil
}

// pushPayload is what the service worker receives (sw.js).
type pushPayload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Key    string `json:"key"`    // the session
	Device string `json:"device"` // the id this machine gave the phone: which machine to open
}

// pushBody says which terminal waits, and for what.
func pushBody(s SessionInfo) string {
	name := s.Name
	if name == "" {
		name = "A terminal"
	}
	kind := map[string]string{"permission": "needs a permission", "question": "has a question", "auth": "needs a login"}[s.Agent.Kind]
	if kind == "" {
		kind = "is waiting"
	}
	body := name + " " + kind
	if s.Agent.Msg != "" {
		body += ": " + s.Agent.Msg
	}
	if rs := []rune(body); len(rs) > maxPushBody {
		body = string(rs[:maxPushBody-1]) + "…"
	}
	return body
}

// sendPush posts one notification. A subscription the service says is gone
// (the phone turned it off, removed the app) is forgotten.
func (r *Remote) sendPush(ctx context.Context, device string, sub pushSub, s SessionInfo) error {
	plain, err := json.Marshal(pushPayload{Title: r.name, Body: pushBody(s), Key: s.Key, Device: device})
	if err != nil {
		return err
	}
	body, err := encryptPush(plain, sub.P256DH, sub.Auth)
	if err != nil {
		return err
	}
	auth, err := vapidAuth(sub.Endpoint, sub.VapidPub, sub.VapidPriv, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	h := req.Header
	h.Set("Authorization", auth)
	h.Set("Content-Encoding", "aes128gcm")
	h.Set("Content-Type", "application/octet-stream")
	h.Set("TTL", fmt.Sprint(int(pushTTL.Seconds())))
	h.Set("Urgency", "high")
	// A newer notification for the same terminal replaces an undelivered one.
	topic := sha256.Sum256([]byte(s.Key))
	h.Set("Topic", base64.RawURLEncoding.EncodeToString(topic[:])[:32])
	resp, err := r.pushClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		r.push.remove(device, sub.Endpoint)
		return fmt.Errorf("subscription gone (%d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("push service answered %d", resp.StatusCode)
	}
	return nil
}

// pushScan remembers which agents are waiting, by key and since when.
type pushScan struct {
	seen    map[string]time.Time
	started bool
}

// desktopHost is what a host may add: someone is at the machine, so an agent
// that starts waiting will be seen there.
type desktopHost interface {
	DesktopActive() bool
}

// scanPush finds the agents that started waiting since the last scan and
// notifies the phones that subscribed. The first scan only takes note: what
// was already waiting when remote control turned on is not news. A phone with
// a connection open has the app on screen: it sees it there.
func (r *Remote) scanPush(ctx context.Context, sc *pushScan) {
	cur := map[string]time.Time{}
	var fresh []SessionInfo
	for _, s := range r.host.Sessions() {
		if s.Agent == nil || s.Agent.State != "blocked" {
			continue
		}
		cur[s.Key] = s.Agent.Since
		if prev, ok := sc.seen[s.Key]; (!ok || !prev.Equal(s.Agent.Since)) && sc.started {
			fresh = append(fresh, s)
		}
	}
	sc.seen, sc.started = cur, true
	if len(fresh) == 0 {
		return
	}
	if d, ok := r.host.(desktopHost); ok && d.DesktopActive() {
		return
	}
	subs := r.push.all()
	if len(subs) == 0 {
		return
	}
	connected := map[string]bool{}
	r.liveMu.Lock()
	for lc := range r.live {
		if lc.authed {
			connected[lc.deviceID] = true
		}
	}
	r.liveMu.Unlock()
	for device, sub := range subs {
		if connected[device] {
			continue
		}
		name, known := r.deviceName(device)
		if !known {
			r.push.remove(device, "")
			continue
		}
		for _, s := range fresh {
			go func() {
				if err := r.sendPush(ctx, device, sub, s); err != nil && ctx.Err() == nil {
					r.setErr("push to " + name + ": " + logField(err.Error()))
				}
			}()
		}
	}
}

// pushLoop runs for as long as one Enable lasts.
func (r *Remote) pushLoop(s *serving) {
	t := time.NewTicker(r.pushEvery)
	defer t.Stop()
	sc := &pushScan{}
	r.scanPush(s.ctx, sc)
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			r.scanPush(s.ctx, sc)
		}
	}
}
