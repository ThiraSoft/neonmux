package ui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/draw"
	"image/jpeg"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/cdp"
	"github.com/ThiraSoft/neonmux/internal/gfx"
)

// BrowserPane shows a web page rendered by a headless browser (cdp): the
// browser streams JPEG frames of the page (Page.startScreencast), the pane
// sends back the mouse and the keyboard.
type BrowserPane struct {
	paneBase
	app     *App
	url     string
	title   string
	loading bool
	err     error
	started bool
	held    *cdp.Browser // the browser this pane holds (acquireBrowser)
	sess    *cdp.Session
	closed  bool

	frame   *ebiten.Image
	shown   int // frames shown so far
	content image.Rectangle
	view    image.Rectangle // the page, under the address bar
	bar     int             // height of the address bar
	cellW   int
	dsf     float64
	vw, vh  int // viewport sent to the browser, in CSS pixels
	dirty   bool
	button  string // mouse button held on the page
	video   bool   // a video plays: lighter stream (startStream)
	away    bool   // out of sight: no stream (setAway)

	// Frames go from the reader goroutine to a decoding goroutine, then
	// to the main thread. Only the latest one counts.
	frames chan screencastFrame
	done   chan struct{}
	mu     sync.Mutex
	next   *image.RGBA // decoded, waiting for the main thread
	posted bool
}

type screencastFrame struct {
	Data      string `json:"data"`
	SessionID int    `json:"sessionId"`
}

// NewBrowserPane opens address u; the browser starts with the first layout.
func NewBrowserPane(id int, u string) *BrowserPane {
	p := &BrowserPane{url: u, loading: true}
	p.id = id
	return p
}

func (p *BrowserPane) Kind() string { return "browser" }
func (p *BrowserPane) Mode() string { return "" }
func (p *BrowserPane) Done() bool   { return false }

func (p *BrowserPane) Title() string {
	if p.title != "" && p.title != p.url && !strings.HasPrefix(p.url, p.title) {
		return p.title
	}
	if u, err := url.Parse(p.url); err == nil && u.Host != "" {
		return u.Host
	}
	return "browser"
}

func (p *BrowserPane) Info() string {
	if p.err == nil && p.loading {
		return "loading…"
	}
	return ""
}

func (p *BrowserPane) start(app *App) {
	p.started = true
	p.app = app
	p.frames = make(chan screencastFrame, 1)
	p.done = make(chan struct{})
	app.acquireBrowser(func(b *cdp.Browser, err error) {
		if err != nil {
			p.fail(err)
			return
		}
		p.held = b
		if p.closed {
			p.release()
			return
		}
		ua := app.web.ua
		go func() {
			s, err := p.open(b, ua)
			app.post(func() {
				switch {
				case err != nil:
					p.fail(err)
					p.release()
				case p.closed:
					s.Close()
					p.release()
				default:
					p.sess = s
					app.web.pages[s.TargetID] = p
					p.vw, p.vh = 0, 0
					p.applyViewport()
					s.Send("Page.navigate", map[string]any{"url": p.url})
				}
			})
		}()
	})
}

// titleBinding is called by titleScript on every change of title.
// Target.targetInfoChanged misses some of them (the <title> tag of a
// page loaded from data:, a title set by a script later on).
const titleBinding = "__neonmuxTitle"

// titleScript watches the title from the <head>, not the whole document:
// a busy page would call the binding on every change of its DOM.
const titleScript = `(() => {
	let last;
	const send = () => { if (document.title !== last) { last = document.title; window.` + titleBinding + `(last) } };
	const watch = () => {
		send();
		if (document.head) new MutationObserver(send).observe(document.head, {subtree: true, childList: true, characterData: true});
	};
	if (document.readyState === "loading") addEventListener("DOMContentLoaded", watch); else watch();
})();
(() => {
	let last;
	const media = () => {
		let n = 0;
		for (const v of document.querySelectorAll("video")) if (!v.paused && !v.ended) n++;
		const playing = n > 0 ? "1" : "0";
		if (playing !== last) { last = playing; window.` + videoBinding + `(playing) }
	};
	// Media events do not bubble: they are caught on the way down.
	for (const e of ["play", "playing", "pause", "ended", "emptied"]) addEventListener(e, media, true);
})()`

// videoBinding says whether a video is playing ("1") or not ("0"): the
// stream then goes down to one pixel per CSS pixel and a lower JPEG
// quality, which roughly quarters the work per frame.
const videoBinding = "__neonmuxVideo"

// open creates the page and prepares it. It runs off the main thread.
func (p *BrowserPane) open(b *cdp.Browser, ua string) (*cdp.Session, error) {
	var s *cdp.Session
	s, err := b.NewPage(func(method string, params json.RawMessage) { p.event(s, method, params) })
	if err != nil {
		return nil, err
	}
	setup := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Runtime.addBinding", map[string]any{"name": titleBinding}},
		{"Runtime.addBinding", map[string]any{"name": videoBinding}},
		{"Runtime.enable", nil}, // Runtime.bindingCalled only comes with it
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": titleScript}},
		{"Emulation.setUserAgentOverride", map[string]any{"userAgent": ua}},
		// Neon themes are dark: pages that offer a dark look use it.
		{"Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-color-scheme", "value": "dark"}}}},
		// The page thinks it has focus: caret, focus and blur events.
		{"Emulation.setFocusEmulationEnabled", map[string]any{"enabled": true}},
	}
	for _, c := range setup {
		if err := s.Call(c.method, c.params, nil); err != nil {
			s.Close()
			return nil, err
		}
	}
	go p.decode(s)
	return s, nil
}

// event runs on the reader goroutine: it never blocks.
func (p *BrowserPane) event(s *cdp.Session, method string, params json.RawMessage) {
	switch method {
	case "Page.screencastFrame":
		var f screencastFrame
		if json.Unmarshal(params, &f) != nil {
			return
		}
		// Acknowledged on arrival: the browser renders the next frame
		// while this one is decoded. Only the latest one is kept.
		s.Send("Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID})
		for {
			select {
			case p.frames <- f:
				return
			default:
			}
			select {
			case <-p.frames:
			default:
			}
		}
	case "Page.frameStartedLoading", "Page.frameStoppedLoading":
		var ev struct {
			FrameID string `json:"frameId"`
		}
		if json.Unmarshal(params, &ev) == nil && ev.FrameID == s.TargetID {
			loading := method == "Page.frameStartedLoading"
			p.app.post(func() { p.loading, p.dirty = loading, true })
		}
	case "Page.frameNavigated", "Page.navigatedWithinDocument":
		var ev struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
				Fragment string `json:"urlFragment"`
			} `json:"frame"`
			FrameID string `json:"frameId"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		u := ev.URL
		if method == "Page.frameNavigated" {
			if ev.Frame.ParentID != "" {
				return
			}
			u = ev.Frame.URL + ev.Frame.Fragment
		} else if ev.FrameID != s.TargetID {
			return
		}
		p.app.post(func() {
			if p.url != u {
				p.url, p.dirty = u, true
				p.app.layoutDirty = true
			}
		})
	case "Runtime.bindingCalled":
		var ev struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		switch ev.Name {
		case titleBinding:
			p.app.post(func() { p.app.browserTarget(targetInfo{TargetID: s.TargetID, Title: ev.Payload}) })
		case videoBinding:
			video := ev.Payload == "1"
			p.app.post(func() {
				if p.video != video {
					p.video = video
					p.startStream()
				}
			})
		}
	case "Page.javascriptDialogOpening":
		// alert, confirm, prompt or beforeunload would freeze the page:
		// they are accepted, and their message shows as a toast.
		var ev struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		_ = json.Unmarshal(params, &ev)
		s.Send("Page.handleJavaScriptDialog", map[string]any{"accept": true})
		if ev.Message != "" {
			p.app.post(func() { p.app.toast("page: "+gfx.Truncate(ev.Message, 80), p.app.theme.Accent2) })
		}
	}
}

// decode turns the frames into pixels for the main thread.
func (p *BrowserPane) decode(s *cdp.Session) {
	for {
		var f screencastFrame
		select {
		case <-p.done:
			return
		case f = <-p.frames:
		}
		img, err := jpeg.Decode(base64.NewDecoder(base64.StdEncoding, bytes.NewReader([]byte(f.Data))))
		if err != nil {
			continue
		}
		rgba := image.NewRGBA(img.Bounds().Sub(img.Bounds().Min))
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		p.mu.Lock()
		p.next = rgba
		post := !p.posted
		p.posted = true
		p.mu.Unlock()
		if post {
			p.app.post(p.upload)
		}
	}
}

// upload puts the latest decoded frame on the GPU (main thread).
func (p *BrowserPane) upload() {
	p.mu.Lock()
	img := p.next
	p.next, p.posted = nil, false
	p.mu.Unlock()
	if img == nil || p.closed {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if p.frame != nil && p.frame.Bounds().Size() != image.Pt(w, h) {
		p.frame.Deallocate()
		p.frame = nil
	}
	if p.frame == nil {
		p.frame = ebiten.NewImage(w, h)
	}
	p.frame.WritePixels(img.Pix)
	p.shown++
	p.dirty = true
}

// release lets go of the browser.
func (p *BrowserPane) release() {
	b := p.held
	p.held = nil
	p.app.releaseBrowser(b)
}

func (p *BrowserPane) fail(err error) {
	p.err, p.loading, p.dirty = err, false, true
}

// lost is called when the browser went away: the pane shows why, and
// starts it again on the next address.
func (p *BrowserPane) lost(err error) {
	p.sess, p.held = nil, nil
	p.started = false
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
	p.fail(err)
}

// navigate opens u in the pane.
func (p *BrowserPane) navigate(u string) {
	p.url, p.loading, p.dirty = u, true, true
	if p.app != nil {
		p.app.layoutDirty = true
	}
	if p.sess != nil {
		p.sess.Send("Page.navigate", map[string]any{"url": u})
		return
	}
	if p.err != nil && p.app != nil {
		// The browser went away or never started: try again.
		p.err = nil
		p.start(p.app)
	}
}

// applyViewport sizes the page to the pane, in CSS pixels, and restarts
// the stream at the pane's size in physical pixels.
func (p *BrowserPane) applyViewport() {
	if p.sess == nil || p.view.Dx() <= 0 || p.view.Dy() <= 0 {
		return
	}
	dsf := max(p.dsf, 1)
	w, h := max(1, int(float64(p.view.Dx())/dsf)), max(1, int(float64(p.view.Dy())/dsf))
	if w == p.vw && h == p.vh {
		return
	}
	p.vw, p.vh = w, h
	p.sess.Send("Emulation.setDeviceMetricsOverride", map[string]any{
		"width": w, "height": h, "deviceScaleFactor": dsf, "mobile": false,
	})
	p.startStream()
}

// startStream (re)starts the stream of frames: at the pane's size in
// physical pixels, or in CSS pixels at a lower quality while a video
// plays. A pane out of sight gets none.
func (p *BrowserPane) startStream() {
	if p.sess == nil || p.view.Dx() <= 0 || p.view.Dy() <= 0 {
		return
	}
	p.sess.Send("Page.stopScreencast", nil)
	if p.away {
		return
	}
	w, h, q := p.view.Dx(), p.view.Dy(), 80
	if p.video {
		dsf := max(p.dsf, 1)
		w, h, q = max(1, int(float64(w)/dsf)), max(1, int(float64(h)/dsf)), 60
	}
	p.sess.Send("Page.startScreencast", map[string]any{"format": "jpeg", "quality": q, "maxWidth": w, "maxHeight": h})
}

// setAway stops the stream while the pane is out of sight (garage,
// another workspace, monocle). The page keeps running, sound included:
// music goes on from the garage.
func (p *BrowserPane) setAway(away bool) {
	p.away = away
	p.startStream()
}

func (p *BrowserPane) Layout(ctx *Ctx, content image.Rectangle) {
	p.content = content
	p.dsf = ctx.DSF
	p.cellW = ctx.UI.CellW
	p.bar = ctx.UI.CellH + int(6*ctx.Px)
	p.view = image.Rect(content.Min.X, min(content.Min.Y+p.bar, content.Max.Y), content.Max.X, content.Max.Y)
	if !p.started && p.err == nil && !p.view.Empty() {
		p.start(ctx.App)
	}
	p.applyViewport()
	p.dirty = true
}

func (p *BrowserPane) Update(ctx *Ctx) bool {
	if p.hidden != p.away {
		p.setAway(p.hidden)
	}
	d := p.dirty || p.loading && p.err == nil
	p.dirty = false
	return d
}

// AnimFPS animates the loading line.
func (p *BrowserPane) AnimFPS(ctx *Ctx) int {
	if p.loading && p.err == nil {
		return 30
	}
	return 0
}

func (p *BrowserPane) NextWake(ctx *Ctx) time.Time { return time.Time{} }

func (p *BrowserPane) Close() {
	if p.closed {
		return
	}
	p.closed = true
	if p.done != nil {
		close(p.done)
		p.done = nil
	}
	if p.sess != nil {
		delete(p.app.web.pages, p.sess.TargetID)
		p.sess.Close()
		p.sess = nil
	}
	if p.held != nil {
		p.release()
	}
	if p.frame != nil {
		p.frame.Deallocate()
		p.frame = nil
	}
}

// cdpModifiers encodes the modifiers as CDP expects them.
func cdpModifiers(m Mods) int {
	n := 0
	if m&ModAlt != 0 {
		n |= 1
	}
	if m&ModCtrl != 0 {
		n |= 2
	}
	if m&ModMeta != 0 {
		n |= 4
	}
	if m&ModShift != 0 {
		n |= 8
	}
	return n
}

// cdpKey describes a non-printable key for Input.dispatchKeyEvent.
type cdpKey struct {
	key  string
	vk   int
	text string
}

var cdpKeys = map[string]cdpKey{
	"enter": {"Enter", 13, "\r"}, "tab": {"Tab", 9, ""}, "backspace": {"Backspace", 8, ""},
	"escape": {"Escape", 27, ""}, "delete": {"Delete", 46, ""}, "insert": {"Insert", 45, ""},
	"up": {"ArrowUp", 38, ""}, "down": {"ArrowDown", 40, ""}, "left": {"ArrowLeft", 37, ""}, "right": {"ArrowRight", 39, ""},
	"home": {"Home", 36, ""}, "end": {"End", 35, ""}, "pageup": {"PageUp", 33, ""}, "pagedown": {"PageDown", 34, ""},
}

func (p *BrowserPane) sendKey(key, code string, vk int, text string, mods Mods) {
	m := cdpModifiers(mods)
	down := map[string]any{"type": "rawKeyDown", "key": key, "code": code, "windowsVirtualKeyCode": vk, "modifiers": m}
	if text != "" {
		down["type"], down["text"], down["unmodifiedText"] = "keyDown", text, text
	}
	p.sess.Send("Input.dispatchKeyEvent", down)
	p.sess.Send("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "code": code, "windowsVirtualKeyCode": vk, "modifiers": m})
}

func (p *BrowserPane) Key(ctx *Ctx, ev KeyEvent) bool {
	ctrl, alt := ev.Mods&ModCtrl != 0, ev.Mods&ModAlt != 0
	switch {
	case ctrl && ev.Name == "l", ev.Name == "f6":
		ctx.App.promptBrowser(p)
		return true
	case ctrl && ev.Name == "d":
		ctx.App.toggleBookmark(p)
		return true
	case ctrl && ev.Name == "r", ev.Name == "f5":
		p.reload()
		return true
	case alt && ev.Name == "left":
		p.history(-1)
		return true
	case alt && ev.Name == "right":
		p.history(1)
		return true
	}
	if p.sess == nil {
		return false
	}
	if k, ok := cdpKeys[ev.Name]; ok {
		p.sendKey(k.key, k.key, k.vk, k.text, ev.Mods)
		return true
	}
	// ctrl+letter: shortcuts of the page (ctrl+a, ctrl+z, ctrl+f…).
	if ctrl && len(ev.Name) == 1 && ev.Name[0] >= 'a' && ev.Name[0] <= 'z' {
		up := strings.ToUpper(ev.Name)
		p.sendKey(ev.Name, "Key"+up, int(up[0]), "", ev.Mods)
		return true
	}
	return false
}

func (p *BrowserPane) Chars(ctx *Ctx, rs []rune) {
	if p.sess == nil {
		return
	}
	for _, r := range rs {
		if !unicode.IsPrint(r) {
			continue
		}
		s := string(r)
		vk := 0
		if u := unicode.ToUpper(r); u < 128 && (unicode.IsLetter(u) || unicode.IsDigit(u)) {
			vk = int(u)
		}
		p.sendKey(s, "", vk, s, ctx.Mods&ModShift)
	}
}

// paste types s into the focused field of the page.
func (p *BrowserPane) paste(s string) {
	if p.sess != nil && s != "" {
		p.sess.Send("Input.insertText", map[string]any{"text": s})
	}
}

// selection reads the selected text of the page, off the main thread.
func (p *BrowserPane) selection(done func(string)) {
	s := p.sess
	if s == nil {
		return
	}
	go func() {
		var r struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = s.Call("Runtime.evaluate", map[string]any{"expression": "String(window.getSelection())", "returnByValue": true}, &r)
		done(r.Result.Value)
	}()
}

func (p *BrowserPane) reload() {
	if p.sess != nil {
		p.loading, p.dirty = true, true
		p.sess.Send("Page.reload", nil)
	} else {
		p.navigate(p.url)
	}
}

func (p *BrowserPane) history(d int) {
	if p.sess == nil {
		return
	}
	expr := "history.back()"
	if d > 0 {
		expr = "history.forward()"
	}
	p.sess.Send("Runtime.evaluate", map[string]any{"expression": expr})
}

// Bar buttons, in cells from the left edge of the bar: back, forward,
// reload, then the address.
const (
	barBack    = 0
	barForward = 2
	barReload  = 4
	barAddress = 6
)

// pagePoint converts a screen point into CSS pixels of the page.
func (p *BrowserPane) pagePoint(x, y int) (float64, float64) {
	dsf := max(p.dsf, 1)
	cx := min(max(x, p.view.Min.X), p.view.Max.X-1)
	cy := min(max(y, p.view.Min.Y), p.view.Max.Y-1)
	return float64(cx-p.view.Min.X) / dsf, float64(cy-p.view.Min.Y) / dsf
}

func (p *BrowserPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	if ev.Kind == MousePress && ev.Y < p.view.Min.Y {
		if ev.Button != ebiten.MouseButtonLeft || p.cellW == 0 {
			return true
		}
		switch cell := (ev.X - p.content.Min.X - p.cellW/2) / p.cellW; {
		case ev.X >= p.content.Max.X-2*p.cellW:
			ctx.App.toggleBookmark(p)
		case cell == barBack:
			p.history(-1)
		case cell == barForward:
			p.history(1)
		case cell == barReload:
			p.reload()
		case cell >= barAddress:
			ctx.App.promptBrowser(p)
		}
		return true
	}
	if p.sess == nil {
		return true
	}
	x, y := p.pagePoint(ev.X, ev.Y)
	m := cdpModifiers(ev.Mods)
	button := map[ebiten.MouseButton]string{ebiten.MouseButtonLeft: "left", ebiten.MouseButtonMiddle: "middle", ebiten.MouseButtonRight: "right"}[ev.Button]
	buttons := map[string]int{"left": 1, "right": 2, "middle": 4}
	switch ev.Kind {
	case MousePress:
		if ev.Y < p.view.Min.Y {
			return true
		}
		p.button = button
		p.sess.Send("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "button": button,
			"buttons": buttons[button], "clickCount": max(ev.Clicks, 1), "modifiers": m})
	case MouseRelease:
		if p.button == "" {
			return true
		}
		p.sess.Send("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x, "y": y, "button": p.button,
			"clickCount": max(ev.Clicks, 1), "modifiers": m})
		p.button = ""
	case MouseMove:
		if p.button == "" && !image.Pt(ev.X, ev.Y).In(p.view) {
			return true
		}
		b := p.button
		if b == "" {
			b = "none"
		}
		p.sess.Send("Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": b,
			"buttons": buttons[p.button], "modifiers": m})
	case MouseWheel:
		dx, dy := 0.0, -ev.Wheel*100
		if ev.Mods&ModShift != 0 {
			dx, dy = dy, 0
		}
		p.sess.Send("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": x, "y": y,
			"deltaX": dx, "deltaY": dy, "modifiers": m})
	}
	return true
}

func (p *BrowserPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	th, P, f := ctx.Theme, ctx.P, ctx.UI
	view := image.Rect(content.Min.X, min(content.Min.Y+p.bar, content.Max.Y), content.Max.X, content.Max.Y)
	sub := dst.SubImage(view).(*ebiten.Image)
	sub.Fill(th.BG)
	switch {
	case p.err != nil:
		for i, l := range wrapWords(p.err.Error(), (content.Dx()-16)/f.CellW) {
			P.Text(f, float32(view.Min.X+8), float32(view.Min.Y+8+i*f.CellH), l, th.Warn, 1)
		}
	case p.frame != nil:
		op := &ebiten.DrawImageOptions{}
		fw := p.frame.Bounds().Dx()
		if s := float64(view.Dx()) / float64(fw); s != 1 {
			op.GeoM.Scale(s, s)
			op.Filter = ebiten.FilterLinear
		}
		op.GeoM.Translate(float64(view.Min.X), float64(view.Min.Y))
		sub.DrawImage(p.frame, op)
	default:
		P.Text(f, float32(view.Min.X+8), float32(view.Min.Y+8), "starting the browser…", th.Dim, 1)
	}

	// Address bar.
	bx, by := float32(content.Min.X), float32(content.Min.Y)
	bw, bh := float32(content.Dx()), float32(p.bar)
	P.Rect(bx, by, bw, bh, th.BarBG, 1)
	ty := by + (bh-float32(f.CellH))/2
	x0 := bx + float32(f.CellW)/2
	cell := func(n int) float32 { return x0 + float32(n*f.CellW) }
	P.Text(f, cell(barBack), ty, "‹", th.Accent, 1)
	P.Text(f, cell(barForward), ty, "›", th.Accent, 1)
	P.Text(f, cell(barReload), ty, "↻", th.Accent, 1)
	cols := int((bw - cell(barAddress) + bx) / float32(f.CellW))
	P.Text(f, cell(barAddress), ty, gfx.Truncate(p.url, max(cols-3, 1)), th.BarFG, 1)
	// The star, at the right end: lit when the page is a bookmark.
	star, starC := "☆", th.Dim
	if ctx.App.isBookmarked(p.url) {
		star, starC = "★", th.Accent
	}
	P.Text(f, bx+bw-float32(f.CellW)*1.5, ty, star, starC, 1)
	if p.loading && p.err == nil {
		// A segment that sweeps the bottom of the bar.
		t := float32(ctx.Now.UnixMilli()%1200) / 1200
		seg := bw / 4
		x0, x1 := max(bx+(bw+seg)*t-seg, bx), min(bx+(bw+seg)*t, bx+bw)
		P.Rect(x0, by+bh-2*ctx.Px, x1-x0, 2*ctx.Px, th.Accent, 1)
	}
}
