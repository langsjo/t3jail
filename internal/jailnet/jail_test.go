package jailnet

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReadListMergesEphemeralEntries(t *testing.T) {
	dir := t.TempDir()
	j := &Jail{listPath: filepath.Join(dir, "network.list"), ephemeralPath: filepath.Join(dir, "ephemeral.list")}
	os.WriteFile(j.listPath, []byte("# comment\ngithub.com\n"), 0o600)

	entries := func() []string {
		t.Helper()
		l, err := j.readList()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range l.Entries() {
			out = append(out, e.String())
		}
		return out
	}
	// A missing ephemeral list is normal: nothing was allowed this run.
	if got := entries(); !slices.Equal(got, []string{"github.com"}) {
		t.Errorf("got %v", got)
	}
	os.WriteFile(j.ephemeralPath, []byte("example.com:22\ngithub.com\n"), 0o600)
	if got := entries(); !slices.Equal(got, []string{"github.com", "example.com:22"}) {
		t.Errorf("got %v", got)
	}
	os.WriteFile(j.ephemeralPath, []byte("bad name\n"), 0o600)
	if _, err := j.readList(); err == nil {
		t.Error("broken ephemeral list accepted")
	}
}
