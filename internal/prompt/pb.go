package prompt

// pbField reads a length-delimited field of a protobuf message, following
// path (field numbers, outermost first). It knows no schema: agy has none
// published. It never panics, whatever the bytes.
func pbField(msg []byte, path ...int) ([]byte, bool) {
	for _, want := range path {
		found := false
		for i := 0; i < len(msg); {
			key, n := pbUvarint(msg[i:])
			if n <= 0 {
				return nil, false
			}
			i += n
			field, typ := int(key>>3), key&7
			switch typ {
			case 0:
				_, n := pbUvarint(msg[i:])
				if n <= 0 {
					return nil, false
				}
				i += n
			case 1:
				i += 8
			case 5:
				i += 4
			case 2:
				l, n := pbUvarint(msg[i:])
				if n <= 0 || l > uint64(len(msg)-i-n) {
					return nil, false
				}
				i += n
				if field == want {
					msg = msg[i : i+int(l)]
					found = true
				} else {
					i += int(l)
				}
			default:
				return nil, false
			}
			if found {
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return msg, true
}

func pbUvarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}
