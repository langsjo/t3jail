package portfwd

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/langsjo/t3jail/internal/control"
)

func TestSaveLoadKeepsBindAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.toml")
	want := Persisted{Forwards: []control.PortForward{
		{Direction: control.PortToVM, Bind: netip.MustParseAddr("100.64.1.2"), Listen: 8080, Target: 3000},
		{Direction: control.PortToVM, Bind: netip.MustParseAddr("::1"), Listen: 5173, Target: 5173},
		{Direction: control.PortFromVM, Listen: 5432, Target: 5432},
	}}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Forwards, want.Forwards) {
		t.Errorf("loaded %+v, want %+v", got.Forwards, want.Forwards)
	}
}

func TestLoadFillsDefaultBind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.toml")
	os.WriteFile(path, []byte("[[forward]]\ndirection = \"to\"\nlisten = 3000\ntarget = 3000\n"), 0o600)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Forwards) != 1 || p.Forwards[0].Bind != control.DefaultPortBind {
		t.Errorf("loaded %+v", p.Forwards)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, body := range map[string]string{
		"from with bind": `direction = "from"` + "\nbind = \"127.0.0.1\"\nlisten = 1\ntarget = 1",
		"port 0":         `direction = "to"` + "\nlisten = 0\ntarget = 1",
		"bad direction":  `direction = "up"` + "\nlisten = 1\ntarget = 1",
		"unknown key":    `direction = "to"` + "\nlisten = 1\ntarget = 1\nproto = \"udp\"",
		"bad bind":       `direction = "to"` + "\nbind = \"localhost\"\nlisten = 1\ntarget = 1",
		"listed twice": `direction = "to"` + "\nlisten = 1\ntarget = 1\n[[forward]]\n" +
			`direction = "to"` + "\nlisten = 1\ntarget = 2",
	} {
		path := filepath.Join(t.TempDir(), "ports.toml")
		os.WriteFile(path, []byte("[[forward]]\n"+body+"\n"), 0o600)
		if p, err := Load(path); err == nil {
			t.Errorf("%s: loaded %+v", name, p.Forwards)
		} else if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
	}
}
