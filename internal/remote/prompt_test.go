package remote

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/prompt"
)

func (h *fakeHost) Prompter() *prompt.Hub { return h.hub }

// promptConn authenticates a phone on a pipe; cancel ends the connection.
func promptConn(t *testing.T, r *Remote) (*testClient, AuthedMsg, func()) {
	t.Helper()
	sRw, cRw := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.serveConn(ctx, sRw, "https://host.ts.net", netip.MustParseAddr("192.168.1.100"))
		close(done)
	}()
	c := &testClient{rw: cRw}
	_ = r.devices.Remove("dev-test") // authenticateClient registers it again with a new key
	a := authenticateClient(t, c, r, "https://host.ts.net")
	return c, a, func() { cancel(); cRw.Close(); <-done }
}

// readQueue reads frames until a queue of n tracks arrives.
func readQueue(t *testing.T, c *testClient, n int) prompt.Queue {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.rw.(net.Conn).SetReadDeadline(deadline)
		typ, payload := c.readMsg(t)
		if typ != wPromptQueue {
			continue
		}
		var q prompt.Queue
		if err := json.Unmarshal(payload, &q); err != nil {
			t.Fatal(err)
		}
		if len(q.Tracks) == n {
			return q
		}
	}
	t.Fatalf("no queue of %d", n)
	return prompt.Queue{}
}

func TestAuthedAdvertisesPrompt(t *testing.T) {
	h := newFakeHost()
	h.hub = prompt.NewHub(prompt.Opts{})
	r, _ := setupTestRemote(t, h)
	_, a, stop := promptConn(t, r)
	defer stop()
	if !a.Prompt {
		t.Fatal("Authed.Prompt is false")
	}
}

func TestPhonePromptOpenAndPush(t *testing.T) {
	h := newFakeHost()
	h.hub = prompt.NewHub(prompt.Opts{})
	r, _ := setupTestRemote(t, h)
	c, _, stop := promptConn(t, r)
	defer stop()
	c.writeMsg(t, wPrompt, PromptMsg{Op: "open"})
	readQueue(t, c, 0)
	h.hub.Add("k", "claude", "", "Fini.")
	q := readQueue(t, c, 1)
	c.writeMsg(t, wPrompt, PromptMsg{Op: "dismiss", Seq: q.Tracks[0].Seq})
	readQueue(t, c, 0)
	if q := h.hub.Queue("dev-test"); !q.Open {
		t.Fatal("the phone's queue is not under its device id")
	}
	c.writeMsg(t, wPrompt, PromptMsg{Op: "close"})
	time.Sleep(100 * time.Millisecond)
	if h.hub.AnyOpen() {
		t.Fatal("still open after close")
	}
}

func TestPhonePromptResume(t *testing.T) {
	h := newFakeHost()
	h.hub = prompt.NewHub(prompt.Opts{})
	r, _ := setupTestRemote(t, h)
	c, _, stop := promptConn(t, r)
	c.writeMsg(t, wPrompt, PromptMsg{Op: "open"})
	readQueue(t, c, 0)
	stop()
	h.hub.Add("k", "claude", "", "pendant la veille")
	c, _, stop = promptConn(t, r)
	defer stop()
	readQueue(t, c, 1) // sent at once, without a new open
}

func TestAuthedCarriesPromptContext(t *testing.T) {
	h := newFakeHost()
	h.hub = prompt.NewHub(prompt.Opts{Context: 0})
	r, _ := setupTestRemote(t, h)
	_, a, stop := promptConn(t, r)
	defer stop()
	if a.PromptContext == nil || *a.PromptContext != 0 {
		t.Fatalf("context %v", a.PromptContext)
	}
}

func TestAuthedCarriesPromptBlankHold(t *testing.T) {
	h := newFakeHost()
	h.hub = prompt.NewHub(prompt.Opts{BlankMs: 40, Hold: true})
	r, _ := setupTestRemote(t, h)
	_, a, stop := promptConn(t, r)
	defer stop()
	if a.PromptBlink == nil || *a.PromptBlink {
		t.Fatalf("blink %v", a.PromptBlink)
	}
	if a.PromptBlankMs != 40 || !a.PromptHold {
		t.Fatalf("blank %d hold %v", a.PromptBlankMs, a.PromptHold)
	}
}
