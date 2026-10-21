package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Ebiten has no clipboard: the system tools are used instead, in a
// goroutine so rendering never blocks.

func clipboardCmds() (copyCmd, pasteCmd []string) {
	switch {
	case runtime.GOOS == "darwin":
		return []string{"pbcopy"}, []string{"pbpaste"}
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return []string{"wl-copy"}, []string{"wl-paste", "--no-newline"}
	default:
		return []string{"xclip", "-selection", "clipboard"}, []string{"xclip", "-selection", "clipboard", "-o"}
	}
}

// clipboardTimeout bounds each command: a frozen X11 owner would leave
// xclip -o stuck forever, one more on every cmd+v.
var clipboardTimeout = 2 * time.Second

// clipboardCmd prepares a clipboard command, killed when ctx ends.
// Launched from the Finder, the app has no locale: pbcopy and pbpaste would
// then read UTF-8 as MacRoman ("é" would turn into "√©").
func clipboardCmd(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// A child that keeps the output open does not hold up Wait.
	cmd.WaitDelay = 500 * time.Millisecond
	if runtime.GOOS == "darwin" {
		cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
	}
	return cmd
}

// clipboardErr turns a failed clipboard command into a readable error.
func clipboardErr(ctx context.Context, tool string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s is not responding", tool)
	case errors.Is(err, exec.ErrNotFound):
		pkg := "xclip"
		if strings.HasPrefix(tool, "wl-") {
			pkg = "wl-clipboard"
		}
		return fmt.Errorf("%s not found (install %s)", tool, pkg)
	}
	return fmt.Errorf("%s: %v", tool, err)
}

// copyText sends s to the clipboard in the background, then calls done
// (if it is not nil) with the error, if any.
func copyText(s string, done func(error)) {
	c, _ := clipboardCmds()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
		defer cancel()
		cmd := clipboardCmd(ctx, c)
		cmd.Stdin = strings.NewReader(s)
		err := clipboardErr(ctx, c[0], cmd.Run())
		if done != nil {
			done(err)
		}
	}()
}

// pasteText reads the clipboard in the background and passes the text to
// done, or the error to fail (if it is not nil).
func pasteText(done func(string), fail func(error)) {
	_, p := clipboardCmds()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
		defer cancel()
		out, err := clipboardCmd(ctx, p).Output()
		if err = clipboardErr(ctx, p[0], err); err == nil {
			done(string(out))
		} else if fail != nil {
			fail(err)
		}
	}()
}

// CopyText sends text to the clipboard.
func CopyText(s string) { copyText(s, nil) }

// PasteText reads the clipboard and passes the text to done.
func PasteText(done func(string)) { pasteText(done, nil) }

// copyNotify copies s, then shows ok, or the error. With an empty ok, a
// successful copy stays silent (copy on select).
func (a *App) copyNotify(s, ok string) {
	copyText(s, func(err error) {
		a.post(func() {
			if err != nil {
				a.toast("copy failed: "+err.Error(), a.theme.Warn)
				return
			}
			if ok != "" {
				a.toast(ok, a.theme.Accent2)
			}
		})
	})
}

// pasteInto pastes the clipboard into t, or reports the error.
func (a *App) pasteInto(t *TermPane) {
	pasteText(func(s string) { a.post(func() { t.Paste(s) }) }, func(err error) {
		a.post(func() { a.toast("paste failed: "+err.Error(), a.theme.Warn) })
	})
}
