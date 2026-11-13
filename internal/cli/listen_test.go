package cli

import (
	"net/netip"
	"slices"
	"testing"
)

func TestAbsentFrom(t *testing.T) {
	have := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1"), netip.MustParseAddr("fe80::1")}
	listen := []string{"127.0.0.1:37730", "100.64.1.2:37730", "[::1]:37730", "[fe80::1%eth0]:37730", "garbage"}
	got := absentFrom(listen, have)
	want := []string{"100.64.1.2:37730", "garbage"}
	if !slices.Equal(got, want) {
		t.Errorf("absentFrom = %q, want %q", got, want)
	}
}

func TestAbsentListenAddrsSeesLoopback(t *testing.T) {
	if got := absentListenAddrs([]string{"127.0.0.1:37730"}); len(got) != 0 {
		t.Errorf("loopback reported absent: %q", got)
	}
}
