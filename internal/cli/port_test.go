package cli

import (
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
