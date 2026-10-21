//go:build !darwin && !linux

package ui

func readBattery() battery { return battery{} }
