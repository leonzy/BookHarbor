package localmeta

// Palm text records may end in length-prefixed index entries and a UTF-8 overlap.
// They are outside the compressed stream, so remove them before PalmDOC decoding.
func palmWithoutTrailer(data []byte, flags uint32) ([]byte, bool) {
	for mask := flags >> 1; mask != 0; mask >>= 1 {
		if mask&1 == 0 {
			continue
		}
		length := 0
		for i := max(0, len(data)-4); i < len(data); i++ {
			b := data[i]
			if b&128 != 0 {
				length = 0
			}
			length = (length << 7) | int(b&127)
		}
		if length <= 0 || length > len(data) {
			return nil, false
		}
		data = data[:len(data)-length]
	}
	if flags&1 != 0 {
		if len(data) == 0 {
			return nil, false
		}
		length := int(data[len(data)-1]&3) + 1
		if length > len(data) {
			return nil, false
		}
		data = data[:len(data)-length]
	}
	return data, true
}
