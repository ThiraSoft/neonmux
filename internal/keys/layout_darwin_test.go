package keys

import (
	"os"
	"runtime"
	"testing"
)

// The main thread stays with the main goroutine: TestMain reads the active
// layout there, as the window does.
func init() { runtime.LockOSThread() }

// mainDigit1 is what the 1 key produces, without then with shift, read on
// the main thread.
var mainDigit1 [2]string

func TestMain(m *testing.M) {
	mainDigit1 = [2]string{systemKeyChar("1", false), systemKeyChar("1", true)}
	os.Exit(m.Run())
}

// The macOS binding answers on the main thread: the 1 key gives two
// different characters with and without shift ("1" and "!" in QWERTY,
// "&" and "1" in AZERTY).
func TestSystemLayoutOnMainThread(t *testing.T) {
	if !loadCarbon() {
		t.Skip("Text Input Sources unavailable")
	}
	a, b := mainDigit1[0], mainDigit1[1]
	t.Logf("1 key: %q, with shift %q", a, b)
	if a == "" || b == "" || a == b {
		t.Fatalf("unreadable layout: %q / %q", a, b)
	}
}

// Off the main thread, the layout is not read: macOS forbids it.
func TestSystemLayoutOffMainThread(t *testing.T) {
	if c := systemKeyChar("1", false); c != "" {
		t.Fatalf("read off the main thread: %q", c)
	}
}
