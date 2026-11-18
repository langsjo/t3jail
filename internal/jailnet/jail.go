package jailnet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sync"

	"github.com/miekg/dns"
	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/dnsfilter"
	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/netrules"
)

// Jail is the filtering half of the helper: firewall, DNS filter and the API
// the daemon uses to change them while the VM runs.
type Jail struct {
	fw            *Firewall
	filter        *dnsfilter.Filter
	listPath      string
	ephemeralPath string
	markerPath    string
	overridePath  string
	guest         []netip.Addr
	gateways      []netip.Addr

	mu           sync.Mutex
	prefixAllows []netrules.Destination
	filterOff    bool
}

// HostNetsRequest replaces the set of host networks the guest may not reach.
type HostNetsRequest struct {
	Prefixes []netip.Prefix `json:"prefixes"`
}

func startJail(abi guestabi.ABI, spec HelperSpec) (*Jail, error) {
	j := &Jail{
		fw:            NewFirewall(),
		listPath:      spec.NetworkList,
		ephemeralPath: spec.EphemeralNetworkList,
		markerPath:    spec.NetworkOffMarker,
		overridePath:  spec.NetworkFilterOverride,
		guest:         []netip.Addr{abi.Guest4, abi.Guest6},
		gateways:      []netip.Addr{abi.Gateway4, abi.Gateway6},
	}
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
	if err := j.applyFilterState(); err != nil {
		return nil, err
	}

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

// readList reads the persistent allowlist plus the entries allowed only until
// the VM stops.
func (j *Jail) readList() (*netrules.List, error) {
	list, err := readListFile(j.listPath)
	if err != nil {
		return nil, err
	}
	ephemeral, err := readListFile(j.ephemeralPath)
	if errors.Is(err, fs.ErrNotExist) {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range ephemeral.Entries() {
		list.Add(e)
	}
	return list, nil
}

func readListFile(path string) (*netrules.List, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	list, err := netrules.ParseList(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

// Reload applies the current allowlists. A list that does not parse changes
// nothing; if applying fails partway, the error says which part.
func (j *Jail) Reload(context.Context, control.Empty) (control.Empty, error) {
	list, err := j.readList()
	if err != nil {
		return control.Empty{}, err
	}
	if err := j.setPrefixAllows(list); err != nil {
		return control.Empty{}, fmt.Errorf("address entries: %w", err)
	}
	if err := j.filter.SetList(list); err != nil {
		return control.Empty{}, fmt.Errorf("name entries: %w", err)
	}
	if err := j.applyFilterState(); err != nil {
		return control.Empty{}, fmt.Errorf("switching the allowlist: %w", err)
	}
	return control.Empty{}, nil
}

// applyFilterState switches the allowlist off or on as its state files say.
func (j *Jail) applyFilterState() error {
	state, err := netrules.LoadFilterState(j.markerPath, j.overridePath)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if state.Off == j.filterOff {
		return nil
	}
	// Names resolve only while the firewall lets their addresses through.
	if state.Off {
		if err := j.fw.SetUnfiltered(true); err != nil {
			return err
		}
		j.filter.SetOpen(true)
	} else {
		j.filter.SetOpen(false)
		if err := j.fw.SetUnfiltered(false); err != nil {
			return err
		}
		cutUnallowed(j.guest, j.gateways, append(slices.Clone(j.prefixAllows), j.filter.Granted()...))
	}
	j.filterOff = state.Off
	return nil
}

func (j *Jail) setPrefixAllows(list *netrules.List) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var dests []netrules.Destination
	for _, r := range list.Prefixes() {
		dests = append(dests, r.Destinations()...)
	}
	dests = netrules.Normalize(dests)
	if err := j.fw.SetPrefixAllows(dests); err != nil {
		return err
	}
	old := j.prefixAllows
	j.prefixAllows = dests
	cutConnections(old, dests)
	return nil
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

// Update returns an error only if the firewall did not change, so the
// filter's record of granted elements always matches the nft set.
func (g dnsGrants) Update(remove, add []netrules.Destination) error {
	if err := g.fw.UpdateDNSAllows(remove, add); err != nil {
		return err
	}
	// Elements of a normalized set do not overlap, so a removed port is
	// still allowed only if an added element covers it.
	cutConnections(remove, add)
	return nil
}

// cutConnections ends established connections that removed allowed and kept
// does not, which the firewall's established rule would otherwise let
// continue. It runs after the firewall changed and is best effort: new
// connections are already refused, a missed one only runs its course.
func cutConnections(removed, kept []netrules.Destination) {
	if len(removed) == 0 {
		return
	}
	err := cutFlows(func(f conntrack.Flow) bool {
		addr, proto, port := flowDestination(f)
		return netrules.Covers(removed, addr, proto, port) && !netrules.Covers(kept, addr, proto, port)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "t3jail: ending connections to removed destinations: %v\n", err)
	}
}

// cutUnallowed ends the guest's connections that only the switched-off
// allowlist let through: those to destinations not in allowed, which has
// only TCP and UDP. Best effort, like cutConnections.
func cutUnallowed(guest, gateways []netip.Addr, allowed []netrules.Destination) {
	err := cutFlows(func(f conntrack.Flow) bool {
		addr, proto, port := flowDestination(f)
		src := f.TupleOrig.IP.SourceAddress.Unmap()
		if !slices.Contains(guest, src) || slices.Contains(gateways, addr) {
			return false
		}
		return !netrules.Covers(allowed, addr, proto, port)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "t3jail: ending connections made while the allowlist was off: %v\n", err)
	}
}

func flowDestination(f conntrack.Flow) (netip.Addr, netrules.Proto, uint16) {
	return f.TupleOrig.IP.DestinationAddress.Unmap(), netrules.Proto(f.TupleOrig.Proto.Protocol), f.TupleOrig.Proto.DestinationPort
}

func cutFlows(cut func(conntrack.Flow) bool) error {
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
		if cut(f) {
			// ENOENT: the flow ended between the dump and the delete.
			if err := c.Delete(f); err != nil && !errors.Is(err, unix.ENOENT) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
