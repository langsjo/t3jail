package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/langsjo/t3jail/internal/termsafe"
)

// Sideband channels (gitprotocol-pack(5)): data, progress and error.
const (
	bandData     = 1
	bandProgress = 2
	bandError    = 3
)

// filterGitWire copies what a git service in the VM sends (protocol v0/v1,
// gitprotocol-common(5) pkt-lines) to dst, masking control characters in
// every part git may print: ref names and capabilities, status lines such as
// a push's rejection reasons, and sideband progress and errors. git masks
// some of these itself, but not all. Masking keeps lengths, so the framing
// stays intact.
//
// binaryData says the sideband data channel carries a pack or archive, which
// is passed on as is. For receive-pack it carries the push's status report,
// itself pkt-lines of text, which is masked.
func filterGitWire(dst io.Writer, src io.Reader, binaryData bool) error {
	r := bufio.NewReaderSize(src, 65536)
	advertised := false // first flush seen: ref advertisement (or archive ACK) over
	buf := make([]byte, 65520)
	for {
		head, err := r.Peek(4)
		if errors.Is(err, io.EOF) && len(head) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		n, err := strconv.ParseUint(string(head), 16, 16)
		if err != nil {
			// Without sideband, the pack follows the negotiation raw. git
			// prints nothing of it.
			if advertised && binaryData && string(head) == "PACK" {
				_, err := io.Copy(dst, r)
				return err
			}
			return fmt.Errorf("the VM sent an invalid pkt-line header %q", termsafe.Escape(head))
		}
		switch {
		case n == 0:
			advertised = true
			fallthrough
		case n < 4: // delimiter and response-end packets of protocol v2
			if _, err := r.Discard(4); err != nil {
				return err
			}
			if _, err := dst.Write(head); err != nil {
				return err
			}
			continue
		case n > uint64(len(buf)):
			return fmt.Errorf("the VM sent a pkt-line of %d bytes", n)
		}
		pkt := buf[:n]
		if _, err := io.ReadFull(r, pkt); err != nil {
			return err
		}
		payload := pkt[4:]
		band := byte(0)
		if advertised && len(payload) > 0 && payload[0] <= bandError {
			band = payload[0]
		}
		switch {
		case band == bandData && binaryData:
		case band != 0:
			// git's progress meters redraw with carriage returns.
			termsafe.Mask(payload[1:], "\r")
		default:
			// NUL separates a ref from its capabilities.
			termsafe.Mask(payload, "\x00")
		}
		if _, err := dst.Write(pkt); err != nil {
			return err
		}
	}
}
