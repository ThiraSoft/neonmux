package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// Change counter: what an agent has modified in its repository since the
// last commit (tracked files and new files), read in the background on every
// state change of the agent.

// gitStat sums up the state of a repository.
type gitStat struct {
	ok       bool
	root     string
	branch   string
	files    int
	add, del int
}

// gitInfo is the counter of a terminal.
type gitInfo struct {
	gitStat
	busy, again bool
	at          time.Time
}

const gitTimeout = 3 * time.Second

// gitCommand prepares git so that it never takes the index lock: an agent
// committing at the same moment must not run into "index.lock exists".
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	cmd.WaitDelay = time.Second
	return cmd
}

// gitCmd runs git and returns its output; on failure, the error carries
// git's message rather than "exit status 128".
func gitCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := gitCommand(ctx, dir, args...).Output()
	return out, gitError(err, nil)
}

// gitError replaces the error of a failed git run with the first line of
// its stderr (the ExitError's one if stderr is empty).
func gitError(err error, stderr []byte) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if len(stderr) == 0 && errors.As(err, &ee) {
		stderr = ee.Stderr
	}
	msg, _, _ := strings.Cut(strings.TrimSpace(string(stderr)), "\n")
	if msg == "" {
		return err
	}
	return errors.New(strings.TrimPrefix(msg, "fatal: "))
}

// readGitStat reads the branch and the changes of the repository that
// contains dir.
func readGitStat(dir string) gitStat {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := gitCmd(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitStat{}
	}
	st := gitStat{ok: true, root: strings.TrimSpace(string(out))}
	if b, err := gitCmd(ctx, st.root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		st.branch = strings.TrimSpace(string(b))
	}
	if out, err := gitCmd(ctx, st.root, "diff", "--numstat", "HEAD", "--"); err == nil {
		st.files, st.add, st.del = parseNumstat(out)
	}
	if out, err := gitCmd(ctx, st.root, "ls-files", "--others", "--exclude-standard", "-z"); err == nil {
		for i, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
			if name == "" || i >= 500 {
				break
			}
			st.files++
			st.add += countLines(filepath.Join(st.root, name))
		}
	}
	return st
}

// parseNumstat adds up the output of git diff --numstat ("-" for a binary
// file).
func parseNumstat(out []byte) (files, add, del int) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 3)
		if len(f) < 3 {
			continue
		}
		files++
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		add += a
		del += d
	}
	return
}

// lineCache remembers the line count of new files: read again every 5s
// while an agent works, they rarely change.
var lineCache = struct {
	sync.Mutex
	m map[string]lineCount
}{m: map[string]lineCount{}}

type lineCount struct {
	size  int64
	mtime time.Time
	n     int
}

// countLines counts the lines of a new text file (0 for a binary, a link
// or a file larger than 1 MB).
func countLines(path string) int {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return 0
	}
	lineCache.Lock()
	c, ok := lineCache.m[path]
	lineCache.Unlock()
	if ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.n
	}
	n := 0
	if data, err := os.ReadFile(path); err == nil && bytes.IndexByte(data[:min(len(data), 8000)], 0) < 0 {
		n = bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] != '\n' {
			n++
		}
	}
	lineCache.Lock()
	if len(lineCache.m) > 5000 {
		clear(lineCache.m)
	}
	lineCache.m[path] = lineCount{st.Size(), st.ModTime(), n}
	lineCache.Unlock()
	return n
}

// refreshGit reads the counter of a terminal again in the background. A
// call during a run starts another one when it ends.
func (a *App) refreshGit(p *TermPane) {
	g := &p.git
	if g.busy {
		g.again = true
		return
	}
	dir := p.workDir()
	if dir == "" {
		return
	}
	g.busy, g.at = true, time.Now()
	go func() {
		st := readGitStat(dir)
		a.post(func() {
			g.busy = false
			if !p.hasProg {
				return // the agent left during the run
			}
			changed := st != g.gitStat
			g.gitStat = st
			if changed {
				p.dirty = true
				a.gitChanged(st)
			}
			if g.again {
				g.again = false
				a.refreshGit(p)
			}
		})
	}()
}

// gitWhileWorking reads the counter of a working agent again every 5s: its
// changes show up as they happen.
func (p *TermPane) gitWhileWorking(ctx *Ctx) {
	if p.hasProg && p.prog.State == vt.PSWorking && ctx.Now.Sub(p.git.at) > 5*time.Second {
		ctx.App.refreshGit(p)
	}
}

// gitChanged notifies the diff panes of the same repository.
func (a *App) gitChanged(st gitStat) {
	for _, q := range a.panes {
		if d, ok := q.(*DiffPane); ok && st.ok && d.root == st.root {
			d.reload(a)
		}
	}
}

type textPart struct {
	text string
	c    color.RGBA
}

// statParts returns "+120 −34" in color, or nothing when there is no change.
func (g gitStat) statParts(th *theme.Theme) []textPart {
	if !g.ok || g.files == 0 {
		return nil
	}
	return []textPart{{"+" + itoa(g.add), th.ANSI[2]}, {"−" + itoa(g.del), th.ANSI[1]}}
}
