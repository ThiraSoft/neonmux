// neonmux: a GPU terminal multiplexer in Go and Ebiten, in the spirit of
// cmux, with a built-in file explorer, image viewer and audio player.
package main

import (
	"flag"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/ThiraSoft/neonmux/internal/fx"
	"github.com/ThiraSoft/neonmux/internal/remote"
	"github.com/ThiraSoft/neonmux/internal/server"
	"github.com/ThiraSoft/neonmux/internal/ui"
)

const usage = `neonmux: terminals, files, images and sound in a single window.

Usage:
  neonmux [options]            starts the application
  neonmux open <file>...       opens a file in a new pane (image, sound, directory, text)
  neonmux notify <message>     flags the current pane (handy in a Claude Code hook)
  neonmux fx <effect>          plays a visual effect: confetti, glitch
  neonmux theme <name>         switches the theme (neon-tokyo, synthwave, matrix, amber, ice, acid,
                               dark-magic, blood-moon, vaporwave, abyss)
  neonmux rename <name>        renames the current pane (empty: automatic name)
  neonmux float [-c corner] [-s COLSxROWS] [command…]
                               without a command: toggles the current pane between tile and floating;
                               with a command: opens it in a new floating pane.
                               corner: tl, tr, bl, br or center (default: first free corner)
  neonmux diff [directory]     opens the repository diff (changes since the last commit)
  neonmux agent <name> [prompt]
                               runs claude --worktree <name> in a new tile
  neonmux ls                   lists the terminals kept by the daemon
  neonmux capture [pane]       prints a pane's screen as text (key from neonmux ls,
                               program name, number in -no-server mode; default: the current pane)
  neonmux send-keys <pane> <key>...
                               sends keys to a pane's program: "text:ls -la",
                               enter, tab, escape, up, ctrl+c, j…; "wait:500ms" adds a pause
  neonmux restart              restarts the window and the daemon on the installed binary,
                               without interrupting running programs
  neonmux restart --force      kills everything (daemon, windows, terminals and their processes),
                               erases the saved sessions and starts a fresh window;
                               works even when everything is stuck ("-- args" for the window)
  neonmux restart-server       restarts only the daemon (hot restart)
  neonmux remote on|off|status
                               remote control from the phone: turns it on or off, shows its state
  neonmux remote revoke <device>
                               unpairs a phone (name or id from neonmux remote status)
  neonmux server               daemon for persistent terminals (started automatically)

Options:
`

// version is set at link time (-ldflags "-X main.version=v1.2.3").
var version string

// buildVersion returns the link-time version, or else the one the Go
// toolchain records: the module version for go install, a pseudo-version
// built from the git commit for a local build.
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "devel"
}

func main() {
	if len(os.Args) > 1 {
		switch cmd := os.Args[1]; cmd {
		case "icon":
			// Used by install.sh to build the icon of the .app bundle.
			if len(os.Args) < 3 || ui.WriteIconset(os.Args[2]) != nil {
				fmt.Fprintln(os.Stderr, "usage: neonmux icon <dir.iconset>")
				os.Exit(1)
			}
			return
		case "server":
			resume := ""
			if len(os.Args) > 3 && os.Args[2] == "--resume" {
				resume = os.Args[3]
			}
			// Run only returns on an error: the daemon's normal shutdown
			// goes through os.Exit.
			err := server.Run(server.SocketPath(), server.StateDir(), resume, serverOptions())
			fmt.Fprintln(os.Stderr, "neonmux server:", err)
			os.Exit(1)
		case "restart":
			if slices.Contains(os.Args[2:], "--force") {
				forceRestart(os.Args[2:])
				return
			}
			fallthrough
		case "open", "notify", "fx", "theme", "toast", "rename", "float", "diff", "agent", "ls", "kill-server", "restart-server", "remote":
			// A mistyped float option is reported here rather than in the window.
			if cmd == "float" {
				if _, err := ui.ParseFloatArgs(os.Args[2:]); err != nil {
					fmt.Fprintln(os.Stderr, "neonmux float:", err)
					os.Exit(2)
				}
			}
			lines, err := ui.Send(ui.Request{Cmd: cmd, Args: os.Args[2:]})
			for _, l := range lines {
				fmt.Println(l)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "neonmux:", err)
				os.Exit(1)
			}
			return
		case "capture", "send-keys":
			// After the case above, since restart falls through into it.
			os.Exit(paneCommand(cmd, os.Args[2:]))
		default:
			// An unknown word must never open a window (for example a
			// subcommand of a newer version run from a pane).
			if !strings.HasPrefix(cmd, "-") {
				fmt.Fprintf(os.Stderr, "neonmux: unknown command %q (neonmux -h for help)\n", cmd)
				os.Exit(2)
			}
		}
	}

	var (
		exec     = flag.String("e", "", "command for the first terminal (e.g. \"nvim main.go\")")
		demo     = flag.Bool("demo", false, "opens an editor, the file explorer, an image and a sound")
		shot     = flag.String("screenshot", "", "saves a PNG screenshot then quits (tests)")
		shotWait = flag.Duration("wait", 3*time.Second, "delay before the screenshot")
		th       = flag.String("theme", "", "theme at startup")
		font     = flag.String("font", "", "font: iosevka (default), pixel, gomono or the path of a .ttf/.otf/.ttc file")
		size     = flag.Float64("size", 0, "font size in points")
		width    = flag.Int("width", 1600, "window width")
		height   = flag.Int("height", 960, "window height")
		config   = flag.String("config", "", "config file (default ~/.config/neonmux/config.json)")
		script   = flag.String("script", "", "injected keys, separated by | (e.g. \"wait:2s|text:ls|enter|cmd+e\")")
		cpuprof  = flag.String("cpuprofile", "", "writes a pprof CPU profile")
		noServer = flag.Bool("no-server", false, "terminals tied to the window, with no daemon and no restore")
		persist  = flag.Bool("persist", false, "forces the daemon even with -e, -demo or -screenshot (tests)")
		fresh    = flag.Bool("fresh", false, "ignores the saved layout")
		showVer  = flag.Bool("version", false, "prints the version and exits")
	)
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Println("neonmux", buildVersion())
		return
	}

	if *cpuprof != "" {
		if f, err := os.Create(*cpuprof); err == nil {
			_ = pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}
	if *config != "" {
		os.Setenv("NEONMUX_CONFIG", *config)
	}
	cfg, warn, err := ui.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config ignored (nothing will be written to it until it is fixed):", err)
	}
	if warn != "" {
		fmt.Fprintln(os.Stderr, "config:", warn)
	}
	if *th != "" {
		cfg.Theme = *th
	}
	if *font != "" {
		cfg.Font = *font
	}
	if *size > 0 {
		cfg.FontSize = *size
	}
	// The options apply to this launch only: the window only saves what
	// changes afterwards.
	ui.SetConfigBase(cfg)
	if cfg.SymbolsFont == "" || cfg.SymbolsFont == "auto" {
		ui.PrefetchSymbolsFont() // looked up while the window opens
	}
	if cfg.Shell != "" {
		os.Setenv("SHELL", cfg.Shell)
	}
	// Launched from the Finder or Spotlight, the process starts in "/", and
	// GLFW (initialized by Ebiten before main) moves it to the bundle's
	// Contents/Resources: the first terminal then starts from home.
	if wd, err := os.Getwd(); err != nil || wd == "/" || strings.HasSuffix(wd, ".app/Contents/Resources") {
		if home, err := os.UserHomeDir(); err == nil {
			_ = os.Chdir(home)
		}
	}

	// At rest the app allocates nothing; under load, a compact heap is preferred.
	debug.SetGCPercent(60)
	debug.SetMemoryLimit(256 << 20)

	sock, err := windowSocket()
	if err != nil {
		fmt.Fprintln(os.Stderr, "window socket unavailable:", err)
		sock = ""
	}
	startDir, _ := os.Getwd()
	opts := ui.Options{Demo: *demo, Screenshot: *shot, ScreenshotWait: *shotWait, Socket: sock, StartDir: startDir, Fresh: *fresh}
	// A one-off launch (command, demo, screenshot) does not create a
	// persistent session unless explicitly asked to.
	oneShot := *exec != "" || *demo || *shot != ""
	opts.Persist = cfg.Persist && !*noServer && (!oneShot || *persist)
	if *script != "" {
		opts.Script = strings.Split(*script, "|")
	}
	if *exec != "" {
		opts.Exec = strings.Fields(*exec)
		// Loops, pipes, quotes: let the shell interpret it.
		if strings.ContainsAny(*exec, "'\"|;&$<>(){}*") {
			opts.Exec = []string{"/bin/sh", "-c", *exec}
		}
	}
	app, err := ui.New(cfg, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "neonmux:", err)
		os.Exit(1)
	}
	cleanStaleSockets()
	// PID file of the window: this is how the forced restart finds it.
	removeGuiPid := server.WritePidFile("gui-" + strconv.Itoa(os.Getpid()) + ".pid")
	defer removeGuiPid()
	stop := func() {}
	if sock != "" {
		if stop, err = ui.StartIPC(app, sock); err != nil {
			fmt.Fprintln(os.Stderr, "socket unavailable:", err)
		}
	}
	defer stop()
	// Killed by a signal, the process must still remove its socket.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		// macOS logout or kill: save and detach. A frozen window with a full
		// queue does not hold up the shutdown.
		done := make(chan struct{})
		if app.PostWithin(func() { app.Shutdown(); close(done) }, 300*time.Millisecond) {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
		stop()
		removeGuiPid()
		os.Exit(130)
	}()

	ebiten.SetWindowTitle("neonmux")
	ebiten.SetWindowDecorated(cfg.Decorations)
	// Closing goes through the app: layout saved, terminals detached.
	ebiten.SetWindowClosingHandled(true)
	ebiten.SetWindowSize(*width, *height)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetRunnableOnUnfocused(true)
	ebiten.SetScreenClearedEveryFrame(false)
	fx.Enable()

	if err := ebiten.RunGameWithOptions(app, &ebiten.RunGameOptions{SingleThread: true}); err != nil {
		fmt.Fprintln(os.Stderr, "neonmux:", err)
		os.Exit(1)
	}
}

// serverOptions reads the remote control settings for the daemon. A
// broken config leaves it off.
func serverOptions() server.Options {
	cfg, warn, err := ui.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "neonmux server: config ignored:", err)
		cfg = ui.DefaultConfig()
	} else if warn != "" {
		fmt.Fprintln(os.Stderr, "neonmux server: config:", warn)
	}
	name, _ := os.Hostname()
	name = strings.TrimSuffix(name, ".local")
	return server.Options{
		RemoteOn: cfg.Remote,
		Remote: remote.Config{
			Dir: filepath.Join(server.StateDir(), "remote"), Name: name,
			// ParseConfig already put invalid values back to their default.
			Port: cfg.RemotePort, PagePort: cfg.RemotePagePort, IdleOff: cfg.RemoteIdle(), Version: buildVersion(),
		},
	}
}

// maxSockPath is the maximum length of a Unix socket path on macOS: 104
// bytes, including the trailing zero (108 on Linux).
const maxSockPath = 103

// windowSockDirs returns the possible directories for the window socket:
// the daemon's, or else a short directory in os.TempDir().
func windowSockDirs() []string {
	return []string{server.InstanceDir(), filepath.Join(os.TempDir(), "neonmux-"+strconv.Itoa(os.Getuid()))}
}

// windowSocket returns the socket of this window (neonmux capture,
// send-keys, open… in daemonless mode), exported to panes in
// $NEONMUX_SOCK. It lives next to the daemon's, in its private directory
// (0700): on Linux, /tmp/neonmux-<pid>.sock had a predictable name and
// permissions that depended on the umask. A directory whose path is too
// long for a socket (a test NEONMUX_SERVER_SOCK, for example), or that is
// not ours, gives way to the next one.
func windowSocket() (string, error) {
	name := "gui-" + strconv.Itoa(os.Getpid()) + ".sock"
	err := fmt.Errorf("path too long for a socket: %s", filepath.Join(server.InstanceDir(), name))
	for _, dir := range windowSockDirs() {
		p := filepath.Join(dir, name)
		if len(p) > maxSockPath {
			continue
		}
		if err = server.PrivateDir(dir); err == nil {
			return p, nil
		}
	}
	return "", err
}

// cleanStaleSockets removes the sockets left by windows that are gone: in
// the new location, and the old /tmp/neonmux-<pid>.sock ones if they are
// ours.
func cleanStaleSockets() {
	for _, dir := range windowSockDirs() {
		removeStaleSockets(filepath.Join(dir, "gui-*.sock"), "gui-")
	}
	removeStaleSockets(filepath.Join(os.TempDir(), "neonmux-*.sock"), "neonmux-")
}

func removeStaleSockets(pattern, prefix string) {
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), prefix), ".sock"))
		if err != nil || pid == os.Getpid() || !ownSocket(m) {
			continue
		}
		// Signal 0: checks that the process exists without sending it anything.
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			os.Remove(m)
		}
	}
}

// ownSocket reports whether path is a socket that belongs to us.
func ownSocket(path string) bool {
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == os.Getuid()
}

// forceRestart is the way out when something is stuck: nothing goes
// through the daemon or the window.
func forceRestart(args []string) {
	detached := slices.Contains(args, "--detached")
	noLaunch := slices.Contains(args, "--no-launch")
	var fwd []string
	if i := slices.Index(args, "--"); i >= 0 {
		fwd = args[i+1:]
	}
	exe, err := os.Executable()
	if err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real // the bundle path, not the ~/.local/bin link
		}
	}
	if os.Getenv("NEONMUX_PANE") != "" && !detached {
		// Run from a pane: the terminal hosting us is about to be killed,
		// so carry on in a detached process.
		cmdArgs := append([]string{"restart", "--force", "--detached"}, args...)
		cmd := osexec.Command(exe, cmdArgs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		logPath := filepath.Join(server.StateDir(), "force.log")
		_ = os.MkdirAll(server.StateDir(), 0o700)
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close() // the started process keeps its copy
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "neonmux:", err)
			os.Exit(1)
		}
		_ = cmd.Process.Release()
		fmt.Println("forced restart started (log: " + logPath + ")")
		return
	}
	rep, err := server.ForceKill()
	if err != nil {
		fmt.Fprintln(os.Stderr, "neonmux:", err)
		os.Exit(1)
	}
	fmt.Println(time.Now().Format("15:04:05"), rep)
	fmt.Println("saved sessions and layout erased, config kept")
	if noLaunch {
		return
	}
	cmd := osexec.Command(exe, fwd...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "neonmux: relaunch failed:", err)
		os.Exit(1)
	}
	_ = cmd.Process.Release()
	fmt.Println("new window started")
}
