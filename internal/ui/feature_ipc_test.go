package ui

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSock returns a short socket path (104 bytes at most on macOS) in a
// temporary directory owned by the test.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "w.sock")
}

func ipcRequest(t *testing.T, sock, req string) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte(req + "\n"))
	l, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("no response: %v", err)
	}
	return l
}

// The window socket accepts send-keys: only its owner can reach it,
// whatever the umask.
func TestIPCSocketPrivate(t *testing.T) {
	sock := shortSock(t)
	a := featureApp()
	stop, err := StartIPC(a, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	st, err := os.Stat(sock)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600 (%v)", st.Mode().Perm(), err)
	}
	if got := ipcRequest(t, sock, `{"cmd":"rename","args":["x"]}`); got != "ok\n" {
		t.Fatalf("response %q", got)
	}
}

// Frozen window, full queue: the socket still replies once the timeout
// expires, instead of blocking forever.
func TestIPCFrozenWindow(t *testing.T) {
	defer func(d time.Duration) { ipcTimeout = d }(ipcTimeout)
	ipcTimeout = 200 * time.Millisecond
	sock := shortSock(t)
	a := featureApp()
	a.posted = make(chan func()) // nobody drains the queue
	stop, err := StartIPC(a, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, req := range []string{`{"cmd":"toast","args":["x"]}`, `{"cmd":"capture"}`} {
		if got := ipcRequest(t, sock, req); got != "error: the window is not responding\n" {
			t.Errorf("%s: %q", req, got)
		}
	}
	if a.postWithin(func() {}, 50*time.Millisecond) {
		t.Error("postWithin posted into a full queue")
	}
}

// Without the daemon, neonmux remote reaches the window, which cannot
// serve it: it says so instead of answering ok.
func TestIPCRemoteNeedsDaemon(t *testing.T) {
	sock := shortSock(t)
	a := featureApp()
	stop, err := StartIPC(a, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if got := ipcRequest(t, sock, `{"cmd":"remote","args":["on"]}`); got != "error: remote control needs the daemon\n" {
		t.Fatalf("response %q", got)
	}
}
