package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ThiraSoft/neonmux/internal/remote"
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
	if !strings.Contains(out, "#") || !strings.Contains(out, "continue") {
		t.Fatalf("schedule failed: %q", out)
	}

	// 3. Schedule list with job, as the window reads it
	out, err = cliReq(sock, `{"cmd":"schedule","args":["list","--json"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	var jobs []remote.ScheduleJob
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &jobs); err != nil || len(jobs) != 1 || jobs[0].Pane != "k1" || jobs[0].Desc != "continue" {
		t.Fatalf("job not listed: %q %v", out, err)
	}
	jobID := jobs[0].ID

	// 4. Cancel job
	out, err = cliReq(sock, `{"cmd":"schedule","args":["cancel", "`+jobID+`"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	if !strings.Contains(out, "cancelled #"+jobID) {
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

// A scheduled command is typed into its pane when its time comes.
func TestScheduleRuns(t *testing.T) {
	_, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()
	r := cl.Create(CreateReq{Key: "k1", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	out, err := cliReq(sock, `{"cmd":"schedule","args":["k1","+1s","echo","sched-$((40+2))"]}`, 3*time.Second)
	if err != nil {
		t.Fatalf("%v %q", err, out)
	}
	waitFor(t, "the command ran", 5*time.Second, func() bool {
		return strings.Contains(strings.Join(r.Terminal().ScreenText(), "\n"), "sched-42")
	})
}

func TestScheduleRefusals(t *testing.T) {
	s, sock := inProcess(t)
	cl := connect(t, sock)
	defer cl.Close()
	r := cl.Create(CreateReq{Key: "k1", Dir: t.TempDir(), Cols: 80, Rows: 24})
	waitFor(t, "remote ready", 3*time.Second, func() bool { return r.Ready() })

	for _, args := range []string{`["k1","5","continue"]`, `["k1","25:99","continue"]`, `["nope","+5m","continue"]`} {
		if out, err := cliReq(sock, `{"cmd":"schedule","args":`+args+`}`, 3*time.Second); err == nil {
			t.Errorf("%s accepted: %q", args, out)
		}
	}
	if _, err := s.Schedule("k1", "+5m", "  "); err == nil {
		t.Error("empty command accepted")
	}
	s.get("k1").ephemeral.Store(true)
	if _, err := s.Schedule("k1", "+5m", "continue"); err == nil {
		t.Error("a spare pane accepted a job")
	}
}
