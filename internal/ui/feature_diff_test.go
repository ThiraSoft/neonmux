package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo creates an empty repository in a temporary directory and returns
// a way to run git in it.
func gitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	return dir, run
}

// diff.noprefix and diff.mnemonicPrefix change the a/ and b/ prefixes:
// the file name used to come out wrong.
func TestReadDiffIgnoresPrefixConfig(t *testing.T) {
	for _, conf := range []string{"diff.noprefix", "diff.mnemonicPrefix"} {
		dir, run := gitRepo(t)
		run("config", conf, "true")
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
		run("add", ".")
		run("commit", "-q", "-m", "init")
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644)
		d := readDiff(dir)
		if d.err != nil || len(d.files) != 1 || d.files[0].path != "a.txt" || d.files[0].add != 1 || d.files[0].del != 1 {
			t.Errorf("%s: %+v %v", conf, d.files, d.err)
		}
	}
}

// Without a commit, no "git: exit status 128": the new files (tracked or
// not) are shown.
func TestReadDiffWithoutCommit(t *testing.T) {
	dir, run := gitRepo(t)
	os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("a\nb\n"), 0o644)
	run("add", "staged.txt")
	os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("c\n"), 0o644)
	d := readDiff(dir)
	if d.err != nil || d.branch != "main" || len(d.files) != 2 || d.files[0].path != "staged.txt" || d.files[1].path != "untracked.txt" {
		t.Fatalf("%+v %v %q", d.files, d.err, d.branch)
	}
}

// When git fails, its own message is shown.
func TestReadDiffShowsGitError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	d := readDiff(t.TempDir())
	if d.err == nil || !strings.Contains(d.err.Error(), "not a git repository") {
		t.Fatalf("error %v", d.err)
	}
}

// A line longer than 4 MB used to stop the Scanner silently: the files
// after it disappeared. It is now cut.
func TestParseDiffLongLine(t *testing.T) {
	out := "diff --git a/min.js b/min.js\n--- a/min.js\n+++ b/min.js\n@@ -1 +1 @@\n-x\n+" +
		strings.Repeat("y", 5<<20) + "\ndiff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-1\n+2\n"
	files, rows := parseDiff([]byte(out))
	if len(files) != 2 || files[1].path != "b.txt" || files[1].add != 1 {
		t.Fatalf("files %+v", files)
	}
	for _, r := range rows {
		if len(r.text) > 4096 {
			t.Fatalf("line of %d bytes kept", len(r.text))
		}
	}
}

// Past diffMaxRows, git is stopped and the pane says the diff is
// truncated.
func TestReadDiffTruncated(t *testing.T) {
	dir, run := gitRepo(t)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte("x\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("line\n", diffMaxRows+5000)), 0o644)
	d := readDiff(dir)
	if d.err != nil || !d.truncated || len(d.rows) > diffMaxRows+1 {
		t.Fatalf("truncated %v, %d rows, %v", d.truncated, len(d.rows), d.err)
	}
	if last := d.rows[len(d.rows)-1]; !strings.Contains(last.text, "diff truncated") {
		t.Errorf("last row %+v", last)
	}
	p := NewDiffPane(1, dir)
	p.files, p.rows, p.truncated, p.loaded = d.files, d.rows, d.truncated, true
	if !strings.Contains(p.Info(), "truncated") {
		t.Errorf("info %q", p.Info())
	}
}
