package remote

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/vt"
)

// takeInput returns what the host received for key so far and clears it.
func takeInput(h *fakeHost, key string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := string(h.inputs[key])
	h.inputs[key] = nil
	return s
}

// readFrameMsg reads messages until a Frame and decodes it.
func readFrameMsg(t *testing.T, c *testClient) FrameMsg {
	t.Helper()
	for {
		typ, payload := c.readMsg(t)
		if typ != wFrame {
			continue
		}
		var f FrameMsg
		if err := json.Unmarshal(payload, &f); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		return f
	}
}

// The phone scrolls a full-screen program as the window's wheel does:
// mouse wheel events when it tracks the mouse, arrow keys on the alternate
// screen, the history otherwise. Frames say which case applies.
func TestWheelFullScreenApp(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	for i := 0; i < 30; i++ {
		_, _ = term.Write([]byte("line\r\n"))
	}
	// As Claude Code: alternate screen, any-motion tracking, SGR encoding.
	_, _ = term.Write([]byte("\x1b[?1049h\x1b[?1003h\x1b[?1006h"))
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "claude"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	authenticateClient(t, client, r, origin)

	// Not watching: ignored.
	client.writeMsg(t, wWheel, WheelMsg{Steps: 1})
	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	f := readFrameMsg(t, client)
	if !f.Alt || !f.Mouse {
		t.Fatalf("frame alt=%v mouse=%v, want both", f.Alt, f.Mouse)
	}
	if got := takeInput(h, "s1"); got != "" {
		t.Fatalf("wheel before watch reached the session: %q", got)
	}

	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		got := ""
		for time.Now().Before(deadline) {
			h.mu.Lock()
			got = string(h.inputs["s1"])
			h.mu.Unlock()
			if len(got) >= len(want) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		takeInput(h, "s1")
		if got != want {
			t.Fatalf("input %q, want %q", got, want)
		}
	}

	client.writeMsg(t, wWheel, WheelMsg{Steps: 2, X: 5, Y: 3})
	wait("\x1b[<64;6;4M\x1b[<64;6;4M")

	// Steps are capped, the cell is clamped to the screen.
	client.writeMsg(t, wWheel, WheelMsg{Steps: -50, X: 999, Y: -4})
	want := ""
	for i := 0; i < maxWheelSteps; i++ {
		want += "\x1b[<65;80;1M"
	}
	wait(want)

	// Alternate screen without the mouse: arrow keys.
	_, _ = term.Write([]byte("\x1b[?1003l"))
	client.writeMsg(t, wWheel, WheelMsg{Steps: -2})
	wait("\x1b[B\x1b[B")

	// Main screen: the history scrolls, nothing is written.
	_, _ = term.Write([]byte("\x1b[?1049l"))
	client.writeMsg(t, wWheel, WheelMsg{Steps: 2})
	for {
		f = readFrameMsg(t, client)
		if f.Offset != 0 {
			break
		}
	}
	if f.Offset != 6 || f.Alt || f.Mouse {
		t.Fatalf("frame offset=%d alt=%v mouse=%v, want offset 6 and no app", f.Offset, f.Alt, f.Mouse)
	}
	if got := takeInput(h, "s1"); got != "" {
		t.Fatalf("history wheel wrote %q", got)
	}
}
