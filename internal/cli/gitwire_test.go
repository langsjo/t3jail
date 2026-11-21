package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func pkt(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

func TestFilterGitWire(t *testing.T) {
	const oid = "0123456789012345678901234567890123456789"
	pack := "PACK\x00\x00\x00\x02\x1b[2J\x9b\xff"
	cases := []struct {
		name       string
		in, want   string
		binaryData bool
	}{{
		name: "ref advertisement",
		in:   pkt(oid+" HEAD\x00side-band-64k symref=HEAD:refs/heads/\x1b[2J\n") + pkt(oid+" refs/heads/\u009b31m\n") + "0000",
		want: pkt(oid+" HEAD\x00side-band-64k symref=HEAD:refs/heads/?[2J\n") + pkt(oid+" refs/heads/??31m\n") + "0000",
	}, {
		name:       "fetch with sideband",
		in:         pkt(oid+" HEAD\n") + "0000" + pkt("NAK\n") + pkt("\x01"+pack) + pkt("\x02Counting\r\x1b[1A\n") + pkt("\x03ERR \x1b]0;x\x07") + "0000",
		want:       pkt(oid+" HEAD\n") + "0000" + pkt("NAK\n") + pkt("\x01"+pack) + pkt("\x02Counting\r?[1A\n") + pkt("\x03ERR ?]0;x?") + "0000",
		binaryData: true,
	}, {
		name:       "fetch without sideband",
		in:         pkt(oid+" HEAD\n") + "0000" + pkt("NAK\n") + pack,
		want:       pkt(oid+" HEAD\n") + "0000" + pkt("NAK\n") + pack,
		binaryData: true,
	}, {
		// Before the advertisement ends, a sideband-like line is text.
		name:       "data band before flush",
		in:         pkt("\x01\x1b[2J") + "0000",
		want:       pkt("??[2J") + "0000",
		binaryData: true,
	}, {
		name: "push status report in sideband",
		in:   pkt(oid+" refs/heads/main\x00report-status side-band-64k\n") + "0000" + pkt("\x01"+pkt("unpack ok\n")+pkt("ng refs/heads/main evil \x1b]52;c;aGk=\x07\n")+"0000") + "0000",
		want: pkt(oid+" refs/heads/main\x00report-status side-band-64k\n") + "0000" + pkt("\x01"+pkt("unpack ok\n")+pkt("ng refs/heads/main evil ?]52;c;aGk=?\n")+"0000") + "0000",
	}, {
		name: "push status report without sideband",
		in:   pkt(oid+" refs/heads/main\n") + "0000" + pkt("ng refs/heads/main \u009b2J\n") + "0000",
		want: pkt(oid+" refs/heads/main\n") + "0000" + pkt("ng refs/heads/main ??2J\n") + "0000",
	}}
	for _, c := range cases {
		var out bytes.Buffer
		if err := filterGitWire(&out, strings.NewReader(c.in), c.binaryData); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if out.String() != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, out.String(), c.want)
		}
	}
}

func TestFilterGitWireRejectsBadFraming(t *testing.T) {
	for name, in := range map[string]string{
		"not a header":        "\x1b[2J",
		"raw pack in ref ads": "PACK\x00\x00\x00\x02",
		"raw pack in push":    pkt("x\n") + "0000PACK",
		"truncated":           "0010abc",
	} {
		if err := filterGitWire(&bytes.Buffer{}, strings.NewReader(in), name != "raw pack in push"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
