package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// supply writes a fake /sys/class/power_supply entry.
func supply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadPowerSupply(t *testing.T) {
	root := t.TempDir()
	if got := readPowerSupply(root); got.present {
		t.Fatalf("no battery: %+v", got)
	}

	supply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "42", "status": "Discharging"})
	// A wireless mouse reports a battery too: it is not the laptop's.
	supply(t, root, "hidpp_battery_0", map[string]string{"type": "Battery", "scope": "Device", "capacity": "5", "status": "Discharging"})
	supply(t, root, "AC", map[string]string{"type": "Mains", "online": "0"})
	if got := readPowerSupply(root); got != (battery{present: true, percent: 42}) {
		t.Fatalf("on battery: %+v", got)
	}

	supply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
	if got := readPowerSupply(root); got != (battery{present: true, percent: 42, onAC: true}) {
		t.Fatalf("plugged in: %+v", got)
	}

	// Two batteries (some ThinkPads): their average.
	supply(t, root, "BAT1", map[string]string{"type": "Battery", "capacity": "80", "status": "Charging"})
	if got := readPowerSupply(root); got.percent != 61 {
		t.Fatalf("two batteries: %+v", got)
	}
}

// Without a Mains entry, the status of the battery tells whether it is
// plugged in.
func TestReadPowerSupplyStatusOnly(t *testing.T) {
	root := t.TempDir()
	supply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "100", "status": "Full"})
	if got := readPowerSupply(root); got != (battery{present: true, percent: 100, onAC: true}) {
		t.Fatalf("full: %+v", got)
	}
}

func TestBatteryInStatusBar(t *testing.T) {
	a := featureApp()
	a.setBattery(battery{present: true, percent: 42})
	now := time.Now()
	if sig := string(a.statusSignature(now)); !strings.Contains(sig, "42%") {
		t.Errorf("battery missing from the status bar: %q", sig)
	}
	a.cfg.Battery = false
	if sig := string(a.statusSignature(now)); strings.Contains(sig, "42%") {
		t.Errorf("battery shown although turned off: %q", sig)
	}
	a.cfg.Battery = true
	a.setBattery(battery{})
	if sig := string(a.statusSignature(now)); strings.Contains(sig, "%") {
		t.Errorf("battery shown without a battery: %q", sig)
	}
}

func TestBatteryToggle(t *testing.T) {
	configEnv(t, "")
	a := featureApp()
	if !a.cfg.Battery {
		t.Fatal("the battery indicator should be on by default")
	}
	a.do("battery.toggle")
	if a.cfg.Battery {
		t.Fatal("battery.toggle did not turn it off")
	}
}

func TestBatteryIconLevels(t *testing.T) {
	for pct, want := range map[int]string{0: "batt-0", 1: "batt-1", 20: "batt-1", 21: "batt-2", 84: "batt-5", 100: "batt-5"} {
		if got := batteryIcon(pct); got != want {
			t.Errorf("batteryIcon(%d) = %q, want %q", pct, got, want)
		}
		if len(icons[want]) == 0 {
			t.Errorf("no %s icon", want)
		}
	}
}

// Plugged in, a plug comes before the battery: the bolt already marks the
// power mode right next to it.
func TestBatteryItemPlug(t *testing.T) {
	a := featureApp()
	a.setBattery(battery{present: true, percent: 50, onAC: true})
	it := a.batteryItem(a.theme)
	if len(it.icons) != 2 || it.icons[0].kind != "plug" || len(icons["plug"]) == 0 {
		t.Fatalf("icons %+v", it.icons)
	}
	a.setBattery(battery{present: true, percent: 50})
	if it := a.batteryItem(a.theme); len(it.icons) != 1 {
		t.Fatalf("on battery: icons %+v", it.icons)
	}
}

// Readings that do not change are not reported: the render loop is only
// woken when the bar has something new to show.
func TestWatchBatteryReportsChanges(t *testing.T) {
	readings := []battery{{true, 80, false}, {true, 80, false}, {true, 79, false}, {true, 79, true}}
	i := 0
	read := func() battery {
		b := readings[min(i, len(readings)-1)]
		i++
		return b
	}
	tick := make(chan time.Time, 3)
	for range 3 {
		tick <- time.Now()
	}
	close(tick)
	var got []battery
	watchBattery(tick, read, func(b battery) { got = append(got, b) })
	want := []battery{{true, 80, false}, {true, 79, false}, {true, 79, true}}
	if !slices.Equal(got, want) {
		t.Fatalf("reported %+v, want %+v", got, want)
	}
}

// On a machine without a battery, the watch reports it once and stops.
func TestWatchBatteryStopsWithoutBattery(t *testing.T) {
	tick := make(chan time.Time) // never ticks: the watch has to return on its own
	reads := 0
	var got []battery
	watchBattery(tick, func() battery { reads++; return battery{} }, func(b battery) { got = append(got, b) })
	if reads != 1 || len(got) != 1 || got[0].present {
		t.Fatalf("%d readings, reported %+v", reads, got)
	}
}
