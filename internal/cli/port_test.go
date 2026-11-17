package cli

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/langsjo/t3jail/internal/control"
)

func TestParsePortForward(t *testing.T) {
	for in, want := range map[string]control.PortForward{
		"3000":           {Listen: 3000, Target: 3000},
		"8080:3000":      {Listen: 8080, Target: 3000},
		"65535:1":        {Listen: 65535, Target: 1},
		"0":              {},
		"65536":          {},
		"8080:":          {},
		":8080":          {},
		"80:90:100":      {},
		"localhost:8080": {},
		"-1":             {},
	} {
		got, err := parsePortForward(in)
		if want == (control.PortForward{}) {
			if err == nil {
				t.Errorf("%q: accepted as %+v", in, got)
			}
		} else if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
}

func TestPortRows(t *testing.T) {
	to3000 := control.PortForward{Direction: control.PortToVM, Bind: control.DefaultPortBind, Listen: 3000, Target: 3000}
	to3000other := to3000
	to3000other.Target = 4000
	from5432 := control.PortForward{Direction: control.PortFromVM, Listen: 5432, Target: 5432}
	to8080 := control.PortForward{Direction: control.PortToVM, Bind: netip.MustParseAddr("100.64.1.2"), Listen: 8080, Target: 8080}

	got := portRows([]control.PortForward{to3000, from5432}, []control.PortForward{to3000other, from5432, to8080}, true)
	want := []string{
		"to    host 127.0.0.1:3000 -> VM localhost:3000\topen",
		"from  VM localhost:5432 -> host localhost:5432\topen, persisted",
		"to    host 127.0.0.1:3000 -> VM localhost:4000\tpersisted, not open (see `t3jail logs --log daemon`)",
		"to    host 100.64.1.2:8080 -> VM localhost:8080\tpersisted, not open (see `t3jail logs --log daemon`)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("running:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	got = portRows(nil, []control.PortForward{to8080}, false)
	if want := []string{"to    host 100.64.1.2:8080 -> VM localhost:8080\tat next start"}; !slices.Equal(got, want) {
		t.Errorf("stopped: %q, want %q", got, want)
	}
}
