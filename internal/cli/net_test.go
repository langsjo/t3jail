package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestNetAllowAndRemoveEditTheList(t *testing.T) {
	inst := testInstance(t)
	run := func(args ...string) (string, error) {
		root := NewRoot()
		root.SetArgs(args)
		// Output goes to os.Stdout; the list file is what matters here.
		err := root.Execute()
		data, _ := os.ReadFile(inst.NetworkList())
		return string(data), err
	}

	list, err := run("net", "allow", "Example.com:22", "*.npmjs.org")
	if err != nil {
		t.Fatal(err)
	}
	// The default list is created first, comments and all.
	for _, want := range []string{"# ", "cache.nixos.org\n", "example.com:22\n", "*.npmjs.org\n"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}

	// One bad entry rejects the whole command.
	if after, err := run("net", "allow", "ok.example", "bad name"); err == nil || after != list {
		t.Errorf("err = %v, list changed: %v", err, after != list)
	}
	// Removing something absent fails without touching the file.
	if after, err := run("net", "remove", "example.com:22", "absent.example"); err == nil || after != list {
		t.Errorf("err = %v, list changed: %v", err, after != list)
	}

	list, err = run("net", "remove", "example.com:22")
	if err != nil || strings.Contains(list, "example.com:22") {
		t.Errorf("remove: err %v, list:\n%s", err, list)
	}
}
