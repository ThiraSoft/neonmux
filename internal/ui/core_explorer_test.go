package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// explorerDir creates a directory with a subdirectory, a text file and an
// image.
func explorerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644)
	writePNG(t, filepath.Join(dir, "pic.png"), 8, 8)
	return dir
}

// openExplorerOn opens the explorer on dir above the focused pane and waits
// for the directory to be read.
func openExplorerOn(t *testing.T, a *App, dir string) *Explorer {
	t.Helper()
	a.openExplorer(dir)
	e := a.explorer
	if e == nil {
		t.Fatal("no explorer")
	}
	a.updateExplorer(a.frameCtx(time.Now()))
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	return e
}

// The explorer is not a pane: above a shell at its prompt, it covers the
// content of that shell, below its header, without adding a pane.
func TestExplorerCoversShellAtPrompt(t *testing.T) {
	a, ts := keysApp(t, 2)
	a.setFocus(1)
	e := openExplorerOn(t, a, explorerDir(t))
	if len(a.panes) != 2 || a.countPanes(true) != 0 {
		t.Fatalf("%d panes, %d floating: the explorer must not be one", len(a.panes), a.countPanes(true))
	}
	if e.host != ts[1] {
		t.Fatalf("host %v, want the focused shell", e.host)
	}
	_, header, _, _ := a.metrics()
	r := ts[1].target
	box, content := a.explorerRects()
	if box.Min.Y != r.Min.Y+header || box.Min.X != r.Min.X || box.Max != r.Max {
		t.Errorf("frame %v, want the shell %v below its header", box, r)
	}
	if !content.In(box) || content.Empty() {
		t.Errorf("content %v outside the frame %v", content, box)
	}
}

// Above a terminal that runs a program, the explorer floats in the center
// of the screen and acts on no terminal.
func TestExplorerFloatsOverBusyTerminal(t *testing.T) {
	a, ts := keysApp(t, 2)
	ts[0].s.(*fakeBackend).fg = "claude"
	e := openExplorerOn(t, a, explorerDir(t))
	if e.host != nil {
		t.Fatalf("host %v, want none", e.host)
	}
	box, _ := a.explorerRects()
	area := a.floatArea()
	center := box.Min.Add(box.Max).Div(2)
	want := area.Min.Add(area.Max).Div(2)
	if d := center.Sub(want); d.X*d.X+d.Y*d.Y > 4 {
		t.Errorf("center %v, want %v", center, want)
	}
	if box.Dx() >= area.Dx() || box.Dy() >= area.Dy() || box.Dx() < area.Dx()/2 {
		t.Errorf("frame %v in %v", box, area)
	}
	if len(a.panes) != 2 {
		t.Errorf("%d panes", len(a.panes))
	}
}

// keysApp is coreApp with the default shortcuts.
func keysApp(t *testing.T, n int) (*App, []*TermPane) {
	t.Helper()
	a, ts := coreApp(t, n)
	a.mod = ParseMods(a.cfg.Mod)
	a.buildBindings()
	return a, ts
}

// press feeds keys to the app the way the keyboard does (runScript):
// "j", "enter", "text:abc", "cmd+e".
func press(a *App, toks ...string) {
	for _, tok := range toks {
		a.script, a.scriptAt = []string{tok}, time.Time{}
		a.runScript(a.frameCtx(time.Now()))
	}
}

// While it is open, the explorer takes the keyboard: nothing reaches the
// shell below. q closes it, and so does its shortcut pressed again.
func TestExplorerTakesTheKeyboard(t *testing.T) {
	a, ts := keysApp(t, 1)
	be := ts[0].s.(*fakeBackend)
	e := openExplorerOn(t, a, explorerDir(t))
	press(a, "j", "text:k", "down")
	if got := be.sent(); got != "" {
		t.Fatalf("the shell received %q", got)
	}
	if en, _ := e.current(); en.name != "notes.txt" {
		t.Errorf("selection %q, want notes.txt", en.name)
	}
	press(a, "q")
	if a.explorer != nil || len(a.panes) != 1 {
		t.Fatalf("q: explorer %v, %d panes", a.explorer, len(a.panes))
	}
	press(a, a.hintFor("files.open"))
	if a.explorer == nil {
		t.Fatal("the shortcut does not open the explorer")
	}
	press(a, a.hintFor("files.open"))
	if a.explorer != nil {
		t.Fatal("the shortcut pressed again must close it")
	}
	if got := be.sent(); got != "" {
		t.Fatalf("the shell received %q", got)
	}
}

// esc first clears the filter, then closes the explorer.
func TestExplorerEscapeClearsFilterFirst(t *testing.T) {
	a, _ := keysApp(t, 1)
	e := openExplorerOn(t, a, explorerDir(t))
	press(a, "/", "text:pic")
	if len(e.view) != 1 {
		t.Fatalf("filter %q: %v", string(e.filter), names(e))
	}
	press(a, "escape")
	if a.explorer == nil || len(e.filter) != 0 || len(e.view) != 3 {
		t.Fatalf("after the first esc: open %v, filter %q, %d items", a.explorer != nil, string(e.filter), len(e.view))
	}
	press(a, "escape")
	if a.explorer != nil {
		t.Fatal("the second esc must close the explorer")
	}
}

// A global shortcut still works with the explorer open, and closes it on
// the way.
func TestExplorerShortcutClosesIt(t *testing.T) {
	a, _ := keysApp(t, 2)
	openExplorerOn(t, a, explorerDir(t))
	press(a, a.hintFor("focus.2"))
	if a.explorer != nil || a.focus != 1 {
		t.Fatalf("explorer %v, focus %d", a.explorer, a.focus)
	}
}

// Above a shell at its prompt, the explorer acts on that shell: a directory
// opens in the explorer, a text file in the editor started right in the
// shell, then the explorer closes. No pane is added.
func TestExplorerOpensFileInShell(t *testing.T) {
	a, ts := keysApp(t, 1)
	a.cfg.Editor = "vi"
	be := ts[0].s.(*fakeBackend)
	dir := explorerDir(t)
	e := openExplorerOn(t, a, dir)
	press(a, "enter") // sub/
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	if a.explorer != e || e.dir != filepath.Join(dir, "sub") {
		t.Fatalf("a directory must open in the explorer: open %v, dir %s", a.explorer != nil, e.dir)
	}
	press(a, "h")
	drain(t, a, 2*time.Second, func() bool { return !e.listing })
	selectName(t, e, "notes.txt")
	press(a, "enter")
	if got, want := be.sent(), "\x15vi '"+filepath.Join(dir, "notes.txt")+"'\r"; got != want {
		t.Fatalf("the shell received %q, want %q", got, want)
	}
	if a.explorer != nil || len(a.panes) != 1 {
		t.Fatalf("explorer %v, %d panes", a.explorer, len(a.panes))
	}
}

// c moves the covered shell into the selected directory, then closes the
// explorer.
func TestExplorerCdInShell(t *testing.T) {
	a, ts := keysApp(t, 2)
	be0, be1 := ts[0].s.(*fakeBackend), ts[1].s.(*fakeBackend)
	a.setFocus(1)
	dir := explorerDir(t)
	openExplorerOn(t, a, dir)
	press(a, "c") // on sub/
	if got, want := be1.sent(), "\x15cd -- '"+filepath.Join(dir, "sub")+"'\r"; got != want {
		t.Fatalf("the covered shell received %q, want %q", got, want)
	}
	if got := be0.sent(); got != "" {
		t.Errorf("the other shell received %q", got)
	}
	if a.explorer != nil {
		t.Error("the explorer must close")
	}
}

// y copies the path, then closes the explorer.
func TestExplorerCopyCloses(t *testing.T) {
	fakeClipboard(t, "/bin/cat > /dev/null\n")
	a, _ := keysApp(t, 1)
	openExplorerOn(t, a, explorerDir(t))
	press(a, "y")
	if a.explorer != nil {
		t.Error("the explorer must close")
	}
}

// Above a terminal that runs a program, c types nothing: the explorer says
// why and stays open.
func TestExplorerCdRefusedOnBusyTerminal(t *testing.T) {
	a, ts := keysApp(t, 1)
	be := ts[0].s.(*fakeBackend)
	be.fg = "claude"
	openExplorerOn(t, a, explorerDir(t))
	press(a, "c")
	if got := be.sent(); got != "" {
		t.Fatalf("the busy terminal received %q", got)
	}
	if a.explorer == nil || !strings.Contains(lastToast(a), "claude") {
		t.Fatalf("open %v, toast %q", a.explorer != nil, lastToast(a))
	}
}

// Opening a directory (neonmux open, a drop) opens the explorer on it, not
// a pane.
func TestOpenPathDirOpensExplorer(t *testing.T) {
	a, _ := keysApp(t, 1)
	dir := explorerDir(t)
	a.OpenPath(dir)
	if a.explorer == nil || a.explorer.dir != dir || len(a.panes) != 1 {
		t.Fatalf("explorer %v, %d panes", a.explorer, len(a.panes))
	}
}

// When the covered shell leaves the screen (closed, sent to the garage),
// the explorer closes with it.
func TestExplorerClosesWithItsShell(t *testing.T) {
	for _, leave := range []func(a *App, p *TermPane){
		func(a *App, p *TermPane) { a.closePane(slices.Index(a.panes, Pane(p))) },
		func(a *App, p *TermPane) { a.park(p) },
	} {
		a, ts := keysApp(t, 2)
		a.setFocus(1)
		openExplorerOn(t, a, explorerDir(t))
		leave(a, ts[1])
		a.updateExplorer(a.frameCtx(time.Now()))
		if a.explorer != nil {
			t.Error("the explorer outlives its shell")
		}
	}
}

// What the explorer opens never sends a tile to the garage: when the tiles
// are full, it floats.
func TestExplorerOpensBesideWhenTilesFull(t *testing.T) {
	a, ts := keysApp(t, 2)
	a.cfg.MaxPanes = 2
	a.setFocus(1)
	e := openExplorerOn(t, a, explorerDir(t))
	selectName(t, e, "pic.png")
	press(a, "enter")
	if a.explorer != nil || len(a.panes) != 3 {
		t.Fatalf("explorer %v, %d panes", a.explorer, len(a.panes))
	}
	img, ok := a.focused().(*ImagePane)
	if !ok || !isFloat(img) {
		t.Fatalf("focused %T, floating %v", a.focused(), ok && isFloat(img))
	}
	for _, p := range ts {
		if isParked(p) {
			t.Errorf("terminal %d went to the garage", p.id)
		}
	}
}
