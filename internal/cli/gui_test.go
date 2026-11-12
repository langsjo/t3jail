package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeNix stands in for `nix build --out-link root`: it points root at a new
// fake store path holding bin/t3code-desktop.
type fakeNix struct {
	t       *testing.T
	store   string
	version int
	calls   int
}

func (n *fakeNix) fetch(root string) (string, error) {
	n.calls++
	out := filepath.Join(n.store, "t3code-"+string(rune('0'+n.version)))
	os.MkdirAll(filepath.Join(out, "bin"), 0o755)
	os.WriteFile(filepath.Join(out, "bin", desktopBinary), nil, 0o755)
	os.Remove(root)
	if err := os.Symlink(out, root); err != nil {
		n.t.Fatal(err)
	}
	return out, nil
}

func answers(t *testing.T, replies ...string) func(string) (string, error) {
	return func(q string) (string, error) {
		if len(replies) == 0 {
			t.Fatalf("unexpected question: %s", q)
		}
		r := replies[0]
		replies = replies[1:]
		return r, nil
	}
}

func TestDesktopCommandPrefersConfigThenPath(t *testing.T) {
	inst := testInstance(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	nix := &fakeNix{t: t, store: t.TempDir()}

	got, err := desktopCommand(inst, []string{"/opt/t3", "--flag"}, answers(t), nix.fetch)
	if err != nil || !slices.Equal(got, []string{"/opt/t3", "--flag"}) {
		t.Fatalf("configured: %v %v", got, err)
	}

	os.WriteFile(filepath.Join(bin, desktopBinary), []byte("#!/bin/sh\n"), 0o755)
	got, err = desktopCommand(inst, nil, answers(t), nix.fetch)
	if err != nil || !slices.Equal(got, []string{filepath.Join(bin, desktopBinary)}) {
		t.Fatalf("PATH: %v %v", got, err)
	}
	if nix.calls != 0 {
		t.Error("fetched although a command was available")
	}
}

func TestDesktopCommandFetchesOnlyWhenAccepted(t *testing.T) {
	inst := testInstance(t)
	t.Setenv("PATH", t.TempDir())
	nix := &fakeNix{t: t, store: t.TempDir(), version: 1}
	root := filepath.Join(inst.GCRoots(), "t3code")

	if _, err := desktopCommand(inst, nil, answers(t, "\n"), nix.fetch); err == nil || nix.calls != 0 {
		t.Fatalf("declined: err %v, fetches %d", err, nix.calls)
	}

	got, err := desktopCommand(inst, nil, answers(t, "y\n"), nix.fetch)
	if err != nil || !slices.Equal(got, []string{filepath.Join(root, "bin", desktopBinary)}) {
		t.Fatalf("accepted: %v %v", got, err)
	}
	first, _ := os.Readlink(root)

	// Next time: the default answer reuses the root without fetching.
	if _, err := desktopCommand(inst, nil, answers(t, "\n"), nix.fetch); err != nil || nix.calls != 1 {
		t.Fatalf("reuse: err %v, fetches %d", err, nix.calls)
	}

	// Fetching a newer version moves the root to it.
	nix.version = 2
	if _, err := desktopCommand(inst, nil, answers(t, "f\n"), nix.fetch); err != nil || nix.calls != 2 {
		t.Fatalf("update: err %v, fetches %d", err, nix.calls)
	}
	if now, _ := os.Readlink(root); now == first {
		t.Errorf("root still points at %s", first)
	}
}

func TestGUIProfileForcesLocalEnvironmentOff(t *testing.T) {
	p := guiProfile{dir: t.TempDir()}
	settingsPath := filepath.Join(p.stateDir(), "desktop-settings.json")
	read := func() map[string]any {
		t.Helper()
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if err := p.prepare(); err != nil {
		t.Fatal(err)
	}
	if read()["localEnvironmentEnabled"] != false {
		t.Fatal("fresh profile has the local environment on")
	}

	// The user turned it back on in the app; other settings must survive.
	os.WriteFile(settingsPath, []byte(`{"localEnvironmentEnabled": true, "mainWindowMaximized": true}`), 0o600)
	if err := p.prepare(); err != nil {
		t.Fatal(err)
	}
	if s := read(); s["localEnvironmentEnabled"] != false || s["mainWindowMaximized"] != true {
		t.Errorf("settings after prepare: %v", s)
	}

	os.WriteFile(settingsPath, []byte(`{not json`), 0o600)
	if err := p.prepare(); err == nil {
		t.Error("launching with unreadable settings, which could have the local environment on")
	}
}

func TestGUIProfilePaired(t *testing.T) {
	p := guiProfile{dir: t.TempDir()}
	p.prepare()
	registry := filepath.Join(p.stateDir(), "saved-environments.json")
	if p.paired() {
		t.Error("paired without a registry")
	}
	os.WriteFile(registry, []byte(`{"version":1,"records":[]}`), 0o600)
	if p.paired() {
		t.Error("paired with no records")
	}
	os.WriteFile(registry, []byte(`{"version":1,"records":[{"label":"t3jail"}]}`), 0o600)
	if !p.paired() {
		t.Error("not paired with a record")
	}
}

func TestGUIProfileEnvOverridesExisting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/home/me/.config")
	p := guiProfile{dir: "/state/gui-profile"}
	env := p.env()
	var found []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_CONFIG_HOME=") || strings.HasPrefix(kv, "T3CODE_HOME=") {
			found = append(found, kv)
		}
	}
	want := []string{"XDG_CONFIG_HOME=/state/gui-profile/config", "T3CODE_HOME=/state/gui-profile/t3"}
	if !slices.Equal(found, want) {
		t.Errorf("got %v, want %v", found, want)
	}
}

func TestParsePairingOutputOnlyAcceptsOurPort(t *testing.T) {
	base := "http://127.0.0.1:37730"
	ok := `{"id":"x","credential":"abc","pairUrl":"http://127.0.0.1:37730/pair#token=abc"}`
	if got, err := parsePairingOutput([]byte(ok), base); err != nil || got != "http://127.0.0.1:37730/pair#token=abc" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{
		`{"pairUrl":"https://evil.example/pair#token=abc"}`,
		`{"pairUrl":"http://127.0.0.1:37730.evil.example/pair#token=abc"}`,
		"{\"pairUrl\":\"http://127.0.0.1:37730/pair#token=\\u001b[31m\"}",
		`not json`,
	} {
		if _, err := parsePairingOutput([]byte(bad), base); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
