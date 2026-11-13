package cli

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// absentListenAddrs returns the T3 listen addresses (ip:port) whose IP this
// host does not have right now. The daemon listens on them anyway
// (IP_FREEBIND), but a typo would never work, so say so.
func absentListenAddrs(listen []string) []string {
	ifAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var have []netip.Addr
	for _, a := range ifAddrs {
		if ipNet, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipNet.IP); ok {
				have = append(have, ip.Unmap())
			}
		}
	}
	return absentFrom(listen, have)
}

func absentFrom(listen []string, have []netip.Addr) []string {
	var absent []string
	for _, s := range listen {
		ap, err := netip.ParseAddrPort(s)
		if err != nil || !slices.Contains(have, ap.Addr().WithZone("").Unmap()) {
			absent = append(absent, s)
		}
	}
	return absent
}

// describeListen lists the addresses for `status`, marking absent ones.
func describeListen(listen []string) string {
	absent := absentListenAddrs(listen)
	parts := make([]string, len(listen))
	for i, s := range listen {
		parts[i] = s
		if slices.Contains(absent, s) {
			parts[i] += " (not on this host yet)"
		}
	}
	return strings.Join(parts, ", ")
}
