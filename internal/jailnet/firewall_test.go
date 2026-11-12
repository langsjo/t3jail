package jailnet

import (
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/netrules"
)

// runInFreshNetns runs nft scripts in order inside a throwaway user+net
// namespace and returns `nft list table inet t3jail`.
func runInFreshNetns(t *testing.T, scripts []string) string {
	t.Helper()
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if exec.Command("unshare", "-rn", "true").Run() != nil {
		t.Skip("unprivileged user namespaces unavailable")
	}
	// Each script is applied by its own nft run, like Firewall.Apply does.
	var sh strings.Builder
	for i, s := range scripts {
		sh.WriteString("nft -f - <<'NFT" + string(rune('A'+i)) + "'\n" + s + "\nNFT" + string(rune('A'+i)) + "\n")
	}
	sh.WriteString("nft list table inet t3jail\n")
	out, err := exec.Command("unshare", "-rn", "sh", "-euc", sh.String()).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

func recordingFirewall() (*Firewall, *[]string) {
	var scripts []string
	return &Firewall{Apply: func(s string) error {
		scripts = append(scripts, s)
		return nil
	}}, &scripts
}

func TestFirewallOperationsApplyToRealNft(t *testing.T) {
	fw, scripts := recordingFirewall()
	if err := fw.Init(guestabi.Get()); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(fw.SetHostNets([]netip.Prefix{netip.MustParsePrefix("81.2.69.0/24"), netip.MustParsePrefix("2a00:1450::/32")}))
	// Replacing must drop the old host networks.
	must(fw.SetHostNets([]netip.Prefix{netip.MustParsePrefix("93.184.0.0/16")}))
	must(fw.SetPrefixAllows([]netrules.Destination{
		{Prefix: netip.MustParsePrefix("192.168.1.50/32"), Ports: netrules.PortRange{First: 22, Last: 22}},
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), Ports: netrules.PortRange{First: 8000, Last: 8100}},
	}))
	keep := netrules.Destination{Prefix: netip.MustParsePrefix("140.82.121.4/32"), Ports: netrules.PortRange{First: 443, Last: 443}}
	gone := netrules.Destination{Prefix: netip.MustParsePrefix("140.82.121.5/32"), Ports: netrules.PortRange{First: 443, Last: 443}}
	v6 := netrules.Destination{Prefix: netip.MustParsePrefix("2606:50c0:8000::154/128"), Ports: netrules.PortRange{First: 22, Last: 22}}
	narrow := netrules.Destination{Prefix: netip.MustParsePrefix("140.82.121.6/32"), Ports: netrules.PortRange{First: 80, Last: 90}}
	wide := netrules.Destination{Prefix: narrow.Prefix, Ports: netrules.PortRange{First: 80, Last: 95}}
	must(fw.UpdateDNSAllows(nil, []netrules.Destination{keep, gone, v6, narrow}))
	// Replacing an element with an overlapping wider one must work in one go.
	must(fw.UpdateDNSAllows([]netrules.Destination{gone, narrow}, []netrules.Destination{wide}))

	table := runInFreshNetns(t, *scripts)
	for _, want := range []string{
		"93.184.0.0/16",
		"192.168.1.50 . 22",
		"2001:db8::/32 . 8000-8100",
		"140.82.121.4 . 443",
		"2606:50c0:8000::154 . 22",
		"140.82.121.6 . 80-95",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	for _, unwanted := range []string{"81.2.69.0/24", "2a00:1450::/32", "140.82.121.5", "80-90"} {
		if strings.Contains(table, unwanted) {
			t.Errorf("table still has %q:\n%s", unwanted, table)
		}
	}
}

func TestEmptyUpdatesAreValidScripts(t *testing.T) {
	fw, scripts := recordingFirewall()
	if err := fw.Init(guestabi.Get()); err != nil {
		t.Fatal(err)
	}
	fw.SetHostNets(nil)
	fw.SetPrefixAllows(nil)
	fw.UpdateDNSAllows(nil, nil)
	runInFreshNetns(t, *scripts)
}
