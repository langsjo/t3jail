// Package dnsfilter is the guest's only resolver. It answers for the names the
// mode allows and lets the firewall through to exactly the addresses it handed
// out, so the rules hold even for clients that ignore DNS.
package dnsfilter

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/langsjo/t3jail/internal/netrules"
)

// Grants receives changes to the destinations the filter allows. Together the
// elements always form a normalized set (see netrules.Normalize); remove and
// add are applied as one change.
type Grants interface {
	Update(remove, add []netrules.Destination) error
}

type Filter struct {
	// Upstream is the resolver allowed queries go to, as host:port.
	Upstream string
	Grants   Grants
	// HaveIPv6 reports whether the guest can reach anything over IPv6.
	// Without it, AAAA answers would make clients try IPv6 first and stall.
	HaveIPv6 func() bool

	logMu sync.Mutex
	log   io.Writer

	mu   sync.Mutex
	list *netrules.List
	// In open mode the filter answers every name, granting only listed ones:
	// the firewall lets everything through meanwhile.
	mode netrules.Mode
	// Addresses handed out per queried name, with when they last were, kept
	// so that list and mode changes can recompute exactly which destinations
	// are still justified.
	resolved map[string]map[netip.Addr]time.Time
	// granted holds each address's merged ports, as the firewall has them.
	granted map[netip.Addr][]netrules.PortRange
	now     func() time.Time
}

// maxResolvedNames bounds the grants a guest can pile up by looking up many
// names; the least recently looked up are forgotten first. A variable for
// tests.
var maxResolvedNames = 10000

const (
	// blocklistTTL is how long an address stays open in blocklist mode after
	// the last lookup that returned it.
	blocklistTTL = 24 * time.Hour
)

// allPorts is what a name gets in blocklist mode.
var allPorts = []netrules.PortRange{{Proto: netrules.TCP, First: 1, Last: 65535}, {Proto: netrules.UDP, First: 1, Last: 65535}}

func New(list *netrules.List, upstream string, grants Grants, haveIPv6 func() bool, log io.Writer) *Filter {
	return &Filter{
		Upstream: upstream,
		Grants:   grants,
		HaveIPv6: haveIPv6,
		log:      log,
		list:     list,
		mode:     netrules.Allowlist,
		resolved: map[string]map[netip.Addr]time.Time{},
		granted:  map[netip.Addr][]netrules.PortRange{},
		now:      time.Now,
	}
}

const upstreamTimeout = 5 * time.Second

func (f *Filter) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) != 1 {
		reply(w, new(dns.Msg).SetRcode(req, dns.RcodeFormatError))
		return
	}
	q := req.Question[0]
	name := canonicalName(q.Name)
	qtype := dns.TypeToString[q.Qtype]

	f.mu.Lock()
	mode := f.mode
	blocked := mode != netrules.Open && f.list.Blocked(name)
	_, allowed := f.portsFor(name)
	f.mu.Unlock()
	if blocked {
		f.logf("blocked\t%s\t%s", qtype, name)
		reply(w, new(dns.Msg).SetRcode(req, dns.RcodeRefused))
		return
	}
	if !allowed && mode != netrules.Open {
		f.logf("refused\t%s\t%s", qtype, name)
		reply(w, new(dns.Msg).SetRcode(req, dns.RcodeRefused))
		return
	}

	ipv6 := f.HaveIPv6()
	if q.Qtype == dns.TypeAAAA && !ipv6 {
		reply(w, new(dns.Msg).SetReply(req))
		return
	}

	overTCP := w.LocalAddr().Network() == "tcp"
	resp, err := f.exchange(req, overTCP)
	if err != nil {
		f.logf("error\t%s\t%s\t%v", qtype, name, err)
		reply(w, new(dns.Msg).SetRcode(req, dns.RcodeServerFailure))
		return
	}
	if !ipv6 {
		stripIPv6(resp)
	}

	chain := chainNames(resp, q.Name)
	f.mu.Lock()
	blockedTarget := f.mode != netrules.Open && slices.ContainsFunc(chain, f.list.Blocked)
	f.mu.Unlock()
	// A CNAME into a blocked name, as trackers use to look first-party.
	if blockedTarget {
		f.logf("blocked\t%s\t%s\t%s", qtype, name, strings.Join(chain, " "))
		reply(w, new(dns.Msg).SetRcode(req, dns.RcodeRefused))
		return
	}

	addrs := chainAddrs(resp, chain)
	if allowed {
		// The firewall must know the addresses before the guest does, or
		// its first connection races the set update.
		if err := f.grant(name, addrs); err != nil {
			f.logf("error\t%s\t%s\t%v", qtype, name, err)
			reply(w, new(dns.Msg).SetRcode(req, dns.RcodeServerFailure))
			return
		}
		f.logf("allowed\t%s\t%s\t%s", qtype, name, joinAddrs(addrs))
	} else {
		f.logf("unfiltered\t%s\t%s\t%s", qtype, name, joinAddrs(addrs))
	}

	resp.Id = req.Id
	if !overTCP {
		size := dns.MinMsgSize
		if opt := req.IsEdns0(); opt != nil {
			size = int(opt.UDPSize())
		}
		resp.Truncate(size)
	}
	reply(w, resp)
}

func (f *Filter) exchange(req *dns.Msg, overTCP bool) (*dns.Msg, error) {
	ctx, cancel := context.WithTimeout(context.Background(), upstreamTimeout)
	defer cancel()
	query := req.Copy()
	query.Id = dns.Id()
	if !overTCP {
		resp, _, err := (&dns.Client{Net: "udp"}).ExchangeContext(ctx, query, f.Upstream)
		if err != nil || !resp.Truncated {
			return resp, err
		}
	}
	resp, _, err := (&dns.Client{Net: "tcp"}).ExchangeContext(ctx, query, f.Upstream)
	return resp, err
}

func (f *Filter) grant(name string, addrs []netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ports, ok := f.portsFor(name)
	if !ok || len(addrs) == 0 {
		// The list or mode changed while the query was in flight.
		return nil
	}
	known := f.resolved[name]
	if known == nil {
		known = map[netip.Addr]time.Time{}
		f.resolved[name] = known
	}
	now := f.now()
	for _, addr := range addrs {
		known[addr] = now
	}
	if len(f.resolved) > maxResolvedNames {
		f.forgetOldest()
		return f.recompute()
	}
	// Only these addresses change: each one's ports are the union over the
	// names that resolved to it, and those already include what it has.
	want := map[netip.Addr][]netrules.PortRange{}
	for _, addr := range addrs {
		want[addr] = netrules.MergePorts(append(slices.Clone(f.granted[addr]), ports...))
	}
	return f.apply(want)
}

// portsFor returns the ports a lookup of name opens, if any. Must hold f.mu.
func (f *Filter) portsFor(name string) ([]netrules.PortRange, bool) {
	if f.mode == netrules.Open {
		// Grants for listed names keep them working once the mode changes.
		return f.list.PortsForName(name)
	}
	if f.list.Blocked(name) {
		return nil, false
	}
	if f.mode == netrules.Blocklist {
		return allPorts, true
	}
	return f.list.PortsForName(name)
}

// forgetOldest forgets the least recently looked up tenth of the names, so
// that the next ones fit without recomputing every time. Must hold f.mu.
func (f *Filter) forgetOldest() {
	type named struct {
		name string
		last time.Time
	}
	byAge := make([]named, 0, len(f.resolved))
	for name, addrs := range f.resolved {
		var last time.Time
		for _, t := range addrs {
			if t.After(last) {
				last = t
			}
		}
		byAge = append(byAge, named{name, last})
	}
	slices.SortFunc(byAge, func(a, b named) int { return a.last.Compare(b.last) })
	for _, n := range byAge[:len(byAge)-maxResolvedNames*9/10] {
		delete(f.resolved, n.name)
	}
}

// SetMode switches the mode and changes the grants to match.
func (f *Filter) SetMode(mode netrules.Mode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
	return f.recompute()
}

// Expire closes, in blocklist mode, the addresses no lookup returned within
// blocklistTTL. In allowlist mode the list bounds what is open.
func (f *Filter) Expire() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mode != netrules.Blocklist {
		return nil
	}
	cutoff := f.now().Add(-blocklistTTL)
	for name, addrs := range f.resolved {
		for addr, at := range addrs {
			if at.Before(cutoff) {
				delete(addrs, addr)
			}
		}
		if len(addrs) == 0 {
			delete(f.resolved, name)
		}
	}
	return f.recompute()
}

// Granted returns the destinations currently allowed for resolved names.
func (f *Filter) Granted() []netrules.Destination {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []netrules.Destination
	for addr, ports := range f.granted {
		out = append(out, destinations(addr, ports)...)
	}
	return out
}

// SetList switches to a new list. Destinations that no entry justifies any
// more are revoked; names whose ports changed get the new ports.
func (f *Filter) SetList(list *netrules.List) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = list
	return f.recompute()
}

// recompute grants exactly what the resolved names justify under the list
// and mode, and forgets names that justify nothing. Must hold f.mu.
func (f *Filter) recompute() error {
	want := map[netip.Addr][]netrules.PortRange{}
	for name, addrs := range f.resolved {
		ports, ok := f.portsFor(name)
		if !ok {
			delete(f.resolved, name)
			continue
		}
		for addr := range addrs {
			want[addr] = append(want[addr], ports...)
		}
	}
	for addr, ports := range want {
		want[addr] = netrules.MergePorts(ports)
	}
	// Addresses no name justifies any more lose their ports.
	for addr := range f.granted {
		if _, ok := want[addr]; !ok {
			want[addr] = nil
		}
	}
	return f.apply(want)
}

// apply gives each address in want exactly its (merged) ports, in one update.
// Grants are single addresses, so merged ports never overlap in the set.
func (f *Filter) apply(want map[netip.Addr][]netrules.PortRange) error {
	var remove, add []netrules.Destination
	for addr, ports := range want {
		old := f.granted[addr]
		if slices.Equal(old, ports) {
			continue
		}
		remove = append(remove, destinations(addr, old)...)
		add = append(add, destinations(addr, ports)...)
	}
	if len(remove) == 0 && len(add) == 0 {
		return nil
	}
	// The same element may be removed and added again; leave it alone.
	remove, add = without(remove, add), without(add, remove)
	if err := f.Grants.Update(remove, add); err != nil {
		return err
	}
	for addr, ports := range want {
		if len(ports) == 0 {
			delete(f.granted, addr)
		} else {
			f.granted[addr] = ports
		}
	}
	return nil
}

func without(ds, drop []netrules.Destination) []netrules.Destination {
	dropped := make(map[netrules.Destination]bool, len(drop))
	for _, d := range drop {
		dropped[d] = true
	}
	var out []netrules.Destination
	for _, d := range ds {
		if !dropped[d] {
			out = append(out, d)
		}
	}
	return out
}

func (f *Filter) logf(format string, args ...any) {
	if f.log == nil {
		return
	}
	f.logMu.Lock()
	defer f.logMu.Unlock()
	fmt.Fprintf(f.log, time.Now().UTC().Format(time.RFC3339)+"\t"+format+"\n", args...)
}

func reply(w dns.ResponseWriter, m *dns.Msg) {
	_ = w.WriteMsg(m)
}

func canonicalName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func destinations(addr netip.Addr, ports []netrules.PortRange) []netrules.Destination {
	prefix := netip.PrefixFrom(addr, addr.BitLen())
	dests := make([]netrules.Destination, 0, len(ports))
	for _, p := range ports {
		dests = append(dests, netrules.Destination{Prefix: prefix, Ports: p})
	}
	return dests
}

// chainNames returns qname and the names reached from it through the answer's
// CNAMEs, canonical and in chain order.
func chainNames(resp *dns.Msg, qname string) []string {
	chain := []string{canonicalName(qname)}
	for changed := true; changed; {
		changed = false
		for _, rr := range resp.Answer {
			if c, ok := rr.(*dns.CNAME); ok && slices.Contains(chain, canonicalName(c.Hdr.Name)) && !slices.Contains(chain, canonicalName(c.Target)) {
				chain = append(chain, canonicalName(c.Target))
				changed = true
			}
		}
	}
	return chain
}

// chainAddrs returns the addresses the answer gives for the chain's names: A
// and AAAA records and SVCB/HTTPS address hints. Records for unrelated names
// are ignored, so an upstream cannot smuggle extra addresses into the allow
// sets.
func chainAddrs(resp *dns.Msg, chain []string) []netip.Addr {
	var addrs []netip.Addr
	add := func(ip net.IP) {
		if a, ok := netip.AddrFromSlice(ip); ok {
			addrs = append(addrs, a.Unmap())
		}
	}
	for _, rr := range resp.Answer {
		if !slices.Contains(chain, canonicalName(rr.Header().Name)) {
			continue
		}
		switch rr := rr.(type) {
		case *dns.A:
			add(rr.A)
		case *dns.AAAA:
			add(rr.AAAA)
		case *dns.HTTPS:
			for _, ip := range svcbHints(&rr.SVCB) {
				add(ip)
			}
		case *dns.SVCB:
			for _, ip := range svcbHints(rr) {
				add(ip)
			}
		}
	}
	return addrs
}

func svcbHints(rr *dns.SVCB) []net.IP {
	var ips []net.IP
	for _, kv := range rr.Value {
		switch kv := kv.(type) {
		case *dns.SVCBIPv4Hint:
			ips = append(ips, kv.Hint...)
		case *dns.SVCBIPv6Hint:
			ips = append(ips, kv.Hint...)
		}
	}
	return ips
}

func stripIPv6(resp *dns.Msg) {
	resp.Answer = withoutIPv6(resp.Answer)
	resp.Extra = withoutIPv6(resp.Extra)
}

func withoutIPv6(rrs []dns.RR) []dns.RR {
	kept := rrs[:0]
	for _, rr := range rrs {
		switch rr := rr.(type) {
		case *dns.AAAA:
			continue
		case *dns.HTTPS:
			rr.Value = withoutIPv6Hints(rr.Value)
		case *dns.SVCB:
			rr.Value = withoutIPv6Hints(rr.Value)
		}
		kept = append(kept, rr)
	}
	return kept
}

func withoutIPv6Hints(kvs []dns.SVCBKeyValue) []dns.SVCBKeyValue {
	kept := kvs[:0]
	for _, kv := range kvs {
		if _, ok := kv.(*dns.SVCBIPv6Hint); !ok {
			kept = append(kept, kv)
		}
	}
	return kept
}

func joinAddrs(addrs []netip.Addr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return strings.Join(s, " ")
}
