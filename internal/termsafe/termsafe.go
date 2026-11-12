// Package termsafe makes guest-controlled text safe to print: control
// characters (terminal escape sequences in particular) are shown cat -v style
// instead of being interpreted by the user's terminal.
package termsafe

import (
	"bytes"
	"io"
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

// Writer escapes everything written through it.
type Writer struct{ W io.Writer }

func (w Writer) Write(p []byte) (int, error) {
	if _, err := w.W.Write(Escape(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
