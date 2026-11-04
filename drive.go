package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ThiraSoft/neonmux/internal/ui"
)

// driveCommand handles the commands that go to the window itself (state,
// actions, action, key, focus, shot) and neonmux wait, which polls
// capture.
func driveCommand(cmd string, args []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "neonmux:", err)
		return 1
	}
	if cmd == "wait" {
		return waitCommand(args)
	}
	lines, err := ui.SendWindow(ui.Request{Cmd: cmd, Args: args})
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// waitCommand polls a pane's screen until it matches the regexp (or no
// longer does, with -gone). The matching line is printed.
func waitCommand(args []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	timeout := new(time.Duration)
	*timeout = 10 * time.Second
	fs.Func("t", "how long to wait: 10s, 500ms, or seconds (5)", func(v string) error {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			*timeout = time.Duration(n * float64(time.Second))
			return nil
		}
		d, err := time.ParseDuration(v)
		*timeout = d
		return err
	})
	gone := fs.Bool("gone", false, "wait for the pattern to go away")
	every := fs.Duration("every", 100*time.Millisecond, "poll interval")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: neonmux wait [-t 10s] [-gone] <pane> <regexp>")
		return 2
	}
	re, err := regexp.Compile(fs.Arg(1))
	if err != nil {
		fmt.Fprintln(os.Stderr, "neonmux wait:", err)
		return 2
	}
	end := time.Now().Add(*timeout)
	for {
		lines, err := capture(fs.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, "neonmux:", err)
			return 1
		}
		hit := ""
		for _, l := range lines {
			if l = strings.TrimPrefix(l, "|"); re.MatchString(l) {
				hit = l
				break
			}
		}
		if (hit != "") != *gone {
			if hit != "" {
				fmt.Println(strings.TrimRight(hit, " "))
			}
			return 0
		}
		if time.Now().After(end) {
			fmt.Fprintf(os.Stderr, "neonmux wait: timeout after %v\n", *timeout)
			for _, l := range lines {
				fmt.Fprintln(os.Stderr, strings.TrimRight(strings.TrimPrefix(l, "|"), " "))
			}
			return 1
		}
		time.Sleep(*every)
	}
}

// capture reads a pane through the window, which knows every way to name
// it (number, name, program), or through the daemon without a window.
func capture(pane string) ([]string, error) {
	r := ui.Request{Cmd: "capture", Pane: pane}
	if _, err := ui.WindowSocket(); err == nil {
		return ui.SendWindow(r)
	}
	return ui.Send(r)
}
