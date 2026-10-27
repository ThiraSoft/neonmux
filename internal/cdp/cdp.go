// Package cdp drives a headless Chromium-family browser (Chromium, Chrome,
// Brave, Edge, Vivaldi) through the Chrome DevTools Protocol.
//
// The browser is driven over --remote-debugging-pipe rather than a TCP
// port: file descriptors 3 and 4 of the child carry NUL-terminated JSON
// messages. No port is opened, so no other user of the machine can
// connect to it, and no websocket is needed.
package cdp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// callTimeout bounds a call that the browser never answers.
const callTimeout = 15 * time.Second

// ErrClosed is returned by calls made after the browser went away.
var ErrClosed = errors.New("browser closed")

// Handler receives the events of a session (or of the browser, for the
// root session). It runs on the reader goroutine: it must not block, and
// must not wait for a Call.
type Handler func(method string, params json.RawMessage)

// Browser is a running browser process.
type Browser struct {
	cmd  *exec.Cmd
	w    io.WriteCloser
	wmu  sync.Mutex // one message at a time on the pipe
	done chan struct{}

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan reply
	sessions map[string]Handler // by session id, "" for the browser
	err      error              // why the browser went away
}

type reply struct {
	result json.RawMessage
	err    error
}

// message is any message on the pipe: answer or event.
type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Launch starts the browser argv (binary then its own arguments) headless,
// with its profile in dataDir. onEvent receives the browser-level events
// (Target.*) and may be nil.
func Launch(argv []string, dataDir string, onEvent Handler) (*Browser, error) {
	if len(argv) == 0 {
		return nil, errors.New("no browser")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	// in: the browser reads it (fd 3). out: the browser writes it (fd 4).
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	args := append(argv[1:len(argv):len(argv)],
		"--headless=new",
		"--remote-debugging-pipe",
		"--user-data-dir="+dataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-default-apps",
		// navigator.webdriver stays false: sites (Cloudflare, YouTube)
		// treat a page that says it is automated as a bot.
		"--disable-blink-features=AutomationControlled",
		"about:blank",
	)
	cmd := exec.Command(argv[0], args...)
	cmd.ExtraFiles = []*os.File{inR, outW}
	cmd.SysProcAttr = sysProcAttr()
	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	// The child holds its own copies.
	inR.Close()
	outW.Close()
	b := &Browser{
		cmd:      cmd,
		w:        inW,
		done:     make(chan struct{}),
		pending:  map[int64]chan reply{},
		sessions: map[string]Handler{"": onEvent},
	}
	go b.read(outR)
	return b, nil
}

// Done is closed when the browser went away.
func (b *Browser) Done() <-chan struct{} { return b.done }

// Err says why the browser went away.
func (b *Browser) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func (b *Browser) read(r io.ReadCloser) {
	defer r.Close()
	br := bufio.NewReaderSize(r, 1<<16)
	var err error
	for {
		var data []byte
		data, err = br.ReadBytes(0)
		if err != nil {
			break
		}
		var m message
		if json.Unmarshal(data[:len(data)-1], &m) != nil {
			continue
		}
		b.dispatch(&m)
	}
	waitErr := b.cmd.Wait()
	b.mu.Lock()
	if waitErr != nil {
		b.err = fmt.Errorf("browser exited: %w", waitErr)
	} else {
		b.err = ErrClosed
	}
	pending := b.pending
	b.pending = map[int64]chan reply{}
	b.mu.Unlock()
	for _, ch := range pending {
		ch <- reply{err: ErrClosed}
	}
	close(b.done)
}

func (b *Browser) dispatch(m *message) {
	if m.ID != 0 {
		b.mu.Lock()
		ch := b.pending[m.ID]
		delete(b.pending, m.ID)
		b.mu.Unlock()
		if ch == nil {
			return
		}
		if m.Error != nil {
			ch <- reply{err: fmt.Errorf("%s (%d)", m.Error.Message, m.Error.Code)}
		} else {
			ch <- reply{result: m.Result}
		}
		return
	}
	b.mu.Lock()
	h := b.sessions[m.SessionID]
	b.mu.Unlock()
	if h != nil {
		h(m.Method, m.Params)
	}
}

// send writes a command and returns the channel of its answer (nil if
// the answer is not wanted).
func (b *Browser) send(session, method string, params any, want bool) (chan reply, error) {
	select {
	case <-b.done:
		return nil, ErrClosed
	default:
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	var ch chan reply
	if want {
		ch = make(chan reply, 1)
		b.pending[id] = ch
	}
	b.mu.Unlock()
	m := struct {
		ID        int64  `json:"id"`
		Method    string `json:"method"`
		Params    any    `json:"params,omitempty"`
		SessionID string `json:"sessionId,omitempty"`
	}{id, method, params, session}
	data, err := json.Marshal(m)
	if err != nil {
		b.forget(id)
		return nil, err
	}
	b.wmu.Lock()
	_, err = b.w.Write(append(data, 0))
	b.wmu.Unlock()
	if err != nil {
		b.forget(id)
		return nil, ErrClosed
	}
	return ch, nil
}

func (b *Browser) forget(id int64) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

func (b *Browser) call(session, method string, params any, out any) error {
	ch, err := b.send(session, method, params, true)
	if err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("%s: %w", method, r.err)
		}
		if out != nil && len(r.result) > 0 {
			return json.Unmarshal(r.result, out)
		}
		return nil
	case <-time.After(callTimeout):
		return fmt.Errorf("%s: no answer from the browser", method)
	}
}

// Call sends a browser-level command and waits for its answer.
func (b *Browser) Call(method string, params any, out any) error {
	return b.call("", method, params, out)
}

// Send sends a browser-level command without waiting for its answer.
func (b *Browser) Send(method string, params any) {
	_, _ = b.send("", method, params, false)
}

// Close asks the browser to quit, and kills it if it lingers.
func (b *Browser) Close() {
	_, _ = b.send("", "Browser.close", nil, false)
	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		kill(b.cmd)
		<-b.done
	}
	b.w.Close()
}

// Session is a page the browser shows, attached in flat mode.
type Session struct {
	b        *Browser
	ID       string
	TargetID string
}

// NewPage opens a blank page and attaches to it. h receives its events.
func (b *Browser) NewPage(h Handler) (*Session, error) {
	var t struct {
		TargetID string `json:"targetId"`
	}
	// One window per page: in a shared window, only the active tab
	// renders, and the others would stop streaming.
	if err := b.Call("Target.createTarget", map[string]any{"url": "about:blank", "newWindow": true}, &t); err != nil {
		return nil, err
	}
	s, err := b.Attach(t.TargetID, h)
	if err != nil {
		_ = b.Call("Target.closeTarget", map[string]any{"targetId": t.TargetID}, nil)
	}
	return s, err
}

// Attach attaches to an existing target. h receives its events.
func (b *Browser) Attach(targetID string, h Handler) (*Session, error) {
	var a struct {
		SessionID string `json:"sessionId"`
	}
	// A page sends no events before its domains are enabled (Page.enable),
	// which only happens once the handler is registered.
	if err := b.Call("Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &a); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.sessions[a.SessionID] = h
	b.mu.Unlock()
	return &Session{b: b, ID: a.SessionID, TargetID: targetID}, nil
}

// Call sends a command to the page and waits for its answer.
func (s *Session) Call(method string, params any, out any) error {
	return s.b.call(s.ID, method, params, out)
}

// Send sends a command to the page without waiting: input events,
// frame acknowledgements. Errors are dropped; a page that went away
// shows up in its events.
func (s *Session) Send(method string, params any) {
	_, _ = s.b.send(s.ID, method, params, false)
}

// Close closes the page.
func (s *Session) Close() {
	s.b.mu.Lock()
	delete(s.b.sessions, s.ID)
	s.b.mu.Unlock()
	_, _ = s.b.send("", "Target.closeTarget", map[string]any{"targetId": s.TargetID}, false)
}
