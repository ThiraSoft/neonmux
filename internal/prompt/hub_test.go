package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHubOnlyOpenDevices(t *testing.T) {
	h := NewHub(allOn)
	h.Add("k", "claude", "/p", "avant")
	h.Open("window")
	if q := h.Queue("window"); !q.Open || len(q.Tracks) != 0 {
		t.Fatalf("queue before any reply: %+v", q)
	}
	h.Add("k", "claude", "/p", "apres ouverture")
	q := h.Queue("window")
	if len(q.Tracks) != 1 || q.Tracks[0].Words[0].T != "apres" || q.Tracks[0].Key != "k" || q.Tracks[0].Agent != "claude" {
		t.Fatalf("queue %+v", q)
	}
	if q := h.Queue("phone"); q.Open || len(q.Tracks) != 0 {
		t.Fatalf("closed device got %+v", q)
	}
}

func TestHubDevicesIndependent(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	h.Open("phone")
	h.Add("k", "claude", "", "un")
	h.Add("k", "claude", "", "deux")
	first := h.Queue("window").Tracks[0].Seq
	h.Dismiss("window", first)
	if n := len(h.Queue("window").Tracks); n != 1 {
		t.Fatalf("window has %d tracks", n)
	}
	if n := len(h.Queue("phone").Tracks); n != 2 {
		t.Fatalf("phone has %d tracks", n)
	}
	if s := h.Queue("phone").Tracks[1].Seq; s <= first {
		t.Fatalf("seq not increasing: %d after %d", s, first)
	}
}

func TestHubCloseDrops(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	h.Add("k", "claude", "", "un")
	h.Close("window")
	if h.AnyOpen() {
		t.Fatal("still open")
	}
	h.Open("window")
	if n := len(h.Queue("window").Tracks); n != 0 {
		t.Fatalf("%d tracks after close and reopen", n)
	}
}

func TestHubReopenKeeps(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	h.Add("k", "claude", "", "un")
	h.Open("window")
	if n := len(h.Queue("window").Tracks); n != 1 {
		t.Fatalf("%d tracks after a second open", n)
	}
}

func TestHubCap(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	for range 51 {
		h.Add("k", "claude", "", "mot")
	}
	q := h.Queue("window")
	if len(q.Tracks) != 50 || q.Tracks[0].Seq != 2 {
		t.Fatalf("%d tracks, first seq %d", len(q.Tracks), q.Tracks[0].Seq)
	}
}

func TestHubTextCap(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	h.Add("k", "claude", "", strings.Repeat("abcdefg ", 100<<10/8))
	ws := h.Queue("window").Tracks[0].Words
	if last := ws[len(ws)-1]; last.T != "[cut]" || !last.M {
		t.Fatalf("last word %+v", last)
	}
	n := 0
	for _, w := range ws[:len(ws)-1] {
		n += len(w.T) + 1
	}
	if n > 64<<10 {
		t.Fatalf("%d bytes of text kept", n)
	}
}

func TestHubQueueFrameFits(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	big := strings.Repeat("abcdefg ", 64<<10/8)
	for range 50 {
		h.Add("k", "claude", "", big)
	}
	data, _ := json.Marshal(h.Queue("window"))
	if len(data) >= 900<<10 {
		t.Fatalf("queue is %d bytes", len(data))
	}
	if len(h.Queue("window").Tracks) == 0 {
		t.Fatal("everything dropped")
	}
}

func TestHubWatch(t *testing.T) {
	h := NewHub(allOn)
	h.Open("window")
	calls := 0
	cancel := h.Watch("window", func() { calls++ })
	h.Add("k", "claude", "", "un")
	if calls != 1 {
		t.Fatalf("%d calls after Add", calls)
	}
	cancel()
	h.Add("k", "claude", "", "deux")
	if calls != 1 {
		t.Fatalf("%d calls after cancel", calls)
	}
}
