package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ThiraSoft/neonmux/internal/theme"
)

// battery is the state of the laptop battery shown in the status bar.
type battery struct {
	present bool
	percent int  // 0 to 100
	onAC    bool // plugged in, charging or full
}

// readPowerSupply reads the batteries and power adapters listed in a
// /sys/class/power_supply directory (Linux). The battery of a device, such
// as a wireless mouse, is ignored. Several batteries are averaged.
func readPowerSupply(dir string) battery {
	entries, _ := os.ReadDir(dir)
	sum, n := 0, 0
	mains, online, plugged := false, false, false
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch sysfsValue(p, "type") {
		case "Mains":
			mains = true
			online = online || sysfsValue(p, "online") == "1"
		case "Battery":
			if sysfsValue(p, "scope") == "Device" {
				continue
			}
			c, err := strconv.Atoi(sysfsValue(p, "capacity"))
			if err != nil {
				continue
			}
			sum, n = sum+c, n+1
			switch sysfsValue(p, "status") {
			case "Charging", "Full", "Not charging":
				plugged = true
			}
		}
	}
	if n == 0 {
		return battery{}
	}
	// The adapter is the reliable source. Without one, the battery status
	// says whether it is plugged in.
	if mains {
		plugged = online
	}
	return battery{present: true, percent: min(max(sum/n, 0), 100), onAC: plugged}
}

// batteryEvery is how often the battery is read. A reading costs at most
// about 60 µs (IOKit on macOS) and only wakes the render loop when
// something changed.
const batteryEvery = 5 * time.Second

// watchBattery reports a first reading, then a new one at each tick when it
// changes. Without a battery, it stops after the first one.
func watchBattery(tick <-chan time.Time, read func() battery, report func(battery)) {
	last := read()
	report(last)
	if !last.present {
		return
	}
	for range tick {
		if b := read(); b != last {
			last = b
			report(b)
		}
	}
}

func sysfsValue(dir, name string) string {
	data, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(data))
}

// setBattery records a new reading. The status bar text is built here, so
// that statusSignature does not allocate on every frame.
func (a *App) setBattery(b battery) {
	a.batt, a.battText = b, ""
	if b.present {
		a.battText = strconv.Itoa(b.percent) + "%"
	}
}

// startBattery watches the battery for the lifetime of the window. Turning
// the indicator off only hides it: the last reading is ready when it comes
// back.
func (a *App) startBattery() {
	go watchBattery(time.Tick(batteryEvery), readBattery, func(b battery) {
		a.post(func() { a.setBattery(b) })
	})
}

// batteryItem is the battery field of the status bar: a plug while plugged
// in, a battery filled to its level, then the percentage. It turns to the
// warning color under 20% when not plugged in.
func (a *App) batteryItem(th *theme.Theme) barItem {
	c := th.BarFG
	if a.batt.percent < 20 && !a.batt.onAC {
		c = th.Warn
	}
	it := barItem{text: a.battText, color: c, icons: []barIcon{{batteryIcon(a.batt.percent), c}}}
	if a.batt.onAC {
		it.icons = append([]barIcon{{"plug", th.Accent2}}, it.icons...)
	}
	return it
}

var batteryIcons = [...]string{"batt-0", "batt-1", "batt-2", "batt-3", "batt-4", "batt-5"}

// batteryIcon returns the icon for a level: the inside of the battery has
// five steps of 20%.
func batteryIcon(pct int) string {
	return batteryIcons[min(max((pct+19)/20, 0), 5)]
}

func init() {
	icons["plug"] = []string{".#..#.", ".#..#.", "######", "######", ".####.", "..##.."}
	for n, name := range batteryIcons {
		inside := "#" + strings.Repeat("#", n) + strings.Repeat(".", 5-n) + "##"
		icons[name] = []string{"........", "#######.", inside, inside, "#######.", "........"}
	}
}
