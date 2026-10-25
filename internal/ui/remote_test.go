package ui

import (
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/remote"
	"github.com/ThiraSoft/neonmux/internal/server"
)

// fakeLink records the remote commands the window sends to the daemon.
type fakeLink struct {
	cmds  []server.RemoteCmd
	proto int
}

func (f *fakeLink) Remote(c server.RemoteCmd) { f.cmds = append(f.cmds, c) }
func (f *fakeLink) ServerProto() int          { return f.proto }

func remoteApp(t *testing.T) (*App, *fakeLink) {
	t.Helper()
	a, _ := testApp()
	l := &fakeLink{proto: 3}
	a.rlink = l
	return a, l
}

func key(a *App, name string) { a.handleKey(nil, KeyEvent{Name: name}) }

func lastCmd(l *fakeLink) server.RemoteCmd {
	if len(l.cmds) == 0 {
		return server.RemoteCmd{}
	}
	return l.cmds[len(l.cmds)-1]
}

func hasToast(a *App, text string) bool {
	return slices.ContainsFunc(a.toasts, func(t toastMsg) bool { return strings.Contains(t.text, text) })
}

var remoteLabels = []string{
	"Remote: enable",
	"Remote: disable",
	"Remote: pair a phone",
	"Remote: devices",
	"Remote: regenerate identity (unpairs every phone)",
}

// TestRemotePaletteEntries: the palette offers the five remote control
// entries with a daemon, and none without one (-no-server).
func TestRemotePaletteEntries(t *testing.T) {
	a, _ := remoteApp(t)
	labels := func() []string {
		var out []string
		for _, c := range a.commands() {
			out = append(out, c.label)
		}
		return out
	}
	got := labels()
	for _, l := range remoteLabels {
		if !slices.Contains(got, l) {
			t.Errorf("palette lacks %q", l)
		}
	}
	a.rlink = nil
	for _, l := range labels() {
		if strings.HasPrefix(l, "Remote:") {
			t.Errorf("without a daemon, the palette offers %q", l)
		}
	}
	// Reached anyway (a binding): a toast, nothing else.
	a.do("remote.pair")
	if a.rview != nil || !hasToast(a, "needs the daemon") {
		t.Errorf("without a daemon: overlay %v, toasts %+v", a.rview != nil, a.toasts)
	}
}

// TestRemotePairSendsPair: the palette entry asks the daemon for a pairing
// and opens the pairing screen.
func TestRemotePairSendsPair(t *testing.T) {
	a, l := remoteApp(t)
	a.openPalette("", a.commands())
	a.palette.chars([]rune("Remote: pair a phone"))
	key(a, "enter")
	if !slices.Equal(l.cmds, []server.RemoteCmd{{Op: "pair"}}) {
		t.Fatalf("sent %+v, want pair", l.cmds)
	}
	if a.rview == nil || a.rview.kind != viewPair {
		t.Fatal("the pairing screen is not open")
	}
	// An old daemon does not know the command: said, nothing sent.
	a, l = remoteApp(t)
	l.proto = 2
	a.do("remote.pair")
	if len(l.cmds) != 0 || a.rview != nil || !hasToast(a, "restart") {
		t.Errorf("old daemon: sent %+v, toasts %+v", l.cmds, a.toasts)
	}
}

// TestRemotePairingConfirm: Enter does nothing until the code is shown,
// then sends confirm. The screen draws the QR, the code and the hint.
func TestRemotePairingConfirm(t *testing.T) {
	a, _ := coreApp(t, 1)
	l := &fakeLink{proto: 3}
	a.rlink = l
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)

	a.do("remote.pair")
	key(a, "enter")
	exp := time.Now().Add(2 * time.Minute)
	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: `{"t":"secret-token"}`, Expires: exp}})
	key(a, "enter")
	if c := lastCmd(l); c.Op != "pair" || len(l.cmds) != 1 {
		t.Fatalf("enter before the code sent %+v", l.cmds)
	}
	lines := a.pairTexts(time.Now())
	if !slices.Contains(lines, "Scan from the neonmux app on your phone") {
		t.Errorf("pairing screen: %q", lines)
	}
	a.needRedraw = true
	a.Draw(screen)

	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: `{"t":"secret-token"}`, Code: "123456", Expires: exp}})
	if a.rview == nil {
		t.Fatal("the overlay closed when the code arrived")
	}
	lines = a.pairTexts(time.Now())
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"123456", "enter: it matches · esc: cancel"} {
		if !strings.Contains(joined, want) {
			t.Errorf("pairing screen lacks %q: %q", want, lines)
		}
	}
	a.needRedraw = true
	a.Draw(screen)
	key(a, "enter")
	if c := lastCmd(l); c.Op != "confirm" {
		t.Fatalf("enter with the code sent %+v, want confirm", l.cmds)
	}
	for _, ts := range a.toasts {
		if strings.Contains(ts.text, "secret-token") {
			t.Errorf("the token reached a toast: %q", ts.text)
		}
	}
}

// TestRemotePairingEsc: Esc rejects and closes, with or without a code.
func TestRemotePairingEsc(t *testing.T) {
	a, l := remoteApp(t)
	a.do("remote.pair")
	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: "x", Code: "123456", Expires: time.Now().Add(time.Minute)}})
	key(a, "escape")
	if c := lastCmd(l); c.Op != "reject" {
		t.Fatalf("esc sent %+v, want reject", l.cmds)
	}
	if a.rview != nil {
		t.Error("esc left the pairing screen open")
	}
}

// TestRemotePairingExpired: the pairing gone after its expiry closes the
// screen with a toast. Enter after expiry confirms nothing.
func TestRemotePairingExpired(t *testing.T) {
	a, l := remoteApp(t)
	a.do("remote.pair")
	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: "x", Code: "123456", Expires: time.Now().Add(-time.Second)}})
	key(a, "enter")
	if c := lastCmd(l); c.Op == "confirm" {
		t.Fatal("enter confirmed an expired pairing")
	}
	a.setRemoteState(remote.State{Enabled: true})
	if a.rview != nil || !hasToast(a, "pairing expired") {
		t.Errorf("expired: overlay %v, toasts %+v", a.rview != nil, a.toasts)
	}
	// Decided elsewhere (not expired): closes without the expiry toast.
	a, _ = remoteApp(t)
	a.do("remote.pair")
	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: "x", Expires: time.Now().Add(time.Minute)}})
	a.setRemoteState(remote.State{Enabled: true})
	if a.rview != nil || hasToast(a, "pairing expired") {
		t.Errorf("ended: overlay %v, toasts %+v", a.rview != nil, a.toasts)
	}
}

// TestRemotePairingConfirmedPaired: the states the daemon sends around a
// confirmed pairing (the code kept while the decision is carried out, then
// the device) end with "phone paired", never "pairing failed".
func TestRemotePairingConfirmedPaired(t *testing.T) {
	a, l := remoteApp(t)
	a.do("remote.pair")
	exp := time.Now().Add(5 * time.Minute)
	coded := remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: "x", Code: "123456", Expires: exp}}
	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: "x", Expires: exp}})
	a.setRemoteState(coded)
	key(a, "enter")
	if c := lastCmd(l); c.Op != "confirm" {
		t.Fatalf("enter sent %+v, want confirm", l.cmds)
	}
	a.setRemoteState(coded) // decision being carried out
	if a.rview == nil {
		t.Fatal("the pairing screen closed before the device appeared")
	}
	a.setRemoteState(remote.State{Enabled: true, Devices: []remote.Device{{ID: "d1", Name: "iPhone"}}})
	if a.rview != nil || !hasToast(a, "phone paired") || hasToast(a, "pairing failed") {
		t.Errorf("confirmed: overlay %v, toasts %+v", a.rview != nil, a.toasts)
	}
}

// TestRemotePairingCodeFont: the code is drawn with Iosevka rasterized at
// three times the interface size (not a scaled bitmap), built once and
// rebuilt when the fonts are (display scale change). The box reserves the
// face's height for it.
func TestRemotePairingCodeFont(t *testing.T) {
	a, _ := coreApp(t, 1)
	a.rlink = &fakeLink{proto: 3}
	screen := ebiten.NewImage(a.w, a.h)
	settle(a, screen)
	a.do("remote.pair")
	exp := time.Now().Add(5 * time.Minute)
	a.setRemoteState(remote.State{Enabled: true, Pairing: &remote.PairingState{Payload: "x", Code: "123456", Expires: exp}})
	a.needRedraw = true
	a.Draw(screen)

	cf := a.pairCodeFont()
	if cf == nil || cf.Pixel || cf.Name != gfx.FontIosevka {
		t.Fatalf("code font = %+v, want Iosevka", cf)
	}
	want, err := gfx.LoadFont(gfx.FontIosevka, a.cfg.UIFontSize*3, a.dsf)
	if err != nil {
		t.Fatal(err)
	}
	defer want.Dispose()
	if cf.CellH != want.CellH || cf.CellW != want.CellW {
		t.Errorf("code cell %dx%d, want %dx%d (3x the UI size at dsf %v)", cf.CellW, cf.CellH, want.CellW, want.CellH, a.dsf)
	}
	a.needRedraw = true
	a.Draw(screen)
	if a.pairCodeFont() != cf {
		t.Error("the code face is built again on each frame")
	}

	lines := a.pairTexts(time.Now())
	_, withCode := a.pairLayout(lines, 0)
	_, without := a.pairLayout(slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return l == "123456" }), 0)
	if withCode-without < float32(cf.CellH) {
		t.Errorf("height reserved for the code %v, less than the face's %d", withCode-without, cf.CellH)
	}

	a.dsf *= 2
	a.loadFonts()
	if c2 := a.pairCodeFont(); c2 == cf || c2.CellH < cf.CellH*3/2 {
		t.Errorf("after a display scale change: same face %v, cell height %d (was %d)", c2 == cf, c2.CellH, cf.CellH)
	}
}

// TestRemoteStatusBar: nothing when off, "remote" dim when on, the phone's
// name in the accent color when one is connected.
func TestRemoteStatusBar(t *testing.T) {
	a, _ := remoteApp(t)
	a.setRemoteState(remote.State{})
	if it := a.remoteBar(); it.text != "" {
		t.Errorf("disabled: %q", it.text)
	}
	a.setRemoteState(remote.State{Enabled: true})
	if it := a.remoteBar(); it.text != "remote" || it.color != a.theme.Dim {
		t.Errorf("enabled: %q %v", it.text, it.color)
	}
	a.setRemoteState(remote.State{Enabled: true, Connected: []string{"iPhone\x1b[31m"}})
	if it := a.remoteBar(); it.text != "remote · iPhone[31m" || it.color != a.theme.Accent {
		t.Errorf("connected: %q %v", it.text, it.color)
	}
}

// TestRemoteIdleOffConfig: remote_idle_off is a number of minutes (0 never),
// the old duration string still works, and an invalid value is reported and
// the default kept.
func TestRemoteIdleOffConfig(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{`30`, 30 * time.Minute},
		{`0`, 0},
		{`1`, time.Minute},
		{`"45m"`, 45 * time.Minute},
		{`"1h"`, time.Hour},
		{`"0"`, 0},
	}
	for _, tc := range ok {
		c, warn, err := ParseConfig([]byte(`{"remote_idle_off": ` + tc.in + `}`))
		if err != nil || warn != "" || c.RemoteIdle() != tc.want {
			t.Errorf("%s: %v %q %v", tc.in, c.RemoteIdle(), warn, err)
		}
	}
	for _, in := range []string{`-5`, `1.5`, `"abc"`, `"-5m"`, `true`} {
		c, warn, err := ParseConfig([]byte(`{"remote_idle_off": ` + in + `}`))
		if err != nil || !strings.Contains(warn, "remote_idle_off") || c.RemoteIdle() != 30*time.Minute {
			t.Errorf("%s: %v %q %v", in, c.RemoteIdle(), warn, err)
		}
		if out, _ := json.Marshal(c.RemoteIdleOff); string(out) != "30" {
			t.Errorf("%s: kept %s, want 30", in, out)
		}
	}
	c, warn, _ := ParseConfig([]byte(`{"remote_idle_off": -5, "remote_port": 70000, "remote_page_port": -1}`))
	if c.RemoteIdle() != 30*time.Minute || c.RemotePort != 8007 || c.RemotePagePort != 8006 || !strings.Contains(warn, "remote_port") {
		t.Errorf("out of range: %v %d %d %q", c.RemoteIdle(), c.RemotePort, c.RemotePagePort, warn)
	}
}

// TestDefaultConfigIdleOffIsNumber: the default config shows the number form.
func TestDefaultConfigIdleOffIsNumber(t *testing.T) {
	out, err := json.Marshal(DefaultConfig())
	if err != nil || !strings.Contains(string(out), `"remote_idle_off":30,`) {
		t.Fatalf("%v %s", err, out)
	}
}

// TestRemoteRevokeAsks: d asks first, y sends the device ID, n cancels.
func TestRemoteRevokeAsks(t *testing.T) {
	a, l := remoteApp(t)
	a.setRemoteState(remote.State{Enabled: true, Devices: []remote.Device{
		{ID: "id-1", Name: "iPad", Added: time.Now()},
		{ID: "id-2", Name: "iPhone", Added: time.Now()},
	}})
	a.do("remote.devices")
	if a.rview == nil || a.rview.kind != viewDevices {
		t.Fatal("the devices list is not open")
	}
	key(a, "down")
	key(a, "d")
	if a.rview.ask != "revoke iPhone? y/n" || len(l.cmds) != 0 {
		t.Fatalf("prompt %q, sent %+v", a.rview.ask, l.cmds)
	}
	key(a, "n")
	if a.rview == nil || a.rview.ask != "" || len(l.cmds) != 0 {
		t.Fatalf("n: sent %+v", l.cmds)
	}
	key(a, "d")
	key(a, "y")
	if !slices.Equal(l.cmds, []server.RemoteCmd{{Op: "revoke", ID: "id-2"}}) {
		t.Fatalf("y: sent %+v", l.cmds)
	}
	key(a, "escape")
	if a.rview != nil {
		t.Error("esc left the list open")
	}
}

// TestRemoteRegenAsks: regenerating the identity asks first.
func TestRemoteRegenAsks(t *testing.T) {
	a, l := remoteApp(t)
	a.do("remote.regen")
	key(a, "n")
	if len(l.cmds) != 0 || a.rview != nil {
		t.Fatalf("n: sent %+v, overlay %v", l.cmds, a.rview != nil)
	}
	a.do("remote.regen")
	key(a, "y")
	if !slices.Equal(l.cmds, []server.RemoteCmd{{Op: "regen"}}) {
		t.Fatalf("y: sent %+v", l.cmds)
	}
}

// TestRemoteDeviceName: a name chosen on the phone loses its control
// characters and is capped.
func TestRemoteDeviceName(t *testing.T) {
	got := deviceLabel("evil\x1b]0;x\x07‮name" + strings.Repeat("a", 100))
	if strings.ContainsFunc(got, func(r rune) bool { return r < ' ' || r == 0x7f || r == '‮' }) {
		t.Errorf("control characters left: %q", got)
	}
	if n := len([]rune(got)); n > maxDeviceName {
		t.Errorf("%d runes, want at most %d", n, maxDeviceName)
	}
	if deviceLabel("\x1b\x07") != "unnamed" {
		t.Errorf("empty name: %q", deviceLabel("\x1b\x07"))
	}
}

// TestRemoteStateNeverBlocks: state pushes never block the client's reader
// (which also carries every pane's output), even when the window does not
// drain its posted queue. Once it does, it holds the last state.
func TestRemoteStateNeverBlocks(t *testing.T) {
	a, _ := remoteApp(t)
	a.pacer = fx.NewPacer(60)
	a.posted = make(chan func(), 64)
	for len(a.posted) < cap(a.posted) {
		a.posted <- func() {}
	}
	var latest atomic.Pointer[remote.State]
	cb := a.remoteStateFunc(latest.Load)
	done := make(chan struct{})
	go func() {
		for i := range 100 {
			st := remote.State{Enabled: true, Connected: []string{"phone " + itoa(i)}}
			latest.Store(&st)
			cb(st)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Unblock the reader before failing.
		go func() {
			for range a.posted {
			}
		}()
		t.Fatal("state pushes block while the window is not draining")
	}
	for len(a.posted) > 0 {
		(<-a.posted)()
	}
	a.pollRemote()
	if got := a.remoteBar().text; got != "remote · phone 99" {
		t.Errorf("after the drain: %q, want the last state", got)
	}
}

// TestRemoteErrorToast: a daemon error keeps its words (only control
// characters go), within a longer cap than a device name.
func TestRemoteErrorToast(t *testing.T) {
	a, _ := remoteApp(t)
	msg := "listen udp 192.168.1.20:8007: bind: address already in use"
	a.setRemoteState(remote.State{Err: msg + "\x1b[2J"})
	if !hasToast(a, "remote control: "+msg) {
		t.Errorf("toasts %+v", a.toasts)
	}
	a.setRemoteState(remote.State{Err: strings.Repeat("x", 500)})
	if n := len([]rune(a.toasts[len(a.toasts)-1].text)); n > maxErrText+len("remote control: ") {
		t.Errorf("error toast of %d runes", n)
	}
}
