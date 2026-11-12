package cli

import (
	"os/exec"
	"slices"
	"testing"

	"github.com/langsjo/t3jail/internal/mounts"
)

func TestShellQuoteSurvivesTheRemoteShell(t *testing.T) {
	for _, s := range []string{"/work/my dir", "it's", `$(rm -rf ~)`, "a\"b`c", "", "new\nline"} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("%q came back as %q (%v)", s, out, err)
		}
	}
}

func TestMountRows(t *testing.T) {
	active := []mounts.Mount{{HostPath: "/h/a", GuestPath: "/a"}, {HostPath: "/h/b", GuestPath: "/b", ReadOnly: true}}
	persisted := []mounts.Mount{{HostPath: "/h/b", GuestPath: "/b", ReadOnly: true}, {HostPath: "/h/c", GuestPath: "/c"}}
	want := []string{
		"/h/a -> /a\trw\tmounted",
		"/h/b -> /b\tro\tmounted, persisted",
		"/h/c -> /c\trw\tat next start",
	}
	if got := mountRows(active, persisted); !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestMountAddNeedsPersistWhenStopped(t *testing.T) {
	inst := testInstance(t)
	dir := t.TempDir()
	if err := runCLI("mount", "add", dir+":/work/x"); err == nil {
		t.Fatal("non-persistent mount accepted for a stopped VM")
	}
	if err := runCLI("mount", "add", "--persist", "--ro", dir+":/work/x"); err != nil {
		t.Fatal(err)
	}
	p, err := mounts.Load(inst.MountsFile())
	if err != nil || len(p.Mounts) != 1 || p.Mounts[0] != (mounts.Mount{HostPath: dir, GuestPath: "/work/x", ReadOnly: true}) {
		t.Fatalf("persisted: %+v, %v", p, err)
	}
	if err := runCLI("mount", "add", "--persist", dir+":/work/x"); err == nil {
		t.Error("same guest path persisted twice")
	}
	if err := runCLI("mount", "remove", "/work/x/"); err != nil {
		t.Fatal(err)
	}
	if p, _ := mounts.Load(inst.MountsFile()); len(p.Mounts) != 0 {
		t.Errorf("still persisted: %+v", p)
	}
}
