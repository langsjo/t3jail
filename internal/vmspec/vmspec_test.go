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

func TestCheckResize(t *testing.T) {
	r := Resources{CPUs: 4, MaxCPUs: 8, Memory: 8 * config.GiB, MaxMemory: 16 * config.GiB}
	// Boot memory is 2G (a quarter of 8G).
	for _, ok := range []struct {
		cpus int
		mem  config.ByteSize
	}{{0, 0}, {1, 0}, {8, 0}, {0, 2 * config.GiB}, {0, 16 * config.GiB}, {6, 12*config.GiB + 128*config.MiB}} {
		if err := r.CheckResize(ok.cpus, ok.mem); err != nil {
			t.Errorf("CheckResize(%d, %v): %v", ok.cpus, ok.mem, err)
		}
	}
	for _, bad := range []struct {
		cpus int
		mem  config.ByteSize
	}{{-1, 0}, {9, 0}, {0, 2*config.GiB - 128*config.MiB}, {0, 17 * config.GiB}, {0, 3*config.GiB + config.MiB}} {
		if err := r.CheckResize(bad.cpus, bad.mem); err == nil {
			t.Errorf("CheckResize(%d, %v) accepted", bad.cpus, bad.mem)
		}
	}
}

func TestForRescueHasNoHotplugMemory(t *testing.T) {
	normal := Resources{CPUs: 2, MaxCPUs: 4, Memory: 8 * GiB, MaxMemory: 16 * GiB}
	rescue := normal.ForRescue()
	if err := rescue.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := rescue.SplitMemory(); got != (Memory{Boot: 8 * GiB}) {
		t.Errorf("SplitMemory = %+v, want all 8G as boot memory", got)
	}
	if err := rescue.CheckResize(0, 4*GiB); err == nil {
		t.Error("memory resize allowed without a hotplug region")
	}
	if err := rescue.CheckResize(3, 0); err != nil {
		t.Errorf("CPU resize: %v", err)
	}

	small := Resources{CPUs: 1, MaxCPUs: 1, Memory: 1 * GiB, MaxMemory: 1 * GiB}.ForRescue()
	if got := small.SplitMemory().Boot; got != MinRescueMemory {
		t.Errorf("boot memory %v, want at least %v for the initrd", got, MinRescueMemory)
	}
}
