// Package netrules parses and matches the network allowlist.
//
// Entry syntax, one per line in network.list:
//
//	github.com          exact name, TCP 80 and 443
//	*.github.com        github.com and all its subdomains
//	github.com:22       exact name, only TCP 22
//	host.example:80,8000-8100
//	dns.example:53/udp  UDP ports end in /udp (/tcp is the default)
//	203.0.113.0/24      addresses (also overrides the default private/host blocks)
//	[2001:db8::/32]:443 IPv6 must be bracketed when ports follow
//	!ads.example        a block: wins over any entry that allows the name,
//	!*.ads.example      or for addresses, the address; no ports
//	!198.51.100.0/24
package netrules

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

type Kind int

const (
	KindName     Kind = iota // exact DNS name
	KindWildcard             // name and all subdomains
	KindPrefix               // IP address or CIDR
)

// Proto is a transport protocol, by its IP protocol number.
type Proto uint8

const (
	TCP Proto = 6
	UDP Proto = 17
)

func (p Proto) String() string {
	switch p {
	case TCP:
		return "tcp"
	case UDP:
		return "udp"
	}
	return strconv.Itoa(int(p))
}

// PortRange is an inclusive range of ports of one protocol.
type PortRange struct {
	Proto       Proto
	First, Last uint16
}

// Contains reports whether r includes all of o.
func (r PortRange) Contains(o PortRange) bool {
	return r.Proto == o.Proto && r.First <= o.First && o.Last <= r.Last
}

// DefaultPorts apply to entries without an explicit port list.
var DefaultPorts = []PortRange{{TCP, 80, 80}, {TCP, 443, 443}}

type Entry struct {
	// Block makes the entry refuse what it matches, whatever else allows it.
	Block bool
	Kind  Kind
	// Name is lower-case without a trailing dot; for wildcards the "*." is
	// stripped.
	Name   string
	Prefix netip.Prefix
	// Ports is nil for the defaults.
	Ports []PortRange
}

// EffectivePorts returns the entry's ports or the defaults.
func (e Entry) EffectivePorts() []PortRange {
	if e.Ports == nil {
		return DefaultPorts
	}
	return e.Ports
}

// MatchesName reports whether a DNS name (any case, trailing dot allowed) is
// covered by a name or wildcard entry.
func (e Entry) MatchesName(name string) bool {
	name = normalizeName(name)
	switch e.Kind {
	case KindName:
		return name == e.Name
	case KindWildcard:
		return name == e.Name || strings.HasSuffix(name, "."+e.Name)
	}
	return false
}

// String is the canonical form, used to compare and store entries.
// Covers reports whether e allows everything the name entry o allows.
// Address entries are never covered: they are not matched by name. Blocks
// allow nothing and are never covered.
func (e Entry) Covers(o Entry) bool {
	if e.Block || o.Block {
		return false
	}
	switch o.Kind {
	case KindName:
		if !e.MatchesName(o.Name) {
			return false
		}
	case KindWildcard:
		if e.Kind != KindWildcard || !e.MatchesName(o.Name) {
			return false
		}
	default:
		return false
	}
	// Port ranges are merged, so each of o's must lie within one of e's.
	for _, r := range o.EffectivePorts() {
		if !slices.ContainsFunc(e.EffectivePorts(), func(a PortRange) bool { return a.Contains(r) }) {
			return false
		}
	}
	return true
}

func (e Entry) String() string {
	var host string
	switch e.Kind {
	case KindName:
		host = e.Name
	case KindWildcard:
		host = "*." + e.Name
	case KindPrefix:
		host = formatPrefix(e.Prefix)
		if e.Prefix.Addr().Is6() && e.Ports != nil {
			host = "[" + host + "]"
		}
	}
	if e.Block {
		host = "!" + host
	}
	if e.Ports == nil {
		return host
	}
	parts := make([]string, len(e.Ports))
	for i, r := range e.Ports {
		parts[i] = strconv.Itoa(int(r.First))
		if r.Last != r.First {
			parts[i] += "-" + strconv.Itoa(int(r.Last))
		}
		if r.Proto != TCP {
			parts[i] += "/" + r.Proto.String()
		}
	}
	return host + ":" + strings.Join(parts, ",")
}

func formatPrefix(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// Parse parses one entry.
func Parse(s string) (Entry, error) {
	s = strings.TrimSpace(s)
	rest, block := strings.CutPrefix(s, "!")
	if rest == "" {
		return Entry{}, errors.New("empty entry")
	}
	host, portSpec, err := splitHostPorts(rest)
	if err != nil {
		return Entry{}, err
	}
	e, err := parseHost(host)
	if err != nil {
		return Entry{}, err
	}
	e.Block = block
	if block && portSpec != nil {
		return Entry{}, fmt.Errorf("%q: blocks cover all ports; leave out the ports", s)
	}
	if portSpec != nil {
		if e.Ports, err = parsePorts(*portSpec); err != nil {
			return Entry{}, fmt.Errorf("%q: %w", s, err)
		}
	}
	return e, nil
}

// splitHostPorts separates "host:ports"; IPv6 must use "[addr]:ports".
func splitHostPorts(s string) (string, *string, error) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", nil, fmt.Errorf("%q: missing ]", s)
		}
		host, rest := s[1:end], s[end+1:]
		if rest == "" {
			return host, nil, nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", nil, fmt.Errorf("%q: expected :ports after ]", s)
		}
		ports := rest[1:]
		return host, &ports, nil
	}
	switch strings.Count(s, ":") {
	case 0:
		return s, nil, nil
	case 1:
		host, ports, _ := strings.Cut(s, ":")
		return host, &ports, nil
	default:
		if _, err := netip.ParsePrefix(s); err == nil {
			return s, nil, nil
		}
		if _, err := netip.ParseAddr(s); err == nil {
			return s, nil, nil
		}
		return "", nil, fmt.Errorf("%q: put IPv6 addresses in brackets when giving ports, e.g. [2001:db8::1]:443", s)
	}
}

func parseHost(host string) (Entry, error) {
	if p, err := netip.ParsePrefix(host); err == nil {
		if p.Masked() != p {
			return Entry{}, fmt.Errorf("%q has host bits set; did you mean %s?", host, p.Masked())
		}
		return Entry{Kind: KindPrefix, Prefix: p}, nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" {
			return Entry{}, fmt.Errorf("%q: zoned addresses are not supported", host)
		}
		return Entry{Kind: KindPrefix, Prefix: netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())}, nil
	}
	kind := KindName
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		kind, host = KindWildcard, rest
	}
	name := normalizeName(host)
	if err := validateName(name); err != nil {
		return Entry{}, err
	}
	return Entry{Kind: kind, Name: name}, nil
}

func normalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

func validateName(name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("invalid domain name %q", name)
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("invalid domain name %q", name)
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return fmt.Errorf("invalid character %q in domain name %q (wildcards are only allowed as a leading \"*.\")", c, name)
			}
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid domain name %q", name)
		}
	}
	return nil
}

// parsePorts parses "22", "80,443", "8000-8100" or "443,443/udp", sorted
// and merged.
func parsePorts(spec string) ([]PortRange, error) {
	if spec == "" {
		return nil, errors.New("empty port list")
	}
	var ranges []PortRange
	for _, part := range strings.Split(spec, ",") {
		proto := TCP
		if nums, name, ok := strings.Cut(part, "/"); ok {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "tcp":
			case "udp":
				proto = UDP
			default:
				return nil, fmt.Errorf("unknown protocol %q (want tcp or udp)", name)
			}
			part = nums
		}
		first, last, isRange := strings.Cut(part, "-")
		a, err := parsePort(first)
		if err != nil {
			return nil, err
		}
		b := a
		if isRange {
			if b, err = parsePort(last); err != nil {
				return nil, err
			}
			if b < a {
				return nil, fmt.Errorf("port range %q is reversed", part)
			}
		}
		ranges = append(ranges, PortRange{proto, a, b})
	}
	return mergeRanges(ranges), nil
}

func parsePort(s string) (uint16, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return uint16(n), nil
}

// MergePorts returns ranges sorted and merged, leaving ranges unchanged.
func MergePorts(ranges []PortRange) []PortRange {
	if len(ranges) == 0 {
		return nil
	}
	return mergeRanges(slices.Clone(ranges))
}

// mergeRanges sorts ranges by protocol, then port, and merges overlapping
// and adjacent ones of the same protocol.
func mergeRanges(ranges []PortRange) []PortRange {
	slices.SortFunc(ranges, comparePorts)
	merged := ranges[:1]
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Proto == last.Proto && uint32(r.First) <= uint32(last.Last)+1 {
			last.Last = max(last.Last, r.Last)
		} else {
			merged = append(merged, r)
		}
	}
	return merged
}

func comparePorts(a, b PortRange) int {
	if a.Proto != b.Proto {
		return int(a.Proto) - int(b.Proto)
	}
	return int(a.First) - int(b.First)
}
