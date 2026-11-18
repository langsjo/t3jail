package netrules

import (
	"net/netip"
	"slices"
)

// Normalize turns destinations into an equivalent set without overlaps, which
// nft interval sets require: port ranges of the same prefix are merged, and a
// prefix inside another loses the ports the outer one already allows.
func Normalize(dests []Destination) []Destination {
	byPrefix := map[netip.Prefix][]PortRange{}
	for _, d := range dests {
		p := d.Prefix.Masked()
		byPrefix[p] = append(byPrefix[p], d.Ports)
	}
	for p, ports := range byPrefix {
		byPrefix[p] = mergeRanges(slices.Clone(ports))
	}

	var out []Destination
	for p, ports := range byPrefix {
		for outer, outerPorts := range byPrefix {
			if outer.Bits() < p.Bits() && outer.Contains(p.Addr()) {
				ports = subtractRanges(ports, outerPorts)
			}
		}
		for _, r := range ports {
			out = append(out, Destination{Prefix: p, Ports: r})
		}
	}
	slices.SortFunc(out, compareDestinations)
	return out
}

func compareDestinations(a, b Destination) int {
	if c := a.Prefix.Addr().Compare(b.Prefix.Addr()); c != 0 {
		return c
	}
	if c := a.Prefix.Bits() - b.Prefix.Bits(); c != 0 {
		return c
	}
	return comparePorts(a.Ports, b.Ports)
}

// subtractRanges returns the ports in a that are not in b. Both are sorted
// (see mergeRanges) and non-overlapping.
func subtractRanges(a, b []PortRange) []PortRange {
	var out []PortRange
	for _, r := range a {
		first, last := uint32(r.First), uint32(r.Last)
		for _, cut := range b {
			if cut.Proto != r.Proto || uint32(cut.Last) < first || uint32(cut.First) > last {
				continue
			}
			if uint32(cut.First) > first {
				out = append(out, PortRange{r.Proto, uint16(first), cut.First - 1})
			}
			first = uint32(cut.Last) + 1
		}
		if first <= last {
			out = append(out, PortRange{r.Proto, uint16(first), uint16(last)})
		}
	}
	return out
}

// Covers reports whether some destination in dests allows addr:port over
// proto.
func Covers(dests []Destination, addr netip.Addr, proto Proto, port uint16) bool {
	for _, d := range dests {
		if d.Prefix.Contains(addr) && d.Ports.Proto == proto && port >= d.Ports.First && port <= d.Ports.Last {
			return true
		}
	}
	return false
}
