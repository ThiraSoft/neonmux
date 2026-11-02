package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/prompt"
)

// waitQueue reads frames until a MsgPromptQueue with n tracks arrives.
func (w *rawWindow) waitQueue(t *testing.T, n int) prompt.Queue {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, open := <-w.frames:
			if !open {
				t.Fatalf("connection closed while waiting for a queue of %d", n)
			}
			if f.typ != MsgPromptQueue {
				continue
			}
			var q prompt.Queue
			if err := json.Unmarshal(f.data, &q); err != nil {
				t.Fatal(err)
			}
			if len(q.Tracks) == n {
				return q
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a queue of %d", n)
		}
	}
}

func TestPromptOpenSendsQueue(t *testing.T) {
	s, sock := inProcess(t)
	w := dialWindow(t, sock, protoVersion)
	w.send(t, MsgPrompt, "", PromptCmd{Op: "open"})
	if q := w.waitQueue(t, 0); !q.Open {
		t.Fatal("queue not open")
	}
	s.hub.Add("k", "claude", "/p", "Fini.")
	q := w.waitQueue(t, 1)
	if q.Tracks[0].Words[0].T != "Fini." {
		t.Fatalf("queue %+v", q)
	}
}

func TestPromptKeptAcrossReconnect(t *testing.T) {
	s, sock := inProcess(t)
	w := dialWindow(t, sock, protoVersion)
	w.send(t, MsgPrompt, "", PromptCmd{Op: "open"})
	w.waitQueue(t, 0)
	w.c.Close()
	time.Sleep(100 * time.Millisecond)
	s.hub.Add("k", "claude", "/p", "pendant la coupure")
	w2 := dialWindow(t, sock, protoVersion)
	w2.send(t, MsgPrompt, "", PromptCmd{Op: "open"})
	w2.waitQueue(t, 1)
}

func TestPromptDismissAndClose(t *testing.T) {
	s, sock := inProcess(t)
	w := dialWindow(t, sock, protoVersion)
	w.send(t, MsgPrompt, "", PromptCmd{Op: "open"})
	w.waitQueue(t, 0)
	s.hub.Add("k", "claude", "", "un")
	s.hub.Add("k", "claude", "", "deux")
	q := w.waitQueue(t, 2)
	w.send(t, MsgPrompt, "", PromptCmd{Op: "dismiss", Seq: q.Tracks[0].Seq})
	if q := w.waitQueue(t, 1); q.Tracks[0].Words[0].T != "deux" {
		t.Fatalf("queue %+v", q)
	}
	w.send(t, MsgPrompt, "", PromptCmd{Op: "close"})
	time.Sleep(100 * time.Millisecond)
	if s.hub.AnyOpen() {
		t.Fatal("still open after close")
	}
}

func TestPromptOldWindowGetsNothing(t *testing.T) {
	s, sock := inProcess(t)
	w := dialWindow(t, sock, 3)
	w.send(t, MsgPrompt, "", PromptCmd{Op: "open"})
	time.Sleep(200 * time.Millisecond)
	s.hub.Add("k", "claude", "", "un")
	time.Sleep(200 * time.Millisecond)
	for {
		select {
		case f := <-w.frames:
			if f.typ == MsgPromptQueue {
				t.Fatal("an old window received MsgPromptQueue")
			}
		default:
			if s.hub.AnyOpen() {
				t.Fatal("an old window opened a prompter")
			}
			return
		}
	}
}

// TestClientReopensAfterReconnect: the window's client sends open again to
// a new daemon connection, and gets the kept queue.
func TestClientReopensAfterReconnect(t *testing.T) {
	s, sock := inProcess(t)
	cl, err := Connect(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	got := make(chan prompt.Queue, 16)
	cl.OnPromptQueue = func(q prompt.Queue) { got <- q }
	cl.Prompt(PromptCmd{Op: "open"})
	waitClientQueue(t, got, 0)
	s.hub.Add("k", "claude", "", "un")
	waitClientQueue(t, got, 1)
	time.Sleep(200 * time.Millisecond)
	for len(got) > 0 {
		<-got
	}
	s.mu.Lock()
	for c := range s.wins {
		c.c.Close()
	}
	s.mu.Unlock()
	waitClientQueue(t, got, 1) // sent again after the reconnect
}

func waitClientQueue(t *testing.T, got chan prompt.Queue, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case q := <-got:
			if len(q.Tracks) == n {
				return
			}
		case <-deadline:
			t.Fatalf("no queue of %d", n)
		}
	}
}
