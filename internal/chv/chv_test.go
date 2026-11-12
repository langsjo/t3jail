package chv

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/vmspec"
)

func baseSpec() LaunchSpec {
	return LaunchSpec{
		APISocket: "/r/ch.sock", SerialSocket: "/r/serial.sock", VsockSocket: "/r/vsock.sock", VsockCID: 3,
		Disk: "/s/disk.raw", Firmware: "/fw/CLOUDHV.fd", CPUs: 2, MaxCPUs: 8,
		Memory: vmspec.Memory{Boot: 2 * config.GiB, Hotplug: 6 * config.GiB, Plugged: 2 * config.GiB},
	}
}

// argValue returns the value following flag, failing if it appears != once.
func argValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) || slices.Index(args[i+1:], flag) >= 0 {
		t.Fatalf("%s must appear exactly once with a value in %q", flag, args)
	}
	return args[i+1]
}

func TestArgsFirmwareBoot(t *testing.T) {
	args, err := Args(baseSpec())
	if err != nil {
		t.Fatal(err)
	}
	if got := argValue(t, args, "--memory"); got != "size=2147483648,hotplug_method=virtio-mem,hotplug_size=6442450944,hotplugged_size=2147483648,shared=on" {
		t.Errorf("--memory = %q", got)
	}
	if got := argValue(t, args, "--cpus"); got != "boot=2,max=8" {
		t.Errorf("--cpus = %q", got)
	}
	if got := argValue(t, args, "--firmware"); got != "/fw/CLOUDHV.fd" {
		t.Errorf("--firmware = %q", got)
	}
	if slices.Contains(args, "--kernel") || slices.Contains(args, "--net") || slices.Contains(args, "--fs") {
		t.Errorf("unexpected optional devices in %q", args)
	}
}

func TestArgsKernelBootWithNetAndShares(t *testing.T) {
	s := baseSpec()
	s.Firmware = ""
	s.Kernel, s.Initramfs, s.Cmdline = "/k/vmlinux", "/k/initrd", "console=ttyS0 init=/x"
	s.Memory = vmspec.Memory{Boot: 4 * config.GiB}
	s.TapFD, s.GuestMAC = 3, "52:54:00:00:00:02"
	s.Shares = []Share{{ID: "fs-meta", Tag: "meta", Socket: "/r/a.sock"}, {ID: "fs-b", Tag: "b", Socket: "/r/b.sock"}}
	args, err := Args(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := argValue(t, args, "--cmdline"); got != "console=ttyS0 init=/x" {
		t.Errorf("--cmdline = %q", got)
	}
	// Without a hotplug region, cloud-hypervisor must not get virtio-mem.
	if got := argValue(t, args, "--memory"); got != "size=4294967296,shared=on" {
		t.Errorf("--memory = %q", got)
	}
	if got := argValue(t, args, "--net"); got != "fd=3,mac=52:54:00:00:00:02" {
		t.Errorf("--net = %q", got)
	}
	if n := strings.Count(strings.Join(args, " "), "--fs "); n != 2 {
		t.Errorf("want 2 --fs flags, got %d", n)
	}
}

func TestArgsNeedsExactlyOneBootMethod(t *testing.T) {
	s := baseSpec()
	s.Kernel = "/k"
	if _, err := Args(s); err == nil {
		t.Error("firmware and kernel together accepted")
	}
	s.Firmware, s.Kernel = "", ""
	if _, err := Args(s); err == nil {
		t.Error("no boot method accepted")
	}
}

// fakeCH serves the API on a Unix socket and records requests.
func fakeCH(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "ch.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return NewClient(sock)
}

func TestClientResizeSendsOnlyGivenValues(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	c := fakeCH(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Resize(context.Background(), 0, 4<<30); err != nil {
		t.Fatal(err)
	}
	if gotPath != "PUT /api/v1/vm.resize" {
		t.Errorf("request = %q", gotPath)
	}
	if _, ok := gotBody["desired_vcpus"]; ok || gotBody["desired_ram"] != float64(4<<30) {
		t.Errorf("body = %v", gotBody)
	}
}

func TestClientReportsErrorBody(t *testing.T) {
	c := fakeCH(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Error resizing VM: too big", http.StatusInternalServerError)
	})
	err := c.Resize(context.Background(), 99, 0)
	if err == nil || !strings.Contains(err.Error(), "too big") {
		t.Fatalf("error = %v, want CH's message", err)
	}
}

func TestClientInfoDecodes(t *testing.T) {
	c := fakeCH(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"state":"Running","memory_actual_size":4294967296,"config":{"cpus":{"boot_vcpus":2,"max_vcpus":8},"fs":[{"tag":"t3jail-meta","id":"fs-meta"}]}}`)
	})
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.State != "Running" || info.MemoryActualSize != 4<<30 || info.Config.CPUs.BootVCPUs != 2 || info.Config.Fs[0].Tag != "t3jail-meta" {
		t.Errorf("info = %+v", info)
	}
}
