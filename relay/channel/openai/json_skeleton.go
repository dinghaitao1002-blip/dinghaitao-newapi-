package openai

import "bytes"

// jsonSkeletonWriter copies a JSON document while cutting every string value
// down to its first limit bytes. The result decodes to the same structure as
// the original (usage, error, data entries with their url / b64_json fields
// present and non-empty), so a multi-megabyte image response shrinks to a few
// kilobytes that the billing code can inspect without the body ever being
// held whole. Strings are cut at a UTF-8 rune boundary and never inside an
// escape sequence, so the skeleton stays valid JSON. It is a plain byte
// scanner, not a JSON decoder: input that is not JSON produces a skeleton
// that fails to decode exactly as the original would.
type jsonSkeletonWriter struct {
	limit int
	out   bytes.Buffer

	inString bool
	cutting  bool // the current string reached limit; its remaining bytes are dropped
	strLen   int  // bytes of the current string kept so far
	// escapeRemaining counts the bytes still owed to the escape sequence
	// being scanned; escapeUnicodeCheck marks the byte right after a
	// backslash, where a 'u' extends the sequence by four hex digits.
	escapeRemaining    int
	escapeUnicodeCheck bool
}

func (w *jsonSkeletonWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		w.push(b)
	}
	return len(p), nil
}

func (w *jsonSkeletonWriter) Bytes() []byte {
	return w.out.Bytes()
}

func (w *jsonSkeletonWriter) push(b byte) {
	if !w.inString {
		w.out.WriteByte(b)
		if b == '"' {
			w.inString, w.cutting, w.strLen = true, false, 0
		}
		return
	}
	if w.escapeRemaining > 0 {
		w.escapeRemaining--
		if w.escapeUnicodeCheck {
			w.escapeUnicodeCheck = false
			if b == 'u' {
				w.escapeRemaining = 4
			}
		}
		w.keep(b)
		return
	}
	switch b {
	case '\\':
		// Cut before an escape sequence starts rather than inside it.
		if w.strLen >= w.limit {
			w.cutting = true
		}
		w.escapeRemaining, w.escapeUnicodeCheck = 1, true
		w.keep(b)
	case '"':
		w.inString = false
		w.out.WriteByte(b)
	default:
		// Only cut in front of an ASCII byte or the first byte of a rune,
		// never between the continuation bytes of a multi-byte character.
		if w.strLen >= w.limit && b&0xC0 != 0x80 {
			w.cutting = true
		}
		w.keep(b)
	}
}

func (w *jsonSkeletonWriter) keep(b byte) {
	if w.cutting {
		return
	}
	w.out.WriteByte(b)
	w.strLen++
}
