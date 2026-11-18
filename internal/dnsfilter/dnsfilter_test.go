package dnsfilter

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/langsjo/t3jail/internal/netrules"
)

type fakeGrants struct {
	mu     sync.Mutex
	active map[string]bool
	elems  []netrules.Destination
	// failNext makes the next Update fail without changing anything, as an
	// nft transaction does.
	failNext bool
}

// Update applies the change like nft would: removing an absent element or
// adding one that overlaps the set is an error.
func (g *fakeGrants) Update(remove, add []netrules.Destination) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failNext {
		g.failNext = false
		return errors.New("nft: transaction failed")
	}
	for _, d := range remove {
		if !g.active[key(d)] {
			panic("revoked unknown: " + key(d))
		}
		delete(g.active, key(d))
		g.elems = slices.DeleteFunc(g.elems, func(e netrules.Destination) bool { return e == d })
	}
	for _, d := range add {
		for _, e := range g.elems {
			if e.Prefix.Overlaps(d.Prefix) && e.Ports.Proto == d.Ports.Proto && e.Ports.First <= d.Ports.Last && d.Ports.First <= e.Ports.Last {
				panic(fmt.Sprintf("%s overlaps %s", key(d), key(e)))
			}
		}
		g.active[key(d)] = true
		g.elems = append(g.elems, d)
	}
	return nil
}

func (g *fakeGrants) list() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for k := range g.active {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func key(d netrules.Destination) string {
	s := d.Prefix.Addr().String() + ":" + itoa(d.Ports.First)
	if d.Ports.Last != d.Ports.First {
		s += "-" + itoa(d.Ports.Last)
	}
	if d.Ports.Proto != netrules.TCP {
		s += "/" + d.Ports.Proto.String()
	}
	return s
}

func itoa(n uint16) string { return strconv.Itoa(int(n)) }

// zone is the fake upstream: owner name and type to answer records.
type zone map[string][]string

type harness struct {
	t        *testing.T
	filter   *Filter
	grants   *fakeGrants
	log      *syncBuffer
	addr     string
	queries  []string // what reached upstream
	queryMu  sync.Mutex
	haveIPv6 atomic.Bool
}

func newHarness(t *testing.T, list string, z zone) *harness {
	t.Helper()
	l, err := netrules.ParseList([]byte(list))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, grants: &fakeGrants{active: map[string]bool{}}, log: &syncBuffer{}}
	h.haveIPv6.Store(true)

	upstream := serve(t, "udp", dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		q := req.Question[0]
		h.queryMu.Lock()
		h.queries = append(h.queries, dns.TypeToString[q.Qtype]+" "+q.Name)
		h.queryMu.Unlock()
		m := new(dns.Msg).SetReply(req)
		for _, s := range z[dns.TypeToString[q.Qtype]+" "+strings.ToLower(q.Name)] {
			rr, err := dns.NewRR(s)
			if err != nil {
				t.Errorf("bad fixture %q: %v", s, err)
			}
			m.Answer = append(m.Answer, rr)
		}
		w.WriteMsg(m)
	}))

	h.filter = New(l, upstream, h.grants, func() bool { return h.haveIPv6.Load() }, h.log)
	h.addr = serve(t, "udp", h.filter)
	return h
}

// The filter writes from server goroutines; tests read concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (h *harness) upstreamQueries() []string {
	h.queryMu.Lock()
	defer h.queryMu.Unlock()
	return slices.Clone(h.queries)
}

func serve(t *testing.T, network string, handler dns.Handler) string {
	t.Helper()
	pc, err := net.ListenPacket(network, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: handler}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	return pc.LocalAddr().String()
}

func (h *harness) query(name string, qtype uint16) *dns.Msg {
	h.t.Helper()
	resp, _, err := new(dns.Client).Exchange(new(dns.Msg).SetQuestion(dns.Fqdn(name), qtype), h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) wantGrants(want ...string) {
	h.t.Helper()
	slices.Sort(want)
	if got := h.grants.list(); !slices.Equal(got, want) {
		h.t.Errorf("grants = %v, want %v", got, want)
	}
}

func (h *harness) setList(list string) {
	h.t.Helper()
	l, err := netrules.ParseList([]byte(list))
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.filter.SetList(l); err != nil {
		h.t.Fatal(err)
	}
}

func TestUnlistedNameIsRefusedWithoutAskingUpstream(t *testing.T) {
	h := newHarness(t, "github.com\n", zone{"A example.com.": {"example.com. 60 IN A 93.184.215.14"}})
	for _, name := range []string{"example.com", "api.github.com", "github.com.evil.example"} {
		if resp := h.query(name, dns.TypeA); resp.Rcode != dns.RcodeRefused {
			t.Errorf("%s: rcode %s", name, dns.RcodeToString[resp.Rcode])
		}
	}
	if q := h.upstreamQueries(); len(q) != 0 {
		t.Errorf("upstream saw %v", q)
	}
	if !strings.Contains(h.log.String(), "refused\tA\texample.com") {
		t.Errorf("log: %q", h.log.String())
	}
	h.wantGrants()
}

func TestBlockedNameIsRefusedEvenIfAllowed(t *testing.T) {
	h := newHarness(t, "*.example.com\n!ads.example.com\n", zone{
		"A ads.example.com.": {"ads.example.com. 60 IN A 192.0.2.66"},
		"A www.example.com.": {"www.example.com. 60 IN A 192.0.2.80"},
	})
	if resp := h.query("ADS.example.com", dns.TypeA); resp.Rcode != dns.RcodeRefused {
		t.Errorf("rcode %s", dns.RcodeToString[resp.Rcode])
	}
	if q := h.upstreamQueries(); len(q) != 0 {
		t.Errorf("upstream saw %v", q)
	}
	if !strings.Contains(h.log.String(), "blocked\tA\tads.example.com") {
		t.Errorf("log: %q", h.log.String())
	}
	h.query("www.example.com", dns.TypeA)
	h.wantGrants("192.0.2.80:80", "192.0.2.80:443")

	// Blocking a resolved name revokes its addresses.
	h.setList("*.example.com\n!ads.example.com\n!www.example.com\n")
	h.wantGrants()
}

func TestCNAMEIntoABlockedNameIsRefused(t *testing.T) {
	h := newHarness(t, "metrics.example.com\n!*.tracker.example\n", zone{"A metrics.example.com.": {
		"metrics.example.com. 60 IN CNAME eu.tracker.example.",
		"eu.tracker.example. 60 IN A 192.0.2.66",
	}})
	if resp := h.query("metrics.example.com", dns.TypeA); resp.Rcode != dns.RcodeRefused {
		t.Errorf("rcode %s", dns.RcodeToString[resp.Rcode])
	}
	h.wantGrants()
	if !strings.Contains(h.log.String(), "blocked\tA\tmetrics.example.com\tmetrics.example.com eu.tracker.example") {
		t.Errorf("log: %q", h.log.String())
	}
}

func TestCNAMEChainIsFollowedAndUnrelatedRecordsIgnored(t *testing.T) {
	h := newHarness(t, "github.com\n", zone{"A github.com.": {
		"github.com. 60 IN CNAME edge.cdn.example.",
		"edge.cdn.example. 60 IN CNAME deeper.cdn.example.",
		"deeper.cdn.example. 60 IN A 140.82.121.4",
		"unrelated.example. 60 IN A 6.6.6.6",
	}})
	resp := h.query("GitHub.com", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 4 {
		t.Fatalf("answer not passed through: %v", resp)
	}
	h.wantGrants("140.82.121.4:80", "140.82.121.4:443")
}

func TestOnlyTheMatchingEntrysPortsAreGranted(t *testing.T) {
	h := newHarness(t, "github.com\n*.github.com:22\n", zone{
		"A api.github.com.": {"api.github.com. 60 IN A 140.82.121.6"},
		"A github.com.":     {"github.com. 60 IN A 140.82.121.4"},
	})
	h.query("api.github.com", dns.TypeA)
	h.query("github.com", dns.TypeA)
	h.wantGrants("140.82.121.6:22", "140.82.121.4:22", "140.82.121.4:80", "140.82.121.4:443")
}

func TestUDPPortsAreGrantedSeparately(t *testing.T) {
	h := newHarness(t, "svc.example:443,443/udp,3478-3480/udp\n", zone{
		"A svc.example.": {"svc.example. 60 IN A 192.0.2.80"},
	})
	h.query("svc.example", dns.TypeA)
	h.wantGrants("192.0.2.80:443", "192.0.2.80:443/udp", "192.0.2.80:3478-3480/udp")
}

func TestHTTPSHintsAreGranted(t *testing.T) {
	h := newHarness(t, "svc.example\n", zone{"HTTPS svc.example.": {
		`svc.example. 60 IN HTTPS 1 . alpn="h2" ipv4hint="192.0.2.80" ipv6hint="2606:4700::1"`,
	}})
	h.query("svc.example", dns.TypeHTTPS)
	h.wantGrants("192.0.2.80:80", "192.0.2.80:443", "2606:4700::1:80", "2606:4700::1:443")
}

func TestWithoutIPv6AAAAIsEmptyAndHintsAreStripped(t *testing.T) {
	h := newHarness(t, "svc.example\n", zone{
		"AAAA svc.example.":  {"svc.example. 60 IN AAAA 2606:4700::1"},
		"HTTPS svc.example.": {`svc.example. 60 IN HTTPS 1 . ipv4hint="192.0.2.80" ipv6hint="2606:4700::1"`},
	})
	h.haveIPv6.Store(false)

	resp := h.query("svc.example", dns.TypeAAAA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Errorf("AAAA answer: %v", resp)
	}
	if q := h.upstreamQueries(); len(q) != 0 {
		t.Errorf("AAAA went upstream: %v", q)
	}

	resp = h.query("svc.example", dns.TypeHTTPS)
	if s := resp.String(); strings.Contains(s, "ipv6hint") || !strings.Contains(s, "ipv4hint") {
		t.Errorf("HTTPS answer: %s", s)
	}
	h.wantGrants("192.0.2.80:80", "192.0.2.80:443")
}

func TestSetListRevokesOnlyWhatNoEntryJustifies(t *testing.T) {
	h := newHarness(t, "a.example\nb.example:22\nc.example\n", zone{
		"A a.example.": {"a.example. 60 IN A 198.51.100.1"},
		// b shares a's address, as names behind one CDN do.
		"A b.example.": {"b.example. 60 IN A 198.51.100.1"},
		"A c.example.": {"c.example. 60 IN A 198.51.100.3"},
	})
	for _, n := range []string{"a.example", "b.example", "c.example"} {
		h.query(n, dns.TypeA)
	}
	h.wantGrants("198.51.100.1:22", "198.51.100.1:80", "198.51.100.1:443", "198.51.100.3:80", "198.51.100.3:443")

	// Removing a keeps the shared address for b's port only; c gets new ports.
	h.setList("b.example:22\nc.example:8080\n")
	h.wantGrants("198.51.100.1:22", "198.51.100.3:8080")

	// A removed name is forgotten: adding it back needs a fresh lookup.
	h.setList("a.example\nb.example:22\nc.example:8080\n")
	h.wantGrants("198.51.100.1:22", "198.51.100.3:8080")
	if resp := h.query("a.example", dns.TypeA); resp.Rcode != dns.RcodeSuccess {
		t.Fatal(resp)
	}
	h.wantGrants("198.51.100.1:22", "198.51.100.1:80", "198.51.100.1:443", "198.51.100.3:8080")

	h.setList("")
	h.wantGrants()
}

func TestTruncatedUpstreamAnswerIsRetriedOverTCP(t *testing.T) {
	l, _ := netrules.ParseList([]byte("big.example\n"))
	grants := &fakeGrants{active: map[string]bool{}}

	// One listener address for both transports: UDP always truncates.
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", tcpLn.Addr().String())
	if err != nil {
		t.Skip("cannot bind UDP on the TCP port:", err)
	}
	full := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg).SetReply(req)
		rr, _ := dns.NewRR("big.example. 60 IN A 198.51.100.9")
		m.Answer = []dns.RR{rr}
		w.WriteMsg(m)
	})
	truncated := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg).SetReply(req)
		m.Truncated = true
		w.WriteMsg(m)
	})
	for _, srv := range []*dns.Server{{Listener: tcpLn, Handler: full}, {PacketConn: pc, Handler: truncated}} {
		go srv.ActivateAndServe()
		t.Cleanup(func() { srv.Shutdown() })
	}

	f := New(l, tcpLn.Addr().String(), grants, func() bool { return true }, nil)
	resp, _, err := new(dns.Client).Exchange(new(dns.Msg).SetQuestion("big.example.", dns.TypeA), serve(t, "udp", f))
	if err != nil || len(resp.Answer) != 1 {
		t.Fatalf("resp %v, err %v", resp, err)
	}
	if got := grants.list(); !slices.Equal(got, []string{"198.51.100.9:443", "198.51.100.9:80"}) {
		t.Errorf("grants = %v", got)
	}
}

func TestOverlappingPortsOnASharedAddressAreMerged(t *testing.T) {
	h := newHarness(t, "a.example:80-90\nb.example:85-95\nc.example\n", zone{
		"A a.example.": {"a.example. 60 IN A 198.51.100.1"},
		"A b.example.": {"b.example. 60 IN A 198.51.100.1"},
		"A c.example.": {"c.example. 60 IN A 198.51.100.1"},
	})
	for _, n := range []string{"a.example", "b.example", "c.example"} {
		if resp := h.query(n, dns.TypeA); resp.Rcode != dns.RcodeSuccess {
			t.Fatalf("%s: %s", n, dns.RcodeToString[resp.Rcode])
		}
	}
	h.wantGrants("198.51.100.1:80-95", "198.51.100.1:443")

	h.setList("a.example:80-90\nc.example\n")
	h.wantGrants("198.51.100.1:80-90", "198.51.100.1:443")
}

func TestFailedUpdateKeepsBookkeepingInSyncWithTheFirewall(t *testing.T) {
	h := newHarness(t, "a.example\nb.example:22\n", zone{
		"A a.example.": {"a.example. 60 IN A 198.51.100.1"},
		"A b.example.": {"b.example. 60 IN A 198.51.100.2"},
	})
	h.query("a.example", dns.TypeA)
	h.wantGrants("198.51.100.1:80", "198.51.100.1:443")

	h.grants.mu.Lock()
	h.grants.failNext = true
	h.grants.mu.Unlock()
	if resp := h.query("b.example", dns.TypeA); resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode %s, want SERVFAIL", dns.RcodeToString[resp.Rcode])
	}
	h.wantGrants("198.51.100.1:80", "198.51.100.1:443")

	// Later changes must only remove what the firewall really has; the fake
	// panics like nft errors on deleting a missing element.
	h.query("b.example", dns.TypeA)
	h.setList("b.example:22\n")
	h.wantGrants("198.51.100.2:22")
}

func (h *harness) setMode(m netrules.Mode) {
	h.t.Helper()
	if err := h.filter.SetMode(m); err != nil {
		h.t.Fatal(err)
	}
}

func TestOpenModeAnswersEveryNameButGrantsOnlyListedOnes(t *testing.T) {
	h := newHarness(t, "github.com\n!ads.example\n", zone{
		"A example.com.": {"example.com. 60 IN A 93.184.215.14"},
		"A github.com.":  {"github.com. 60 IN A 140.82.121.4"},
		"A ads.example.": {"ads.example. 60 IN A 192.0.2.66"},
	})
	h.setMode(netrules.Open)
	for _, name := range []string{"example.com", "ads.example"} {
		if resp := h.query(name, dns.TypeA); resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
			t.Fatalf("open mode: %s answered %v", name, resp)
		}
	}
	// The firewall lets everything through while open; grants are kept for
	// listed names so they still work once the mode changes.
	h.query("github.com", dns.TypeA)
	h.wantGrants("140.82.121.4:80", "140.82.121.4:443")
	if !strings.Contains(h.log.String(), "unfiltered\tA\texample.com") {
		t.Errorf("log: %q", h.log.String())
	}

	h.setMode(netrules.Allowlist)
	if resp := h.query("example.com", dns.TypeA); resp.Rcode != dns.RcodeRefused {
		t.Errorf("allowlist again: rcode %s", dns.RcodeToString[resp.Rcode])
	}
	h.wantGrants("140.82.121.4:80", "140.82.121.4:443")
}

func TestBlocklistModeGrantsEveryPortOfUnblockedNames(t *testing.T) {
	h := newHarness(t, "github.com:22\n!ads.example\n", zone{
		"A example.com.": {"example.com. 60 IN A 93.184.215.14"},
		"A github.com.":  {"github.com. 60 IN A 140.82.121.4"},
		"A ads.example.": {"ads.example. 60 IN A 192.0.2.66"},
	})
	h.setMode(netrules.Blocklist)
	h.query("example.com", dns.TypeA)
	h.query("github.com", dns.TypeA)
	if resp := h.query("ads.example", dns.TypeA); resp.Rcode != dns.RcodeRefused {
		t.Errorf("blocked name: rcode %s", dns.RcodeToString[resp.Rcode])
	}
	h.wantGrants("93.184.215.14:1-65535", "93.184.215.14:1-65535/udp", "140.82.121.4:1-65535", "140.82.121.4:1-65535/udp")

	// Back in allowlist mode only listed names keep their listed ports.
	h.setMode(netrules.Allowlist)
	h.wantGrants("140.82.121.4:22")
	h.setMode(netrules.Blocklist)
	h.wantGrants("140.82.121.4:1-65535", "140.82.121.4:1-65535/udp")
}

func TestBlocklistGrantsExpireWithoutLookups(t *testing.T) {
	h := newHarness(t, "", zone{
		"A a.example.": {"a.example. 60 IN A 198.51.100.1"},
		"A b.example.": {"b.example. 60 IN A 198.51.100.2"},
	})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.filter.mu.Lock()
	h.filter.now = func() time.Time { return now }
	h.filter.mu.Unlock()
	h.setMode(netrules.Blocklist)

	h.query("a.example", dns.TypeA)
	now = now.Add(blocklistTTL / 2)
	h.query("b.example", dns.TypeA)
	now = now.Add(blocklistTTL/2 + time.Second)
	if err := h.filter.Expire(); err != nil {
		t.Fatal(err)
	}
	h.wantGrants("198.51.100.2:1-65535", "198.51.100.2:1-65535/udp")
}

func TestOldestNamesAreForgottenBeyondTheLimit(t *testing.T) {
	h := newHarness(t, "", zone{})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.filter.mu.Lock()
	h.filter.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	h.filter.mu.Unlock()
	h.setMode(netrules.Blocklist)
	defer func(n int) { maxResolvedNames = n }(maxResolvedNames)
	maxResolvedNames = 50
	for i := range maxResolvedNames + 1 {
		addr := netip.AddrFrom4([4]byte{198, 51, byte(i >> 8), byte(i)})
		if err := h.filter.grant(fmt.Sprintf("n%d.example", i), []netip.Addr{addr}); err != nil {
			t.Fatal(err)
		}
	}
	h.filter.mu.Lock()
	_, first := h.filter.resolved["n0.example"]
	n := len(h.filter.resolved)
	h.filter.mu.Unlock()
	if first || n != maxResolvedNames*9/10 {
		t.Errorf("kept %d names, first still there: %v", n, first)
	}
	if got := len(h.grants.list()); got != 2*n {
		t.Errorf("%d grants for %d names", got, n)
	}
}
