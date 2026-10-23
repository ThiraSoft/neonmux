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

	pairMu    sync.Mutex
	pairing   *Pairing
	pairingCh chan bool
}

func (r *Remote) openPairing(now time.Time) *Pairing {
	r.pairMu.Lock()
	defer r.pairMu.Unlock()
	r.pairing = NewPairing(now)
	r.pairingCh = make(chan bool, 1)
	return r.pairing
}

func (r *Remote) decidePairing(accept bool) {
	r.pairMu.Lock()
	ch := r.pairingCh
	r.pairMu.Unlock()
	if ch != nil {
		select {
		case ch <- accept:
		default:
		}
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
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			return true
		}
	}
	return false
}

func encodeText(text string, enter bool, modes vt.Mode) []byte {
	var payload string
	if strings.Contains(text, "\n") {
		if modes&vt.ModeBracketedPaste != 0 {
			payload = "\x1b[200~" + text + "\x1b[201~"
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

type clientCmd struct {
	typ     uint8
	payload []byte
}

func (r *Remote) serveConn(ctx context.Context, rw io.ReadWriteCloser, origin string, peer netip.Addr) {
	defer rw.Close()

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

	authTimer := time.AfterFunc(60*time.Second, func() {
		_ = rw.Close()
	})
	defer authTimer.Stop()

	// 1. Send Challenge right away
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	if err := writeFrame(rw, wChallenge, ChallengeMsg{
		Proto: 1,
		Nonce: nonce,
		Name:  r.name,
	}); err != nil {
		return
	}

	// 2. Read initial message from client
	typ, payload, err := readFrame(rw)
	if err != nil {
		return
	}

	if typ == wHello {
		var h HelloMsg
		if err := json.Unmarshal(payload, &h); err != nil || h.Proto != 1 {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "proto", Min: 1, Max: 1})
			return
		}
		typ, payload, err = readFrame(rw)
		if err != nil {
			return
		}
	}

	now := time.Now()
	switch typ {
	case wAuth:
		var authMsg AuthMsg
		if err := json.Unmarshal(payload, &authMsg); err != nil {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		dev, ok := r.devices.ByCredID(authMsg.Assertion.CredID)
		if !ok || dev.Origin != origin {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if err := VerifyAssertion(authMsg.Assertion.toAssertion(), dev.PubKey, origin, nonce); err != nil {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
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
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		devID, err := r.resumes.Verify(resumeMsg.ID, nonce, resumeMsg.Sig, now)
		if err != nil {
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
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
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
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		r.pairMu.Lock()
		p := r.pairing
		ch := r.pairingCh
		r.pairMu.Unlock()
		if p == nil {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		if err := p.Use(pairMsg.Token, now); err != nil {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		cert := r.certs.Current(now)
		if len(cert.Certificate) == 0 {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		certHash := sha256.Sum256(cert.Certificate[0])
		code := SASCode(pairMsg.Token, certHash, pairMsg.Reg.PubKey)
		if err := writeFrame(rw, wPairCode, PairCodeMsg{Code: code}); err != nil {
			return
		}

		select {
		case accept := <-ch:
			if !accept {
				_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
				return
			}
		case <-ctx.Done():
			return
		}

		reg := pairMsg.Reg.toRegistration()
		if err := VerifyRegistration(reg, origin, nonce); err != nil {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}

		var rawID [16]byte
		if _, err := rand.Read(rawID[:]); err != nil {
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}
		newDevID := hex.EncodeToString(rawID[:])

		name := reg.Name
		if name == "" {
			name = "Mobile Phone"
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
			_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
			return
		}

		resumeID := r.resumes.Issue(dev.ID, pairMsg.SessionPub, now)
		r.resumes.Connected(dev.ID)
		authedDeviceID = dev.ID

		r.pairMu.Lock()
		r.pairing = nil
		r.pairingCh = nil
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
		_ = writeFrame(rw, wError, ErrorMsg{Msg: "auth"})
		return
	}

	// Auth succeeded: cancel auth timeout
	authTimer.Stop()

	watcher := &connWatcher{
		changeCh: make(chan struct{}, 1),
		exitCh:   make(chan struct{}, 1),
	}

	cmdCh := make(chan clientCmd, 32)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer rw.Close()
		for {
			cTyp, cPayload, cErr := readFrame(rw)
			if cErr != nil {
				return
			}
			select {
			case cmdCh <- clientCmd{typ: cTyp, payload: cPayload}:
			case <-ctx.Done():
				return
			}
		}
	}()

	var (
		watchedKey    string
		scrollOffset  int
		lastHashes    []uint64
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

		case <-readerDone:
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
			if fullFrame || len(lastHashes) != rows {
				lines := make([]FrameLine, rows)
				newHashes := make([]uint64, rows)
				for y := 0; y < rows; y++ {
					lines[y] = FrameLine{Y: y, Runs: screen.Lines[y]}
					newHashes[y] = LineHash(screen.Lines[y])
				}
				lastHashes = newHashes
				fullFrame = false
				_ = writeFrame(rw, wFrame, FrameMsg{
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
				lastFrameTime = time.Now()
			} else {
				var diffLines []FrameLine
				for y := 0; y < rows; y++ {
					h := LineHash(screen.Lines[y])
					if h != lastHashes[y] {
						diffLines = append(diffLines, FrameLine{Y: y, Runs: screen.Lines[y]})
						lastHashes[y] = h
					}
				}
				_ = writeFrame(rw, wFrame, FrameMsg{
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
				lastFrameTime = time.Now()
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

		case cmd := <-cmdCh:
			switch cmd.typ {
			case wList:
				curSessions := r.host.Sessions()
				lastSessions = curSessions
				_ = writeFrame(rw, wSessions, SessionsMsg{List: curSessions})

			case wWatch:
				var wm WatchMsg
				if err := json.Unmarshal(cmd.payload, &wm); err != nil {
					_ = writeFrame(rw, wError, ErrorMsg{Msg: "invalid watch payload"})
					continue
				}
				if unwatchFunc != nil {
					unwatchFunc()
					unwatchFunc = nil
				}
				unwatch, ok := r.host.Watch(wm.Key, watcher)
				if !ok {
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
					cols, rows := t.Size()
					watcher.setSize(cols, rows)
					screen := Capture(t, 0, lastPal)
					rows = screen.Rows
					lines := make([]FrameLine, rows)
					lastHashes = make([]uint64, rows)
					for y := 0; y < rows; y++ {
						lines[y] = FrameLine{Y: y, Runs: screen.Lines[y]}
						lastHashes[y] = LineHash(screen.Lines[y])
					}
					fullFrame = false
					_ = writeFrame(rw, wFrame, FrameMsg{
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
					lastFrameTime = time.Now()
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
				if err := json.Unmarshal(cmd.payload, &sm); err != nil {
					continue
				}
				watcher.setSize(sm.Cols, sm.Rows)
				if watchedKey != "" {
					r.host.SetSize(watchedKey, watcher)
					fullFrame = true
					triggerFrame()
				}

			case wScroll:
				var sc ScrollMsg
				if err := json.Unmarshal(cmd.payload, &sc); err != nil {
					continue
				}
				scrollOffset = sc.Offset
				fullFrame = true
				triggerFrame()

			case wText:
				var tm TextMsg
				if err := json.Unmarshal(cmd.payload, &tm); err != nil {
					continue
				}
				if watchedKey != "" {
					t := r.host.Terminal(watchedKey)
					if t != nil {
						b := encodeText(tm.Text, tm.Enter, t.Modes())
						scrollOffset = 0
						r.host.Input(watchedKey, watcher, b)
					}
				}

			case wKeys:
				var km KeysMsg
				if err := json.Unmarshal(cmd.payload, &km); err != nil {
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
						scrollOffset = 0
						r.host.Input(watchedKey, watcher, encoded)
					}
				}
			}
		}
	}
}
