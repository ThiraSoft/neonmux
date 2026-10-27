package ui

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/cdp"
)

func TestBrowserURL(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"":                       "",
		"https://go.dev/doc":     "https://go.dev/doc",
		"go.dev":                 "https://go.dev",
		"go.dev/doc?x=1":         "https://go.dev/doc?x=1",
		"localhost:8080":         "http://localhost:8080",
		"127.0.0.1:3000/api":     "http://127.0.0.1:3000/api",
		"about:blank":            "about:blank",
		"/tmp/a b.html":          "file:///tmp/a%20b.html",
		"~/index.html":           "file://" + filepath.Join(home, "index.html"),
		"golang generics":        "https://duckduckgo.com/?q=golang+generics",
		"neonmux":                "https://duckduckgo.com/?q=neonmux",
		"  https://example.org ": "https://example.org",
	} {
		if got := browserURL(in); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrowserPaneSaved(t *testing.T) {
	a, _ := coreApp(t, 1)
	a.panes = append(a.panes, NewBrowserPane(a.newID(), "https://go.dev"))
	st := a.layoutState()
	if len(st.Panes) != 2 || st.Panes[1].Kind != "browser" || st.Panes[1].Path != "https://go.dev" {
		t.Fatalf("saved panes: %+v", st.Panes)
	}
}

// pump runs what the goroutines posted and draws, until ok or timeout.
func pump(t *testing.T, a *App, screen *ebiten.Image, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(20 * time.Millisecond):
		}
		ctx := a.frameCtx(time.Now())
		for _, p := range a.panes {
			p.Update(ctx)
		}
		a.needRedraw = true
		a.Draw(screen)
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestBrowserPaneDrivesARealBrowser opens a page in the browser found on
// the machine, types into it, clicks it, follows a popup, then checks that
// the browser quits with the last pane.
func TestBrowserPaneDrivesARealBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := cdp.Find(""); err != nil {
		t.Skip(err)
	}
	t.Setenv("NEONMUX_STATE", t.TempDir())
	a, _ := coreApp(t, 0)
	screen := ebiten.NewImage(a.w, a.h)
	page := `<title>neon</title><body style="margin:0">
<input id="q" autofocus oninput="document.title=this.value" style="position:absolute;left:0;top:0;width:200px;height:40px">
<button id="b" style="position:absolute;left:0;top:100px;width:200px;height:100px"
 onclick="document.title='clicked'">go</button>
<a id="pop" target="_blank" href="popped.html"
 style="position:absolute;left:0;top:300px;width:200px;height:50px;display:block">pop</a>
<button style="position:absolute;left:0;top:220px;width:200px;height:60px" onclick="v.play()">play</button>
<video id="v" muted style="position:absolute;left:0;top:400px;width:64px;height:64px"
 onplaying="document.title='playing'" onpause="document.title='paused'"></video>
<script>
const c = document.createElement("canvas"); c.width = c.height = 64;
const g = c.getContext("2d");
setInterval(() => { g.fillStyle = "#" + Math.floor(Math.random() * 4096).toString(16).padStart(3, "0"); g.fillRect(0, 0, 64, 64) }, 40);
v.srcObject = c.captureStream();
</script></body>`
	// Pages from files: the test needs no network.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "popped.html"), []byte("<title>popped</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := browserURL(filepath.Join(dir, "index.html"))
	p := NewBrowserPane(a.newID(), home)
	a.addPane(p)
	settle(a, screen)

	pump(t, a, screen, "first frame and title", func() bool {
		return p.frame != nil && p.title == "neon"
	})
	if p.err != nil {
		t.Fatal(p.err)
	}
	if p.vw*2 != p.view.Dx() || p.frame.Bounds().Dx() > p.view.Dx() {
		t.Errorf("viewport %d css px for %d px of pane, frame %v", p.vw, p.view.Dx(), p.frame.Bounds())
	}

	ctx := a.frameCtx(time.Now())
	p.Chars(ctx, []rune("héllo"))
	p.Key(ctx, KeyEvent{Name: "backspace"})
	pump(t, a, screen, "typed text", func() bool { return p.title == "héll" })

	// The button sits at 100..200 CSS px from the top of the page.
	click := func(cssY int) {
		pt := image.Pt(p.view.Min.X+20, p.view.Min.Y+cssY*2)
		p.Mouse(ctx, MouseEvent{Kind: MousePress, X: pt.X, Y: pt.Y, Button: ebiten.MouseButtonLeft, Clicks: 1})
		p.Mouse(ctx, MouseEvent{Kind: MouseRelease, X: pt.X, Y: pt.Y, Button: ebiten.MouseButtonLeft, Clicks: 1})
	}
	// Focus moving around leaves the browser running.
	a.arrive()
	if a.web.b == nil || p.sess == nil {
		t.Fatal("browser stopped when the focus moved")
	}
	click(150)
	pump(t, a, screen, "click", func() bool { return p.title == "clicked" })

	// A video lightens the stream. Out of sight, the stream stops and the
	// video plays on (sound from the garage).
	click(250)
	pump(t, a, screen, "video playing", func() bool { return p.video && p.title == "playing" })
	pump(t, a, screen, "frames in CSS pixels", func() bool { return p.frame.Bounds().Dx() <= p.view.Dx()/2 })
	p.hidden = true
	pump(t, a, screen, "out of sight", func() bool { return p.away })
	// A frame already on its way may still land.
	pump(t, a, screen, "a moment", func() bool { time.Sleep(100 * time.Millisecond); return true })
	shown := p.shown
	for range 10 {
		pump(t, a, screen, "a moment", func() bool { time.Sleep(30 * time.Millisecond); return true })
	}
	if p.shown != shown || p.title != "playing" {
		t.Errorf("out of sight: %d new frames, title %q", p.shown-shown, p.title)
	}
	p.hidden = false
	pump(t, a, screen, "stream back", func() bool { return !p.away && p.shown > shown })

	click(320)
	pump(t, a, screen, "popup opened in the pane", func() bool { return p.title == "popped" && p.url == browserURL(filepath.Join(dir, "popped.html")) })
	if len(a.panes) != 1 {
		t.Errorf("%d panes after the popup", len(a.panes))
	}

	b := a.web.b
	a.closePane(0)
	select {
	case <-b.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("browser still running after its last pane closed")
	}
	if a.web.b != nil || len(a.web.pages) != 0 {
		t.Errorf("host not released: %+v", a.web)
	}
}
