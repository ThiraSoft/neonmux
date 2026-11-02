package prompt

import (
	"encoding/hex"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Protobuf encoding for the fixtures.
func pbVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}
func pbLen(field int, data []byte) []byte {
	b := pbVarint(uint64(field<<3 | 2))
	b = append(b, pbVarint(uint64(len(data)))...)
	return append(b, data...)
}
func pbInt(field int, v uint64) []byte { return cat(pbVarint(uint64(field<<3)), pbVarint(v)) }
func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func userStep(s string) []byte { return cat(pbInt(1, 14), pbLen(19, pbLen(2, []byte(s)))) }
func modelText(s string) []byte {
	return cat(pbInt(1, 15), pbLen(5, pbInt(3, 2)), pbLen(20, cat(pbLen(1, []byte(s)), pbLen(6, []byte("bot")))))
}
func modelTool(s string) []byte {
	return cat(pbInt(1, 15), pbLen(20, cat(pbLen(1, []byte(s)), pbLen(7, pbLen(2, []byte("run_command"))))))
}
func toolRun() []byte { return cat(pbInt(1, 132), pbLen(5, pbLen(4, pbLen(2, []byte("run_command"))))) }

type step struct {
	typ, status int
	payload     []byte
}

func agyDB(t *testing.T, steps ...step) string {
	t.Helper()
	if !AgyAvailable() {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "c.db")
	var sql strings.Builder
	sql.WriteString("create table steps (idx integer, step_type integer not null default 0, status integer not null default 0, step_payload blob, primary key (idx));")
	for i, s := range steps {
		sql.WriteString("insert into steps values (" + itoa(i) + "," + itoa(s.typ) + "," + itoa(s.status) + ",X'" + hex.EncodeToString(s.payload) + "');")
	}
	if out, err := exec.Command("sqlite3", db, sql.String()).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
	return db
}

func TestAgyReply(t *testing.T) {
	db := agyDB(t,
		step{14, 3, userStep("ancien")}, step{15, 3, modelText("ancienne reponse")},
		step{14, 3, userStep("fais le")},
		step{15, 3, modelTool("je regarde")}, step{132, 3, toolRun()},
		step{15, 3, modelText("Fini.")}, step{15, 3, modelText("Voilà.")},
	)
	r, ok := AgyReply(db, false)
	if !ok || r.Text != "Fini.\n\nVoilà." || r.ID != db+"#6" {
		t.Fatalf("got %+v %v", r, ok)
	}
	r, ok = AgyReply(db, true)
	if !ok || r.Text != "je regarde\n\nFini.\n\nVoilà." {
		t.Fatalf("intermediate got %+v %v", r, ok)
	}
}

func TestAgyTurnNotOver(t *testing.T) {
	db := agyDB(t, step{14, 3, userStep("go")}, step{15, 3, modelTool("je lance")})
	if r, ok := AgyReply(db, false); ok {
		t.Fatalf("tool call made a track: %+v", r)
	}
	db = agyDB(t, step{14, 3, userStep("go")}, step{15, 1, modelText("en cours")})
	if r, ok := AgyReply(db, false); ok {
		t.Fatalf("running step made a track: %+v", r)
	}
}

func TestAgyBadPayload(t *testing.T) {
	db := agyDB(t, step{14, 3, userStep("go")}, step{15, 3, []byte{0xff, 0xff, 0x01, 0x02}})
	if r, ok := AgyReply(db, false); ok {
		t.Fatalf("garbage made a track: %+v", r)
	}
	if _, ok := AgyReply(filepath.Join(t.TempDir(), "missing.db"), false); ok {
		t.Fatal("missing db made a track")
	}
}

func TestPbFieldGarbage(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		b := make([]byte, rng.IntN(64))
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		pbField(b, 20, 1)
		pbField(b, 1)
	}
	if v, ok := pbField(modelText("salut"), 20, 1); !ok || string(v) != "salut" {
		t.Fatalf("got %q %v", v, ok)
	}
	if _, ok := pbField(modelText("salut"), 20, 7); ok {
		t.Fatal("found a missing field")
	}
}

func TestAgyConversationOf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	dir := filepath.Join(t.TempDir(), "antigravity-cli", "conversations")
	os.MkdirAll(dir, 0o700)
	db := filepath.Join(dir, "abc.db")
	f, err := os.Create(db)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, ok := agyConversationOf([]int{os.Getpid()})
	if !ok || got != db {
		t.Fatalf("got %q %v, want %q", got, ok, db)
	}
}
