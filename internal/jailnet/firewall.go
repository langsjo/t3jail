package jailnet

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"text/template"

	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/netrules"
)

//go:embed ruleset.nft.tmpl
var rulesetTemplate string

// Special-purpose ranges blocked by default (RFC 6890 and friends): this
// network, private, CGNAT, loopback, link-local (also cloud metadata and
// pasta's DNS forwarder), documentation, benchmarking, multicast and reserved.
var (
	defaultBlocked4 = []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3",
	}
	defaultBlocked6 = []string{
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
	}
)

// nftElement formats a destination as an `addr . ports` set element.
func nftElement(e netrules.Destination) string {
	ports := fmt.Sprint(e.Ports.First)
	if e.Ports.Last != e.Ports.First {
		ports += fmt.Sprintf("-%d", e.Ports.Last)
	}
	addr := e.Prefix.String()
	if e.Prefix.IsSingleIP() {
		addr = e.Prefix.Addr().String()
	}
	return addr + " . " + ports
}

// Firewall applies nft scripts in the jail's network namespace.
type Firewall struct {
	// Apply runs an nft script atomically; replaceable in tests.
	Apply func(script string) error
}

func NewFirewall() *Firewall {
	return &Firewall{Apply: runNft}
}

func runNft(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// RenderRuleset returns the complete ruleset with empty dynamic sets.
func RenderRuleset(abi guestabi.ABI) (string, error) {
	tmpl, err := template.New("ruleset").Funcs(template.FuncMap{"join": strings.Join}).Parse(rulesetTemplate)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	err = tmpl.Execute(&b, map[string]any{
		"Tap":      TapName,
		"Gateway4": abi.Gateway4,
		"Gateway6": abi.Gateway6,
		"Blocked4": defaultBlocked4,
		"Blocked6": defaultBlocked6,
	})
	return b.String(), err
}

func (f *Firewall) Init(abi guestabi.ABI) error {
	ruleset, err := RenderRuleset(abi)
	if err != nil {
		return err
	}
	return f.Apply(ruleset)
}

// SetHostNets replaces the host address sets.
func (f *Firewall) SetHostNets(prefixes []netip.Prefix) error {
	var v4, v6 []string
	for _, p := range prefixes {
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	return f.Apply(replaceSet("hostnets4", v4) + replaceSet("hostnets6", v6))
}

// SetPrefixAllows replaces the explicit address allows. dests must be
// normalized (netrules.Normalize).
func (f *Firewall) SetPrefixAllows(dests []netrules.Destination) error {
	v4, v6 := splitElements(dests)
	return f.Apply(replaceSet("cidr_allow4", v4) + replaceSet("cidr_allow6", v6))
}

// UpdateDNSAllows removes and adds resolved destinations in one transaction,
// so replacing an element with a wider one never leaves a gap. The resulting
// set must be normalized (netrules.Normalize).
func (f *Firewall) UpdateDNSAllows(remove, add []netrules.Destination) error {
	return f.Apply(changeElements("delete", remove) + changeElements("add", add))
}

func splitElements(elems []netrules.Destination) (v4, v6 []string) {
	for _, e := range elems {
		if e.Prefix.Addr().Is4() {
			v4 = append(v4, nftElement(e))
		} else {
			v6 = append(v6, nftElement(e))
		}
	}
	return v4, v6
}

func changeElements(verb string, elems []netrules.Destination) string {
	v4, v6 := splitElements(elems)
	var b strings.Builder
	for set, items := range map[string][]string{"dns_allow4": v4, "dns_allow6": v6} {
		if len(items) > 0 {
			fmt.Fprintf(&b, "%s element inet t3jail %s { %s }\n", verb, set, strings.Join(items, ", "))
		}
	}
	return b.String()
}

func replaceSet(set string, elems []string) string {
	s := fmt.Sprintf("flush set inet t3jail %s\n", set)
	if len(elems) > 0 {
		s += fmt.Sprintf("add element inet t3jail %s { %s }\n", set, strings.Join(elems, ", "))
	}
	return s
}
