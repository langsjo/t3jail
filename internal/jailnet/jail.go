package jailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sync"

	"github.com/miekg/dns"
	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/dnsfilter"
	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/netrules"
)

// Jail is the filtering half of the helper: firewall, DNS filter and the API
// the daemon uses to change them while the VM runs.
type Jail struct {
	fw       *Firewall
	filter   *dnsfilter.Filter
	listPath string

	mu           sync.Mutex
	prefixAllows []netrules.Destination
}

// HostNetsRequest replaces the set of host networks the guest may not reach.
type HostNetsRequest struct {
	Prefixes []netip.Prefix `json:"prefixes"`
}

func startJail(abi guestabi.ABI, spec HelperSpec) (*Jail, error) {
	j := &Jail{fw: NewFirewall(), listPath: spec.NetworkList}
	if err := j.fw.Init(abi); err != nil {
		return nil, err
	}
	if err := j.fw.SetHostNets(spec.HostNets); err != nil {
		return nil, err
	}
	list, err := j.readList()
	if err != nil {
		return nil, err
	}
	if err := j.setPrefixAllows(list); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(spec.DNSLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	upstream := net.JoinHostPort(DNSForward, "53")
	j.filter = dnsfilter.New(list, upstream, dnsGrants{j.fw}, haveIPv6Route, logFile)

	for _, addr := range []netip.Addr{abi.Gateway4, abi.Gateway6} {
		if err := serveDNS(netip.AddrPortFrom(addr, 53), j.filter); err != nil {
			return nil, err
		}
	}
	if err := j.serveAPI(spec.APISocket); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *Jail) readList() (*netrules.List, error) {
	data, err := os.ReadFile(j.listPath)
	if err != nil {
		return nil, err
	}
	list, err := netrules.ParseList(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", j.listPath, err)
	}
	return list, nil
}

// Reload applies the current network.list. On error the old rules stay.
func (j *Jail) Reload(context.Context, control.Empty) (control.Empty, error) {
	list, err := j.readList()
	if err != nil {
		return control.Empty{}, err
	}
	if err := j.setPrefixAllows(list); err != nil {
		return control.Empty{}, err
	}
	return control.Empty{}, j.filter.SetList(list)
}

func (j *Jail) setPrefixAllows(list *netrules.List) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	rules := list.Prefixes()
	if err := j.fw.SetPrefixAllows(rules); err != nil {
		return err
	}
	var current []netrules.Destination
	for _, r := range rules {
		current = append(current, r.Destinations()...)
	}
	var removed []netrules.Destination
	for _, d := range j.prefixAllows {
		if !slices.Contains(current, d) {
			removed = append(removed, d)
		}
	}
	j.prefixAllows = current
	return flushConntrack(removed)
}

func (j *Jail) SetHostNets(_ context.Context, req HostNetsRequest) (control.Empty, error) {
	return control.Empty{}, j.fw.SetHostNets(req.Prefixes)
}

func (j *Jail) serveAPI(socket string) error {
	os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/reload", control.Handler(j.Reload))
	mux.Handle("PUT /v1/hostnets", control.Handler(j.SetHostNets))
	go http.Serve(l, mux)
	return nil
}

func serveDNS(addr netip.AddrPort, handler dns.Handler) error {
	pc, err := net.ListenPacket("udp", addr.String())
	if err != nil {
		return fmt.Errorf("DNS on %v: %w", addr, err)
	}
	l, err := net.Listen("tcp", addr.String())
	if err != nil {
		pc.Close()
		return fmt.Errorf("DNS on %v: %w", addr, err)
	}
	go (&dns.Server{PacketConn: pc, Handler: handler}).ActivateAndServe()
	go (&dns.Server{Listener: l, Handler: handler}).ActivateAndServe()
	return nil
}

// pasta only gives the namespace an IPv6 default route if the host has one.
func haveIPv6Route() bool {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V6)
	if err != nil {
		return false
	}
	for _, r := range routes {
		if r.Dst == nil || r.Dst.IP.IsUnspecified() {
			return true
		}
	}
	return false
}

type dnsGrants struct{ fw *Firewall }

func (g dnsGrants) Grant(ds []netrules.Destination) error { return g.fw.AddDNSAllows(ds) }

func (g dnsGrants) Revoke(ds []netrules.Destination) error {
	if err := g.fw.RemoveDNSAllows(ds); err != nil {
		return err
	}
	return flushConntrack(ds)
}

// flushConntrack ends established connections to revoked destinations, which
// the firewall would otherwise keep accepting.
func flushConntrack(ds []netrules.Destination) error {
	if len(ds) == 0 {
		return nil
	}
	c, err := conntrack.Dial(nil)
	if err != nil {
		return err
	}
	defer c.Close()
	flows, err := c.Dump(nil)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range flows {
		if matchesAny(ds, f.TupleOrig) {
			if err := c.Delete(f); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func matchesAny(ds []netrules.Destination, t conntrack.Tuple) bool {
	const tcp = 6
	if t.Proto.Protocol != tcp {
		return false
	}
	for _, d := range ds {
		port := t.Proto.DestinationPort
		if d.Prefix.Contains(t.IP.DestinationAddress.Unmap()) && port >= d.Ports.First && port <= d.Ports.Last {
			return true
		}
	}
	return false
}
