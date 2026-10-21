package ui

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// IOKit gives the same reading as pmset, without starting a process.
func TestIOKitMatchesPmset(t *testing.T) {
	out, err := exec.Command("/usr/bin/pmset", "-g", "batt").Output()
	if err != nil {
		t.Skip("pmset:", err)
	}
	want, got := parsePmset(string(out)), ioKitBattery()
	if got.present != want.present || got.onAC != want.onAC || abs(got.percent-want.percent) > 1 {
		t.Fatalf("IOKit %+v, pmset %+v", got, want)
	}
}

func BenchmarkIOKitBattery(b *testing.B) {
	for b.Loop() {
		ioKitBattery()
	}
}

func abs(n int) int { return max(n, -n) }

// parsePmset reads the output of `pmset -g batt`, the reference the IOKit
// reading is checked against:
//
//	Now drawing from 'AC Power'
//	 -InternalBattery-0 (id=123)	84%; charging; 1:02 remaining present: true
//
// A Mac without a battery only prints the first line.
func parsePmset(out string) battery {
	var b battery
	ac := false
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, "Now drawing from") {
			ac = strings.Contains(line, "'AC Power'")
		}
		if !strings.Contains(line, "InternalBattery") {
			continue
		}
		before, _, ok := strings.Cut(line, "%")
		if !ok {
			continue
		}
		pct, err := strconv.Atoi(before[strings.LastIndexAny(before, " \t")+1:])
		if err != nil {
			continue
		}
		b = battery{present: true, percent: min(max(pct, 0), 100)}
	}
	if b.present {
		b.onAC = ac
	}
	return b
}

func TestParsePmset(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      battery
	}{
		{"discharging", "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=123)\t84%; discharging; 5:12 remaining present: true\n",
			battery{present: true, percent: 84}},
		{"charged on AC", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=123)\t100%; charged; 0:00 remaining present: true\n",
			battery{present: true, percent: 100, onAC: true}},
		{"charging", "Now drawing from 'AC Power'\n -InternalBattery-0 (id=123)\t7%; charging; (no estimate) present: true\n",
			battery{present: true, percent: 7, onAC: true}},
		{"desktop Mac", "Now drawing from 'AC Power'\n", battery{}},
		{"no output", "", battery{}},
	} {
		if got := parsePmset(c.out); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
