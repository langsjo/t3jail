package reclaim

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

func TestProcParsers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parse   func(string) (uint64, error)
		input   string
		want    uint64
		wantErr bool
	}{
		{"PSI some", parseStall, "some avg10=0.00 avg60=0.01 avg300=0.10 total=1234\nfull avg10=0.00 total=42\n", 1234, false},
		{"PSI missing some", parseStall, "full avg10=0.00 total=42\n", 0, true},
		{"PSI malformed total", parseStall, "some avg10=0.00 total=-42\n", 0, true},
		{"refaults", parseRefaults, "workingset_refault_anon 7\nworkingset_refault_file 80\n", 80, false},
		{"refaults missing", parseRefaults, "workingset_refault_anon 7\n", 0, true},
		{"refaults malformed", parseRefaults, "workingset_refault_file eighty\n", 0, true},
		{"file cache", parseFileCache, "Cached: 400 kB\nActive(file): 120 kB\nInactive(file): 280 kB\n", 400, false},
		{"file cache missing", parseFileCache, "Inactive(file): 280 kB\n", 0, true},
		{"file cache wrong units", parseFileCache, "Active(file): 120 MB\nInactive(file): 280 kB\n", 0, true},
		{"file cache malformed", parseFileCache, "Active(file): xx kB\nInactive(file): 280 kB\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.parse(tc.input)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got (%d, %v), want (%d, error=%t)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestReportingEnabled(t *testing.T) {
	other := fstest.MapFS{
		"virtio0/device":   {Data: []byte("0x00000002\n")},
		"virtio0/features": {Data: []byte("000001000\n")},
		"virtio0/driver":   {Mode: fs.ModeDir},
	}
	balloon := func(name, bits string, bound bool) fstest.MapFS {
		devices := fstest.MapFS{
			name + "/device":   {Data: []byte("0x00000005\n")},
			name + "/features": {Data: []byte(bits)},
		}
		if bound {
			devices[name+"/driver"] = &fstest.MapFile{Mode: fs.ModeDir}
		}
		return devices
	}
	for _, tc := range []struct {
		name     string
		balloons []fstest.MapFS
		want     bool
		wantErr  bool
	}{
		{name: "no balloon"},
		{name: "bit five set", balloons: []fstest.MapFS{balloon("virtio1", "000001000\n", true)}, want: true},
		{name: "other bits set", balloons: []fstest.MapFS{balloon("virtio1", "111110111\n", true)}},
		{name: "short feature list", balloons: []fstest.MapFS{balloon("virtio1", "11111\n", true)}},
		{name: "no driver bound", balloons: []fstest.MapFS{balloon("virtio1", "000001000\n", false)}},
		{name: "second balloon reports", balloons: []fstest.MapFS{
			balloon("virtio1", "000000000\n", true),
			balloon("virtio2", "000001000\n", true),
		}, want: true},
		{name: "malformed features", balloons: []fstest.MapFS{balloon("virtio1", "01x0111\n", true)}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			devices := fstest.MapFS{}
			maps.Copy(devices, other)
			for _, b := range tc.balloons {
				maps.Copy(devices, b)
			}
			got, err := reportingEnabled(devices)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got (%t, %v), want (%t, error=%t)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// Needs root and cgroup v2. An empty cgroup has nothing to reclaim, so its
// memory.reclaim fails with EAGAIN at once, which must not block.
func TestWriteReclaimReturnsEAGAIN(t *testing.T) {
	dir := filepath.Join("/sys/fs/cgroup", fmt.Sprintf("t3jail-reclaim-test-%d", os.Getpid()))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("cannot create a cgroup: %v", err)
	}
	t.Cleanup(func() { os.Remove(dir) })
	path := filepath.Join(dir, "memory.reclaim")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no memory controller in child cgroups: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- writeReclaim(path, 1024) }()
	select {
	case err := <-done:
		if !errors.Is(err, syscall.EAGAIN) {
			t.Fatalf("got %v, want EAGAIN", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write blocked")
	}
}
