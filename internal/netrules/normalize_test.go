package netrules

import (
	"net/netip"
	"slices"
	"testing"
)

func dest(prefix string, first, last uint16) Destination {
	return Destination{Prefix: netip.MustParsePrefix(prefix), Ports: PortRange{TCP, first, last}}
}

func udp(prefix string, first, last uint16) Destination {
	return Destination{Prefix: netip.MustParsePrefix(prefix), Ports: PortRange{UDP, first, last}}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   []Destination
		want []Destination
	}{
		{
			name: "same address, overlapping and adjacent ports merge",
			in:   []Destination{dest("192.0.2.10/32", 80, 90), dest("192.0.2.10/32", 85, 95), dest("192.0.2.10/32", 96, 96), dest("192.0.2.10/32", 443, 443)},
			want: []Destination{dest("192.0.2.10/32", 80, 96), dest("192.0.2.10/32", 443, 443)},
		},
		{
			name: "address inside an allowed network keeps only extra ports",
			in:   []Destination{dest("203.0.113.0/24", 80, 90), dest("203.0.113.5/32", 85, 100), dest("203.0.113.6/32", 80, 80)},
			want: []Destination{dest("203.0.113.0/24", 80, 90), dest("203.0.113.5/32", 91, 100)},
		},
		{
			name: "outer ports split an inner range",
			in:   []Destination{dest("10.0.0.0/8", 443, 443), dest("10.1.0.0/16", 1, 65535)},
			want: []Destination{dest("10.0.0.0/8", 443, 443), dest("10.1.0.0/16", 1, 442), dest("10.1.0.0/16", 444, 65535)},
		},
		{
			name: "protocols merge and subtract separately",
			in:   []Destination{dest("203.0.113.0/24", 50, 60), udp("203.0.113.0/24", 53, 53), udp("203.0.113.5/32", 50, 60), dest("203.0.113.5/32", 61, 61), udp("203.0.113.5/32", 61, 61)},
			want: []Destination{dest("203.0.113.0/24", 50, 60), udp("203.0.113.0/24", 53, 53), dest("203.0.113.5/32", 61, 61), udp("203.0.113.5/32", 50, 52), udp("203.0.113.5/32", 54, 61)},
		},
		{
			name: "disjoint networks and families are untouched",
			in:   []Destination{dest("2001:db8::/32", 22, 22), dest("198.51.100.0/24", 22, 22)},
			want: []Destination{dest("198.51.100.0/24", 22, 22), dest("2001:db8::/32", 22, 22)},
		},
	}
	for _, c := range cases {
		got := Normalize(c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got  %v\n want %v", c.name, got, c.want)
		}
		// Normalizing must never change what is allowed.
		for _, d := range c.in {
			for _, port := range []uint16{d.Ports.First, d.Ports.Last} {
				if !Covers(got, d.Prefix.Addr(), d.Ports.Proto, port) {
					t.Errorf("%s: lost %v port %d", c.name, d.Prefix, port)
				}
			}
		}
	}
}
