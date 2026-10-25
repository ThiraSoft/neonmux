package ui

import (
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/hajimehoshi/ebiten/v2"
	"rsc.io/qr"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/remote"
	"github.com/ThiraSoft/neonmux/internal/server"
)

// Remote control from the window: the palette entries, the pairing screen,
// the devices list and the status bar indicator. The daemon runs the
// server (internal/server/remotehost.go) and pushes its state; the window
// only shows it and sends commands.

// remoteLink is the part of the daemon client remote control uses.
type remoteLink interface {
	Remote(server.RemoteCmd)
	ServerProto() int
}

// The kinds of remote overlay.
const (
	viewPair    = iota + 1 // QR code, then the code to confirm
	viewDevices            // paired phones, d revokes
	viewRegen              // regenerate the identity? y/n
)

// maxDeviceName caps a phone's name on screen.
const maxDeviceName = 24

// pairStartWait is how long the pairing screen waits for the daemon's
// first pairing state before giving up.
const pairStartWait = 10 * time.Second

// remoteView is the remote control overlay. It takes every key while open.
type remoteView struct {
	kind int

	// Pairing screen.
	opened  time.Time
	shown   bool      // a pairing from the daemon was shown
	expires time.Time // of the pairing shown
	devices int       // paired phones when the pairing started
	sent    bool      // confirm sent
	qrFor   string    // payload qr was built from (never logged)
	qr      *qr.Code

	// Devices list.
	sel, top int
	ask      string // revoke prompt
	askID    string
}

// maxErrText caps a daemon error in a toast.
const maxErrText = 120

// cleanText makes a text from outside safe to draw: no control or format
// characters (escapes, bidi overrides), capped at max cells.
func cleanText(s string, max int) string {
	return gfx.Truncate(strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)), max)
}

// deviceLabel makes a name chosen on the phone safe to draw.
func deviceLabel(name string) string {
	if s := cleanText(name, maxDeviceName); s != "" {
		return s
	}
	return "unnamed"
}

// remoteReady tells whether remote commands can be sent, with a toast
// when they cannot.
func (a *App) remoteReady() bool {
	switch {
	case a.rlink == nil:
		a.toast("remote control needs the daemon (neonmux runs without it)", a.theme.Warn)
		return false
	case a.rlink.ServerProto() < 3:
		a.toast("remote control needs a newer daemon: restart neonmux", a.theme.Warn)
		return false
	}
	return true
}

func (a *App) remoteCommands() []command {
	if a.rlink == nil {
		return nil
	}
	return []command{
		{label: "Remote: enable", action: "remote.on"},
		{label: "Remote: disable", action: "remote.off"},
		{label: "Remote: pair a phone", action: "remote.pair"},
		{label: "Remote: devices", action: "remote.devices"},
		{label: "Remote: regenerate identity (unpairs every phone)", action: "remote.regen"},
	}
}

// remoteAction runs a remote.* palette action.
func (a *App) remoteAction(op string) {
	if !a.remoteReady() {
		return
	}
	switch op {
	case "on", "off":
		a.rlink.Remote(server.RemoteCmd{Op: op})
	case "pair":
		a.rlink.Remote(server.RemoteCmd{Op: "pair"})
		a.rview = &remoteView{kind: viewPair, opened: time.Now(), devices: len(a.rstate.Devices)}
	case "devices":
		a.rview = &remoteView{kind: viewDevices}
	case "regen":
		a.rview = &remoteView{kind: viewRegen}
	}
	a.needRedraw = true
}

// remoteStateFunc returns the client's OnRemoteState callback. It runs on
// the client's reader, which also carries every pane's output, so it never
// blocks: it only marks a new state and wakes the window, and the frame
// reads the latest one (pollRemote). Pushes in a row coalesce.
func (a *App) remoteStateFunc(latest func() *remote.State) func(remote.State) {
	a.rlatest = latest
	return func(remote.State) {
		a.rpending.Store(true)
		a.pacer.Kick()
	}
}

// pollRemote takes the latest state, if a new one came. Called every frame.
func (a *App) pollRemote() {
	if a.rlatest == nil || !a.rpending.Swap(false) {
		return
	}
	if st := a.rlatest(); st != nil {
		a.setRemoteState(*st)
	}
}

// setRemoteState takes a state pushed by the daemon.
func (a *App) setRemoteState(st remote.State) {
	prev, first := a.rstate, !a.rhave
	a.rstate, a.rhave = st, true
	a.rbarText = ""
	if st.Enabled {
		a.rbarText = "remote"
		if len(st.Connected) > 0 {
			names := make([]string, len(st.Connected))
			for i, n := range st.Connected {
				names[i] = deviceLabel(n)
			}
			a.rbarText = "remote · " + gfx.Truncate(strings.Join(names, ", "), maxDeviceName*2)
		}
	}
	a.needRedraw = true
	if st.Err != "" && st.Err != prev.Err {
		a.toast("remote control: "+cleanText(st.Err, maxErrText), a.theme.Warn)
	}
	if !first && st.Enabled != prev.Enabled {
		switch {
		case !st.Enabled:
			a.toast("remote control off", a.theme.Dim)
		case len(st.Addrs) == 0:
			a.toast("remote control on, but no private address to listen on", a.theme.Warn)
		default:
			a.toast("remote control on", a.theme.Accent2)
		}
	}
	v := a.rview
	if v == nil {
		return
	}
	switch v.kind {
	case viewPair:
		switch {
		case st.Pairing != nil:
			v.shown, v.expires = true, st.Pairing.Expires
		case v.shown:
			a.endPairing(time.Now())
		case st.Err != "" && st.Err != prev.Err:
			a.rview = nil // the pairing did not start, the error is shown
		}
	case viewDevices:
		v.sel = min(v.sel, max(len(st.Devices)-1, 0))
		if v.ask != "" && !a.hasDevice(v.askID) {
			v.ask, v.askID = "", ""
		}
	}
}

func (a *App) hasDevice(id string) bool {
	for _, d := range a.rstate.Devices {
		if d.ID == id {
			return true
		}
	}
	return false
}

// endPairing closes the pairing screen once the daemon no longer has a
// pairing: expired, confirmed, or ended by the phone.
func (a *App) endPairing(now time.Time) {
	v := a.rview
	a.rview = nil
	a.needRedraw = true
	switch {
	case !now.Before(v.expires.Add(-time.Second)):
		a.toast("pairing expired", a.theme.Warn)
	case len(a.rstate.Devices) > v.devices:
		a.toast("phone paired: "+deviceLabel(a.rstate.Devices[len(a.rstate.Devices)-1].Name), a.theme.Accent2)
	case v.sent:
		a.toast("pairing failed", a.theme.Warn)
	}
}

// tickRemote closes a pairing screen whose pairing expired, or that never
// got one, without waiting for the daemon.
func (a *App) tickRemote(now time.Time) {
	v := a.rview
	if v == nil || v.kind != viewPair {
		return
	}
	switch {
	case v.shown && !now.Before(v.expires):
		a.rview = nil
		a.toast("pairing expired", a.theme.Warn)
	case !v.shown && now.Sub(v.opened) > pairStartWait:
		a.rview = nil
		a.toast("pairing did not start", a.theme.Warn)
	}
}

// remoteLost forgets the state when the daemon is lost: nothing is known
// about its server any more.
func (a *App) remoteLost() {
	a.rview = nil
	a.rstate, a.rhave, a.rbarText = remote.State{}, false, ""
}

// remoteBar is the status bar indicator, empty when remote control is off.
func (a *App) remoteBar() barItem {
	c := a.theme.Dim
	if len(a.rstate.Connected) > 0 {
		c = a.theme.Accent
	}
	return barItem{text: a.rbarText, color: c}
}

func (a *App) remoteKey(ev KeyEvent) {
	v := a.rview
	a.needRedraw = true
	plain := ev.Mods&(ModCtrl|ModMeta|ModAlt) == 0 && !ev.Repeat
	switch v.kind {
	case viewPair:
		switch {
		case ev.Name == "escape":
			// Sent even before the pairing shows: it ends one still on its way.
			a.rlink.Remote(server.RemoteCmd{Op: "reject"})
			a.rview = nil
		case ev.Name == "enter" && plain:
			// Only a code on screen can be confirmed, and only once.
			p := a.rstate.Pairing
			if p != nil && p.Code != "" && time.Now().Before(p.Expires) && !v.sent {
				v.sent = true
				a.rlink.Remote(server.RemoteCmd{Op: "confirm"})
			}
		}
	case viewRegen:
		switch {
		case ev.Name == "y" && plain:
			a.rlink.Remote(server.RemoteCmd{Op: "regen"})
			a.rview = nil
		case ev.Name == "n" || ev.Name == "escape":
			a.rview = nil
		}
	case viewDevices:
		devs := a.rstate.Devices
		if v.ask != "" {
			switch {
			case ev.Name == "y" && plain:
				a.rlink.Remote(server.RemoteCmd{Op: "revoke", ID: v.askID})
				v.ask, v.askID = "", ""
			case ev.Name == "n" || ev.Name == "escape":
				v.ask, v.askID = "", ""
			}
			return
		}
		ctrl := ev.Mods&ModCtrl != 0
		switch {
		case ev.Name == "escape" || ev.Name == "q" && plain:
			a.rview = nil
		case ev.Name == "down", ev.Name == "j" && plain, ctrl && (ev.Name == "j" || ev.Name == "n"):
			if len(devs) > 0 {
				v.sel = (v.sel + 1) % len(devs)
			}
		case ev.Name == "up", ev.Name == "k" && plain, ctrl && (ev.Name == "k" || ev.Name == "p"):
			if len(devs) > 0 {
				v.sel = (v.sel - 1 + len(devs)) % len(devs)
			}
		case ev.Name == "d" && plain:
			if v.sel < len(devs) {
				v.ask, v.askID = "revoke "+deviceLabel(devs[v.sel].Name)+"? y/n", devs[v.sel].ID
			}
		}
	}
}

// pairTexts returns the lines of the pairing screen under the QR code. The
// payload never appears in them.
func (a *App) pairTexts(now time.Time) []string {
	p := a.rstate.Pairing
	if a.rview == nil || !a.rview.shown || p == nil {
		return []string{"starting pairing…", "esc: cancel"}
	}
	left := max(p.Expires.Sub(now), 0).Round(time.Second)
	secs := int(left / time.Second)
	sec := itoa(secs % 60)
	if len(sec) < 2 {
		sec = "0" + sec
	}
	lines := []string{"Scan from the neonmux app on your phone", "expires in " + itoa(secs/60) + ":" + sec}
	if p.Code == "" {
		return append(lines, "esc: cancel")
	}
	return append(lines, p.Code, "enter: it matches · esc: cancel")
}

// qrCode returns the QR code of the pairing on screen, built once per
// payload. nil if it cannot be encoded.
func (v *remoteView) qrCode(payload string) *qr.Code {
	if v.qrFor != payload {
		v.qrFor = payload
		v.qr, _ = qr.Encode(payload, qr.M)
	}
	return v.qr
}

var (
	qrWhite = color.RGBA{255, 255, 255, 255}
	qrBlack = color.RGBA{0, 0, 0, 255}
)

// drawQR draws c at (x, y) with modules of m pixels, on a white quiet zone
// of 4 modules. Each run of dark modules in a row is one rectangle.
func drawQR(P *gfx.Painter, c *qr.Code, x, y, m float32) {
	side := float32(c.Size+8) * m
	P.Rect(x, y, side, side, qrWhite, 1)
	for row := 0; row < c.Size; row++ {
		for col := 0; col < c.Size; {
			if !c.Black(col, row) {
				col++
				continue
			}
			start := col
			for col < c.Size && c.Black(col, row) {
				col++
			}
			P.Rect(x+float32(start+4)*m, y+float32(row+4)*m, float32(col-start)*m, m, qrBlack, 1)
		}
	}
}

// remoteBox draws the veil and a framed box, like the palette.
func (a *App) remoteBox(P *gfx.Painter, x, y, w, h float32) {
	th := a.theme
	P.Rect(0, 0, float32(a.w), float32(a.h), th.BG, 0.55)
	P.Rect(x, y, w, h, th.BarBG, 0.98)
	r := image.Rect(int(x), int(y), int(x+w), int(y+h))
	P.Border(r, 2*a.px, th.Accent2, 1)
	P.Border(r.Inset(-int(a.px)), a.px, th.Accent2, 0.3)
}

func (a *App) drawRemote(ctx *Ctx, dst *ebiten.Image) {
	switch a.rview.kind {
	case viewPair:
		a.drawPairing(ctx)
	case viewDevices:
		a.drawDevices(ctx)
	case viewRegen:
		a.drawAsk(ctx, "regenerate the identity? every phone will be unpaired y/n")
	}
	ctx.P.Flush(dst)
}

func (a *App) drawPairing(ctx *Ctx) {
	f, th, P := a.uiFont, a.theme, ctx.P
	ch := float32(f.CellH)
	lines := a.pairTexts(ctx.Now)
	p := a.rstate.Pairing
	var code *qr.Code
	if a.rview.shown && p != nil {
		code = a.rview.qrCode(p.Payload)
	}
	// The QR code takes about half the screen's height, never less than 4
	// pixels per module.
	var m, side float32
	if code != nil {
		mods := float32(code.Size + 8)
		m = max(4, float32(int(float32(min(a.w, a.h))*0.5/mods)))
		side = mods * m
	}
	w, h := a.pairLayout(lines, side)
	codeH, step := a.pairCodeRow()
	x, y := (float32(a.w)-w)/2, max((float32(a.h)-h)/2, 0)
	a.remoteBox(P, x, y, w, h)
	ly := y + ch
	if code != nil {
		// Whole pixels, so every module has the same size.
		drawQR(P, code, float32(math.Floor(float64(x+(w-side)/2))), float32(math.Floor(float64(ly))), m)
		ly += side + ch*0.6
	} else if a.rview.shown {
		lines = append([]string{"QR code too large to draw"}, lines...)
	}
	for _, l := range lines {
		if p != nil && l == p.Code {
			// The code in large digits, spaced out.
			cf := a.pairCodeFont()
			cx := x + (w-step*float32(len(l)))/2
			for _, r := range l {
				P.Text(cf, cx, ly, string(r), th.Accent, 1)
				cx += step
			}
			ly += codeH
			continue
		}
		c := th.FG
		if strings.HasPrefix(l, "enter:") || strings.HasPrefix(l, "esc:") || strings.HasPrefix(l, "expires") {
			c = th.Dim
		}
		P.Text(f, x+(w-float32(gfx.TextWidth(f, l)))/2, ly, l, c, 1)
		ly += ch * 1.6
	}
}

// pairCodeScale is the size of the pairing code against the interface font.
const pairCodeScale = 3

// pairCodeFont is the face of the pairing code: Iosevka rasterized at its
// real size, so the digits stay sharp. Built on first use, dropped by
// loadFonts (size or display scale change).
func (a *App) pairCodeFont() *gfx.Font {
	if a.codeFont == nil {
		f, err := gfx.LoadFont(gfx.FontIosevka, a.cfg.UIFontSize*pairCodeScale, a.dsf)
		if err != nil {
			return a.uiFont // the embedded font does not fail
		}
		a.codeFont = f
	}
	return a.codeFont
}

// pairCodeRow is the height of the code's row on the pairing screen and
// the step between its digits.
func (a *App) pairCodeRow() (h, step float32) {
	cf := a.pairCodeFont()
	return float32(cf.CellH) + float32(a.uiFont.CellH)*0.6, float32(cf.CellW) * 1.4
}

// pairLayout is the size of the pairing box for its lines and a QR code of
// side pixels.
func (a *App) pairLayout(lines []string, side float32) (w, h float32) {
	f := a.uiFont
	cw, ch := float32(f.CellW), float32(f.CellH)
	code := ""
	if p := a.rstate.Pairing; p != nil {
		code = p.Code
	}
	codeH, step := a.pairCodeRow()
	w, h = side, ch*2+side
	for _, l := range lines {
		if code != "" && l == code {
			w = max(w, step*float32(len(l)))
			h += codeH
			continue
		}
		w = max(w, float32(gfx.TextWidth(f, l)))
		h += ch * 1.6
	}
	return w + cw*4, h
}

func (a *App) drawDevices(ctx *Ctx) {
	f, th, P := a.uiFont, a.theme, ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	v := a.rview
	devs := a.rstate.Devices
	visible := 10
	w := min(float32(a.w)*0.7, cw*72)
	h := ch * (2 + float32(visible)*1.2 + 2.4)
	x, y := (float32(a.w)-w)/2, float32(a.h)*0.16
	a.remoteBox(P, x, y, w, h)
	P.Rect(x, y, w, ch*1.6, th.Accent2, 0.15)
	P.Text(f, x+cw, y+ch*0.3, "REMOTE DEVICES", th.Accent2, 1)

	if v.sel < v.top {
		v.top = v.sel
	}
	if v.sel >= v.top+visible {
		v.top = v.sel - visible + 1
	}
	ly := y + ch*2
	if len(devs) == 0 {
		P.Text(f, x+cw*1.5, ly, "no paired phone", th.Dim, 1)
	}
	connected := func(name string) bool {
		for _, n := range a.rstate.Connected {
			if n == name {
				return true
			}
		}
		return false
	}
	for i := v.top; i < len(devs) && i < v.top+visible; i++ {
		d := devs[i]
		if i == v.sel {
			P.Rect(x+a.px*2, ly-ch*0.1, w-a.px*4, ch*1.2, th.Accent, 0.22)
			P.Rect(x+a.px*2, ly-ch*0.1, 3*a.px, ch*1.2, th.Accent, 1)
		}
		c := th.BarFG
		if i == v.sel {
			c = th.FG
		}
		seen := "never"
		if !d.LastSeen.IsZero() {
			seen = d.LastSeen.Local().Format("2006-01-02 15:04")
		}
		info := "added " + d.Added.Local().Format("2006-01-02") + "   seen " + seen
		iw := float32(gfx.TextWidth(f, info))
		P.Text(f, x+w-iw-cw, ly, info, th.Dim, 1)
		// The name stops before the dates, room kept for the mark.
		room := int((w-iw)/cw) - 4
		on := connected(d.Name)
		if on {
			room -= 12
		}
		nx := x + cw*1.5
		nx += P.Text(f, nx, ly, gfx.Truncate(deviceLabel(d.Name), max(room, 4)), c, 1)
		if on {
			P.Text(f, nx+cw, ly, "● connected", th.Accent, 1)
		}
		ly += ch * 1.2
	}
	footer, fc := "↑↓ move   d revoke   esc close", th.Dim
	if v.ask != "" {
		footer, fc = v.ask, th.Warn
	}
	P.Rect(x, y+h-ch*1.7, w, a.px, th.Border, 1)
	P.Text(f, x+cw, y+h-ch*1.3, gfx.Truncate(footer, int(w/cw)-2), fc, 1)
}

func (a *App) drawAsk(ctx *Ctx, prompt string) {
	f, th, P := a.uiFont, a.theme, ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	w := float32(gfx.TextWidth(f, prompt)) + cw*4
	h := ch * 3
	x, y := (float32(a.w)-w)/2, float32(a.h)*0.3
	a.remoteBox(P, x, y, w, h)
	P.Text(f, x+cw*2, y+ch, prompt, th.Warn, 1)
}

// ---- terminals opened from the phone (server remoteHost.NewTerm)

// phoneKey reports a session key as the daemon picks them for the phone:
// 16 hex digits, like newKey.
func phoneKey(k string) bool {
	if len(k) != 16 {
		return false
	}
	for _, c := range k {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func (a *App) hasTerm(key string) bool {
	for _, p := range a.panes {
		if t, ok := p.(*TermPane); ok && t.key == key {
			return true
		}
	}
	return false
}

// remoteNewTerm opens the terminal the phone asked for, as a new terminal
// of the window, under the key the daemon waits for, in the home
// directory (the phone has no current directory).
func (a *App) remoteNewTerm(key string) {
	if a.client == nil || !a.client.Alive() || !phoneKey(key) || a.hasTerm(key) {
		return
	}
	home, _ := os.UserHomeDir()
	p := NewTermPane(a.newID(), nil, home, a.paneEnv())
	p.key = key
	a.addPane(p)
	a.toast("terminal opened from the phone", a.theme.Accent2)
}

// remoteAdopt takes into the garage the terminals the daemon opened for
// the phone while no window was there, so they are in the layout.
func (a *App) remoteAdopt(keys []string) {
	if a.client == nil {
		return
	}
	n := 0
	for _, key := range keys {
		if !phoneKey(key) || a.hasTerm(key) || len(a.panes) >= maxTotalPanes {
			continue
		}
		t := NewTermPaneAttach(a.newID(), key, "", a.paneEnv())
		b := t.Base()
		b.parked, b.hidden = true, true
		a.panes = append(a.panes, t)
		a.startHidden(t, 0, 0)
		n++
	}
	if n == 0 {
		return
	}
	a.layoutDirty = true
	a.relayout()
	msg := "1 terminal opened from the phone is in the garage"
	if n > 1 {
		msg = itoa(n) + " terminals opened from the phone are in the garage"
	}
	a.toast(msg, a.theme.Accent2)
}
