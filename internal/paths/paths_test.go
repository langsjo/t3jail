package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesXDG(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "/c")
	t.Setenv("XDG_STATE_HOME", "/s")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	inst, err := Resolve("default")
	if err != nil {
		t.Fatal(err)
	}
	want := Instance{Name: "default", Config: "/c/t3jail/default", State: "/s/t3jail/default", Runtime: "/run/user/1000/t3jail/default"}
	if inst != want {
		t.Fatalf("got %+v, want %+v", inst, want)
	}
}

func TestResolveFallsBackForUnsetOrRelative(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "relative/path") // ignored per XDG spec
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	inst, err := Resolve("default")
	if err != nil {
		t.Fatal(err)
	}
	if inst.Config != "/home/u/.config/t3jail/default" || inst.State != "/home/u/.local/state/t3jail/default" {
		t.Fatalf("unexpected fallback: %+v", inst)
	}
}

func TestResolveRequiresRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := Resolve("default"); err == nil {
		t.Fatal("expected error without XDG_RUNTIME_DIR")
	}
}

func TestEnsureMakesPrivateDirs(t *testing.T) {
	root := t.TempDir()
	inst := Instance{Name: "x", Config: filepath.Join(root, "c"), State: filepath.Join(root, "s"), Runtime: filepath.Join(root, "r")}
	// A pre-existing, too-open directory must be tightened.
	if err := os.MkdirAll(inst.State, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := inst.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{inst.Config, inst.State, inst.Runtime, inst.SSHDir(), inst.MetaDir(), inst.Logs()} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s has mode %v, want 0700", dir, fi.Mode().Perm())
		}
	}
}
