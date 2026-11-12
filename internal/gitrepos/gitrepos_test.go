package gitrepos

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuestPath(t *testing.T) {
	ok := map[string]string{
		"t3jail":     ".t3/projects/t3jail",
		"my.project": ".t3/projects/my.project",
		"/etc/nixos": "/etc/nixos",
		"/":          "/",
	}
	for in, want := range ok {
		got, err := GuestPath(in)
		if err != nil || got != want {
			t.Errorf("GuestPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "..", "a/b", "-x", "/etc/../root", "/etc/", "a\nb", "a\x1bb"} {
		if got, err := GuestPath(in); err == nil {
			t.Errorf("GuestPath(%q) = %q, want an error", in, got)
		}
	}
}

func TestDisplay(t *testing.T) {
	if got := Display(".t3/projects/x"); got != "~/.t3/projects/x" {
		t.Errorf("Display = %q", got)
	}
	if got := Display("/etc/nixos"); got != "/etc/nixos" {
		t.Errorf("Display = %q", got)
	}
}

func TestBindingsRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "git-repos.toml")
	b, err := Load(file)
	if err != nil || len(b.Repos) != 0 {
		t.Fatalf("Load(missing) = %v, %v", b, err)
	}
	if !b.Set("/home/u/src/a", "a") || !b.Set("/home/u/src/b", "/etc/nixos") {
		t.Fatal("Set of new hosts reported no change")
	}
	if b.Set("/home/u/src/a", "a") {
		t.Error("Set of the same binding reported a change")
	}
	if !b.Set("/home/u/src/a", "other") {
		t.Error("rebinding reported no change")
	}
	if err := b.Save(file); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if repo, ok := loaded.Lookup("/home/u/src/a"); !ok || repo != "other" {
		t.Errorf("Lookup(a) = %q, %v", repo, ok)
	}
	if repo, ok := loaded.Lookup("/home/u/src/b"); !ok || repo != "/etc/nixos" {
		t.Errorf("Lookup(b) = %q, %v", repo, ok)
	}
	if _, ok := loaded.Lookup("/elsewhere"); ok {
		t.Error("Lookup of an unbound host succeeded")
	}
}

func TestLoadRejectsBadEntries(t *testing.T) {
	for name, content := range map[string]string{
		"relative host": "[[repo]]\nhost_path = \"src/a\"\nrepo = \"a\"\n",
		"bad repo":      "[[repo]]\nhost_path = \"/src/a\"\nrepo = \"a/b\"\n",
		"unknown key":   "[[repo]]\nhost_path = \"/src/a\"\nrepo = \"a\"\nbranch = \"x\"\n",
	} {
		file := filepath.Join(t.TempDir(), "git-repos.toml")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(file); err == nil {
			t.Errorf("%s: Load succeeded", name)
		}
	}
}
