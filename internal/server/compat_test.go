package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Compatibility with the previous version of the daemon. These tests only
// run if NEONMUX_OLD_BIN points to a binary of that version (go build on
// the old code), since the repository does not keep one.

func oldBinary(t *testing.T) string {
	t.Helper()
	p := os.Getenv("NEONMUX_OLD_BIN")
	if p == "" {
		t.Skip("NEONMUX_OLD_BIN absent")
	}
	return p
}

func copyExe(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	// New file, like an install: the running process keeps the old one.
	_ = os.Remove(dst)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestCompatNewClientOldDaemon: a recent window with an old daemon, during
// the transition.
func TestCompatNewClientOldDaemon(t *testing.T) {
	old := oldBinary(t)
	tmp := shortTmp(t)
	d := startDaemon(t, old, tmp, filepath.Join(tmp, "state"))
	cl := connect(t, d.sock)
	r := cl.Create(CreateReq{Key: "k", Dir: tmp, Cols: 60, Rows: 10, Scrollback: 100})
	r.Write([]byte("echo compat-old\n"))
	waitText(t, r, "compat-old\n")
	time.Sleep(300 * time.Millisecond)
	if cl.ServerProto() != 0 || !cl.Alive() {
		t.Fatalf("old daemon: ServerProto=%d Alive=%v", cl.ServerProto(), cl.Alive())
	}
	r.Detach()
	cl.Close()
	cl2 := connect(t, d.sock)
	defer cl2.Close()
	r2 := cl2.Attach("k", 70, 12, 100)
	waitText(t, r2, "compat-old")
	r2.Write([]byte("echo again\n"))
	waitText(t, r2, "again\n")
}

// TestCompatUpgradeFromOldBinary: the restart-server that will move a
// daemon from the previous version to the new binary. The old daemon
// freezes its sessions and execs the installed file: the new binary reads
// back its upgrade.json and snapshots, and the shell goes on.
func TestCompatUpgradeFromOldBinary(t *testing.T) {
	old := oldBinary(t)
	bin := buildBinary(t)
	tmp := shortTmp(t)
	if err := os.Mkdir(filepath.Join(tmp, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(tmp, "bin", "neonmux")
	copyExe(t, old, exe)
	state := filepath.Join(tmp, "state")
	d := startDaemon(t, exe, tmp, state)
	daemonPid := d.cmd.Process.Pid
	cl := connect(t, d.sock)
	defer cl.Close()
	r := cl.Create(CreateReq{Key: "hot", Dir: tmp, Cols: 70, Rows: 12, Scrollback: 100})
	r.Write([]byte("x=42; echo before pid=$$\n"))
	waitText(t, r, "before pid=")
	before := termText(r.Terminal())
	i := strings.Index(before, "\nbefore pid=")
	shellPid := strings.Fields(before[i+len("\nbefore pid="):])[0]

	copyExe(t, bin, exe) // "install" of the new version
	if out, err := cliReq(d.sock, `{"cmd":"restart-server"}`, 2*time.Second); err != nil {
		t.Fatalf("restart-server: %v %q", err, out)
	}
	time.Sleep(500 * time.Millisecond)
	r.Write([]byte("echo after x=$x pid=$$\n"))
	waitText(t, r, "after x=42 pid="+shellPid)
	waitText(t, r, "before pid="+shellPid)
	if err := syscall.Kill(daemonPid, 0); err != nil {
		t.Fatalf("the daemon changed process: %v", err)
	}
	// It really is the new binary answering.
	waitFor(t, "the new daemon's hello reply", 3*time.Second, func() bool { return cl.ServerProto() == protoVersion })
	if _, err := os.Stat(filepath.Join(state, "upgrade.json")); !os.IsNotExist(err) {
		t.Fatalf("upgrade.json left after the resume: %v", err)
	}
	if out, err := cliReq(d.sock, `{"cmd":"ls"}`, 2*time.Second); err != nil || !strings.Contains(out, "hot\t") {
		t.Fatalf("ls after the handover: %v %q", err, out)
	}
	if st, err := os.Stat(d.sock); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket after the handover: %v %v", st.Mode(), err)
	}
}
