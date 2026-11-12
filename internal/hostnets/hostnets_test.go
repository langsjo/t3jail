package hostnets

import (
	"net/netip"
	"slices"
	"testing"
)

func p(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestComputeBlocksAddressesTheirSubnetsAndLinkRoutes(t *testing.T) {
	got := Compute(
		[]netip.Prefix{p("192.168.1.23/24"), p("2a01:4f8:1:2::5/64"), p("100.101.102.103/32")},
		[]netip.Prefix{p("192.168.1.0/24"), p("10.8.0.0/16"), p("0.0.0.0/0")},
	)
	want := []netip.Prefix{
		p("10.8.0.0/16"),
		p("100.101.102.103/32"),
		p("192.168.1.0/24"), p("192.168.1.23/32"),
		p("2a01:4f8:1:2::/64"), p("2a01:4f8:1:2::5/128"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestToPrefix(t *testing.T) {
	got, ok := toPrefix([]byte{192, 168, 1, 23}, []byte{255, 255, 255, 0})
	if !ok || got != p("192.168.1.23/24") {
		t.Errorf("got %v", got)
	}
	// IPv4 addresses often come back 16 bytes long from netlink.
	v4in6 := netip.MustParseAddr("::ffff:10.0.0.1").AsSlice()
	got, ok = toPrefix(v4in6, []byte{255, 0, 0, 0})
	if !ok || got != p("10.0.0.1/8") {
		t.Errorf("got %v", got)
	}
}

func TestCurrentReadsRealNetlink(t *testing.T) {
	got, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, p("127.0.0.1/32")) {
		t.Errorf("loopback missing from %v", got)
	}
}
