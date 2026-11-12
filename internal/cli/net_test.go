package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/paths"
)

func testInstance(t *testing.T) paths.Instance {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
	inst, err := instance()
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

// fakeDaemon answers on the control socket like a running VM and counts
// network reloads.
func fakeDaemon(t *testing.T, inst paths.Instance) *atomic.Int32 {
	t.Helper()
	if err := inst.Ensure(); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", inst.ControlSocket())
	if err != nil {
		t.Fatal(err)
	}
	var reloads atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("GET /v1/status", control.Handler(func(context.Context, control.Empty) (control.Status, error) {
		return control.Status{State: control.StateRunning}, nil
	}))
	mux.Handle("POST /v1/net/reload", control.Handler(func(context.Context, control.Empty) (control.Empty, error) {
		reloads.Add(1)
		return control.Empty{}, nil
	}))
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return &reloads
}

func runCLI(args ...string) error {
	root := NewRoot()
	root.SetArgs(args)
	return root.Execute()
}

func readFile(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestNetAllowNeedsPersistentWhenStopped(t *testing.T) {
	inst := testInstance(t)
	if err := runCLI("net", "allow", "example.com"); err == nil || !strings.Contains(err.Error(), "--persistent") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(inst.EphemeralNetworkList()); err == nil {
		t.Error("ephemeral list written for a stopped VM")
	}
}

func TestNetAllowPersistentEditsTheList(t *testing.T) {
	inst := testInstance(t)
	if err := runCLI("net", "allow", "-p", "Example.com:22", "*.npmjs.org"); err != nil {
		t.Fatal(err)
	}
	list := readFile(inst.NetworkList())
	// The default list is created first, comments and all.
	for _, want := range []string{"# ", "cache.nixos.org\n", "example.com:22\n", "*.npmjs.org\n"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}

	// One bad entry rejects the whole command.
	if err := runCLI("net", "allow", "-p", "ok.example", "bad name"); err == nil || readFile(inst.NetworkList()) != list {
		t.Errorf("err = %v, list changed", err)
	}
	// Removing something absent fails without touching the file.
	if err := runCLI("net", "remove", "example.com:22", "absent.example"); err == nil || readFile(inst.NetworkList()) != list {
		t.Errorf("err = %v, list changed", err)
	}
	if err := runCLI("net", "remove", "example.com:22"); err != nil || strings.Contains(readFile(inst.NetworkList()), "example.com:22") {
		t.Errorf("remove: err %v, list:\n%s", err, readFile(inst.NetworkList()))
	}
}

func TestNetAllowIsEphemeralByDefault(t *testing.T) {
	inst := testInstance(t)
	reloads := fakeDaemon(t, inst)

	if err := runCLI("net", "allow", "example.com", "cache.nixos.org"); err != nil {
		t.Fatal(err)
	}
	ephemeral := readFile(inst.EphemeralNetworkList())
	if ephemeral != "example.com\n" {
		t.Errorf("ephemeral list = %q (entries already in network.list must not be repeated)", ephemeral)
	}
	if strings.Contains(readFile(inst.NetworkList()), "example.com") {
		t.Error("ephemeral entry saved permanently")
	}
	if reloads.Load() != 1 {
		t.Errorf("reloads = %d, want 1", reloads.Load())
	}

	// Remove finds entries in either list.
	if err := runCLI("net", "remove", "example.com", "cache.nixos.org"); err != nil {
		t.Fatal(err)
	}
	if readFile(inst.EphemeralNetworkList()) != "" || strings.Contains(readFile(inst.NetworkList()), "cache.nixos.org") {
		t.Errorf("not removed:\n%s\n%s", readFile(inst.EphemeralNetworkList()), readFile(inst.NetworkList()))
	}
	if reloads.Load() != 2 {
		t.Errorf("reloads = %d, want 2", reloads.Load())
	}
}
