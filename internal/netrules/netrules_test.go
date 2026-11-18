package netrules

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestParseCanonical(t *testing.T) {
	cases := map[string]string{
		"GitHub.com.":            "github.com",
		"*.github.com":           "*.github.com",
		"github.com:22":          "github.com:22",
		"host.example:443,80":    "host.example:80,443",
		"h:8000-8100,8050,8101":  "h:8000-8101",
		"h:53/UDP,443,443/udp":   "h:443,53/udp,443/udp",
		"h:80/tcp":               "h:80",
		"h:1-10/udp,11-20/udp,5": "h:5,1-20/udp",
		"[2001:db8::1]:53/udp":   "[2001:db8::1]:53/udp",
		"203.0.113.7":            "203.0.113.7",
		"203.0.113.0/24:22":      "203.0.113.0/24:22",
		"2001:db8::1":            "2001:db8::1",
		"2001:db8::/32":          "2001:db8::/32",
		"[2001:db8::/32]:443":    "[2001:db8::/32]:443",
		"[2001:db8::1]":          "2001:db8::1",
		"_srv.example.com":       "_srv.example.com",
		"  spaced.example.com  ": "spaced.example.com",
	}
	for in, want := range cases {
		e, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got := e.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, got, want)
		}
		if again, err := Parse(e.String()); err != nil || again.String() != want {
			t.Errorf("canonical form %q does not round-trip: %v", want, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, bad := range []string{
		"", "*", "*.", "a..b", "-a.com", "a-.com", "ex ample.com", "foo*.com", "a.*.com",
		"github.com:", "github.com:0", "github.com:70000", "github.com:443-80", "github.com:x",
		"github.com:53/icmp", "github.com:/udp", "github.com:53/",
		"203.0.113.7/24",   // host bits set
		"[2001:db8::1:443", // missing ]
		"[2001:db8::1]443", // missing :
		"fe80::1%eth0",     // zone
		strings.Repeat("a", 64) + ".com",
	} {
		if e, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) = %v, want error", bad, e)
		}
	}
}

func TestMatchesName(t *testing.T) {
	exact, _ := Parse("github.com")
	wild, _ := Parse("*.github.com")
	cases := []struct {
		e     Entry
		name  string
		match bool
	}{
		{exact, "github.com", true},
		{exact, "GITHUB.COM.", true},
		{exact, "api.github.com", false},
		{exact, "notgithub.com", false},
		{wild, "github.com", true},
		{wild, "api.github.com", true},
		{wild, "a.b.github.com", true},
		{wild, "notgithub.com", false},
		{wild, "github.com.evil.example", false},
	}
	for _, c := range cases {
		if got := c.e.MatchesName(c.name); got != c.match {
			t.Errorf("%v matches %q = %v, want %v", c.e, c.name, got, c.match)
		}
	}
}

func TestPortsForNameUnionsMatchingEntries(t *testing.T) {
	l, err := ParseList([]byte("github.com\n*.github.com:22\nother.example:8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	ports, ok := l.PortsForName("github.com")
	if !ok || !slices.Equal(ports, []PortRange{{TCP, 22, 22}, {TCP, 80, 80}, {TCP, 443, 443}}) {
		t.Errorf("github.com ports = %v, %v", ports, ok)
	}
	ports, ok = l.PortsForName("api.github.com")
	if !ok || !slices.Equal(ports, []PortRange{{TCP, 22, 22}}) {
		t.Errorf("api.github.com ports = %v, %v (exact ports only)", ports, ok)
	}
	if _, ok := l.PortsForName("example.com"); ok {
		t.Error("unlisted name matched")
	}
}

func TestListEditingPreservesComments(t *testing.T) {
	src := "# header\n\ngithub.com # needed for git\n*.example.com\n"
	l, err := ParseList([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := Parse("NEW.example.org")
	if !l.Add(e) || l.Add(e) {
		t.Fatal("Add should succeed once")
	}
	dup, _ := Parse("GitHub.com.")
	if l.Add(dup) {
		t.Fatal("equivalent entry added twice")
	}
	rm, _ := Parse("*.example.com")
	if !l.Remove(rm) || l.Remove(rm) {
		t.Fatal("Remove should succeed once")
	}
	want := "# header\n\ngithub.com # needed for git\nnew.example.org\n"
	if got := string(l.Bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseListReportsLine(t *testing.T) {
	_, err := ParseList([]byte("ok.example\n\nbad name\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultListParses(t *testing.T) {
	l, err := ParseList([]byte(DefaultList))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cache.nixos.org", "github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com"} {
		if _, ok := l.PortsForName(name); !ok {
			t.Errorf("default list does not allow %s", name)
		}
	}
}

func TestPrefixes(t *testing.T) {
	l, _ := ParseList([]byte("192.168.1.50:22\n[2001:db8::/32]\nexample.com\n"))
	got := l.Prefixes()
	want := []PrefixRule{
		{netip.MustParsePrefix("192.168.1.50/32"), []PortRange{{TCP, 22, 22}}},
		{netip.MustParsePrefix("2001:db8::/32"), DefaultPorts},
	}
	if len(got) != 2 || got[0].Prefix != want[0].Prefix || !slices.Equal(got[0].Ports, want[0].Ports) || got[1].Prefix != want[1].Prefix || !slices.Equal(got[1].Ports, want[1].Ports) {
		t.Fatalf("got %+v", got)
	}
}

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		allow, req string
		want       bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "example.com:443", true},
		{"example.com:443", "example.com", false},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "*.api.example.com", true},
		{"api.example.com", "*.api.example.com", false},
		{"example.com:1-1000", "example.com:22,80", true},
		{"example.com:1-1000", "example.com:53/udp", false},
		{"example.com:443,443/udp", "example.com:443/udp", true},
		{"other.com", "example.com", false},
		{"192.0.2.1", "example.com", false},
	} {
		a, b := mustParse(t, tc.allow), mustParse(t, tc.req)
		if got := a.Covers(b); got != tc.want {
			t.Errorf("%s covers %s = %v, want %v", tc.allow, tc.req, got, tc.want)
		}
	}
}

func mustParse(t *testing.T, s string) Entry {
	t.Helper()
	e, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
