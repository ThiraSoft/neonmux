package server

import (
	"strings"
	"testing"
	"time"
)

func TestCLIRemoteDevices(t *testing.T) {
	_, sock := inProcess(t, testRemote(t))
	out, err := cliReq(sock, `{"cmd":"remote","args":["devices"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "no paired devices") && !strings.Contains(out, "ID") {
		t.Fatalf("unexpected devices output: %q", out)
	}
}

func TestCLIScheduleCommands(t *testing.T) {
	_, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()

	r := cl.Create(CreateReq{Key: "k1", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	// 1. Schedule list when empty
	out, err := cliReq(sock, `{"cmd":"schedule","args":["list"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "no scheduled commands") {
		t.Fatalf("expected no scheduled commands, got %q", out)
	}

	// 2. Schedule a command
	out, err = cliReq(sock, `{"cmd":"schedule","args":["k1","+10m","continue"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "scheduled job #") || !strings.Contains(out, "continue") {
		t.Fatalf("schedule failed: %q", out)
	}

	// 3. Schedule list with job
	out, err = cliReq(sock, `{"cmd":"schedule","args":["list"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "k1") || !strings.Contains(out, "continue") {
		t.Fatalf("job not listed: %q", out)
	}

	// Extract job ID
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least header and 1 job line, got %q", out)
	}
	fields := strings.Fields(lines[1])
	jobID := fields[0]

	// 4. Cancel job
	out, err = cliReq(sock, `{"cmd":"schedule","args":["cancel", "`+jobID+`"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "cancelled job #"+jobID) {
		t.Fatalf("cancel failed: %q", out)
	}

	// 5. Schedule list empty again
	out, err = cliReq(sock, `{"cmd":"schedule","args":["list"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "no scheduled commands") {
		t.Fatalf("expected no scheduled commands after cancel, got %q", out)
	}
}
