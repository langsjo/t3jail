package hostnets

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"
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

func TestWatchRetriesFailedUpdatesAndSkipsUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := []netip.Prefix{p("192.168.1.0/24")}
	var mu sync.Mutex
	read := func() ([]netip.Prefix, error) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(state), nil
	}
	events := make(chan struct{}, 1)
	applied := make(chan []netip.Prefix, 10)
	failures := 2
	onChange := func(ps []netip.Prefix) error {
		if failures > 0 {
			failures--
			return errors.New("helper not answering")
		}
		applied <- ps
		return nil
	}
	go watch(ctx, read, events, time.Hour, onChange, func(error) {})

	select {
	case got := <-applied:
		if !slices.Equal(got, state) {
			t.Fatalf("applied %v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("failed update was never retried")
	}

	// An event without a change applies nothing; a real change is applied.
	events <- struct{}{}
	mu.Lock()
	state = []netip.Prefix{p("10.8.0.0/16")}
	mu.Unlock()
	events <- struct{}{}
	select {
	case got := <-applied:
		if !slices.Equal(got, []netip.Prefix{p("10.8.0.0/16")}) {
			t.Fatalf("applied %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("change not applied")
	}
	select {
	case got := <-applied:
		t.Fatalf("unexpected extra update %v", got)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestToPrefixRejectsNonContiguousMask(t *testing.T) {
	if got, ok := toPrefix([]byte{10, 0, 0, 1}, []byte{255, 0, 255, 0}); ok {
		t.Errorf("accepted as %v", got)
	}
}
