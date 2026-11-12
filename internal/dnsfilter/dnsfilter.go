// Package dnsfilter is the guest's only resolver. It answers for names on the
// allowlist and lets the firewall through to exactly the addresses it handed
// out, so the allowlist holds even for clients that ignore DNS.
package dnsfilter

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"github.com/langsjo/t3jail/internal/netrules"
)

// Grants receives the destinations the filter allows or no longer allows.
type Grants interface {
	Grant([]netrules.Destination) error
	Revoke([]netrules.Destination) error
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
	// Addresses handed out per queried name, kept so that list changes can
	// recompute exactly which destinations are still justified.
	resolved map[string]map[netip.Addr]struct{}
	granted  map[netrules.Destination]struct{}
}

func New(list *netrules.List, upstream string, grants Grants, haveIPv6 func() bool, log io.Writer) *Filter {
	return &Filter{
		Upstream: upstream,
		Grants:   grants,
		HaveIPv6: haveIPv6,
		log:      log,
		list:     list,
		resolved: map[string]map[netip.Addr]struct{}{},
		granted:  map[netrules.Destination]struct{}{},
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
	_, allowed := f.list.PortsForName(name)
	f.mu.Unlock()
	if !allowed {
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

	addrs := chainAddrs(resp, q.Name)
	// The firewall must know the addresses before the guest does, or its
	// first connection races the set update.
	if err := f.grant(name, addrs); err != nil {
		f.logf("error\t%s\t%s\t%v", qtype, name, err)
		reply(w, new(dns.Msg).SetRcode(req, dns.RcodeServerFailure))
		return
	}
	f.logf("allowed\t%s\t%s\t%s", qtype, name, joinAddrs(addrs))

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
	ports, ok := f.list.PortsForName(name)
	if !ok || len(addrs) == 0 {
		// The list changed while the query was in flight.
		return nil
	}
	known := f.resolved[name]
	if known == nil {
		known = map[netip.Addr]struct{}{}
		f.resolved[name] = known
	}
	var add []netrules.Destination
	for _, addr := range addrs {
		known[addr] = struct{}{}
		for _, d := range destinations(addr, ports) {
			if _, ok := f.granted[d]; !ok {
				add = append(add, d)
			}
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := f.Grants.Grant(add); err != nil {
		return err
	}
	for _, d := range add {
		f.granted[d] = struct{}{}
	}
	return nil
}

// SetList switches to a new allowlist. Destinations that no entry justifies
// any more are revoked; names whose ports changed get the new ports.
func (f *Filter) SetList(list *netrules.List) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = list

	want := map[netrules.Destination]struct{}{}
	for name, addrs := range f.resolved {
		ports, ok := list.PortsForName(name)
		if !ok {
			delete(f.resolved, name)
			continue
		}
		for addr := range addrs {
			for _, d := range destinations(addr, ports) {
				want[d] = struct{}{}
			}
		}
	}

	var revoke, grant []netrules.Destination
	for d := range f.granted {
		if _, ok := want[d]; !ok {
			revoke = append(revoke, d)
		}
	}
	for d := range want {
		if _, ok := f.granted[d]; !ok {
			grant = append(grant, d)
		}
	}
	if len(revoke) > 0 {
		if err := f.Grants.Revoke(revoke); err != nil {
			return err
		}
		for _, d := range revoke {
			delete(f.granted, d)
		}
	}
	if len(grant) > 0 {
		if err := f.Grants.Grant(grant); err != nil {
			return err
		}
		for _, d := range grant {
			f.granted[d] = struct{}{}
		}
	}
	return nil
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

// chainAddrs returns the addresses the answer gives for qname: A and AAAA
// records and SVCB/HTTPS address hints owned by qname or by a name reached
// from it through CNAMEs. Records for unrelated names are ignored, so an
// upstream cannot smuggle extra addresses into the allow sets.
func chainAddrs(resp *dns.Msg, qname string) []netip.Addr {
	chain := map[string]bool{canonicalName(qname): true}
	for changed := true; changed; {
		changed = false
		for _, rr := range resp.Answer {
			if c, ok := rr.(*dns.CNAME); ok && chain[canonicalName(c.Hdr.Name)] && !chain[canonicalName(c.Target)] {
				chain[canonicalName(c.Target)] = true
				changed = true
			}
		}
	}

	var addrs []netip.Addr
	add := func(ip net.IP) {
		if a, ok := netip.AddrFromSlice(ip); ok {
			addrs = append(addrs, a.Unmap())
		}
	}
	for _, rr := range resp.Answer {
		if !chain[canonicalName(rr.Header().Name)] {
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
