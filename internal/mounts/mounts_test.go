package mounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		arg  string
		ro   bool
		want Mount
	}{
		{"/home/me/proj", false, Mount{"/home/me/proj", "/home/me/proj", false}},
		{"proj/", true, Mount{"/cwd/proj", "/cwd/proj", true}},
		{"/data:/work/data/", false, Mount{"/data", "/work/data", false}},
		{"../x:/srv/x", false, Mount{"/x", "/srv/x", false}},
	}
	for _, c := range cases {
		got, err := Parse(c.arg, "/cwd", c.ro)
		if err != nil || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", c.arg, got, err, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, arg := range []string{
		"", ":/x", "/x:", "/a:b:c", "/x:relative",
		"/x:/", "/x:/home", "/x:/home/agent", "/x:/nix/store", "/x:/etc", "/x:/run/t3jail/host",
		"/x:/home/agent/.t3", "/x:/home/agent/.ssh/keys",
		"/etc", // the guest path defaults to the host path
	} {
		if m, err := Parse(arg, "/cwd", false); err == nil {
			t.Errorf("Parse(%q) = %+v, want error", arg, m)
		}
	}
	for _, ok := range []string{"/x:/home/agent/project", "/x:/tmp/scratch", "/x:/etcetera", "/x:/home/other"} {
		if _, err := Parse(ok, "/cwd", false); err != nil {
			t.Errorf("Parse(%q): %v", ok, err)
		}
	}
}

func TestTagIsStableAndFitsVirtiofs(t *testing.T) {
	a := Mount{GuestPath: "/work/" + strings.Repeat("x", 200)}
	b := Mount{GuestPath: "/work/y"}
	if a.Tag() != a.Tag() || a.Tag() == b.Tag() {
		t.Error("tags must be stable and distinct per guest path")
	}
	if len(a.Tag()) > 36 {
		t.Errorf("tag %q longer than 36 bytes", a.Tag())
	}
}

func TestCheckHostDir(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "real"), 0o700)
	os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link"))
	os.WriteFile(filepath.Join(dir, "file"), nil, 0o600)
	if got, err := CheckHostDir(filepath.Join(dir, "link")); err != nil || got != filepath.Join(dir, "real") {
		t.Errorf("symlink: %q, %v", got, err)
	}
	for _, bad := range []string{filepath.Join(dir, "file"), filepath.Join(dir, "missing")} {
		if _, err := CheckHostDir(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPersistedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts.toml")
	p, err := Load(path)
	if err != nil || len(p.Mounts) != 0 {
		t.Fatalf("missing file: %+v, %v", p, err)
	}
	p.Mounts = []Mount{{"/a", "/work/a", true}, {"/b", "/work/b", false}}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Mounts) != 2 || got.Mounts[0] != p.Mounts[0] || got.Find("/work/b") != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	// A hand-edited file must not smuggle in a reserved path.
	os.WriteFile(path, []byte("[[mount]]\nhost_path = \"/a\"\nguest_path = \"/etc\"\nread_only = false\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("reserved guest path accepted from mounts.toml")
	}
}
