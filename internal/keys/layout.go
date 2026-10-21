package keys

// macOS virtual key codes (kVK_ANSI_0 to kVK_ANSI_9) of the digit keys:
// a digit's name stands for the key at that physical position.
var digitCodes = [10]uint16{0x1d, 0x12, 0x13, 0x14, 0x15, 0x17, 0x16, 0x1a, 0x1c, 0x19}

// charCodes lists the virtual key codes of keys that produce a character:
// letters, digits, punctuation, the ISO key left of 1 (0x0a) and the two
// keys specific to Japanese keyboards. The numeric keypad is not part of
// it: its period must not answer in place of the main keyboard's.
var charCodes = func() []uint16 {
	var c []uint16
	for code := uint16(0); code <= 0x32; code++ {
		switch code {
		case 0x24, 0x30, 0x31: // return, tab, space
			continue
		}
		c = append(c, code)
	}
	return append(c, 0x5d, 0x5e)
}()

func isDigit(name string) bool { return len(name) == 1 && name[0] >= '0' && name[0] <= '9' }

// layoutChar returns the character the key named name produces, with or
// without shift, using tr, which translates a virtual key code. A digit
// stands for a physical position; any other name is the key's character
// without modifiers, which is used to find it.
func layoutChar(name string, shift bool, tr func(code uint16, shift bool) string) string {
	if isDigit(name) {
		return tr(digitCodes[name[0]-'0'], shift)
	}
	if !shift {
		return name
	}
	for _, code := range charCodes {
		if tr(code, false) == name {
			return tr(code, true)
		}
	}
	return ""
}
