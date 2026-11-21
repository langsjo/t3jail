// Package termsafe makes guest-controlled text safe to print: control
// characters (terminal escape sequences in particular) are shown cat -v style
// instead of being interpreted by the user's terminal.
package termsafe

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf8"
)

// Escape keeps printable UTF-8, newlines and tabs; CRLF becomes LF; other
// control bytes become ^X (or M-^X / \xNN for invalid UTF-8).
func Escape(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	out := make([]byte, 0, len(b))
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		switch {
		case r == utf8.RuneError && size == 1:
			out = append(out, '\\', 'x', hex[b[0]>>4], hex[b[0]&0xf])
		case r == '\n' || r == '\t':
			out = append(out, byte(r))
		case r < 0x20:
			out = append(out, '^', byte(r)+'@')
		case r == 0x7f:
			out = append(out, '^', '?')
		case r >= 0x80 && r < 0xa0: // C1 controls, e.g. 8-bit CSI
			out = append(out, 'M', '-', '^', byte(r-0x80)+'@')
		default:
			out = append(out, b[:size]...)
		}
		b = b[size:]
	}
	return out
}

const hex = "0123456789abcdef"

// Mask is Escape for text inside a binary protocol, where lengths must not
// change: in place, it overwrites every byte of a control character (other
// than newline, tab and those in keep) and every invalid UTF-8 byte with '?'.
func Mask(b []byte, keep string) {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		bad := r == utf8.RuneError && size == 1 ||
			(r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0) &&
				r != '\n' && r != '\t' && !strings.ContainsRune(keep, r)
		if bad {
			for j := range size {
				b[i+j] = '?'
			}
		}
		i += size
	}
}

// Writer escapes everything written through it.
type Writer struct{ W io.Writer }

func (w Writer) Write(p []byte) (int, error) {
	if _, err := w.W.Write(Escape(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
