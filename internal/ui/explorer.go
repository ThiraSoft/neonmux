package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/shell"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

var (
	imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".webp": true}
	audioExts = map[string]bool{".mp3": true, ".ogg": true, ".wav": true}
	codeExts  = map[string]bool{".go": true, ".rs": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".c": true,
		".h": true, ".lua": true, ".sh": true, ".zsh": true, ".rb": true, ".java": true, ".kt": true, ".php": true, ".vue": true}
	docExts = map[string]bool{".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".mod": true, ".sum": true}
)

func isImage(p string) bool { return imageExts[strings.ToLower(filepath.Ext(p))] }
func isAudio(p string) bool { return audioExts[strings.ToLower(filepath.Ext(p))] }

type entry struct {
	name  string
	isDir bool
	size  int64
	mod   time.Time
	exec  bool
	link  bool
}

func (e entry) kind() string {
	switch {
	case e.isDir:
		return "dir"
	case isImage(e.name):
		return "image"
	case isAudio(e.name):
		return "audio"
	case codeExts[strings.ToLower(filepath.Ext(e.name))]:
		return "code"
	case docExts[strings.ToLower(filepath.Ext(e.name))]:
		return "doc"
	case e.exec:
		return "exec"
	}
	return "file"
}

// Small pixel art icons, drawn square by square (off the text grid).
var icons = map[string][]string{
	"dir":    {"......", "###...", "######", "#....#", "#....#", "######"},
	"file":   {"####..", "#..##.", "#...##", "#....#", "#....#", "######"},
	"code":   {"######", "#....#", "#.#..#", "#..#.#", "#.#..#", "######"},
	"doc":    {"######", "#....#", "#.##.#", "#....#", "#.##.#", "######"},
	"image":  {"######", "#...##", "#.#..#", "##.#.#", "######", "######"},
	"audio":  {"...###", "...#.#", "...#.#", ".###.#", "####..", ".##..."},
	"exec":   {"######", "#....#", "#.#..#", "#..#.#", "#.#.##", "######"},
	"term":   {"#.....", ".#....", "..#...", ".#....", "#.....", "...###"},
	"files":  {"......", "###...", "######", "#....#", "#....#", "######"},
	"bolt":   {"...##.", "..##..", ".####.", "..##..", ".##...", "##...."},
	"help":   {".####.", "#....#", "...##.", "..#...", "......", "..#..."},
	"diff":   {"..#...", ".###..", "..#...", "......", ".###..", "......"},
	"agents": {"#.#.#.", "......", "###.##", "......", "##.###", "......"},
	// Agent states (badges and bottom bar).
	"st-working": {"..##..", ".####.", "######", "######", ".####.", "..##.."},
	"st-blocked": {"..##..", "..##..", "..##..", "..##..", "......", "..##.."},
	"st-done":    {"......", ".....#", "....##", "#..##.", "####..", ".##..."},
	"st-error":   {"#....#", ".#..#.", "..##..", "..##..", ".#..#.", "#....#"},
	"st-idle":    {"..##..", ".#..#.", "#....#", "#....#", ".#..#.", "..##.."},
}

func iconColor(kind string, th *theme.Theme) color.RGBA {
	switch kind {
	case "dir":
		return th.Accent2
	case "image":
		return th.Accent
	case "audio":
		return th.Warn
	case "code":
		return th.ANSI[5]
	case "exec":
		return th.ANSI[2]
	case "doc":
		return th.FG
	}
	return th.Dim
}

func drawIcon(P *gfx.Painter, kind string, x, y, s float32, c color.RGBA) {
	for j, row := range icons[kind] {
		for i, ch := range row {
			if ch == '#' {
				P.Rect(x+float32(i)*s, y+float32(j)*s, s, s, c, 1)
			}
		}
	}
}

type preview struct {
	path  string
	lines []string
	thumb *ebiten.Image
	info  string
}

// latestWorker runs jobs one at a time, keeping only the last one requested
// while it is busy: when scrolling through the explorer, only the item where
// the cursor stops is read (a 24 Mpx photo takes about 36 MB while it is
// being decoded).
type latestWorker struct {
	mu   sync.Mutex
	next func()
	busy bool
}

func (w *latestWorker) do(job func()) {
	w.mu.Lock()
	w.next = job
	if w.busy {
		w.mu.Unlock()
		return
	}
	w.busy = true
	w.mu.Unlock()
	go func() {
		for {
			w.mu.Lock()
			job := w.next
			w.next = nil
			if job == nil {
				w.busy = false
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			job()
		}
	}()
}

// Explorer is a ranger-style file explorer, driven with vim keys. It is not
// a pane: it opens above the active pane, and closes once its action is
// done (the App methods at the end of this file).
// The directory and the previews are read in the background: a directory of
// 60,000 entries, a network drive or a named pipe never freezes the screen.
type Explorer struct {
	dir       string
	all       []entry
	view      []entry
	sel, top  int
	filter    []rune
	filtering bool
	hidden    bool
	pendingG  bool
	err       error
	content   image.Rectangle
	rows      int
	prev      preview
	thumbs    map[string]*ebiten.Image
	thumbOrd  []string
	dirty     bool
	history   map[string]string // last selected item per directory
	started   bool              // first directory read started
	listing   bool              // directory read in progress
	gen       int               // number of the last requested read
	want      string            // item to select when the read comes back
	lister    latestWorker
	previewer latestWorker
	host      *TermPane // shell at its prompt that the explorer covers (nil: floating)
}

// NewExplorer opens an explorer on dir. The directory is read on the
// first frame (Update), in the background.
func NewExplorer(dir string) *Explorer {
	return &Explorer{dir: dir, thumbs: map[string]*ebiten.Image{}, history: map[string]string{}}
}

func (e *Explorer) Info() string {
	if len(e.view) == 0 {
		if e.listing {
			return "loading…"
		}
		return "empty"
	}
	return fmt.Sprintf("%d/%d", e.sel+1, len(e.view))
}

func (e *Explorer) Mode() string {
	if e.filtering {
		return "FILTER"
	}
	return ""
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// compactPath fits a path into maxCells: the intermediate directories are
// cut down to their initial (fish style), then, if that is not enough, only
// the end is kept.
func compactPath(path string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if gfx.TextCells(path) <= maxCells {
		return path
	}
	parts := strings.Split(path, "/")
	for i := 0; i < len(parts)-1; i++ {
		r := []rune(parts[i])
		switch {
		case len(r) > 2 && r[0] == '.':
			parts[i] = string(r[:2])
		case len(r) > 1:
			parts[i] = string(r[:1])
		}
		if s := strings.Join(parts, "/"); gfx.TextCells(s) <= maxCells {
			return s
		}
	}
	return gfx.Truncate("…/"+parts[len(parts)-1], maxCells)
}

// background runs work in w, then applies its result on the main thread.
// Without an app (tests), everything happens in place.
func background(app *App, w *latestWorker, work func() func()) {
	if app == nil {
		work()()
		return
	}
	w.do(func() { app.post(work()) })
}

// load rereads the directory in the background. A result that arrives
// after a change of directory is ignored.
func (e *Explorer) load(app *App) {
	e.started = true
	e.gen++
	gen, dir := e.gen, e.dir
	e.listing = true
	e.dirty = true
	background(app, &e.lister, func() func() {
		all, err := readEntries(dir)
		return func() {
			if gen != e.gen || e.thumbs == nil {
				return // directory left, or pane closed
			}
			keep := e.want
			if en, ok := e.current(); ok && keep == "" {
				keep = en.name // reload (r): the selection stays
			}
			e.want = ""
			e.listing, e.err, e.all = false, err, all
			e.applyFilter()
			if i := slices.IndexFunc(e.view, func(en entry) bool { return en.name == keep }); i >= 0 {
				e.sel = i
			}
			e.dirty = true
		}
	})
}

// readEntries reads a directory, subdirectories first, in alphabetical order.
func readEntries(dir string) ([]entry, error) {
	ents, err := os.ReadDir(dir)
	all := make([]entry, 0, len(ents))
	for _, d := range ents {
		info, err := d.Info()
		if err != nil {
			continue
		}
		en := entry{name: d.Name(), isDir: d.IsDir(), size: info.Size(), mod: info.ModTime(),
			exec: info.Mode()&0o111 != 0 && !d.IsDir(), link: info.Mode()&os.ModeSymlink != 0}
		if en.link {
			if st, err := os.Stat(filepath.Join(dir, d.Name())); err == nil && st.IsDir() {
				en.isDir = true
			}
		}
		all = append(all, en)
	}
	slices.SortFunc(all, func(a, b entry) int {
		if a.isDir != b.isDir {
			if a.isDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
	return all, err
}

func (e *Explorer) applyFilter() {
	e.view = e.view[:0]
	q := string(e.filter)
	for _, en := range e.all {
		if !e.hidden && strings.HasPrefix(en.name, ".") {
			continue
		}
		if q != "" {
			if _, ok := fuzzy(q, en.name); !ok {
				continue
			}
		}
		e.view = append(e.view, en)
	}
	e.sel = min(e.sel, max(len(e.view)-1, 0))
	e.dirty = true
}

func (e *Explorer) current() (entry, bool) {
	if e.sel < len(e.view) {
		return e.view[e.sel], true
	}
	return entry{}, false
}

func (e *Explorer) Layout(ctx *Ctx, content image.Rectangle) {
	e.content = content
	e.rows = max(1, content.Dy()/ctx.UI.CellH-2)
	e.dirty = true
}

func (e *Explorer) Update(ctx *Ctx) bool {
	if !e.started {
		e.load(ctx.App)
	}
	d := e.dirty
	e.dirty = false
	return d
}

func (e *Explorer) Close() {
	for _, t := range e.thumbs {
		t.Deallocate()
	}
	e.thumbs = nil // reads still in flight are ignored
}

func (e *Explorer) enter(ctx *Ctx) {
	en, ok := e.current()
	if !ok {
		return
	}
	path := filepath.Join(e.dir, en.name)
	if en.isDir {
		e.cd(ctx.App, path)
		return
	}
	ctx.App.openFromExplorer(e, path)
}

// targetDir returns the selected directory, or the displayed directory if
// the selection is a file.
func (e *Explorer) targetDir() string {
	if en, ok := e.current(); ok && en.isDir {
		return filepath.Join(e.dir, en.name)
	}
	return e.dir
}

func (e *Explorer) cd(app *App, dir string) {
	if en, ok := e.current(); ok {
		e.history[e.dir] = en.name
	}
	prev := e.dir
	e.dir = filepath.Clean(dir)
	e.sel, e.top = 0, 0
	e.filter, e.filtering = nil, false
	// The old list goes away: opening one of its items would target the
	// new directory.
	e.all, e.view, e.err = nil, nil, nil
	e.want = e.history[e.dir]
	// Going up selects the directory we came from again.
	if filepath.Dir(prev) == e.dir {
		e.want = filepath.Base(prev)
	}
	e.load(app)
}

func (e *Explorer) moveSel(d int) {
	if len(e.view) == 0 {
		return
	}
	e.sel = min(max(e.sel+d, 0), len(e.view)-1)
	e.dirty = true
}

func (e *Explorer) Key(ctx *Ctx, ev KeyEvent) bool {
	if e.filtering {
		switch ev.Name {
		case "escape":
			e.filtering = false
			e.filter = nil
			e.applyFilter()
		case "enter":
			e.filtering = false
		case "backspace":
			if len(e.filter) > 0 {
				e.filter = e.filter[:len(e.filter)-1]
				e.applyFilter()
			}
		case "down":
			e.moveSel(1)
		case "up":
			e.moveSel(-1)
		}
		return true
	}
	half := max(1, e.rows/2)
	switch {
	case ev.Name == "enter", ev.Name == "right":
		e.enter(ctx)
	case ev.Name == "left", ev.Name == "backspace":
		e.cd(ctx.App, filepath.Dir(e.dir))
	case ev.Name == "down":
		e.moveSel(1)
	case ev.Name == "up":
		e.moveSel(-1)
	case ev.Mods&ModCtrl != 0 && ev.Name == "d", ev.Name == "pagedown":
		e.moveSel(half)
	case ev.Mods&ModCtrl != 0 && ev.Name == "u", ev.Name == "pageup":
		e.moveSel(-half)
	case ev.Name == "escape" && len(e.filter) > 0:
		e.filter = nil
		e.applyFilter()
	case ev.Name == "escape":
		ctx.App.closeExplorer()
	default:
		return false
	}
	return true
}

func (e *Explorer) Chars(ctx *Ctx, rs []rune) {
	for _, r := range rs {
		if e.closed() {
			return
		}
		if e.filtering {
			e.filter = append(e.filter, r)
			e.applyFilter()
			continue
		}
		g := e.pendingG
		e.pendingG = false
		switch r {
		case 'j':
			e.moveSel(1)
		case 'k':
			e.moveSel(-1)
		case 'l':
			e.enter(ctx)
		case 'h', '-':
			e.cd(ctx.App, filepath.Dir(e.dir))
		case 'g':
			if g {
				e.sel = 0
				e.dirty = true
			} else {
				e.pendingG = true
			}
		case 'G':
			e.sel = max(len(e.view)-1, 0)
			e.dirty = true
		case '/':
			e.filtering = true
			e.filter = nil
		case '.':
			e.hidden = !e.hidden
			e.applyFilter()
		case '~':
			if home, err := os.UserHomeDir(); err == nil {
				e.cd(ctx.App, home)
			}
		case 'r':
			e.load(ctx.App)
		case 'y':
			if en, ok := e.current(); ok {
				p := filepath.Join(e.dir, en.name)
				ctx.App.copyNotify(p, "path copied: "+shortPath(p))
				ctx.App.closeExplorer()
			}
		case 'o':
			if en, ok := e.current(); ok {
				opener := "xdg-open"
				if runtime.GOOS == "darwin" {
					opener = "open"
				}
				cmd := exec.Command(opener, filepath.Join(e.dir, en.name))
				if err := cmd.Start(); err != nil {
					ctx.App.toast("cannot open: "+err.Error(), ctx.Theme.Warn)
				} else {
					go cmd.Wait() // otherwise every open leaves a zombie behind
					ctx.App.closeExplorer()
				}
			}
		case 't':
			// New terminal in the selected directory.
			dir := e.targetDir()
			ctx.App.closeExplorer()
			ctx.App.newShellPane(dir, true)
		case 'c':
			// The covered shell (otherwise the last used terminal) cds
			// into the selected directory.
			if ctx.App.cdTerminal(e.host, e.targetDir()) {
				ctx.App.closeExplorer()
			}
		case 'q':
			ctx.App.closeExplorer()
		}
	}
}

// closed tells whether the explorer was closed (Close): the characters
// left in the frame go nowhere.
func (e *Explorer) closed() bool { return e.thumbs == nil }

func (e *Explorer) Mouse(ctx *Ctx, ev MouseEvent) bool {
	f := ctx.UI
	switch ev.Kind {
	case MouseWheel:
		if ev.Wheel > 0 {
			e.moveSel(-1)
		} else if ev.Wheel < 0 {
			e.moveSel(1)
		}
	case MousePress:
		row := (ev.Y-e.content.Min.Y)/f.CellH - 2
		if row >= 0 && e.top+row < len(e.view) {
			e.sel = e.top + row
			e.dirty = true
			if ev.Clicks >= 2 {
				e.enter(ctx)
			}
		}
	}
	return true
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f K", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1f M", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f G", float64(n)/(1<<30))
}

// loadPreview prepares the preview of the selected item (text, directory,
// image thumbnail). It is called from Draw: anything that touches the disk
// happens in the background, and only the result for the item still
// selected is kept.
func (e *Explorer) loadPreview(ctx *Ctx) {
	en, ok := e.current()
	if !ok {
		e.prev = preview{}
		return
	}
	path := filepath.Join(e.dir, en.name)
	if e.prev.path == path {
		if e.prev.thumb == nil && isImage(path) {
			e.prev.thumb = e.thumbs[path]
		}
		return
	}
	e.prev = preview{path: path, info: "…"}
	switch {
	case en.isDir:
	case isImage(path):
		e.prev.info = humanSize(en.size)
		if t, ok := e.thumbs[path]; ok {
			e.prev.thumb = t
			return
		}
	case isAudio(path):
		e.prev.info = "audio · " + humanSize(en.size) + "  enter to play"
		return
	}
	background(ctx.App, &e.previewer, func() func() {
		p, img := readPreview(path, en)
		return func() {
			if e.prev.path != path || e.thumbs == nil {
				return // selection changed in the meantime, or pane closed
			}
			if img != nil {
				e.addThumb(path, ebiten.NewImageFromImage(img))
				e.prev.thumb = e.thumbs[path]
			}
			if p.info != "" {
				e.prev.info = p.info
			}
			e.prev.lines = p.lines
			e.dirty = true
		}
	})
}

// readPreview reads the preview of an item, off the main thread: a
// directory listing, a thumbnail or the start of a text file. Only regular
// files are opened (openRegular).
func readPreview(path string, en entry) (preview, image.Image) {
	var p preview
	switch {
	case en.isDir:
		ents, err := os.ReadDir(path)
		if err != nil {
			p.info = err.Error()
			return p, nil
		}
		for i, d := range ents {
			if i >= 60 {
				break
			}
			name := d.Name()
			if d.IsDir() {
				name += "/"
			}
			p.lines = append(p.lines, name)
		}
		p.info = fmt.Sprintf("%d items", len(ents))
		if len(ents) == 1 {
			p.info = "1 item"
		}
		return p, nil
	case isImage(path):
		img, _, err := decodeImageFile(path, 384)
		if err != nil {
			p.info = humanSize(en.size) + " · " + err.Error()
		}
		return p, img
	}
	f, err := openRegular(path)
	if err != nil {
		p.info = err.Error()
		return p, nil
	}
	defer f.Close()
	buf := make([]byte, 6*1024)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(buf[:max(0, n-4)]) {
		p.info = "binary · " + humanSize(en.size)
		return p, nil
	}
	p.info = humanSize(en.size)
	for _, l := range strings.Split(string(buf), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		p.lines = append(p.lines, strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, l))
	}
	return p, nil
}

// addThumb keeps at most 12 thumbnails in VRAM.
func (e *Explorer) addThumb(path string, img *ebiten.Image) {
	e.thumbs[path] = img
	e.thumbOrd = append(e.thumbOrd, path)
	if len(e.thumbOrd) > 12 {
		old := e.thumbOrd[0]
		e.thumbOrd = e.thumbOrd[1:]
		if t := e.thumbs[old]; t != nil {
			t.Deallocate()
		}
		delete(e.thumbs, old)
	}
}

func (e *Explorer) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle) {
	if content != e.content {
		return // laid out again on the next frame (updateExplorer)
	}
	f := ctx.UI
	th := ctx.Theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	x0, y0 := float32(content.Min.X), float32(content.Min.Y)
	cols := content.Dx() / f.CellW

	listCols := cols
	showPreview := cols >= 70
	if showPreview {
		listCols = cols * 45 / 100
	}

	// Breadcrumb, and the position in the list on the right.
	crumb := shortPath(e.dir)
	if e.filtering || len(e.filter) > 0 {
		crumb += "  / " + string(e.filter)
	}
	info := e.Info()
	infoCols := gfx.TextCells(info)
	P.Text(f, x0, y0, gfx.Truncate(crumb, cols-infoCols-2), th.Accent2, 1)
	P.Text(f, x0+float32(cols-infoCols)*cw, y0, info, th.Dim, 1)
	if e.err != nil {
		P.Text(f, x0, y0+ch*2, gfx.Truncate(e.err.Error(), cols), th.Warn, 1)
		return
	}
	if e.listing && len(e.view) == 0 {
		P.Text(f, x0, y0+ch*2, "loading…", th.Dim, 1)
		return
	}

	if e.sel < e.top {
		e.top = e.sel
	}
	if e.sel >= e.top+e.rows {
		e.top = e.sel - e.rows + 1
	}
	iconS := float32(max(1, f.CellH/9))
	for i := 0; i < e.rows && e.top+i < len(e.view); i++ {
		en := e.view[e.top+i]
		y := y0 + ch*float32(i+2)
		if e.top+i == e.sel {
			P.Rect(x0, y, float32(listCols)*cw, ch, th.Accent, 0.2)
			P.Rect(x0, y, 2*ctx.Px, ch, th.Accent, 1)
		}
		k := en.kind()
		c := iconColor(k, th)
		drawIcon(P, k, x0+cw*0.7, y+(ch-6*iconS)/2, iconS, c)
		name := en.name
		if en.isDir {
			name += "/"
		}
		nameC := th.FG
		if k == "dir" {
			nameC = th.Accent2
		} else if strings.HasPrefix(en.name, ".") {
			nameC = th.Dim
		}
		size := ""
		if !en.isDir {
			size = humanSize(en.size)
		}
		maxName := listCols - 4 - len(size) - 1
		P.Text(f, x0+cw*2.5, y, gfx.Truncate(name, maxName), nameC, 1)
		if size != "" {
			P.Text(f, x0+float32(listCols-len([]rune(size)))*cw, y, size, th.Dim, 1)
		}
	}

	if !showPreview {
		return
	}
	e.loadPreview(ctx)
	px0 := x0 + float32(listCols+2)*cw
	pw := float32(content.Max.X) - px0
	P.Rect(px0-cw, y0+ch*2, ctx.Px, float32(content.Dy())-ch*2, th.Border, 1)
	pcols := int(pw / cw)
	P.Text(f, px0, y0+ch*2, gfx.Truncate(e.prev.info, pcols), th.Dim, 1)
	if e.prev.thumb != nil {
		P.Flush(dst)
		b := e.prev.thumb.Bounds()
		avail := float64(pw)
		availH := float64(content.Dy()) - float64(ch)*4
		s := min(avail/float64(b.Dx()), availH/float64(b.Dy()))
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Scale(s, s)
		op.GeoM.Translate(float64(px0), float64(y0+ch*3.5))
		op.Filter = ebiten.FilterLinear
		if b.Dx() <= 256 {
			op.Filter = ebiten.FilterNearest
		}
		dst.DrawImage(e.prev.thumb, op)
		return
	}
	maxLines := e.rows - 1
	for i, l := range e.prev.lines {
		if i >= maxLines {
			break
		}
		c := th.BarFG
		if strings.HasSuffix(l, "/") {
			c = th.Accent2
		}
		P.Text(f, px0, y0+ch*float32(i+3), gfx.Truncate(l, pcols), c, 1)
	}
}

// atPrompt tells whether a terminal's shell waits at its prompt: what is
// typed there goes to the shell, never into nvim or a build.
func atPrompt(t *TermPane) bool {
	if t == nil || t.s == nil {
		return false
	}
	name := strings.TrimPrefix(t.s.ForegroundName(), "-")
	return name != "" && shell.IsShell(name)
}

// openExplorer opens the explorer on dir. Above a shell at its prompt, it
// covers that shell and acts on it; otherwise it floats in the center.
func (a *App) openExplorer(dir string) {
	a.closeExplorer()
	e := NewExplorer(dir)
	if t := a.focusedTerm(); atPrompt(t) {
		e.host = t
	}
	a.explorer = e
	a.needRedraw = true
}

func (a *App) closeExplorer() {
	if a.explorer == nil {
		return
	}
	a.explorer.Close()
	a.explorer = nil
	a.needRedraw = true
}

// updateExplorer follows the shell the explorer covers, and closes it when
// that shell leaves the screen.
func (a *App) updateExplorer(ctx *Ctx) {
	e := a.explorer
	if h := e.host; h != nil && (!slices.Contains(a.panes, Pane(h)) || isParked(h)) {
		a.closeExplorer()
		return
	}
	if _, content := a.explorerRects(); content != e.content {
		e.Layout(ctx, content)
	}
	if e.Update(ctx) {
		a.needRedraw = true
	}
}

// openFromExplorer opens a file picked in the explorer, which closes. Above
// a shell at its prompt, the editor starts right in that shell. A new pane
// never takes the place of a tile (addBeside).
func (a *App) openFromExplorer(e *Explorer, path string) {
	a.closeExplorer()
	switch {
	case isImage(path):
		a.addBeside(NewImagePane(a.newID(), path))
	case isAudio(path):
		a.addBeside(NewAudioPane(a.newID(), path))
	case atPrompt(e.host):
		e.host.write([]byte("\x15" + editorAt(a.editor(), path, 0) + "\r"))
	default:
		a.openInEditorAt(path, 0)
	}
}

// explorerRects returns the explorer's frame and its content area: the
// shell it covers, below its header (which stays visible), or a large box
// in the center. The shell's final place counts, not the one of its
// animation.
func (a *App) explorerRects() (box, content image.Rectangle) {
	_, header, _, pad := a.metrics()
	if h := a.explorer.host; h != nil {
		r := h.target
		return image.Rect(r.Min.X, r.Min.Y+header, r.Max.X, r.Max.Y), contentRect(r, header, pad)
	}
	box = a.floatRect(a.centerGeom())
	return box, contentRect(box, header, pad)
}

// explorerClick: a click inside the explorer picks an item (a double click
// opens it), a click outside closes it.
func (a *App) explorerClick(ctx *Ctx, ev MouseEvent) {
	box, _ := a.explorerRects()
	pt := image.Pt(ev.X, ev.Y)
	switch {
	case !pt.In(box):
		a.closeExplorer()
	case ev.Button == ebiten.MouseButtonLeft:
		ev.Clicks = a.countClick(ctx.Now, pt, nil)
		a.explorer.Mouse(ctx, ev)
	}
}

// drawExplorer draws the explorer above the panes. Over a shell, it hides
// the shell's text and leaves its header and border in view; floating, it
// gets a header, a shadow and a lit border of its own.
func (a *App) drawExplorer(ctx *Ctx, dst *ebiten.Image) {
	e := a.explorer
	if e == nil {
		return
	}
	th, P, f, px := a.theme, ctx.P, a.uiFont, a.px
	_, header, _, pad := a.metrics()
	box, content := a.explorerRects()
	if e.host != nil {
		fill := image.Rect(box.Min.X+pad, box.Min.Y, box.Max.X-pad, box.Max.Y-pad)
		P.Rect(float32(fill.Min.X), float32(fill.Min.Y), float32(fill.Dx()), float32(fill.Dy()), th.BG, floatAlpha)
	} else {
		x0, y0, hh := float32(box.Min.X), float32(box.Min.Y), float32(header)
		a.drawShadow(P, box)
		P.Rect(x0, y0, float32(box.Dx()), float32(box.Dy()), th.BG, floatAlpha)
		P.Rect(x0, y0, float32(box.Dx()), hh, theme.Mix(th.BarBG, th.Accent, 0.16), 0.96)
		P.Rect(x0, y0, 3*px, hh, th.Accent, 1)
		iconS := float32(max(1, f.CellH/9))
		x := x0 + 3*px + float32(f.CellW)
		drawIcon(P, "files", x, y0+(hh-6*iconS)/2, iconS, th.Accent2)
		P.Text(f, x+6*iconS+float32(f.CellW), y0+(hh-float32(f.CellH))/2, "files", th.BarFG, 1)
	}
	P.Flush(dst)
	e.Draw(ctx, dst, content)
	P.Flush(dst)
	if e.host == nil {
		P.Border(box, 2*px, th.Accent, 1)
		P.Border(box.Inset(-int(px)), px, th.Accent, 0.35)
		P.Flush(dst)
	}
}
