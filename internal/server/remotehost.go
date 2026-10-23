package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/ThiraSoft/neonmux/internal/remote"
	"github.com/ThiraSoft/neonmux/internal/theme"
	"github.com/ThiraSoft/neonmux/vt"
)

// Options configures the daemon.
type Options struct {
	Remote   remote.Config
	RemoteOn bool // config "remote": true, enable at a normal start
}

// remoteHost gives the remote control server access to the sessions. The
// phone follows the same ownership rule as a window watcher (watch.go).
type remoteHost struct{ s *Server }

var _ remote.Host = (*remoteHost)(nil)

// layoutPane is the part of the window's layout.json the phone needs.
type layoutPane struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Garage bool   `json:"garage"`
}

// maxLayout caps the layout file read for the phone's list.
const maxLayout = 4 << 20

// readLayout reads the pane names and garage marks the window saved. A
// missing, oversized or broken file only means no marks.
func readLayout(path string) map[string]layoutPane {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLayout+1))
	if err != nil || len(data) > maxLayout {
		return nil
	}
	var st struct {
		Panes []layoutPane `json:"panes"`
	}
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	out := make(map[string]layoutPane, len(st.Panes))
	for _, p := range st.Panes {
		if p.Key != "" {
			out[p.Key] = p
		}
	}
	return out
}

var progStateNames = map[vt.ProgState]string{
	vt.PSIdle: "idle", vt.PSWorking: "working", vt.PSDone: "done", vt.PSError: "error", vt.PSBlocked: "blocked",
}

// Sessions lists the sessions like the window's picker: blocked agents
// first, then the other agents from most to least urgent, then the rest by
// key. Spares are left out.
func (h *remoteHost) Sessions() []remote.SessionInfo {
	layout := readLayout(filepath.Join(h.s.dir, "layout.json"))
	type entry struct {
		info  remote.SessionInfo
		state vt.ProgState
		since int64
	}
	var list []entry
	for _, se := range h.s.list() {
		if se.ephemeral.Load() {
			continue
		}
		e := entry{info: remote.SessionInfo{Key: se.key, Cwd: se.s.Cwd()}}
		lp := layout[se.key]
		e.info.Name, e.info.Garage = lp.Name, lp.Garage
		if e.info.Name == "" {
			e.info.Name = se.s.ForegroundName()
		}
		if ps, ok := se.s.Term.ProgramStatus(); ok {
			e.state, e.since = ps.State, ps.Since.UnixNano()
			e.info.Agent = &remote.AgentInfo{State: progStateNames[ps.State], Kind: ps.Kind, Msg: ps.Msg, Since: ps.Since}
		}
		list = append(list, e)
	}
	slices.SortFunc(list, func(x, y entry) int {
		// A higher ProgState is more urgent; no status (0) comes last.
		if x.state != y.state {
			return int(y.state) - int(x.state)
		}
		if x.state != 0 && x.since != y.since {
			if x.since < y.since {
				return -1
			}
			return 1
		}
		return strings.Compare(x.info.Key, y.info.Key)
	})
	out := make([]remote.SessionInfo, len(list))
	for i, e := range list {
		out[i] = e.info
	}
	return out
}

func (h *remoteHost) Watch(key string, w remote.Watcher) (func(), bool) {
	se := h.s.get(key)
	if se == nil || se.ephemeral.Load() {
		return nil, false
	}
	return se.addWatcher(w)
}

func (h *remoteHost) Terminal(key string) *vt.Terminal {
	if se := h.s.get(key); se != nil {
		return se.s.Terminal()
	}
	return nil
}

// Input goes through the ownership rule: w takes the session and its size.
func (h *remoteHost) Input(key string, w remote.Watcher, b []byte) {
	se := h.s.get(key)
	if se == nil || se.ephemeral.Load() {
		return
	}
	h.s.inputFrom(se, w, b)
}

// SetSize applies w's size only if w owns the session, within the bounds
// the wire protocol accepts.
func (h *remoteHost) SetSize(key string, w remote.Watcher) {
	se := h.s.get(key)
	if se == nil || w == nil || se.getOwner() != any(w) {
		return
	}
	cols, rows := w.Size()
	cols, rows = min(max(cols, 2), 500), min(max(rows, 1), 300)
	if tc, tr := sizeOf(se.s.Term); tc == cols && tr == rows {
		return
	}
	h.s.resize(se, ResizeReq{Cols: cols, Rows: rows})
}

// Palette is the window's theme when it sent one, else the default theme.
func (h *remoteHost) Palette() remote.Palette {
	h.s.mu.Lock()
	c := h.s.colors
	h.s.mu.Unlock()
	var p remote.Palette
	if c != nil {
		p.FG, p.BG, p.Cursor, p.P = c.FG, c.BG, c.Cursor, c.Palette
		return p
	}
	th := theme.Get(theme.Default)
	p.FG, p.BG, p.Cursor = theme.RGB(th.FG), theme.RGB(th.BG), theme.RGB(th.Cursor)
	for i := range p.P {
		p.P[i] = theme.RGB(th.Palette(uint8(i)))
	}
	return p
}

// remoteJob is a command from a window, run by remoteWorker.
type remoteJob struct {
	c   *conn
	cmd RemoteCmd
}

// initRemote loads the remote server (nothing listens yet). The window
// commands run one at a time, in order, off the window's read loop.
func (s *Server) initRemote(opt Options) error {
	cfg := opt.Remote
	if cfg.Dir == "" {
		cfg.Dir = filepath.Join(s.dir, "remote")
	}
	r, err := remote.New(cfg, &remoteHost{s: s})
	if err != nil {
		s.remErr = err.Error()
		return err
	}
	s.rem, s.remCfg = r, cfg
	s.remKick = make(chan struct{}, 1)
	s.remJobs = make(chan remoteJob, 16)
	r.OnChange(func() {
		select {
		case s.remKick <- struct{}{}:
		default:
		}
	})
	go s.pushRemoteState()
	go s.remoteWorker()
	return nil
}

// startRemote enables the server at daemon start: after a hot restart if
// it was on (remote/active left by upgrade), otherwise if the config asks.
// A remote/active left by a crash does not turn it on.
func (s *Server) startRemote(resumed, on bool) {
	if s.rem == nil {
		return
	}
	active := filepath.Join(s.remCfg.Dir, "active")
	if resumed {
		_, err := os.Stat(active)
		on = err == nil
	} else {
		_ = os.Remove(active)
	}
	if !on {
		return
	}
	if err := s.rem.Enable(); err != nil {
		log.Printf("remote control: %v", err)
	}
}

// remoteState is the state sent to windows.
func (s *Server) remoteState() remote.State {
	if s.rem == nil {
		return remote.State{Err: "remote control unavailable: " + s.remErr}
	}
	return s.rem.State()
}

// pushRemoteState sends the latest state to every window that speaks
// proto 3 after each change. A burst of changes ends with the last state.
func (s *Server) pushRemoteState() {
	for range s.remKick {
		st := s.rem.State()
		for _, c := range s.remoteWindows() {
			c.sendJSON(MsgRemoteState, "", st)
		}
	}
}

func (s *Server) remoteWindows() []*conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*conn
	for c := range s.wins {
		if c.proto.Load() >= 3 {
			out = append(out, c)
		}
	}
	return out
}

// remoteFromWindow queues a MsgRemote. Only a window that said proto 3 or
// more may drive the server; anything else is ignored.
func (s *Server) remoteFromWindow(c *conn, data []byte) {
	if c.proto.Load() < 3 {
		return
	}
	var cmd RemoteCmd
	if json.Unmarshal(data, &cmd) != nil {
		cmd = RemoteCmd{Op: "invalid"}
	}
	if s.rem == nil {
		c.sendJSON(MsgRemoteState, "", s.remoteState())
		return
	}
	select {
	case s.remJobs <- remoteJob{c: c, cmd: cmd}:
	default:
		st := s.rem.State()
		st.Err = "remote control busy"
		c.sendJSON(MsgRemoteState, "", st)
	}
}

func (s *Server) remoteWorker() {
	for j := range s.remJobs {
		if err := s.remoteOp(j.cmd); err != nil {
			st := s.rem.State()
			st.Err = err.Error()
			j.c.sendJSON(MsgRemoteState, "", st)
		}
	}
}

// remoteOp runs one command. remMu keeps it apart from the hot restart,
// which closes the server before exec.
func (s *Server) remoteOp(cmd RemoteCmd) error {
	s.remMu.Lock()
	defer s.remMu.Unlock()
	if s.upgrading.Load() {
		return errors.New("the daemon is restarting")
	}
	switch cmd.Op {
	case "on":
		return s.rem.Enable()
	case "off":
		s.rem.Disable()
	case "pair":
		_, err := s.rem.StartPairing()
		return err
	case "confirm":
		s.rem.ConfirmPairing(true)
	case "reject":
		s.rem.ConfirmPairing(false)
	case "revoke":
		if cmd.ID == "" {
			return errors.New("revoke: no device given")
		}
		return s.rem.Revoke(cmd.ID)
	case "regen":
		return s.rem.Regenerate()
	default:
		return fmt.Errorf("unknown remote command %q", cmd.Op)
	}
	return nil
}

// closeRemoteForUpgrade closes the sockets before exec, keeping
// remote/active so the new binary enables the server again. Returns
// whether it was on.
func (s *Server) closeRemoteForUpgrade() bool {
	if s.rem == nil {
		return false
	}
	s.remMu.Lock()
	defer s.remMu.Unlock()
	on := s.rem.State().Enabled
	if on {
		s.rem.Close()
	}
	return on
}

// cliText keeps a name chosen on the phone from driving the terminal that
// prints it.
func cliText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// serveRemoteCLI answers neonmux remote on|off|status|revoke. Pairing
// needs the window (QR code, code confirmation), so it is not offered here.
func (s *Server) serveRemoteCLI(w io.Writer, req Request) {
	if s.rem == nil {
		fmt.Fprintln(w, "error: remote control unavailable: "+s.remErr)
		return
	}
	op := ""
	if len(req.Args) > 0 {
		op = req.Args[0]
	}
	switch op {
	case "on", "off":
		if err := s.remoteOp(RemoteCmd{Op: op}); err != nil {
			fmt.Fprintln(w, "error: "+err.Error())
			return
		}
	case "status":
		s.remoteStatus(w)
	case "revoke":
		target := strings.TrimSpace(strings.Join(req.Args[1:], " "))
		id, err := s.findDevice(target)
		if err == nil {
			err = s.remoteOp(RemoteCmd{Op: "revoke", ID: id})
		}
		if err != nil {
			fmt.Fprintln(w, "error: "+err.Error())
			return
		}
	default:
		fmt.Fprintln(w, "error: usage: neonmux remote on|off|status|revoke <device name or id>")
		return
	}
	fmt.Fprintln(w, "ok")
}

// findDevice finds a device by id, or by a name only one device has.
func (s *Server) findDevice(target string) (string, error) {
	if target == "" {
		return "", errors.New("revoke: give a device name or id (neonmux remote status lists them)")
	}
	devs := s.rem.State().Devices
	var ids []string
	for _, d := range devs {
		if d.ID == target {
			return d.ID, nil
		}
		if d.Name == target {
			ids = append(ids, d.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no device %q (neonmux remote status lists them)", cliText(target))
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("several devices are named %q, revoke one by id: %s", cliText(target), cliText(strings.Join(ids, ", ")))
}

func (s *Server) remoteStatus(w io.Writer) {
	st := s.rem.State()
	fmt.Fprintf(w, "enabled: %v\n", st.Enabled)
	addrs := "none"
	if len(st.Addrs) > 0 {
		addrs = strings.Join(st.Addrs, ", ")
	}
	fmt.Fprintln(w, "addresses: "+addrs)
	page := fmt.Sprintf("http://127.0.0.1:%d", s.remCfg.PagePort)
	fmt.Fprintln(w, "page: "+page)
	// The page is reached from the phone through tailscale serve, on the
	// HTTPS port 1000 above the local one (8006 -> 9006).
	fmt.Fprintf(w, "tailscale: tailscale serve --bg --https=%d %s\n", s.remCfg.PagePort+1000, page)
	fmt.Fprintf(w, "devices: %d\n", len(st.Devices))
	for _, d := range st.Devices {
		seen := "never"
		if !d.LastSeen.IsZero() {
			seen = d.LastSeen.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %s  id %s  added %s  last seen %s\n", cliText(d.Name), cliText(d.ID), d.Added.Format("2006-01-02"), seen)
	}
	connected := "none"
	if len(st.Connected) > 0 {
		connected = cliText(strings.Join(st.Connected, ", "))
	}
	fmt.Fprintln(w, "connected: "+connected)
	if st.Err != "" {
		fmt.Fprintln(w, "last error: "+cliText(st.Err))
	}
	fmt.Fprintln(w, "log:")
	for _, l := range s.rem.Log(10) {
		fmt.Fprintln(w, "  "+cliText(l))
	}
}
