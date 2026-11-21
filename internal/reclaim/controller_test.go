package reclaim

import (
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
)

type fakeSource struct {
	sample     Sample
	sampleErr  error
	reclaimErr error
	requests   []uint64
}

func (f *fakeSource) Sample() (Sample, error) { return f.sample, f.sampleErr }

func (f *fakeSource) Reclaim(kib uint64) error {
	f.requests = append(f.requests, kib)
	return f.reclaimErr
}

func TestControllerBackoff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stall    uint64
		refaults uint64
	}{
		{name: "stall", stall: 10_001},
		{name: "refaults", refaults: 525},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{sample: Sample{FileCacheKiB: 1024 * 1024}}
			c := NewController(src, 256*1024, 4096, 10*time.Second)
			start := time.Unix(0, 0)
			tick := func(offset time.Duration, want []uint64) {
				t.Helper()
				if err := c.Tick(start.Add(offset)); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(src.requests, want) {
					t.Fatalf("at %s, requests = %v, want %v", offset, src.requests, want)
				}
			}

			tick(0, nil)
			src.sample.StallUS = tc.stall
			src.sample.RefaultPages = tc.refaults
			tick(10*time.Second, nil)

			// A second burst extends backoff even when writes are paused.
			src.sample.StallUS += tc.stall
			src.sample.RefaultPages += tc.refaults
			tick(30*time.Second, nil)
			tick(70*time.Second, nil)
			tick(90*time.Second, []uint64{20971})
		})
	}
}

func TestControllerCacheFloorAndStep(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cacheKiB uint64
		want     []uint64
	}{
		{name: "below floor", cacheKiB: 200 * 1024},
		{name: "at floor after step", cacheKiB: 267493},
		{name: "above floor after step", cacheKiB: 267494, want: []uint64{5349}},
		{name: "two percent", cacheKiB: 3 * 1024 * 1024, want: []uint64{62914}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{sample: Sample{FileCacheKiB: tc.cacheKiB}}
			c := NewController(src, 256*1024, 4096, 10*time.Second)
			start := time.Unix(0, 0)
			if err := c.Tick(start); err != nil {
				t.Fatal(err)
			}
			src.sample.StallUS = 10_000
			if err := c.Tick(start.Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(src.requests, tc.want) {
				t.Fatalf("requests = %v, want %v", src.requests, tc.want)
			}
		})
	}
}

func TestControllerStallLimitScalesWithInterval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stallUS uint64
		want    []uint64
	}{
		{name: "under 0.1% of a minute", stallUS: 60_000, want: []uint64{20971}},
		{name: "over 0.1% of a minute", stallUS: 60_001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{sample: Sample{FileCacheKiB: 1024 * 1024}}
			c := NewController(src, 256*1024, 4096, time.Minute)
			start := time.Unix(0, 0)
			if err := c.Tick(start); err != nil {
				t.Fatal(err)
			}
			src.sample.StallUS = tc.stallUS
			if err := c.Tick(start.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(src.requests, tc.want) {
				t.Fatalf("requests = %v, want %v", src.requests, tc.want)
			}
		})
	}
}

func TestControllerRefaultLimitFollowsStep(t *testing.T) {
	// The limit is a tenth of the 2% step, in 4 KiB pages.
	for _, tc := range []struct {
		name     string
		cacheKiB uint64
		refaults uint64
		want     []uint64
	}{
		{name: "3 GiB cache at the limit", cacheKiB: 3 << 20, refaults: 1572, want: []uint64{62914}},
		{name: "3 GiB cache over the limit", cacheKiB: 3 << 20, refaults: 1573},
		{name: "300 MiB cache at the limit", cacheKiB: 300 << 10, refaults: 153, want: []uint64{6144}},
		{name: "300 MiB cache over the limit", cacheKiB: 300 << 10, refaults: 154},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{sample: Sample{FileCacheKiB: tc.cacheKiB}}
			c := NewController(src, 256*1024, 4096, 10*time.Second)
			start := time.Unix(0, 0)
			if err := c.Tick(start); err != nil {
				t.Fatal(err)
			}
			src.sample.RefaultPages = tc.refaults
			if err := c.Tick(start.Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(src.requests, tc.want) {
				t.Fatalf("requests = %v, want %v", src.requests, tc.want)
			}
		})
	}
}

func TestControllerSampleErrorRestartsBaseline(t *testing.T) {
	src := &fakeSource{sample: Sample{FileCacheKiB: 1024 * 1024}}
	c := NewController(src, 256*1024, 4096, 10*time.Second)
	start := time.Unix(0, 0)
	if err := c.Tick(start); err != nil {
		t.Fatal(err)
	}
	src.sampleErr = errors.New("unreadable")
	if err := c.Tick(start.Add(10 * time.Second)); err == nil {
		t.Fatal("want the sample error")
	}

	// The stall happened while samples failed, so it can't be attributed to
	// one interval; the next good sample only starts a new baseline.
	src.sampleErr = nil
	src.sample.StallUS = 1_000_000
	if err := c.Tick(start.Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(src.requests) != 0 {
		t.Fatalf("reclaimed without a baseline: %v", src.requests)
	}
	if err := c.Tick(start.Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(src.requests) != 1 {
		t.Fatalf("requests = %v, want one", src.requests)
	}
}

func TestControllerWriteErrors(t *testing.T) {
	src := &fakeSource{sample: Sample{FileCacheKiB: 1024 * 1024}}
	c := NewController(src, 256*1024, 4096, 10*time.Second)
	start := time.Unix(0, 0)
	if err := c.Tick(start); err != nil {
		t.Fatal(err)
	}

	src.reclaimErr = &os.PathError{Op: "write", Path: ReclaimPath, Err: syscall.EAGAIN}
	if err := c.Tick(start.Add(10 * time.Second)); err != nil {
		t.Fatalf("EAGAIN should be silent: %v", err)
	}
	if err := c.Tick(start.Add(20 * time.Second)); err != nil || len(src.requests) != 1 {
		t.Fatalf("EAGAIN should back off: err=%v requests=%v", err, src.requests)
	}

	src.reclaimErr = syscall.EPERM
	if err := c.Tick(start.Add(80 * time.Second)); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("got %v, want EPERM", err)
	}
}
