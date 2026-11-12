// Package hostnets tracks the networks the host is directly attached to, so
// the guest can be kept away from the host and its LAN or VPN neighbours even
// when those use public addresses.
package hostnets

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Compute returns the blocked prefixes for the given interface addresses (with
// their prefix length) and the destinations of link-scope routes.
func Compute(addrs, linkRoutes []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, a := range addrs {
		out = append(out, netip.PrefixFrom(a.Addr(), a.Addr().BitLen()), a.Masked())
	}
	for _, r := range linkRoutes {
		if r.Bits() > 0 {
			out = append(out, r.Masked())
		}
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return slices.Compact(out)
}

// Current reads the host's addresses and link-scope routes.
func Current() ([]netip.Prefix, error) {
	addrList, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Prefix
	for _, a := range addrList {
		if p, ok := toPrefix(a.IP, a.Mask); ok {
			addrs = append(addrs, p)
		}
	}
	routeList, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Scope: netlink.SCOPE_LINK}, netlink.RT_FILTER_SCOPE)
	if err != nil {
		return nil, err
	}
	var routes []netip.Prefix
	for _, r := range routeList {
		if r.Dst == nil || r.Type != unix.RTN_UNICAST {
			continue
		}
		if p, ok := toPrefix(r.Dst.IP, r.Dst.Mask); ok {
			routes = append(routes, p)
		}
	}
	return Compute(addrs, routes), nil
}

func toPrefix(ip []byte, mask []byte) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	if len(mask) == 0 {
		return netip.PrefixFrom(addr, addr.BitLen()), true
	}
	ones, bits := net.IPMask(mask).Size()
	if bits == 0 {
		// Not a contiguous mask; the kernel never reports those.
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, ones), true
}

// Watch calls onChange with the current prefixes now and whenever they change,
// until ctx is done. A failed onChange is retried until it succeeds: a missed
// update would leave a new network reachable.
func Watch(ctx context.Context, onChange func([]netip.Prefix) error, onError func(error)) {
	addrUpdates := make(chan netlink.AddrUpdate, 64)
	routeUpdates := make(chan netlink.RouteUpdate, 64)
	done := make(chan struct{})
	defer close(done)
	if err := netlink.AddrSubscribe(addrUpdates, done); err != nil {
		onError(err)
	}
	if err := netlink.RouteSubscribe(routeUpdates, done); err != nil {
		onError(err)
	}
	events := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-addrUpdates:
			case <-routeUpdates:
			case <-done:
				return
			}
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}()
	watch(ctx, Current, events, 30*time.Second, onChange, onError)
}

func watch(ctx context.Context, read func() ([]netip.Prefix, error), events <-chan struct{}, pollEvery time.Duration, onChange func([]netip.Prefix) error, onError func(error)) {

	var last []netip.Prefix
	applied := false
	// refresh reports whether it needs to be retried.
	refresh := func() bool {
		current, err := read()
		if err == nil && (!applied || !slices.Equal(current, last)) {
			err = onChange(current)
			if err == nil {
				last, applied = current, true
			}
		}
		if err != nil {
			onError(err)
			return true
		}
		return false
	}

	var settle <-chan time.Time
	if refresh() {
		settle = time.After(time.Second)
	}

	// Changes come in bursts (an interface coming up adds several addresses
	// and routes); settle briefly, and poll as a fallback for lost events.
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			settle = time.After(200 * time.Millisecond)
		case <-settle:
			settle = nil
			if refresh() {
				settle = time.After(time.Second)
			}
		case <-poll.C:
			if refresh() {
				settle = time.After(time.Second)
			}
		}
	}
}
