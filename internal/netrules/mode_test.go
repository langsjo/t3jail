package netrules

import (
	"os"
	"path/filepath"
	"testing"
)

func testModeFiles(t *testing.T) ModeFiles {
	dir := t.TempDir()
	return ModeFiles{
		Saved:     filepath.Join(dir, "network.mode"),
		Override:  filepath.Join(dir, "run-network.mode"),
		LegacyOff: filepath.Join(dir, "network.off"),
	}
}

func TestModeSwitching(t *testing.T) {
	f := testModeFiles(t)
	want := func(step string, mode, saved Mode) {
		t.Helper()
		got, err := f.Load()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got != (ModeState{Mode: mode, Saved: saved}) {
			t.Errorf("%s: %+v, want mode %s saved %s", step, got, mode, saved)
		}
	}
	set := func(m Mode, persistent bool) {
		t.Helper()
		if err := f.Set(m, persistent); err != nil {
			t.Fatal(err)
		}
	}

	want("fresh", Allowlist, Allowlist)
	set(Open, false)
	want("open until the VM stops", Open, Allowlist)
	set(Blocklist, false)
	want("blocklist until the VM stops", Blocklist, Allowlist)
	set(Allowlist, false)
	want("back to the saved mode", Allowlist, Allowlist)
	if _, err := os.Stat(f.Override); !os.IsNotExist(err) {
		t.Error("override kept although it matches the saved mode")
	}

	set(Open, false)
	set(Blocklist, true)
	want("persistent change ends the override", Blocklist, Blocklist)
	set(Allowlist, false)
	want("allowlist until the VM stops despite the saved mode", Allowlist, Blocklist)

	without := f
	without.Override = ""
	if got, err := without.Load(); err != nil || got != (ModeState{Mode: Blocklist, Saved: Blocklist}) {
		t.Errorf("without override: %+v, %v; want the saved blocklist", got, err)
	}
}

func TestLegacyOffMarkerReadsAsOpenUntilAModeIsSaved(t *testing.T) {
	f := testModeFiles(t)
	os.WriteFile(f.LegacyOff, nil, 0o600)
	if got, _ := f.Load(); got.Mode != Open || got.Saved != Open {
		t.Errorf("legacy marker: %+v, want open", got)
	}
	if err := f.Set(Allowlist, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Load(); got.Mode != Allowlist {
		t.Errorf("after saving allowlist: %+v", got)
	}
	if _, err := os.Stat(f.LegacyOff); !os.IsNotExist(err) {
		t.Error("legacy marker kept")
	}
}

func TestBadModeFileIsAnError(t *testing.T) {
	f := testModeFiles(t)
	os.WriteFile(f.Saved, []byte("off\n"), 0o600)
	if _, err := f.Load(); err == nil {
		t.Error("no error for an unknown mode")
	}
}
