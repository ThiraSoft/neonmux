package cdp

import (
	"encoding/json"
	"testing"
	"time"
)

// launch starts the browser found on this machine, or skips the test.
func launch(t *testing.T, onEvent Handler) *Browser {
	t.Helper()
	argv, err := Find("")
	if err != nil {
		t.Skip(err)
	}
	b, err := Launch(argv, t.TempDir(), onEvent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestPageEvaluatesAndStreams(t *testing.T) {
	b := launch(t, nil)
	frames := make(chan int, 4)
	var s *Session
	s, err := b.NewPage(func(method string, params json.RawMessage) {
		if method != "Page.screencastFrame" {
			return
		}
		var f struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &f)
		s.Send("Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID})
		select {
		case frames <- len(f.Data):
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Call("Page.enable", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Call("Page.navigate", map[string]any{"url": "data:text/html,<title>neon</title><h1>hi</h1>"}, nil); err != nil {
		t.Fatal(err)
	}
	var title string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && title != "neon"; time.Sleep(50 * time.Millisecond) {
		var r struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := s.Call("Runtime.evaluate", map[string]any{"expression": "document.title", "returnByValue": true}, &r); err != nil {
			t.Fatal(err)
		}
		title = r.Result.Value
	}
	if title != "neon" {
		t.Fatalf("title %q, want neon", title)
	}
	if err := s.Call("Page.startScreencast", map[string]any{"format": "jpeg", "quality": 60}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-frames:
		if n == 0 {
			t.Fatal("empty frame")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no screencast frame")
	}
}

func TestCloseEndsTheProcess(t *testing.T) {
	b := launch(t, nil)
	b.Close()
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("browser still running")
	}
	if err := b.Call("Browser.getVersion", nil, nil); err == nil {
		t.Fatal("call after close succeeded")
	}
}
