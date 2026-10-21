package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ThiraSoft/neonmux/internal/ui"
)

// paneCommand handles neonmux capture and neonmux send-keys. The send-keys
// pauses ("wait:500ms") happen here: each run of keys between two pauses
// goes out in its own request.
func paneCommand(cmd string, args []string) int {
	pane := ""
	if len(args) > 0 && (cmd == "capture" || len(args) > 1) {
		pane, args = args[0], args[1:]
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "neonmux:", err)
		return 1
	}
	if cmd == "capture" {
		lines, err := ui.Send(ui.Request{Cmd: cmd, Pane: pane})
		if err != nil {
			return fail(err)
		}
		for _, l := range lines {
			fmt.Println(strings.TrimPrefix(l, "|"))
		}
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: neonmux send-keys <pane> <key>...")
		return 2
	}
	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := ui.Send(ui.Request{Cmd: cmd, Pane: pane, Args: batch})
		batch = nil
		return err
	}
	for _, tok := range args {
		if d, ok := strings.CutPrefix(tok, "wait:"); ok {
			dur, err := time.ParseDuration(d)
			if err != nil {
				return fail(fmt.Errorf("invalid pause %q", tok))
			}
			if err := flush(); err != nil {
				return fail(err)
			}
			time.Sleep(dur)
			continue
		}
		batch = append(batch, tok)
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	return 0
}
