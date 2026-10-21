package ui

// readBattery reads the batteries listed by the kernel.
func readBattery() battery { return readPowerSupply("/sys/class/power_supply") }
