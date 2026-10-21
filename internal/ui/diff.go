package ui

import (
	"bufio"
	"bytes"
	"context"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/gfx"
	"github.com/ThiraSoft/neonmux/internal/theme"
)

// Diff pane: the changes in a repository since the last commit (git diff
// HEAD and new files), in a unified view. It reloads by itself when an agent
// in the same repository changes state.

// diffRow is a displayed row: file header ('F'), hunk header ('@'), added
// line ('+'), removed line ('-'), context line (' ') or note ('\\').
type diffRow struct {
	kind byte
	text string
	file int // file index
	line int // matching line in the new file (0: none)
}

type diffFile struct {
	path     string
	add, del int
	row      int // row of its header
}

const (
	diffMaxRows      = 20000
	diffMaxNewFiles  = 50
	diffMaxNewLines  = 400
	diffContextLines = 3
	// A line read from git is cut at diffMaxLine bytes (a path always
	// fits), its displayed text at diffMaxText: a minified line of several
	// MB no longer stops the reading.
	diffMaxLine = 8 << 10
	diffMaxText = 1 << 10
)

// DiffPane shows the changes in a repository.
type DiffPane struct {
	paneBase
	root     string
	branch   string
	rows     []diffRow
	files    []diffFile
	cur, top int
	pendingG bool
	loading  bool
	again    bool
	loaded   bool
	err      string
	content  image.Rectangle
	visible  int
	dirty    bool
	closed   bool

	// truncated: past diffMaxRows rows or diffMaxNewFiles new files, the
	// rest is not read.
	truncated bool
}

func NewDiffPane(id int, root string) *DiffPane {
	d := &DiffPane{root: root}
	d.id = id
	return d
}

func (d *DiffPane) Kind() string  { return "diff" }
func (d *DiffPane) Title() string { return "diff · " + filepath.Base(d.root) }
func (d *DiffPane) Mode() string  { return "" }

func (d *DiffPane) Info() string {
	switch {
	case !d.loaded:
		return "loading…"
	case len(d.files) == 0:
		return "no changes"
	}
	add, del := d.totals()
	s := plural(len(d.files), "file") + "  +" + itoa(add) + " −" + itoa(del)
	if d.truncated {
		s += "  diff truncated"
	}
	return s
}

func plural(n int, word string) string {
	if n > 1 {
		word += "s"
	}
	return itoa(n) + " " + word
}

func (d *DiffPane) totals() (add, del int) {
	for _, f := range d.files {
		add += f.add
		del += f.del
	}
	return
}

func (d *DiffPane) Layout(ctx *Ctx, content image.Rectangle) {
	d.content = content
	d.visible = max(1, content.Dy()/ctx.Term.CellH-2)
	if !d.loaded && !d.loading {
		d.reload(ctx.App)
	}
	d.dirty = true
}

func (d *DiffPane) Update(ctx *Ctx) bool {
	dirty := d.dirty
	d.dirty = false
	return dirty
}

func (d *DiffPane) AnimFPS(ctx *Ctx) int        { return 0 }
func (d *DiffPane) NextWake(ctx *Ctx) time.Time { return time.Time{} }
func (d *DiffPane) Close()                      {}
func (d *DiffPane) Done() bool                  { return d.closed }

// reload reads the diff again in the background, keeping the cursor on the
// same file and the same line.
func (d *DiffPane) reload(a *App) {
	if d.loading {
		d.again = true
		return
	}
	d.loading = true
	go func() {
		res := readDiff(d.root)
		files, rows, branch, err := res.files, res.rows, res.branch, res.err
		a.post(func() {
			d.loading = false
			var keepPath string
			keepLine := 0
			if d.cur < len(d.rows) && len(d.files) > 0 {
				r := d.rows[d.cur]
				keepPath, keepLine = d.files[r.file].path, r.line
			}
			d.files, d.rows, d.branch, d.loaded = files, rows, branch, true
			d.truncated = res.truncated
			d.err = ""
			if err != nil {
				d.err = err.Error()
			}
			d.cur = 0
			for i, r := range rows {
				if keepPath != "" && files[r.file].path == keepPath && (r.line >= keepLine || r.kind == 'F' && keepLine == 0) {
					d.cur = i
					break
				}
			}
			d.move(0)
			if d.again {
				d.again = false
				d.reload(a)
			}
		})
	}()
}

// diffData is the result of readDiff.
type diffData struct {
	files     []diffFile
	rows      []diffRow
	branch    string
	truncated bool
	err       error
}

// readDiff reads git diff HEAD and the new files of the repository. In a
// repository without a commit, every file is new: the ones in the index and
// the untracked ones.
func readDiff(root string) diffData {
	ctx, cancel := context.WithTimeout(context.Background(), 2*gitTimeout)
	defer cancel()
	var d diffData
	if _, err := gitCmd(ctx, root, "rev-parse", "--git-dir"); err != nil {
		d.err = err
		return d
	}
	if b, err := gitCmd(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		d.branch = strings.TrimSpace(string(b))
	} else if b, err := gitCmd(ctx, root, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		d.branch = strings.TrimSpace(string(b)) // branch without a commit
	}
	var p diffParser
	_, noHead := gitCmd(ctx, root, "rev-parse", "--verify", "-q", "HEAD")
	if noHead == nil {
		complete, err := streamDiff(ctx, root, &p)
		if err != nil {
			d.err = err
			return d
		}
		d.truncated = !complete
	}
	news := []string{"ls-files", "--others", "--exclude-standard", "-z"}
	if noHead != nil {
		news = append(news, "--cached")
	}
	if out, err := gitCmd(ctx, root, news...); err == nil {
		names := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
		slices.Sort(names) // staged and untracked ones, mixed
		for i, name := range names {
			if name == "" {
				break
			}
			if i >= diffMaxNewFiles || len(p.rows) >= diffMaxRows {
				d.truncated = true
				break
			}
			p.files, p.rows = appendNewFile(p.files, p.rows, root, name)
		}
	}
	if d.truncated && len(p.files) > 0 {
		p.rows = append(p.rows, diffRow{kind: '\\', file: len(p.files) - 1,
			text: "diff truncated: at most " + itoa(diffMaxRows) + " lines and " + itoa(diffMaxNewFiles) + " new files"})
	}
	d.files, d.rows = p.files, p.rows
	return d
}

// streamDiff reads git diff HEAD as it comes, without keeping its output in
// memory. As soon as diffMaxRows rows are read, git is stopped: complete is
// then false.
func streamDiff(ctx context.Context, root string, p *diffParser) (complete bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// core.quotePath=false: a path with accents stays readable, without
	// quotes. The a/ and b/ prefixes are forced: diff.noprefix or
	// diff.mnemonicPrefix change them, and the file name depends on them.
	cmd := gitCommand(ctx, root, "-c", "core.quotePath=false", "diff", "HEAD", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", "-U"+itoa(diffContextLines), "--")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	complete, rerr := p.read(out)
	if !complete {
		cancel() // enough rows: no point letting git finish
	}
	werr := cmd.Wait()
	switch {
	case !complete:
		return false, nil
	case rerr != nil:
		return false, rerr
	}
	return true, gitError(werr, stderr.Bytes())
}

// parseDiff splits a unified diff into files and displayable rows.
func parseDiff(out []byte) ([]diffFile, []diffRow) {
	var p diffParser
	p.read(bytes.NewReader(out))
	return p.files, p.rows
}

// diffParser splits a unified diff, line by line. The headers of a file
// (index, ---, +++…) are only recognized between "diff --git" and its first
// hunk: inside a hunk, a removed line "-- comment" or an added line "++i;"
// stays content.
type diffParser struct {
	files   []diffFile
	rows    []diffRow
	newLine int
	header  bool
}

// read reads r to the end, or up to diffMaxRows displayable rows.
// complete says whether everything was read.
func (p *diffParser) read(r io.Reader) (complete bool, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	for len(p.rows) < diffMaxRows {
		l, err := readLine(br, diffMaxLine)
		if len(l) > 0 || err == nil {
			p.line(l)
		}
		if err == io.EOF {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	_, err = br.Peek(1)
	return err == io.EOF, nil
}

// readLine reads a line without its \n (or a trailing \r), cut at maxLen
// bytes: the rest of the line is skipped.
func readLine(br *bufio.Reader, maxLen int) (string, error) {
	var b []byte
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		if n := min(len(chunk), maxLen-len(b)); n < len(chunk) {
			b = append(b, chunk[:n]...)
			cut = cut || n < len(bytes.TrimSuffix(chunk, []byte("\n")))
		} else {
			b = append(b, chunk...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		b = bytes.TrimSuffix(bytes.TrimSuffix(b, []byte("\n")), []byte("\r"))
		if cut {
			return clipText(string(b), maxLen), err
		}
		return string(b), err
	}
}

// clipText cuts s to at most n bytes, on a character boundary, and marks
// the cut with "…".
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// line adds one line of the diff.
func (p *diffParser) line(l string) {
	fi := len(p.files) - 1
	switch {
	case strings.HasPrefix(l, "diff --git "):
		path := l[strings.LastIndex(l, " b/")+3:]
		p.files = append(p.files, diffFile{path: path, row: len(p.rows)})
		p.rows = append(p.rows, diffRow{kind: 'F', text: path, file: len(p.files) - 1})
		p.header = true
	case fi < 0:
	case p.header && strings.HasPrefix(l, "+++ b/"):
		// The exact path, even if it contains "b/".
		p.files[fi].path = strings.TrimPrefix(l, "+++ b/")
		p.rows[p.files[fi].row].text = p.files[fi].path
	case strings.HasPrefix(l, "@@"):
		// Inside a hunk, every line starts with +, -, a space or \:
		// a leading "@@" is always a new hunk.
		p.header = false
		// @@ -a,b +c,d @@ context
		if i := strings.Index(l, "+"); i >= 0 {
			n := l[i+1:]
			if j := strings.IndexAny(n, ", "); j >= 0 {
				n = n[:j]
			}
			p.newLine, _ = strconv.Atoi(n)
		}
		p.rows = append(p.rows, diffRow{kind: '@', text: clipText(l, diffMaxText), file: fi, line: p.newLine})
	case p.header && (strings.HasPrefix(l, "rename from ") || strings.HasPrefix(l, "rename to ") || strings.HasPrefix(l, "Binary files")):
		p.rows = append(p.rows, diffRow{kind: '\\', text: l, file: fi})
	case p.header:
		// index, ---, +++ /dev/null, modes, similarity: nothing to show.
	case strings.HasPrefix(l, "+"):
		p.files[fi].add++
		p.rows = append(p.rows, diffRow{kind: '+', text: clipText(l[1:], diffMaxText), file: fi, line: p.newLine})
		p.newLine++
	case strings.HasPrefix(l, "-"):
		p.files[fi].del++
		p.rows = append(p.rows, diffRow{kind: '-', text: clipText(l[1:], diffMaxText), file: fi, line: p.newLine})
	case strings.HasPrefix(l, " "):
		p.rows = append(p.rows, diffRow{kind: ' ', text: clipText(l[1:], diffMaxText), file: fi, line: p.newLine})
		p.newLine++
	case strings.HasPrefix(l, `\`):
		p.rows = append(p.rows, diffRow{kind: '\\', text: l, file: fi})
	}
}

// diffMaxNewSize bounds the read of a new file: a SQL dump of several GB
// lying around in the repository must not fill the memory.
const diffMaxNewSize = 1 << 20

// appendNewFile adds an untracked file, entirely as additions. Only regular
// files are read (a link to /dev/zero would never end), and at most
// diffMaxNewSize bytes.
func appendNewFile(files []diffFile, rows []diffRow, root, name string) ([]diffFile, []diffRow) {
	fi := len(files)
	files = append(files, diffFile{path: name, row: len(rows)})
	rows = append(rows, diffRow{kind: 'F', text: name, file: fi})
	path := filepath.Join(root, name)
	st, err := os.Lstat(path)
	switch {
	case err != nil:
		return files, append(rows, diffRow{kind: '\\', text: err.Error(), file: fi})
	case st.Mode()&os.ModeSymlink != 0:
		return files, append(rows, diffRow{kind: '\\', text: "new symbolic link", file: fi})
	case !st.Mode().IsRegular():
		return files, append(rows, diffRow{kind: '\\', text: "new special file", file: fi})
	case st.Size() > diffMaxNewSize:
		return files, append(rows, diffRow{kind: '\\', text: "new file too large to preview (" + humanSize(st.Size()) + ")", file: fi})
	}
	f, err := os.Open(path)
	if err != nil {
		return files, append(rows, diffRow{kind: '\\', text: err.Error(), file: fi})
	}
	data, err := io.ReadAll(io.LimitReader(f, diffMaxNewSize))
	f.Close()
	switch {
	case err != nil:
		return files, append(rows, diffRow{kind: '\\', text: err.Error(), file: fi})
	case bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0:
		return files, append(rows, diffRow{kind: '\\', text: "new binary file", file: fi})
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	rows = append(rows, diffRow{kind: '@', text: "@@ new file, " + plural(len(lines), "line") + " @@", file: fi, line: 1})
	for i, l := range lines {
		if i >= diffMaxNewLines {
			rows = append(rows, diffRow{kind: '\\', text: "… " + itoa(len(lines)-i) + " more lines", file: fi})
			break
		}
		rows = append(rows, diffRow{kind: '+', text: l, file: fi, line: i + 1})
	}
	files[fi].add = len(lines)
	return files, rows
}

func (d *DiffPane) move(n int) {
	d.cur = min(max(d.cur+n, 0), max(len(d.rows)-1, 0))
	if d.cur < d.top {
		d.top = d.cur
	}
	if d.cur >= d.top+d.visible {
		d.top = d.cur - d.visible + 1
	}
	d.dirty = true
}

// jumpFile goes to the header of the next file (dir 1) or the previous one (-1).
func (d *DiffPane) jumpFile(dir int) {
	if len(d.rows) == 0 {
		return
	}
	fi := d.rows[d.cur].file + dir
	if dir < 0 && d.rows[d.cur].kind != 'F' {
		fi = d.rows[d.cur].file // first the start of the current file
	}
	if fi >= 0 && fi < len(d.files) {
		d.move(d.files[fi].row - d.cur)
		d.top = d.cur // the file shows at the top
	}
}

// open opens the file under the cursor in the editor, at the right line.
func (d *DiffPane) open(a *App) {
	if d.cur >= len(d.rows) {
		return
	}
	r := d.rows[d.cur]
	path := filepath.Join(d.root, d.files[r.file].path)
	if _, err := os.Stat(path); err != nil {
		a.toast("file deleted: "+d.files[r.file].path, a.theme.Warn)
		return
	}
	a.openInEditorAt(path, r.line)
}

func (d *DiffPane) Key(ctx *Ctx, ev KeyEvent) bool {
	half := max(1, d.visible/2)
	switch {
	case ev.Name == "down":
		d.move(1)
	case ev.Name == "up":
		d.move(-1)
	case ev.Mods&ModCtrl != 0 && ev.Name == "d", ev.Name == "pagedown":
		d.move(half)
	case ev.Mods&ModCtrl != 0 && ev.Name == "u", ev.Name == "pageup":
		d.move(-half)
	case ev.Name == "enter":
		d.open(ctx.App)
	case ev.Name == "escape":
		d.closed = true
	default:
		return false
	}
	return true
}

func (d *DiffPane) Chars(ctx *Ctx, rs []rune) {
	for _, r := range rs {
		g := d.pendingG
		d.pendingG = false
		switch r {
		case 'j':
			d.move(1)
		case 'k':
			d.move(-1)
		case 'J':
			d.jumpFile(1)
		case 'K':
			d.jumpFile(-1)
		case ' ':
			d.move(d.visible)
		case 'g':
			if g {
				d.move(-len(d.rows))
			} else {
				d.pendingG = true
			}
		case 'G':
			d.move(len(d.rows))
		case 'o':
			d.open(ctx.App)
		case 'r':
			d.reload(ctx.App)
		case 'q':
			d.closed = true
		}
	}
}

func (d *DiffPane) Mouse(ctx *Ctx, ev MouseEvent) bool {
	switch ev.Kind {
	case MouseWheel:
		step := 3
		if ev.Wheel > 0 {
			step = -3
		}
		d.top = min(max(d.top+step, 0), max(len(d.rows)-d.visible, 0))
		d.cur = min(max(d.cur, d.top), d.top+d.visible-1)
		d.dirty = true
	case MousePress:
		row := (ev.Y-d.content.Min.Y)/ctx.Term.CellH - 2
		if row >= 0 && d.top+row < len(d.rows) {
			d.cur = d.top + row
			d.dirty = true
			if ev.Clicks >= 2 {
				d.open(ctx.App)
			}
		}
	}
	return true
}

func (d *DiffPane) Draw(ctx *Ctx, dst *ebiten.Image, content image.Rectangle, focused bool) {
	if content != d.content {
		return // layout animation: wait for the final position
	}
	f := ctx.Term
	th := ctx.Theme
	P := ctx.P
	cw, ch := float32(f.CellW), float32(f.CellH)
	x0, y0 := float32(content.Min.X)+cw/2, float32(content.Min.Y)
	cols := content.Dx()/f.CellW - 1
	w := float32(content.Dx()) - cw

	x := x0 + P.Text(f, x0, y0, compactPath(shortPath(d.root), cols-gfx.TextCells(d.branch)-3), th.Dim, 1)
	if d.branch != "" {
		x += cw * 2
		x += P.Text(f, x, y0, d.branch, th.Accent2, 1)
	}
	if d.loading {
		x += cw + P.Text(f, x+cw, y0, "…", th.Dim, 1)
	}
	if d.truncated {
		P.Text(f, x+cw*2, y0, "diff truncated", th.Warn, 1)
	}
	switch {
	case d.err != "":
		P.Text(f, x0, y0+ch*2, gfx.Truncate("git: "+d.err, cols), th.Warn, 1)
		return
	case d.loaded && len(d.rows) == 0:
		P.Text(f, x0, y0+ch*2, "No changes since the last commit.", th.Dim, 1)
		return
	}
	add, del := th.ANSI[2], th.ANSI[1]
	for i := 0; i < d.visible && d.top+i < len(d.rows); i++ {
		idx := d.top + i
		r := d.rows[idx]
		y := y0 + ch*float32(i+2)
		text := strings.ReplaceAll(r.text, "\t", "    ")
		switch r.kind {
		case 'F':
			fl := d.files[r.file]
			P.Rect(x0-cw/2, y, w+cw, ch, th.Accent, 0.12)
			tx := x0 + P.Text(f, x0, y, gfx.Truncate("▸ "+text, cols-14), th.Accent, 1)
			tx += P.Text(f, tx+cw, y, "+"+itoa(fl.add), add, 1) + cw
			P.Text(f, tx+cw, y, "−"+itoa(fl.del), del, 1)
		case '@':
			P.Text(f, x0, y, gfx.Truncate(text, cols), th.Dim, 1)
		case '+':
			P.Rect(x0-cw/2, y, w+cw, ch, add, 0.08)
			P.Text(f, x0, y, "+", add, 1)
			P.Text(f, x0+cw, y, gfx.Truncate(text, cols-1), theme.Mix(th.FG, add, 0.45), 1)
		case '-':
			P.Rect(x0-cw/2, y, w+cw, ch, del, 0.08)
			P.Text(f, x0, y, "-", del, 1)
			P.Text(f, x0+cw, y, gfx.Truncate(text, cols-1), theme.Mix(th.FG, del, 0.45), 1)
		case ' ':
			P.Text(f, x0+cw, y, gfx.Truncate(text, cols-1), theme.Mix(th.FG, th.BG, 0.3), 1)
		default:
			P.Text(f, x0, y, gfx.Truncate(text, cols), th.Dim, 1)
		}
		if idx == d.cur && focused {
			P.Rect(x0-cw/2, y, w+cw, ch, th.Accent, 0.16)
			P.Rect(x0-cw/2, y, 2*ctx.Px, ch, th.Accent, 1)
		}
	}
	if n := len(d.rows); n > d.visible {
		trackY := y0 + ch*2
		trackH := float32(content.Dy()) - ch*2
		thumbH := max(ch, trackH*float32(d.visible)/float32(n))
		thumbY := trackY + (trackH-thumbH)*float32(d.top)/float32(n-d.visible)
		sx := float32(content.Max.X) - 2*ctx.Px
		P.Rect(sx, trackY, ctx.Px, trackH, th.Border, 1)
		P.Rect(sx-ctx.Px/2, thumbY, 2*ctx.Px, thumbH, th.Accent, 0.8)
	}
}

// openDiff opens the diff of the repository that contains dir, floating on
// the right. Already open, it takes the focus; with the focus, it closes. The
// repository root is looked up in the background: a slow disk does not
// freeze the screen.
func (a *App) openDiff(dir string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		out, err := gitCmd(ctx, dir, "rev-parse", "--show-toplevel")
		cancel()
		a.post(func() {
			if err != nil {
				a.toast("no git repository here: "+shortPath(dir), a.theme.Warn)
				return
			}
			a.openDiffRoot(strings.TrimSpace(string(out)))
		})
	}()
}

func (a *App) openDiffRoot(root string) {
	for i, p := range a.panes {
		if d, ok := p.(*DiffPane); ok && d.root == root {
			if i == a.focus {
				a.closePane(i)
			} else {
				a.goTo(p) // in the garage, it comes out
				d.reload(a)
			}
			return
		}
	}
	d := NewDiffPane(a.newID(), root)
	switch {
	case a.countPanes(true) < maxFloats:
		area := a.floatArea()
		d.floating = true
		d.geom = floatGeom{X: 1, Y: 0.5, W: float64(area.Dx()) * 0.5 / a.dsf, H: float64(area.Dy()) * 0.92 / a.dsf}
	case a.countPanes(false) >= a.cfg.MaxPanes:
		a.toast("diff: close a pane first", a.theme.Warn)
		return
	}
	a.addPane(d)
}

// paneDir returns the working directory of a pane: the terminal's, the
// root of a diff.
func (a *App) paneDir(p Pane) string {
	switch p := p.(type) {
	case *TermPane:
		return p.workDir()
	case *DiffPane:
		return p.root
	case *AgentsPane:
		if t := p.selected(); t != nil {
			return t.workDir() // the agent picked in the agent board
		}
	}
	return a.opts.StartDir
}
