package remote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Config struct {
	Dir      string        // <StateDir>/remote
	Name     string        // machine name shown on the phone (hostname)
	Port     int           // UDP, default 8007
	PagePort int           // 127.0.0.1, default 8006
	IdleOff  time.Duration // 0: never
	Version  string        // neonmux version, served in /version.json

	// Tests only: loopback counts as an eligible address, and the address
	// lister can be replaced. Nothing outside this package can set them.
	allowLoopback bool
	listAddrs     func() []netip.Addr

	// How long a QUIC connection may stay open without a WebTransport
	// session (10 s; shorter in tests).
	connectTimeout time.Duration
}

type State struct {
	Enabled   bool
	Devices   []Device
	Connected []string      // device names
	Pairing   *PairingState `json:",omitempty"`
	Addrs     []string      // where it listens
	Err       string        `json:",omitempty"`
}

type PairingState struct {
	Payload, Code string
	Expires       time.Time
}

// life is the lifecycle part of Remote: sockets, page server, live
// connections, limits, log.
type life struct {
	cfg   Config
	limit *limiter

	// lifeMu serializes Enable, Disable, Close and rescans.
	lifeMu sync.Mutex

	mu        sync.Mutex // guards the fields below
	enabled   bool
	srv       *serving
	port      int              // UDP port actually bound (Config.Port may be 0)
	addrs     []netip.AddrPort // sockets open now
	errMsg    string
	idleTimer *time.Timer
	pairTimer *time.Timer
	onChange  []func()

	liveMu     sync.Mutex
	live       map[*liveConn]struct{}
	liveClosed bool
	liveWG     sync.WaitGroup

	logMu    sync.Mutex
	logCount int // lines in Dir/log, -1 until counted
}

// liveConn is one WebTransport session running serveConn.
type liveConn struct {
	peer     netip.Addr
	cancel   context.CancelFunc
	deviceID string // set once authenticated
	authed   bool
	pre      bool // holds a pre-authentication slot
}

type liveKey struct{}

// qrPayload is the JSON inside the pairing QR code.
type qrPayload struct {
	Name  string   `json:"n"`
	Addrs []string `json:"a"`
	Port  int      `json:"p"`
	Hash  b64Bytes `json:"h"`
	Token b64Bytes `json:"t"`
}

// New loads the identity and paired devices from cfg.Dir. Nothing listens
// until Enable.
func New(cfg Config, host Host) (*Remote, error) {
	if cfg.Dir == "" {
		return nil, errors.New("remote: empty state directory")
	}
	if err := ensureDir(cfg.Dir); err != nil {
		return nil, err
	}
	certs, err := LoadCerts(cfg.Dir, time.Now())
	if err != nil {
		return nil, err
	}
	devices, err := LoadDevices(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if cfg.listAddrs == nil {
		cfg.listAddrs = interfaceAddrs
	}
	if cfg.connectTimeout == 0 {
		cfg.connectTimeout = 10 * time.Second
	}
	r := &Remote{
		certs:   certs,
		devices: devices,
		resumes: &Resumes{},
		name:    cfg.Name,
		host:    host,
	}
	r.cfg = cfg
	r.limit = newLimiter(time.Now)
	r.live = make(map[*liveConn]struct{})
	r.logCount = -1
	return r, nil
}

// OnChange registers f, called after every change of State. f runs on the
// goroutine that made the change and must not call Enable, Disable, Close
// or StartPairing synchronously.
func (r *Remote) OnChange(f func()) {
	r.mu.Lock()
	r.onChange = append(r.onChange, f)
	r.mu.Unlock()
}

func (r *Remote) notify() {
	r.mu.Lock()
	fs := slices.Clone(r.onChange)
	r.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// Enable starts the page server and the WebTransport sockets and writes
// Dir/active. It succeeds even when no eligible address exists yet: the
// periodic rescan opens sockets when one appears.
func (r *Remote) Enable() error {
	r.lifeMu.Lock()
	err := r.enableLocked()
	r.lifeMu.Unlock()
	r.notify()
	return err
}

func (r *Remote) enableLocked() error {
	r.mu.Lock()
	enabled := r.enabled
	r.mu.Unlock()
	if enabled {
		return nil
	}
	page, err := startPage(r.cfg.PagePort, r.cfg.Version)
	if err != nil {
		r.setErr("page server: " + err.Error())
		return err
	}
	if err := atomicWriteFile(filepath.Join(r.cfg.Dir, "active"), []byte("1\n"), 0o600); err != nil {
		_ = page.Close()
		r.setErr(err.Error())
		return err
	}
	s := r.newServing()
	s.page = page

	r.liveMu.Lock()
	r.liveClosed = false
	r.liveMu.Unlock()

	r.mu.Lock()
	r.enabled = true
	r.srv = s
	r.port = r.cfg.Port
	r.errMsg = ""
	if r.cfg.IdleOff > 0 {
		r.idleTimer = time.AfterFunc(r.cfg.IdleOff, func() { r.idleFired(s) })
	}
	r.mu.Unlock()

	r.rescanLocked()
	go r.rescanLoop(s)
	return nil
}

func (r *Remote) setErr(msg string) {
	r.mu.Lock()
	r.errMsg = msg
	r.mu.Unlock()
}

// Disable stops listening, cuts every connection, forgets resume keys,
// closes an open pairing and removes Dir/active.
func (r *Remote) Disable() { r.shutdown(true) }

// Close is Disable but keeps Dir/active, so a daemon started by a hot
// restart enables the server again. Returns whether it was enabled.
func (r *Remote) Close() bool { return r.shutdown(false) }

func (r *Remote) shutdown(removeActive bool) bool {
	r.lifeMu.Lock()
	was := r.shutdownLocked(removeActive)
	r.lifeMu.Unlock()
	if was {
		r.notify()
	}
	return was
}

func (r *Remote) shutdownLocked(removeActive bool) bool {
	r.mu.Lock()
	s := r.srv
	was := r.enabled
	r.srv = nil
	r.enabled = false
	r.addrs = nil
	if r.idleTimer != nil {
		r.idleTimer.Stop()
		r.idleTimer = nil
	}
	r.mu.Unlock()

	if removeActive {
		_ = os.Remove(filepath.Join(r.cfg.Dir, "active"))
	}
	r.closePairing()
	if s != nil {
		r.liveMu.Lock()
		r.liveClosed = true
		r.liveMu.Unlock()
		s.close()
		// Every serveConn has returned and unwatched its session.
		r.liveWG.Wait()
	}
	r.resumes.Clear()
	r.nonces.clear()
	return was
}

// idleFired turns the server off when nobody is connected, else waits for
// another idle period.
func (r *Remote) idleFired(s *serving) {
	r.lifeMu.Lock()
	r.mu.Lock()
	current := r.enabled && r.srv == s
	r.mu.Unlock()
	was := false
	switch {
	case !current:
	case r.limit.authedCount() > 0:
		r.activity() // a phone is connected: check again later
	default:
		was = r.shutdownLocked(true)
	}
	r.lifeMu.Unlock()
	if was {
		r.notify()
	}
}

// activity restarts the idle-off timer.
func (r *Remote) activity() {
	r.mu.Lock()
	if r.idleTimer != nil {
		r.idleTimer.Reset(r.cfg.IdleOff)
	}
	r.mu.Unlock()
}

// StartPairing opens a pairing (enabling the server if needed) and returns
// the QR payload. A previous pairing is replaced.
func (r *Remote) StartPairing() (PairingState, error) {
	if err := r.Enable(); err != nil {
		return PairingState{}, err
	}
	now := time.Now()
	p := r.openPairing(now)
	payload, err := r.qrPayload(p.Token(), now)
	if err != nil {
		r.closePairing()
		return PairingState{}, err
	}
	r.pairMu.Lock()
	r.pairCode = ""
	r.pairPayload = payload
	if r.pairTimer != nil {
		r.pairTimer.Stop()
	}
	// Tell the window when the pairing expires so it closes the QR.
	r.pairTimer = time.AfterFunc(time.Until(p.Expires())+10*time.Millisecond, r.notify)
	r.pairMu.Unlock()
	r.activity()
	r.notify()
	return PairingState{Payload: payload, Expires: p.Expires()}, nil
}

func (r *Remote) qrPayload(token []byte, now time.Time) (string, error) {
	cert := r.certs.Current(now)
	if len(cert.Certificate) == 0 {
		return "", errors.New("remote: no valid certificate")
	}
	hash := sha256.Sum256(cert.Certificate[0])
	r.mu.Lock()
	ips := make([]string, 0, len(r.addrs))
	for _, ap := range r.addrs {
		ips = append(ips, ap.Addr().String())
	}
	port := r.port
	r.mu.Unlock()
	b, err := json.Marshal(qrPayload{Name: r.name, Addrs: ips, Port: port, Hash: hash[:], Token: token})
	if err != nil {
		return "", err
	}
	return "nmx1:" + base64.RawURLEncoding.EncodeToString(b), nil
}

// ConfirmPairing answers the code comparison. ok false also cancels a
// pairing no phone has used yet.
func (r *Remote) ConfirmPairing(ok bool) {
	r.decidePairing(ok)
	if !ok {
		r.closePairing()
	}
	r.notify()
}

func (r *Remote) closePairing() {
	r.pairMu.Lock()
	r.pairing = nil
	r.pairWait = false
	r.pairDecide = false
	r.pairingCh = nil
	r.pairCode = ""
	r.pairPayload = ""
	if r.pairTimer != nil {
		r.pairTimer.Stop()
		r.pairTimer = nil
	}
	r.pairMu.Unlock()
}

// pairingOpen reports whether a pairing token can still be presented.
func (r *Remote) pairingOpen(now time.Time) bool {
	r.pairMu.Lock()
	p := r.pairing
	r.pairMu.Unlock()
	return p != nil && now.Before(p.Expires()) && !p.Used()
}

// Revoke forgets a device and cuts its connections now.
func (r *Remote) Revoke(deviceID string) error {
	if err := r.devices.Remove(deviceID); err != nil {
		return err
	}
	r.resumes.Revoke(deviceID)
	r.cutConns(func(lc *liveConn) bool { return lc.deviceID == deviceID })
	r.notify()
	return nil
}

// Regenerate replaces the certificate series and unpairs every phone.
func (r *Remote) Regenerate() error {
	if err := r.certs.Regenerate(time.Now()); err != nil {
		return err
	}
	err := r.devices.Clear()
	r.resumes.Clear()
	r.nonces.clear()
	r.closePairing()
	r.cutConns(func(*liveConn) bool { return true })
	r.notify()
	return err
}

func (r *Remote) cutConns(match func(*liveConn) bool) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	for lc := range r.live {
		if match(lc) {
			lc.cancel()
		}
	}
}

func (r *Remote) State() State {
	now := time.Now()
	r.mu.Lock()
	st := State{Enabled: r.enabled, Err: r.errMsg}
	for _, ap := range r.addrs {
		st.Addrs = append(st.Addrs, ap.String())
	}
	r.mu.Unlock()

	st.Devices = r.devices.List()
	names := map[string]string{}
	for _, d := range st.Devices {
		names[d.ID] = d.Name
	}
	r.liveMu.Lock()
	for lc := range r.live {
		if lc.authed {
			if n, ok := names[lc.deviceID]; ok && !slices.Contains(st.Connected, n) {
				st.Connected = append(st.Connected, n)
			}
		}
	}
	r.liveMu.Unlock()
	slices.Sort(st.Connected)

	r.pairMu.Lock()
	p, code, payload := r.pairing, r.pairCode, r.pairPayload
	shown := r.pairWait || r.pairDecide // the code stays until serveConn ends the pairing
	r.pairMu.Unlock()
	if p != nil && payload != "" && now.Before(p.Expires()) {
		switch {
		case shown && code != "":
			st.Pairing = &PairingState{Payload: payload, Code: code, Expires: p.Expires()}
		case !p.Used():
			st.Pairing = &PairingState{Payload: payload, Expires: p.Expires()}
		}
	}
	return st
}

// markAuthed is called by serveConn once a phone has proven who it is,
// before anything else of the protocol. It enforces the cap on
// authenticated connections and records the device of the connection so
// Revoke can cut it.
func (r *Remote) markAuthed(ctx context.Context, deviceID string) connOutcome {
	lc, _ := ctx.Value(liveKey{}).(*liveConn)
	if lc == nil {
		return outcomeOK // connection not run by the server (tests of serveConn)
	}
	// A phone opening the machine again is the same device, proven again:
	// its older connections make way (Safari closes a session it left
	// only about a minute later). maxAuthed still bounds the phones.
	r.replaceConns(deviceID, lc)
	if !r.limit.startAuthed() {
		return outcomeBusy
	}
	r.liveMu.Lock()
	lc.deviceID = deviceID
	lc.authed = true
	pre := lc.pre
	lc.pre = false
	r.liveMu.Unlock()
	if pre {
		r.limit.endPre(lc.peer)
	}

	// A Revoke between the credential check and here must still win.
	name, ok := r.deviceName(deviceID)
	if !ok {
		return outcomeRefused
	}
	r.logLine(name, lc.peer, "ok")
	r.activity()
	r.notify()
	return outcomeOK
}

// replaceConns cuts the other connections of deviceID and waits, a short
// while at most, for them to release their slots.
func (r *Remote) replaceConns(deviceID string, self *liveConn) {
	var old []*liveConn
	r.liveMu.Lock()
	for lc := range r.live {
		if lc != self && lc.authed && lc.deviceID == deviceID {
			old = append(old, lc)
		}
	}
	r.liveMu.Unlock()
	if len(old) == 0 {
		return
	}
	for _, lc := range old {
		lc.cancel()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.liveMu.Lock()
		left := 0
		for _, lc := range old {
			if _, ok := r.live[lc]; ok {
				left++
			}
		}
		r.liveMu.Unlock()
		if left == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *Remote) deviceName(id string) (string, bool) {
	for _, d := range r.devices.List() {
		if d.ID == id {
			return d.Name, true
		}
	}
	return "", false
}

func errorText(o connOutcome) string {
	if o == outcomeBusy {
		return "busy"
	}
	return "auth"
}

// endLive releases what a connection held and records how it ended.
// cut is true when the server or a revocation ended it.
func (r *Remote) endLive(lc *liveConn, res connResult, cut bool) {
	r.liveMu.Lock()
	delete(r.live, lc)
	pre, authed := lc.pre, lc.authed
	lc.pre, lc.authed = false, false
	r.liveMu.Unlock()
	if pre {
		r.limit.endPre(lc.peer)
	}
	if authed {
		r.limit.endAuthed()
		r.activity()
		r.notify()
	}
	switch {
	case authed:
	case res.outcome == outcomeBusy:
		if r.limit.shouldLog(lc.peer, "ratelimited busy") {
			r.logLine("?", lc.peer, "ratelimited busy")
		}
	case res.outcome == outcomeRefused && !cut:
		r.limit.fail(lc.peer)
		outcome := "refused"
		if res.reason != "" {
			outcome += " " + res.reason
		}
		r.logLine("?", lc.peer, outcome)
	}
}

// Log returns the last n lines of the connection log.
func (r *Remote) Log(n int) []string {
	if n <= 0 {
		return nil
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	lines := r.readLogLocked()
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (r *Remote) readLogLocked() []string {
	data, err := os.ReadFile(filepath.Join(r.cfg.Dir, "log"))
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// logLine appends "time device source outcome" to Dir/log (a refused
// outcome carries its reason after it: "refused timeout"), keeping the
// last 1000 lines once it passes 2000. Never log secrets here.
func (r *Remote) logLine(device string, peer netip.Addr, outcome string) {
	line := time.Now().Format(time.RFC3339) + " " + logField(device) + " " + peer.String() + " " + outcome + "\n"
	r.logMu.Lock()
	defer r.logMu.Unlock()
	path := filepath.Join(r.cfg.Dir, "log")
	if r.logCount < 0 {
		r.logCount = len(r.readLogLocked())
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, err = f.WriteString(line)
	_ = f.Close()
	if err != nil {
		return
	}
	r.logCount++
	if r.logCount > 2000 {
		lines := r.readLogLocked()
		if len(lines) > 1000 {
			lines = lines[len(lines)-1000:]
		}
		if atomicWriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600) == nil {
			r.logCount = len(lines)
		}
	}
}

// logField keeps a device name (chosen on the phone) on one field of one
// line.
func logField(s string) string {
	s = strings.Map(func(c rune) rune {
		if unicode.IsSpace(c) || !unicode.IsPrint(c) {
			return '_'
		}
		return c
	}, s)
	if s == "" {
		return "?"
	}
	return s
}
