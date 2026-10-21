package keys

import (
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The keyboard layout comes from Text Input Sources (HIToolbox) and
// UCKeyTranslate (CoreServices), called through purego, without cgo. Both
// frameworks are only loaded when first needed: the daemon and the command
// line never open them.
//
// Since macOS 14, Text Input Sources requires the main thread. The window
// runs in SingleThread mode: Update, and therefore Encode, run there.
// Elsewhere (tests, another thread), systemKeyChar returns nothing and
// Encode keeps the names as they are.

var (
	carbonOnce sync.Once
	carbonOK   bool

	pthreadMainNP    func() int32
	tisCopyLayout    func() uintptr
	tisProperty      func(src, key uintptr) uintptr
	cfDataBytes      func(data uintptr) unsafe.Pointer
	cfRelease        func(ref uintptr)
	lmGetKbdType     func() uint8
	ucKeyTranslate   func(layout unsafe.Pointer, code, action uint16, mods, kbdType, options uint32, dead *uint32, maxLen uint64, n *uint64, out *uint16) int32
	propUnicodeTable uintptr // kTISPropertyUnicodeKeyLayoutData
)

const (
	ucKeyActionDisplay  = 3 // kUCKeyActionDisplay: the character shown on the key
	ucNoDeadKeys        = 1 // kUCKeyTranslateNoDeadKeysMask
	ucShiftKey          = 0x02
	frameworkCarbon     = "/System/Library/Frameworks/Carbon.framework/Carbon"
	frameworkCoreServ   = "/System/Library/Frameworks/CoreServices.framework/CoreServices"
	frameworkCoreFound  = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	frameworkLibSystem  = "/usr/lib/libSystem.B.dylib"
	propUnicodeTableSym = "kTISPropertyUnicodeKeyLayoutData"
)

func init() { keyChar = systemKeyChar }

// loadCarbon opens the frameworks and binds the functions. It returns
// false if one of them is missing.
func loadCarbon() bool {
	carbonOnce.Do(func() {
		open := func(path string) uintptr {
			h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				return 0
			}
			return h
		}
		bind := func(fn any, lib uintptr, name string) bool {
			if lib == 0 {
				return false
			}
			sym, err := purego.Dlsym(lib, name)
			if err != nil {
				return false
			}
			purego.RegisterFunc(fn, sym)
			return true
		}
		carbon, cs, cf, sys := open(frameworkCarbon), open(frameworkCoreServ), open(frameworkCoreFound), open(frameworkLibSystem)
		ok := bind(&pthreadMainNP, sys, "pthread_main_np") &&
			bind(&tisCopyLayout, carbon, "TISCopyCurrentKeyboardLayoutInputSource") &&
			bind(&tisProperty, carbon, "TISGetInputSourceProperty") &&
			bind(&lmGetKbdType, carbon, "LMGetKbdType") &&
			bind(&ucKeyTranslate, cs, "UCKeyTranslate") &&
			bind(&cfDataBytes, cf, "CFDataGetBytePtr") &&
			bind(&cfRelease, cf, "CFRelease")
		if !ok {
			return
		}
		// The property is a global variable that holds a CFStringRef.
		addr, err := purego.Dlsym(carbon, propUnicodeTableSym)
		if err != nil {
			return
		}
		propUnicodeTable = **(**uintptr)(unsafe.Pointer(&addr))
		carbonOK = propUnicodeTable != 0
	})
	return carbonOK
}

// systemKeyChar queries the active macOS layout (see keyChar).
func systemKeyChar(name string, shift bool) string {
	if !loadCarbon() || pthreadMainNP() == 0 {
		return ""
	}
	src := tisCopyLayout()
	if src == 0 {
		return ""
	}
	defer cfRelease(src)
	data := tisProperty(src, propUnicodeTable)
	if data == 0 {
		return "" // old layout without a Unicode table
	}
	table := cfDataBytes(data)
	kbd := uint32(lmGetKbdType())
	return layoutChar(name, shift, func(code uint16, shift bool) string {
		var mods uint32
		if shift {
			mods = ucShiftKey
		}
		var dead uint32
		var n uint64
		var buf [4]uint16
		if ucKeyTranslate(table, code, ucKeyActionDisplay, mods, kbd, ucNoDeadKeys, &dead, uint64(len(buf)), &n, &buf[0]) != 0 || n == 0 {
			return ""
		}
		return string(utf16.Decode(buf[:min(n, uint64(len(buf)))]))
	})
}
