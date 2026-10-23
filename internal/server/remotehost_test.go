package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"

	"github.com/ThiraSoft/neonmux/internal/remote"
)

// testRemote gives the in-process daemon a remote server with its state in
// a temporary directory and ephemeral ports. Loopback stays ineligible:
// nothing here dials it.
func testRemote(t *testing.T) func(*Server) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote")
	return func(s *Server) {
		if err := s.initRemote(Options{Remote: remote.Config{Dir: dir, Name: "test"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.rem.Disable() })
	}
}

// rawWindow is a window connection driven frame by frame.
type rawWindow struct {
	c      net.Conn
	frames chan frame
}

func dialWindow(t *testing.T, sock string, proto int) *rawWindow {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	w := &rawWindow{c: c, frames: make(chan frame, 256)}
	go func() {
		r := bufio.NewReader(c)
		for {
			f, err := readFrame(r)
			if err != nil {
				close(w.frames)
				return
			}
			w.frames <- f
		}
	}()
	if proto > 0 {
		w.send(t, MsgHello, "", Hello{Proto: proto, Pid: os.Getpid()})
	}
	return w
}

func (w *rawWindow) send(t *testing.T, typ byte, key string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	bw := bufio.NewWriter(w.c)
	if err := writeFrame(bw, frame{typ: typ, key: key, data: data}); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
}

// waitState reads frames until a MsgRemoteState matching ok arrives.
func (w *rawWindow) waitState(t *testing.T, what string, ok func(remote.State) bool) remote.State {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, open := <-w.frames:
			if !open {
				t.Fatalf("connection closed while waiting for %s", what)
			}
			if f.typ != MsgRemoteState {
				continue
			}
			var st remote.State
			if err := json.Unmarshal(f.data, &st); err != nil {
				t.Fatalf("bad remote state: %v", err)
			}
			if ok(st) {
				return st
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestRemoteHostSessionsGarageAndAgents: the phone's list holds every
// session, the garage marks and names come from layout.json, and a blocked
// agent comes first.
func TestRemoteHostSessionsGarageAndAgents(t *testing.T) {
	s, _ := inProcess(t)
	startSess(t, s, "a", "sleep 60")
	startSess(t, s, "b", `printf '\033]7501;state=blocked:kind=permission\007'; sleep 60`)
	spare := startSess(t, s, "c", "sleep 60")
	spare.ephemeral.Store(true)
	layout := `{"version":1,"panes":[{"kind":"term","key":"a","name":"editor","garage":true},{"kind":"term","key":"b"}]}`
	if err := os.WriteFile(filepath.Join(s.dir, "layout.json"), []byte(layout), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &remoteHost{s: s}
	var list []remote.SessionInfo
	waitFor(t, "the blocked status", 5*time.Second, func() bool {
		list = h.Sessions()
		return len(list) == 2 && list[0].Agent != nil
	})
	if list[0].Key != "b" || list[0].Agent.State != "blocked" || list[0].Agent.Kind != "permission" || list[0].Garage {
		t.Errorf("first entry: %+v (agent %+v)", list[0], list[0].Agent)
	}
	if list[1].Key != "a" || !list[1].Garage || list[1].Name != "editor" || list[1].Agent != nil {
		t.Errorf("second entry: %+v", list[1])
	}
	if list[0].Name == "" || list[0].Cwd == "" {
		t.Errorf("name or directory missing: %+v", list[0])
	}

	// A broken layout only loses the marks.
	if err := os.WriteFile(filepath.Join(s.dir, "layout.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	list = h.Sessions()
	if len(list) != 2 || list[1].Garage || list[1].Name == "editor" {
		t.Errorf("with a broken layout: %+v", list)
	}
	os.Remove(filepath.Join(s.dir, "layout.json"))
	if list = h.Sessions(); len(list) != 2 {
		t.Errorf("without a layout: %+v", list)
	}
}

// TestRemoteHostOwnership: input and size from the phone follow the
// ownership rule of Task 1.
func TestRemoteHostOwnership(t *testing.T) {
	s, _ := inProcess(t)
	se := startSess(t, s, "a", "sleep 60")
	h := &remoteHost{s: s}
	w := newFakeWatcher(40, 10)
	other := newFakeWatcher(30, 8)
	unwatch, ok := h.Watch("a", w)
	if !ok {
		t.Fatal("Watch failed")
	}
	defer unwatch()

	// Not the owner yet: SetSize does nothing.
	h.SetSize("a", w)
	if c, r := sizeOf(se.s.Term); c != 80 || r != 24 {
		t.Fatalf("size before input: %dx%d", c, r)
	}
	h.Input("a", w, []byte("x"))
	if c, r := sizeOf(se.s.Term); c != 40 || r != 10 {
		t.Fatalf("size after input: %dx%d", c, r)
	}
	w.mu.Lock()
	w.cols, w.rows = 9999, 0
	w.mu.Unlock()
	h.SetSize("a", w)
	if c, r := sizeOf(se.s.Term); c != 500 || r != 1 {
		t.Fatalf("clamped size: %dx%d", c, r)
	}
	// Another watcher that does not own the session cannot resize it.
	h.SetSize("a", other)
	if c, _ := sizeOf(se.s.Term); c != 500 {
		t.Fatalf("a non-owner resized the session to %d columns", c)
	}
	if h.Terminal("a") != se.s.Term || h.Terminal("nope") != nil {
		t.Error("Terminal")
	}
	if _, ok := h.Watch("nope", w); ok {
		t.Error("Watch of an unknown session")
	}
}

// TestRemoteHostPalette: the window's colors, else the default theme's.
func TestRemoteHostPalette(t *testing.T) {
	s, _ := inProcess(t)
	h := &remoteHost{s: s}
	def := h.Palette()
	if def.FG == ([3]uint8{}) || def.P[1] == ([3]uint8{}) {
		t.Fatalf("default palette empty: %+v", def.FG)
	}
	col := Colors{FG: [3]uint8{1, 2, 3}, BG: [3]uint8{4, 5, 6}, Cursor: [3]uint8{7, 8, 9}}
	col.Palette[200] = [3]uint8{10, 11, 12}
	s.mu.Lock()
	s.colors = &col
	s.mu.Unlock()
	p := h.Palette()
	if p.FG != col.FG || p.BG != col.BG || p.Cursor != col.Cursor || p.P[200] != col.Palette[200] {
		t.Fatalf("window palette not used: %+v", p)
	}
}

// TestRemoteStateReachesWindow: a window that speaks proto 3 gets the state
// after its hello, and again after it turns the server on.
func TestRemoteStateReachesWindow(t *testing.T) {
	_, sock := inProcess(t, testRemote(t))
	w := dialWindow(t, sock, 3)
	w.waitState(t, "the state after hello", func(st remote.State) bool { return !st.Enabled })
	w.send(t, MsgRemote, "", RemoteCmd{Op: "on"})
	w.waitState(t, "the enabled state", func(st remote.State) bool { return st.Enabled })
	w.send(t, MsgRemote, "", RemoteCmd{Op: "off"})
	w.waitState(t, "the disabled state", func(st remote.State) bool { return !st.Enabled })
}

// TestRemoteUnknownOp: an unknown command is answered with an error and
// changes nothing.
func TestRemoteUnknownOp(t *testing.T) {
	s, sock := inProcess(t, testRemote(t))
	w := dialWindow(t, sock, 3)
	w.waitState(t, "the state after hello", func(remote.State) bool { return true })
	w.send(t, MsgRemote, "", RemoteCmd{Op: "enable"})
	st := w.waitState(t, "the error", func(st remote.State) bool { return st.Err != "" })
	if st.Enabled || s.rem.State().Enabled {
		t.Fatal("an unknown command turned the server on")
	}
	w.send(t, MsgRemote, "", RemoteCmd{Op: "revoke"})
	w.waitState(t, "the revoke error", func(st remote.State) bool { return st.Err != "" })
}

// TestOldWindowGetsNoRemoteFrames: a window that never said hello, or said
// an older protocol, never receives MsgRemoteState, and cannot drive the
// remote server.
func TestOldWindowGetsNoRemoteFrames(t *testing.T) {
	s, sock := inProcess(t, testRemote(t))
	startSess(t, s, "k", "echo hello; sleep 5")
	old := dialWindow(t, sock, 0)
	v2 := dialWindow(t, sock, 2)
	old.send(t, MsgRemote, "", RemoteCmd{Op: "on"})
	v2.send(t, MsgRemote, "", RemoteCmd{Op: "on"})
	old.send(t, MsgAttach, "k", AttachReq{Cols: 80, Rows: 24})
	time.Sleep(300 * time.Millisecond)
	if s.rem.State().Enabled {
		t.Fatal("an old window turned the server on")
	}
	// A change of state must not reach them either.
	if err := s.rem.Enable(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	snapshot := false
	for _, w := range []*rawWindow{old, v2} {
	drain:
		for {
			select {
			case f := <-w.frames:
				if f.typ == MsgRemoteState {
					t.Fatal("an old window received MsgRemoteState")
				}
				if f.typ == MsgSnapshot {
					snapshot = true
				}
			default:
				break drain
			}
		}
	}
	if !snapshot {
		t.Fatal("the old window received nothing at all")
	}
}

// TestCLIRemoteStatus: neonmux remote status is answered by the daemon.
func TestCLIRemoteStatus(t *testing.T) {
	_, sock := inProcess(t, testRemote(t))
	out, err := cliReq(sock, `{"cmd":"remote","args":["status"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "enabled: false\n") || !strings.HasSuffix(out, "ok\n") {
		t.Fatalf("status: %q", out)
	}
	if !strings.Contains(out, "tailscale serve --bg --https=") {
		t.Fatalf("no tailscale hint: %q", out)
	}
	out, _ = cliReq(sock, `{"cmd":"remote","args":["on"]}`, 3*time.Second)
	if out != "ok\n" {
		t.Fatalf("on: %q", out)
	}
	out, _ = cliReq(sock, `{"cmd":"remote","args":["status"]}`, 3*time.Second)
	if !strings.Contains(out, "enabled: true\n") {
		t.Fatalf("status after on: %q", out)
	}
	out, _ = cliReq(sock, `{"cmd":"remote","args":["off"]}`, 3*time.Second)
	if out != "ok\n" {
		t.Fatalf("off: %q", out)
	}
	out, _ = cliReq(sock, `{"cmd":"remote","args":["pair"]}`, 3*time.Second)
	if !strings.HasPrefix(out, "error") {
		t.Fatalf("pairing from the CLI: %q", out)
	}
}

// TestCLIRemoteRevoke: revoke by id or by a unique name; a name shared by
// several devices is refused and their ids listed.
func TestCLIRemoteRevoke(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	devs := `[{"id":"d1","name":"iphone"},{"id":"d2","name":"iphone"},{"id":"d3","name":"ipad"}]`
	if err := os.WriteFile(filepath.Join(dir, "devices.json"), []byte(devs), 0o600); err != nil {
		t.Fatal(err)
	}
	s, sock := inProcess(t, func(s *Server) {
		if err := s.initRemote(Options{Remote: remote.Config{Dir: dir}}); err != nil {
			t.Fatal(err)
		}
	})
	out, _ := cliReq(sock, `{"cmd":"remote","args":["revoke","iphone"]}`, 3*time.Second)
	if !strings.HasPrefix(out, "error") || !strings.Contains(out, "d1") || !strings.Contains(out, "d2") {
		t.Fatalf("ambiguous name: %q", out)
	}
	if n := len(s.rem.State().Devices); n != 3 {
		t.Fatalf("%d devices left after an ambiguous revoke", n)
	}
	if out, _ = cliReq(sock, `{"cmd":"remote","args":["revoke","ipad"]}`, 3*time.Second); out != "ok\n" {
		t.Fatalf("revoke by name: %q", out)
	}
	if out, _ = cliReq(sock, `{"cmd":"remote","args":["revoke","d2"]}`, 3*time.Second); out != "ok\n" {
		t.Fatalf("revoke by id: %q", out)
	}
	if out, _ = cliReq(sock, `{"cmd":"remote","args":["revoke","nobody"]}`, 3*time.Second); !strings.HasPrefix(out, "error") {
		t.Fatalf("unknown device: %q", out)
	}
	if st := s.rem.State(); len(st.Devices) != 1 || st.Devices[0].ID != "d1" {
		t.Fatalf("devices left: %+v", st.Devices)
	}
}

// freePort returns a port free right now on network ("udp" or "tcp").
func freePort(t *testing.T, network, host string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// qrInfo is what the phone reads in the pairing QR code.
type qrInfo struct {
	Addrs []string `json:"a"`
	Port  int      `json:"p"`
	Hash  string   `json:"h"`
}

func parseQR(t *testing.T, payload string) (qrInfo, [32]byte) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(payload, "nmx1:"))
	if err != nil {
		t.Fatalf("QR payload: %v", err)
	}
	var q qrInfo
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	h, err := base64.RawURLEncoding.DecodeString(q.Hash)
	if err != nil || len(h) != 32 {
		t.Fatalf("QR hash: %v", err)
	}
	return q, [32]byte(h)
}

// dialChallenge opens a WebTransport session pinned to hash, as the phone
// does, and reads the Challenge frame the daemon sends first.
func dialChallenge(ctx context.Context, addr string, hash [32]byte) (*webtransport.Session, error) {
	tc := &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin below
		VerifyConnection: func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 || sha256.Sum256(s.PeerCertificates[0].Raw) != hash {
				return errors.New("certificate hash mismatch")
			}
			return nil
		},
	}
	cl := &webtransport.Transport{
		TLSClientConfig: tc,
		QUICConfig:      &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
	}
	hdr := http.Header{}
	hdr.Set("Origin", "https://phone.test")
	_, sess, err := cl.Dial(ctx, "https://"+addr+"/nmx", hdr)
	if err != nil {
		return nil, err
	}
	str, err := sess.AcceptStream(ctx)
	if err != nil {
		sess.CloseWithError(0, "")
		return nil, err
	}
	// Wire frame: type (1 byte), payload length (u32 little endian), JSON.
	var hdrb [5]byte
	if _, err := io.ReadFull(str, hdrb[:]); err != nil {
		sess.CloseWithError(0, "")
		return nil, err
	}
	const wChallenge = 12
	n := binary.LittleEndian.Uint32(hdrb[1:])
	if hdrb[0] != wChallenge || n > 4096 {
		sess.CloseWithError(0, "")
		return nil, errors.New("first frame is not a Challenge: " + strconv.Itoa(int(hdrb[0])))
	}
	payload := make([]byte, n)
	var ch struct {
		Proto int    `json:"proto"`
		Nonce string `json:"nonce"`
	}
	if _, err := io.ReadFull(str, payload); err != nil || json.Unmarshal(payload, &ch) != nil || ch.Nonce == "" {
		sess.CloseWithError(0, "")
		return nil, errors.New("bad Challenge payload")
	}
	return sess, nil
}

// TestHotRestartKeepsRemoteOn (Review Focus 3): the real binary, remote on,
// a phone connected, then restart-server. The old server closes its
// sockets before exec (the phone's session ends at once), and the new
// daemon finds remote/active, listens again on the same private address
// with the same certificates, and answers a Challenge.
func TestHotRestartKeepsRemoteOn(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	bin := buildBinary(t)
	tmp := shortTmp(t)
	state := filepath.Join(tmp, "state")
	cfgDir := filepath.Join(tmp, "config")
	if err := os.MkdirAll(filepath.Join(cfgDir, "neonmux"), 0o700); err != nil {
		t.Fatal(err)
	}
	port := freePort(t, "udp", "0.0.0.0")
	pagePort := freePort(t, "tcp", "127.0.0.1")
	cfg := `{"remote": false, "remote_idle_off": "0", "remote_port": ` + strconv.Itoa(port) +
		`, "remote_page_port": ` + strconv.Itoa(pagePort) + `}`
	if err := os.WriteFile(filepath.Join(cfgDir, "neonmux", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(tmp, "neonmux", "server.sock")
	cmd := exec.Command(bin, "server")
	cmd.Env = append(os.Environ(), "NEONMUX_SERVER_SOCK="+sock, "NEONMUX_STATE="+state, "SHELL=/bin/sh",
		"XDG_CONFIG_HOME="+cfgDir, "NEONMUX_CONFIG=")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	waitFor(t, "the daemon", 5*time.Second, func() bool {
		c, err := net.Dial("unix", sock)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	if out, err := cliReq(sock, `{"cmd":"remote","args":["on"]}`, 5*time.Second); err != nil || out != "ok\n" {
		t.Fatalf("remote on: %v %q", err, out)
	}
	active := filepath.Join(state, "remote", "active")
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("no remote/active after on: %v", err)
	}

	// The window opens a pairing, as the phone needs one to be let in
	// before it is paired. The QR gives the addresses, port and pin.
	pair := func() (qrInfo, [32]byte) {
		w := dialWindow(t, sock, 3)
		w.send(t, MsgRemote, "", RemoteCmd{Op: "pair"})
		st := w.waitState(t, "the pairing", func(st remote.State) bool { return st.Pairing != nil })
		return parseQR(t, st.Pairing.Payload)
	}
	q, hash := pair()
	if len(q.Addrs) == 0 {
		t.Skip("no private address (RFC 1918 or Tailscale) on this machine: the remote server has nothing to listen on")
	}
	if q.Port != port {
		t.Fatalf("QR port %d, configured %d", q.Port, port)
	}
	addr := net.JoinHostPort(q.Addrs[0], strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	phone, err := dialChallenge(ctx, addr, hash)
	if err != nil {
		t.Fatalf("before the restart: %v", err)
	}
	defer phone.CloseWithError(0, "")

	if out, err := cliReq(sock, `{"cmd":"restart-server"}`, 3*time.Second); err != nil {
		t.Fatalf("restart-server: %v %q", err, out)
	}
	// The old server closed the phone's session before exec: it does not
	// wait for the QUIC idle timeout.
	select {
	case <-phone.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the phone's session was not closed by the restart")
	}
	waitFor(t, "the new daemon", 5*time.Second, func() bool {
		out, err := cliReq(sock, `{"cmd":"remote","args":["status"]}`, 2*time.Second)
		return err == nil && strings.Contains(out, "enabled: true")
	})
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("remote/active gone after the restart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "upgrade.json")); !os.IsNotExist(err) {
		t.Fatalf("the hot restart did not complete: %v", err)
	}

	// Same certificates (the pin from the first QR still holds), same
	// address and port.
	q2, hash2 := pair()
	if hash2 != hash {
		t.Fatal("the certificate changed across the restart")
	}
	if q2.Port != port {
		t.Fatalf("port after restart: %d", q2.Port)
	}
	var sess *webtransport.Session
	waitFor(t, "a Challenge from the new daemon", 10*time.Second, func() bool {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sess, err = dialChallenge(c, addr, hash)
		return err == nil
	})
	sess.CloseWithError(0, "")
}
