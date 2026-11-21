package gitrepos

import "testing"

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
