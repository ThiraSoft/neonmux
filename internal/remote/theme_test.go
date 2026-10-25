package remote

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// readUntil reads messages until one of type want, failing on any type in
// refuse first.
func readUntil(t *testing.T, c *testClient, want uint8, refuse ...uint8) []byte {
	t.Helper()
	for {
		typ, payload := c.readMsg(t)
		if typ == want {
			return payload
		}
		for _, r := range refuse {
			if typ == r {
				t.Fatalf("got message %d (%s) before %d", typ, payload, want)
			}
		}
	}
}

func runFG(t *testing.T, f FrameMsg, y int) int32 {
	t.Helper()
	for _, l := range f.Lines {
		if l.Y == y && len(l.Runs) > 0 {
			return l.Runs[0].FG
		}
	}
	t.Fatalf("no run on line %d", y)
	return 0
}

// The phone picks its theme among the desktop's: Authed lists them,
// SetTheme switches the connection's palette (Theme and a full frame), ""
// follows the desktop again, an unknown name is refused.
func TestPhoneTheme(t *testing.T) {
	h := newFakeHost()
	term := vt.New(80, 24, 100)
	_, _ = term.Write([]byte("\x1b[31mred\x1b[0m"))
	h.terms["s1"] = term
	h.sessions = []SessionInfo{{Key: "s1", Name: "shell"}}

	r, _ := setupTestRemote(t, h)
	origin := "https://host.ts.net"
	sRw, cRw := net.Pipe()
	client := &testClient{rw: cRw}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go r.serveConn(ctx, sRw, origin, netip.MustParseAddr("192.168.1.100"))
	a := authenticateClient(t, client, r, origin)

	if len(a.Themes) != len(theme.All) {
		t.Fatalf("Authed lists %d themes, want %d", len(a.Themes), len(theme.All))
	}
	for i, th := range theme.All {
		if a.Themes[i].Name != th.Name || a.Themes[i].Label != th.Label {
			t.Fatalf("theme %d = %+v, want %s %q", i, a.Themes[i], th.Name, th.Label)
		}
	}

	syn := theme.Get("synthwave")
	synRed := rgbToInt(theme.RGB(syn.Palette(1)))
	wantTheme := func(payload []byte, fg, bg [3]uint8) {
		t.Helper()
		var tm ThemeMsg
		if err := json.Unmarshal(payload, &tm); err != nil {
			t.Fatal(err)
		}
		if tm.FG != fg || tm.BG != bg {
			t.Fatalf("Theme fg %v bg %v, want %v %v", tm.FG, tm.BG, fg, bg)
		}
	}
	frame := func(payload []byte) FrameMsg {
		t.Helper()
		var f FrameMsg
		if err := json.Unmarshal(payload, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}

	// Sent after authentication, before the first Watch, as the PWA does.
	client.writeMsg(t, wSetTheme, SetThemeMsg{Name: "synthwave"})
	client.writeMsg(t, wWatch, WatchMsg{Key: "s1"})
	wantTheme(readUntil(t, client, wTheme), theme.RGB(syn.FG), theme.RGB(syn.BG))
	f := frame(readUntil(t, client, wFrame))
	if got := runFG(t, f, 0); got != synRed {
		t.Fatalf("red in the phone's theme = %06x, want %06x", got, synRed)
	}

	// The desktop's theme changes: the phone keeps its own.
	h.mu.Lock()
	h.palette.FG = [3]uint8{1, 2, 3}
	h.mu.Unlock()
	time.Sleep(1100 * time.Millisecond) // past the session ticker
	_, _ = term.Write([]byte("\r\n"))
	h.mu.Lock()
	w := h.watchers["s1"]
	h.mu.Unlock()
	w.Changed()
	f = frame(readUntil(t, client, wFrame, wTheme))
	if f.Full {
		t.Fatal("full frame after a desktop theme change the phone does not follow")
	}

	// Unknown theme: refused, nothing changes.
	client.writeMsg(t, wSetTheme, SetThemeMsg{Name: "no-such-theme"})
	var em ErrorMsg
	if err := json.Unmarshal(readUntil(t, client, wError, wTheme), &em); err != nil || em.Msg != "unknown theme" {
		t.Fatalf("error %+v, %v; want unknown theme", em, err)
	}

	// Another theme while watching: Theme, then a full frame in it.
	client.writeMsg(t, wSetTheme, SetThemeMsg{Name: "neon-tokyo"})
	tokyo := theme.Get("neon-tokyo")
	wantTheme(readUntil(t, client, wTheme, wFrame), theme.RGB(tokyo.FG), theme.RGB(tokyo.BG))
	f = frame(readUntil(t, client, wFrame))
	if !f.Full || runFG(t, f, 0) != rgbToInt(theme.RGB(tokyo.Palette(1))) {
		t.Fatalf("frame after SetTheme: full=%v", f.Full)
	}

	// "" follows the desktop again, its current theme.
	client.writeMsg(t, wSetTheme, SetThemeMsg{Name: ""})
	h.mu.Lock()
	host := h.palette
	h.mu.Unlock()
	wantTheme(readUntil(t, client, wTheme, wFrame), host.FG, host.BG)
	f = frame(readUntil(t, client, wFrame))
	if !f.Full || runFG(t, f, 0) != rgbToInt(host.P[1]) {
		t.Fatalf("frame after following the desktop: full=%v", f.Full)
	}
}
