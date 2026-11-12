// Package hostnets tracks the networks the host is directly attached to, so
// the guest can be kept away from the host and its LAN or VPN neighbours even
// when those use public addresses.
package hostnets

import (
	"context"
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
	ones := 0
	for _, b := range mask {
		for ; b != 0; b <<= 1 {
			ones++
		}
	}
	if len(mask) == 0 {
		ones = addr.BitLen()
	}
	return netip.PrefixFrom(addr, ones), true
}

// Watch calls onChange with the current prefixes now and whenever they change,
// until ctx is done.
func Watch(ctx context.Context, onChange func([]netip.Prefix), onError func(error)) {
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

	var last []netip.Prefix
	refresh := func() {
		current, err := Current()
		if err != nil {
			onError(err)
			return
		}
		if !slices.Equal(current, last) {
			last = current
			onChange(current)
		}
	}
	refresh()

	// Changes come in bursts (an interface coming up adds several addresses
	// and routes); settle briefly, and poll as a fallback for lost events.
	var settle <-chan time.Time
	poll := time.NewTicker(30 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-addrUpdates:
			settle = time.After(200 * time.Millisecond)
		case <-routeUpdates:
			settle = time.After(200 * time.Millisecond)
		case <-settle:
			settle = nil
			refresh()
		case <-poll.C:
			refresh()
		}
	}
}
