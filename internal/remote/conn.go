package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThiraSoft/neonmux/internal/keys"
	"github.com/ThiraSoft/neonmux/vt"
)

type Remote struct {
	certs   *Certs
	devices *Devices
	resumes *Resumes
	name    string
	host    Host

	pairMu      sync.Mutex
	pairing     *Pairing
	pairWait    bool // a phone sent Pair, the desktop has not answered
	pairDecide  bool // the desktop answered, serveConn is carrying it out
	pairingCh   chan bool
	pairCode    string // SAS code once a phone sent Pair
	pairPayload string // QR payload of the open pairing

	pairDecided func() // tests: called once the desktop decided, before the outcome

	authTimeout time.Duration // handshake deadline, 60 s when zero (tests shorten it)
	nonces      nonceStore    // Challenge nonces still good for one signature

	life // lifecycle state, see remote.go
}

// connOutcome is how a connection handler ended its handshake.
type connOutcome int

const (
	outcomeRefused connOutcome = iota // ended before authentication
	outcomeOK                         // authenticated
	outcomeBusy                       // authenticated, but too many connections
)

type connResult struct {
	outcome  connOutcome
	deviceID string
	reason   string // why a refused handshake ended, for the log: timeout, auth, pair, proto, closed
}

func (r *Remote) openPairing(now time.Time) *Pairing {
	r.pairMu.Lock()
	defer r.pairMu.Unlock()
	r.pairing = NewPairing(now)
	r.pairWait = false
	r.pairDecide = false
	r.pairingCh = make(chan bool, 1)
	return r.pairing
}

func (r *Remote) decidePairing(accept bool) bool {
	r.pairMu.Lock()
	defer r.pairMu.Unlock()
	if !r.pairWait || r.pairingCh == nil {
		return false
	}
	r.pairWait = false
	r.pairDecide = true
	select {
	case r.pairingCh <- accept:
		return true
	default:
		return false
	}
}

type connWatcher struct {
	mu       sync.Mutex
	cols     int
	rows     int
	changeCh chan struct{}
	exitCh   chan struct{}
}

func (w *connWatcher) Changed() {
	select {
	case w.changeCh <- struct{}{}:
	default:
	}
}

func (w *connWatcher) Exited() {
	select {
	case w.exitCh <- struct{}{}:
	default:
	}
}

func (w *connWatcher) Size() (cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cols, w.rows
}

func (w *connWatcher) setSize(cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cols = cols
	w.rows = rows
}

var allowedKeyNames = map[string]bool{
	"escape":    true,
	"tab":       true,
	"ctrl+c":    true,
	"up":        true,
	"down":      true,
	"left":      true,
	"right":     true,
	"enter":     true,
	"backspace": true,
	"pageup":    true,
	"pagedown":  true,
	"home":      true,
	"end":       true,
}

func isValidKeyName(name string) bool {
	if allowedKeyNames[name] {
		return true
	}
	if strings.HasPrefix(name, "ctrl+") && len(name) == 6 {
		ch := name[5]
		if ch >= 'a' && ch <= 'z' {
			return true
		}
	}
	return false
}

func encodeText(text string, enter bool, modes vt.Mode) []byte {
	var payload string
	if strings.Contains(text, "\n") {
		if modes&vt.ModeBracketedPaste != 0 {
			clean := strings.ReplaceAll(text, "\x1b[200~", "")
			clean = strings.ReplaceAll(clean, "\x1b[201~", "")
			payload = "\x1b[200~" + clean + "\x1b[201~"
		} else {
			payload = strings.ReplaceAll(text, "\n", "\r")
		}
	} else {
		payload = text
	}
	if enter {
		payload += "\r"
	}
	return []byte(payload)
}

// preAuthDeadline is how long a connection may take to authenticate: 60 s,
// or, while a pairing is open, until it expires: creating the passkey on
// the phone can take more than a minute. The origin does not matter: every
// phone shares the PWA's origin, a machine paired again included.
func (r *Remote) preAuthDeadline(now time.Time) time.Duration {
	d := r.authTimeout
	if d == 0 {
		d = 60 * time.Second
	}
	r.pairMu.Lock()
	p := r.pairing
	r.pairMu.Unlock()
	if p == nil || p.Used() || !now.Before(p.Expires()) {
		return d
	}
	return p.Expires().Sub(now)
}

// serveConn runs the wire protocol on one stream until it ends. The result
// tells the caller whether the handshake failed (rate limiting, log).
func (r *Remote) serveConn(ctx context.Context, rw io.ReadWriteCloser, origin string, peer netip.Addr) (result connResult) {
	connDone := make(chan struct{})
	defer close(connDone)

	defer rw.Close()

	stopAfter := context.AfterFunc(ctx, func() {
		_ = rw.Close()
	})
	defer stopAfter()

	var authedDeviceID string
	var unwatchFunc func()

	defer func() {
		if unwatchFunc != nil {
			unwatchFunc()
			unwatchFunc = nil
		}
		if authedDeviceID != "" {
			r.resumes.Disconnected(authedDeviceID, time.Now())
		}
	}()

	// Why the handshake ended, if it fails. Never put secrets here: it goes
	// to the connection log.
	reason := "closed"
	var timedOut atomic.Bool
	defer func() {
		if result.outcome == outcomeRefused {
			result.reason = reason
			if timedOut.Load() {
				result.reason = "timeout"
			}
		}
	}()

	authTimer := time.AfterFunc(r.preAuthDeadline(time.Now()), func() {
		timedOut.Store(true)
		_ = rw.Close()
	})
	defer authTimer.Stop()

	type readRes struct {
		typ     uint8
		payload []byte
		err     error
	}
	frameCh := make(chan readRes, 2)
	go func() {
		defer close(frameCh)
		for {
			t, p, err := readFrame(rw)
			select {
			case frameCh <- readRes{typ: t, payload: p, err: err}:
				if err != nil {
					return
				}
			case <-connDone:
				return
			}
		}
	}()

	readNext := func() (readRes, bool) {
		select {
		case res, ok := <-frameCh:
			if !ok || res.err != nil {
				return res, false
			}
			return res, true
		case <-ctx.Done():
			return readRes{err: ctx.Err()}, false
		}
	}

	// 1. Send Challenge right away
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	r.nonces.add(nonce, time.Now())
	if err := writeFrame(rw, wChallenge, ChallengeMsg{
		Proto: wireProto,
		Nonce: nonce,
		Name:  r.name,
	}); err != nil {
		return
	}

	// 2. Read initial message from client
	first, ok := readNext()
	if !ok {
		return
	}
	typ, payload := first.typ, first.payload

	if typ == wHello {
		var h HelloMsg
		if err := json.Unmarshal(payload, &h); err != nil || h.Proto != 1 {
			reason = "proto"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "proto", Min: 1, Max: 1})
			return
		}
		first, ok = readNext()
		if !ok {
			return
		}
		typ, payload = first.typ, first.payload
	}

	now := time.Now()
	switch typ {
	case wAuth:
		var authMsg AuthMsg
		if err := json.Unmarshal(payload, &authMsg); err != nil {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		dev, ok := r.devices.ByCredID(authMsg.Assertion.CredID)
		if !ok || dev.Origin != origin {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		// The assertion may have been made on a connection that died
		// during Face ID: its nonce, unused, is accepted once.
		challenge, ok := r.claimNonce(challengeOf(authMsg.Assertion.ClientData), now)
		if !ok {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if err := VerifyAssertion(authMsg.Assertion.toAssertion(), dev.PubKey, origin, challenge); err != nil {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if o := r.markAuthed(ctx, dev.ID); o != outcomeOK {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: errorText(o)})
			return connResult{outcome: o, deviceID: dev.ID}
		}
		result = connResult{outcome: outcomeOK, deviceID: dev.ID}
		_ = r.devices.Touch(dev.ID, now)
		resumeID := r.resumes.Issue(dev.ID, authMsg.SessionPub, now)
		r.resumes.Connected(dev.ID)
		authedDeviceID = dev.ID

		hashes := r.certs.Hashes(now)
		if err := writeFrame(rw, wAuthed, AuthedMsg{
			DeviceID: dev.ID,
			ResumeID: resumeID,
			Hashes:   hashes,
		}); err != nil {
			return
		}

	case wResume:
		var resumeMsg ResumeMsg
		if err := json.Unmarshal(payload, &resumeMsg); err != nil {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		signed := []byte(resumeMsg.Nonce)
		if len(signed) == 0 {
			signed = nonce
		}
		challenge, ok := r.claimNonce(signed, now)
		if !ok {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		devID, err := r.resumes.Verify(resumeMsg.ID, challenge, resumeMsg.Sig, now)
		if err != nil {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		devs := r.devices.List()
		var foundDev *Device
		for i := range devs {
			if devs[i].ID == devID {
				foundDev = &devs[i]
				break
			}
		}
		if foundDev == nil || foundDev.Origin != origin {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if o := r.markAuthed(ctx, devID); o != outcomeOK {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: errorText(o)})
			return connResult{outcome: o, deviceID: devID}
		}
		result = connResult{outcome: outcomeOK, deviceID: devID}
		_ = r.devices.Touch(devID, now)
		r.resumes.Connected(devID)
		authedDeviceID = devID

		hashes := r.certs.Hashes(now)
		if err := writeFrame(rw, wAuthed, AuthedMsg{
			DeviceID: devID,
			ResumeID: resumeMsg.ID,
			Hashes:   hashes,
		}); err != nil {
			return
		}

	case wPair:
		var pairMsg PairMsg
		if err := json.Unmarshal(payload, &pairMsg); err != nil {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		r.pairMu.Lock()
		p := r.pairing
		r.pairMu.Unlock()
		if p == nil {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if err := p.Use(pairMsg.Token, now); err != nil {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		// The passkey may have been created on a connection that died
		// while the iOS sheet was up: its nonce, unused, is accepted once.
		regChallenge, ok := r.claimNonce(challengeOf(pairMsg.Reg.ClientData), now)
		if !ok {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		// A valid token: the desktop decision may take longer than the
		// handshake deadline, the pairing expiry bounds the wait below.
		authTimer.Stop()
		cert := r.certs.Current(now)
		if len(cert.Certificate) == 0 {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		certHash := sha256.Sum256(cert.Certificate[0])
		code := SASCode(pairMsg.Token, certHash, pairMsg.Reg.PubKey)

		r.pairMu.Lock()
		r.pairWait = true
		r.pairCode = code
		ch := r.pairingCh
		r.pairMu.Unlock()
		r.notify()

		defer func() {
			r.pairMu.Lock()
			r.pairWait = false
			r.pairDecide = false
			r.pairCode = ""
			r.pairMu.Unlock()
			r.notify()
		}()

		if err := writeFrame(rw, wPairCode, PairCodeMsg{Code: code}); err != nil {
			return
		}

		expiryDuration := time.Until(p.Expires())
		if expiryDuration < 0 {
			expiryDuration = 0
		}
		expiryTimer := time.NewTimer(expiryDuration)
		defer expiryTimer.Stop()

		var decision bool
		select {
		case accept := <-ch:
			decision = accept
			if r.pairDecided != nil {
				r.pairDecided()
			}
		case <-expiryTimer.C:
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		case <-frameCh:
			return
		case <-ctx.Done():
			return
		}

		if !decision {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}

		if !time.Now().Before(p.Expires()) {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}

		reg := pairMsg.Reg.toRegistration()
		if err := VerifyRegistration(reg, origin, regChallenge); err != nil {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}

		var rawID [16]byte
		if _, err := rand.Read(rawID[:]); err != nil {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		newDevID := hex.EncodeToString(rawID[:])

		name := strings.TrimSpace(reg.Name)
		runes := []rune(name)
		if len(runes) > 64 {
			name = string(runes[:64])
		}
		if name == "" {
			name = "phone"
		}

		dev := Device{
			ID:       newDevID,
			Name:     name,
			CredID:   reg.CredID,
			PubKey:   reg.PubKey,
			Origin:   origin,
			Added:    now,
			LastSeen: now,
		}
		if err := r.devices.Add(dev); err != nil {
			reason = "pair"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if o := r.markAuthed(ctx, dev.ID); o != outcomeOK {
			reason = "auth"
			_ = writeFrame(rw, wError, ErrorMsg{Msg: errorText(o)})
			return connResult{outcome: o, deviceID: dev.ID}
		}
		result = connResult{outcome: outcomeOK, deviceID: dev.ID}

		resumeID := r.resumes.Issue(dev.ID, pairMsg.SessionPub, now)
		r.resumes.Connected(dev.ID)
		authedDeviceID = dev.ID

		r.pairMu.Lock()
		r.pairing = nil
		r.pairWait = false
		r.pairDecide = false
		r.pairingCh = nil
		r.pairCode = ""
		r.pairPayload = ""
		r.pairMu.Unlock()

		hashes := r.certs.Hashes(now)
		if err := writeFrame(rw, wAuthed, AuthedMsg{
			DeviceID: dev.ID,
			ResumeID: resumeID,
			Hashes:   hashes,
		}); err != nil {
			return
		}

	default:
		reason = "auth"
		_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
		return
	}

	authTimer.Stop()

	watcher := &connWatcher{
		changeCh: make(chan struct{}, 1),
		exitCh:   make(chan struct{}, 1),
	}

	var (
		watchedKey    string
		scrollOffset  int
		lastHashes    []uint64
		lastCols      int
		lastRows      int
		lastPal       Palette = r.host.Palette()
		lastSessions  []SessionInfo
		fullFrame     bool
		lastFrameTime time.Time
		framePending  bool
		frameTimer    *time.Timer
		frameTimerCh  <-chan time.Time
	)

	triggerFrame := func() {
		if watchedKey == "" {
			return
		}
		framePending = true
		if frameTimer == nil {
			elapsed := time.Since(lastFrameTime)
			wait := 33*time.Millisecond - elapsed
			if wait < 0 {
				wait = 0
			}
			frameTimer = time.NewTimer(wait)
			frameTimerCh = frameTimer.C
		}
	}

	sessionTicker := time.NewTicker(1 * time.Second)
	defer sessionTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-watcher.exitCh:
			if unwatchFunc != nil {
				unwatchFunc()
				unwatchFunc = nil
			}
			watchedKey = ""
			lastHashes = nil
			framePending = false
			if frameTimer != nil {
				frameTimer.Stop()
				frameTimer = nil
				frameTimerCh = nil
			}
			_ = writeFrame(rw, wExited, struct{}{})

		case <-frameTimerCh:
			frameTimer = nil
			frameTimerCh = nil
			if !framePending || watchedKey == "" {
				framePending = false
				continue
			}
			framePending = false

			select {
			case <-watcher.changeCh:
			default:
			}

			t := r.host.Terminal(watchedKey)
			if t == nil {
				continue
			}

			curPal := r.host.Palette()
			if curPal != lastPal {
				lastPal = curPal
				_ = writeFrame(rw, wTheme, ThemeMsg{FG: lastPal.FG, BG: lastPal.BG, Cursor: lastPal.Cursor})
				fullFrame = true
			}

			screen := Capture(t, scrollOffset, lastPal)
			rows := screen.Rows
			cols := screen.Cols
			if fullFrame || len(lastHashes) != rows || lastCols != cols || lastRows != rows {
				lines := make([]FrameLine, rows)
				newHashes := make([]uint64, rows)
				for y := 0; y < rows; y++ {
					lines[y] = FrameLine{Y: y, Runs: screen.Lines[y]}
					newHashes[y] = LineHash(screen.Lines[y])
				}
				err := writeFrame(rw, wFrame, FrameMsg{
					Cols:   screen.Cols,
					Rows:   screen.Rows,
					Offset: screen.Offset,
					Hist:   screen.Hist,
					CX:     screen.CX,
					CY:     screen.CY,
					Cursor: screen.CursorVis,
					Full:   true,
					Lines:  lines,
				})
				if err != nil {
					fullFrame = true
				} else {
					lastHashes = newHashes
					lastCols = cols
					lastRows = rows
					fullFrame = false
					lastFrameTime = time.Now()
				}
			} else {
				type hashUpdate struct {
					y int
					h uint64
				}
				var diffLines []FrameLine
				var updates []hashUpdate
				for y := 0; y < rows; y++ {
					h := LineHash(screen.Lines[y])
					if h != lastHashes[y] {
						diffLines = append(diffLines, FrameLine{Y: y, Runs: screen.Lines[y]})
						updates = append(updates, hashUpdate{y: y, h: h})
					}
				}
				err := writeFrame(rw, wFrame, FrameMsg{
					Cols:   screen.Cols,
					Rows:   screen.Rows,
					Offset: screen.Offset,
					Hist:   screen.Hist,
					CX:     screen.CX,
					CY:     screen.CY,
					Cursor: screen.CursorVis,
					Full:   false,
					Lines:  diffLines,
				})
				if err != nil {
					fullFrame = true
				} else {
					for _, u := range updates {
						lastHashes[u.y] = u.h
					}
					lastCols = cols
					lastRows = rows
					lastFrameTime = time.Now()
				}
			}

		case <-watcher.changeCh:
			triggerFrame()

		case <-sessionTicker.C:
			curSessions := r.host.Sessions()
			if !reflect.DeepEqual(curSessions, lastSessions) {
				lastSessions = curSessions
				_ = writeFrame(rw, wSessions, SessionsMsg{List: curSessions})
			}
			curPal := r.host.Palette()
			if curPal != lastPal {
				lastPal = curPal
				_ = writeFrame(rw, wTheme, ThemeMsg{FG: lastPal.FG, BG: lastPal.BG, Cursor: lastPal.Cursor})
				fullFrame = true
				triggerFrame()
			}

		case res, ok := <-frameCh:
			if !ok || res.err != nil {
				return
			}
			r.activity()
			switch res.typ {
			case wList:
				curSessions := r.host.Sessions()
				lastSessions = curSessions
				_ = writeFrame(rw, wSessions, SessionsMsg{List: curSessions})

			case wWatch:
				var wm WatchMsg
				if err := json.Unmarshal(res.payload, &wm); err != nil {
					_ = writeFrame(rw, wError, ErrorMsg{Msg: "invalid watch payload"})
					continue
				}
				if unwatchFunc != nil {
					unwatchFunc()
					unwatchFunc = nil
				}
				select {
				case <-watcher.exitCh:
				default:
				}
				select {
				case <-watcher.changeCh:
				default:
				}
				unwatch, ok := r.host.Watch(wm.Key, watcher)
				if !ok {
					watchedKey = ""
					lastHashes = nil
					_ = writeFrame(rw, wError, ErrorMsg{Msg: "session not found"})
					continue
				}
				watchedKey = wm.Key
				unwatchFunc = unwatch
				scrollOffset = 0
				lastHashes = nil
				lastPal = r.host.Palette()
				_ = writeFrame(rw, wTheme, ThemeMsg{FG: lastPal.FG, BG: lastPal.BG, Cursor: lastPal.Cursor})
				fullFrame = true

				t := r.host.Terminal(watchedKey)
				if t != nil {
					// Selecting a terminal takes it, as an input does: at
					// the phone's size once it sent one (the Size after
					// Watch applies then), the session's size until then.
					if c, _ := watcher.Size(); c == 0 {
						t.Lock()
						watcher.setSize(t.Size())
						t.Unlock()
					}
					r.host.Take(watchedKey, watcher)
					screen := Capture(t, 0, lastPal)
					rows := screen.Rows
					lines := make([]FrameLine, rows)
					lastHashes = make([]uint64, rows)
					for y := 0; y < rows; y++ {
						lines[y] = FrameLine{Y: y, Runs: screen.Lines[y]}
						lastHashes[y] = LineHash(screen.Lines[y])
					}
					fullFrame = false
					err := writeFrame(rw, wFrame, FrameMsg{
						Cols:   screen.Cols,
						Rows:   screen.Rows,
						Offset: 0,
						Hist:   screen.Hist,
						CX:     screen.CX,
						CY:     screen.CY,
						Cursor: screen.CursorVis,
						Full:   true,
						Lines:  lines,
					})
					if err == nil {
						lastCols = screen.Cols
						lastRows = screen.Rows
						lastFrameTime = time.Now()
					}
				}

			case wUnwatch:
				if unwatchFunc != nil {
					unwatchFunc()
					unwatchFunc = nil
				}
				watchedKey = ""
				lastHashes = nil
				framePending = false
				if frameTimer != nil {
					frameTimer.Stop()
					frameTimer = nil
					frameTimerCh = nil
				}

			case wSize:
				var sm SizeMsg
				if err := json.Unmarshal(res.payload, &sm); err != nil {
					continue
				}
				if sm.Cols <= 0 || sm.Rows <= 0 {
					_ = writeFrame(rw, wError, ErrorMsg{Msg: "invalid size"})
					continue
				}
				cols := sm.Cols
				if cols < 2 {
					cols = 2
				} else if cols > 500 {
					cols = 500
				}
				rows := sm.Rows
				if rows < 1 {
					rows = 1
				} else if rows > 300 {
					rows = 300
				}
				watcher.setSize(cols, rows)
				if watchedKey != "" {
					r.host.SetSize(watchedKey, watcher)
					fullFrame = true
					triggerFrame()
				}

			case wScroll:
				var sc ScrollMsg
				if err := json.Unmarshal(res.payload, &sc); err != nil {
					continue
				}
				offset := sc.Offset
				if offset < 0 {
					offset = 0
				}
				if watchedKey != "" {
					t := r.host.Terminal(watchedKey)
					if t != nil {
						t.Lock()
						hist := t.HistoryLen()
						t.Unlock()
						if offset > hist {
							offset = hist
						}
					}
				}
				scrollOffset = offset
				fullFrame = true
				triggerFrame()

			case wText:
				var tm TextMsg
				if err := json.Unmarshal(res.payload, &tm); err != nil {
					continue
				}
				if watchedKey != "" {
					t := r.host.Terminal(watchedKey)
					if t != nil {
						b := encodeText(tm.Text, tm.Enter, t.Modes())
						if scrollOffset != 0 {
							scrollOffset = 0
							fullFrame = true
							triggerFrame()
						}
						r.host.Input(watchedKey, watcher, b)
					}
				}

			case wKeys:
				var km KeysMsg
				if err := json.Unmarshal(res.payload, &km); err != nil {
					continue
				}
				if len(km.Names) == 0 {
					continue
				}
				if watchedKey != "" {
					valid := true
					for _, name := range km.Names {
						if !isValidKeyName(name) {
							valid = false
							break
						}
					}
					if !valid {
						_ = writeFrame(rw, wError, ErrorMsg{Msg: "unknown key"})
						continue
					}
					t := r.host.Terminal(watchedKey)
					if t != nil {
						encoded, err := keys.EncodeTokens(km.Names, t.Modes())
						if err != nil {
							_ = writeFrame(rw, wError, ErrorMsg{Msg: err.Error()})
							continue
						}
						if scrollOffset != 0 {
							scrollOffset = 0
							fullFrame = true
							triggerFrame()
						}
						r.host.Input(watchedKey, watcher, encoded)
					}
				}
			}
		}
	}
}
