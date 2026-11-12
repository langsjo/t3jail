package vmspec

import (
	"testing"

	"github.com/langsjo/t3jail/internal/config"
)

const (
	MiB = config.MiB
	GiB = config.GiB
)

func TestSplitMemory(t *testing.T) {
	cases := []struct {
		mem, max config.ByteSize
		want     Memory
	}{
		{8 * GiB, 16 * GiB, Memory{Boot: 2 * GiB, Hotplug: 14 * GiB, Plugged: 6 * GiB}},
		// Small VMs keep the minimum boot memory.
		{2 * GiB, 8 * GiB, Memory{Boot: 1 * GiB, Hotplug: 7 * GiB, Plugged: 1 * GiB}},
		// Boot is never larger than the requested size.
		{1 * GiB, 1 * GiB, Memory{Boot: 1 * GiB, Hotplug: 0, Plugged: 0}},
		// A quarter that is not 128M-aligned is rounded up.
		{5 * GiB, 8 * GiB, Memory{Boot: 1280 * MiB, Hotplug: 8*GiB - 1280*MiB, Plugged: 5*GiB - 1280*MiB}},
	}
	for _, c := range cases {
		r := Resources{CPUs: 1, MaxCPUs: 1, Memory: c.mem, MaxMemory: c.max}
		if err := r.Validate(); err != nil {
			t.Fatalf("%v/%v: %v", c.mem, c.max, err)
		}
		got := r.SplitMemory()
		if got != c.want {
			t.Errorf("SplitMemory(%v, max %v) = %+v, want %+v", c.mem, c.max, got, c.want)
		}
		if got.Boot%MemoryAlign != 0 || got.Boot+got.Hotplug != c.max || got.Boot+got.Plugged != c.mem {
			t.Errorf("inconsistent split %+v", got)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Resources{CPUs: 2, MaxCPUs: 4, Memory: 4 * GiB, MaxMemory: 8 * GiB}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]Resources{
		"zero cpus":         {CPUs: 0, MaxCPUs: 4, Memory: 4 * GiB, MaxMemory: 8 * GiB},
		"cpus over max":     {CPUs: 5, MaxCPUs: 4, Memory: 4 * GiB, MaxMemory: 8 * GiB},
		"memory over max":   {CPUs: 1, MaxCPUs: 4, Memory: 9 * GiB, MaxMemory: 8 * GiB},
		"memory too small":  {CPUs: 1, MaxCPUs: 4, Memory: 512 * MiB, MaxMemory: 8 * GiB},
		"unaligned memory":  {CPUs: 1, MaxCPUs: 4, Memory: 4*GiB + 1, MaxMemory: 8 * GiB},
		"unaligned maximum": {CPUs: 1, MaxCPUs: 4, Memory: 4 * GiB, MaxMemory: 8*GiB + 64*MiB},
	}
	for name, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate succeeded", name)
		}
	}
}
