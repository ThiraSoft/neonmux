package ui

import (
	"bytes"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The battery comes from IOKit's power sources, called through purego
// without cgo. A reading takes about 60 µs, where running pmset costs
// about 5 ms of CPU.

const (
	frameworkIOKit = "/System/Library/Frameworks/IOKit.framework/IOKit"
	frameworkCF    = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cfUTF8         = 0x08000100 // kCFStringEncodingUTF8
	cfSInt64       = 4          // kCFNumberSInt64Type
)

var (
	iokitOnce sync.Once
	iokitOK   bool

	psCopyInfo     func() uintptr
	psCopyList     func(blob uintptr) uintptr
	psDescription  func(blob, ps uintptr) uintptr
	cfArrayCount   func(arr uintptr) int
	cfArrayAt      func(arr uintptr, i int) uintptr
	cfDictGet      func(dict, key uintptr) uintptr
	cfTypeID       func(ref uintptr) uintptr
	cfNumberTypeID func() uintptr
	cfStringTypeID func() uintptr
	cfNumberValue  func(num uintptr, typ int, out unsafe.Pointer) bool
	cfStringCreate func(alloc uintptr, s string, enc uint32) uintptr
	cfStringBytes  func(str uintptr, buf *byte, size int, enc uint32) bool
	cfRelease      func(ref uintptr)

	// Keys of a power source description (IOPSKeys.h).
	psType, psState, psCurrent, psMax uintptr
)

// loadIOKit opens the frameworks the first time a reading is needed: the
// daemon and the command line never load them.
func loadIOKit() bool {
	iokitOnce.Do(func() {
		iokit, err := purego.Dlopen(frameworkIOKit, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		cf, err := purego.Dlopen(frameworkCF, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		for _, f := range []struct {
			fn   any
			lib  uintptr
			name string
		}{
			{&psCopyInfo, iokit, "IOPSCopyPowerSourcesInfo"},
			{&psCopyList, iokit, "IOPSCopyPowerSourcesList"},
			{&psDescription, iokit, "IOPSGetPowerSourceDescription"},
			{&cfArrayCount, cf, "CFArrayGetCount"},
			{&cfArrayAt, cf, "CFArrayGetValueAtIndex"},
			{&cfDictGet, cf, "CFDictionaryGetValue"},
			{&cfTypeID, cf, "CFGetTypeID"},
			{&cfNumberTypeID, cf, "CFNumberGetTypeID"},
			{&cfStringTypeID, cf, "CFStringGetTypeID"},
			{&cfNumberValue, cf, "CFNumberGetValue"},
			{&cfStringCreate, cf, "CFStringCreateWithCString"},
			{&cfStringBytes, cf, "CFStringGetCString"},
			{&cfRelease, cf, "CFRelease"},
		} {
			sym, err := purego.Dlsym(f.lib, f.name)
			if err != nil {
				return
			}
			purego.RegisterFunc(f.fn, sym)
		}
		psType = cfStringCreate(0, "Type", cfUTF8)
		psState = cfStringCreate(0, "Power Source State", cfUTF8)
		psCurrent = cfStringCreate(0, "Current Capacity", cfUTF8)
		psMax = cfStringCreate(0, "Max Capacity", cfUTF8)
		iokitOK = psType != 0 && psState != 0 && psCurrent != 0 && psMax != 0
	})
	return iokitOK
}

// readBattery reads the internal battery of the Mac.
func readBattery() battery { return ioKitBattery() }

func ioKitBattery() battery {
	if !loadIOKit() {
		return battery{}
	}
	blob := psCopyInfo()
	if blob == 0 {
		return battery{}
	}
	defer cfRelease(blob)
	list := psCopyList(blob)
	if list == 0 {
		return battery{}
	}
	defer cfRelease(list)
	for i := range cfArrayCount(list) {
		d := psDescription(blob, cfArrayAt(list, i)) // not owned: no release
		if d == 0 || cfString(cfDictGet(d, psType)) != "InternalBattery" {
			continue
		}
		cur, ok := cfInt(cfDictGet(d, psCurrent))
		top, okMax := cfInt(cfDictGet(d, psMax))
		if !ok || !okMax || top <= 0 {
			continue
		}
		pct := int((cur*100 + top/2) / top)
		return battery{present: true, percent: min(max(pct, 0), 100), onAC: cfString(cfDictGet(d, psState)) == "AC Power"}
	}
	return battery{}
}

func cfInt(ref uintptr) (int64, bool) {
	if ref == 0 || cfTypeID(ref) != cfNumberTypeID() {
		return 0, false
	}
	var v int64
	ok := cfNumberValue(ref, cfSInt64, unsafe.Pointer(&v))
	return v, ok
}

func cfString(ref uintptr) string {
	if ref == 0 || cfTypeID(ref) != cfStringTypeID() {
		return ""
	}
	var buf [64]byte
	if !cfStringBytes(ref, &buf[0], len(buf), cfUTF8) {
		return ""
	}
	if n := bytes.IndexByte(buf[:], 0); n >= 0 {
		return string(buf[:n])
	}
	return ""
}
