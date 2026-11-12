package netrules

import (
	"path/filepath"
	"testing"
)

func TestFilterState(t *testing.T) {
	dir := t.TempDir()
	marker, override := filepath.Join(dir, "network.off"), filepath.Join(dir, "filter")
	want := func(step string, off, temporary bool) {
		t.Helper()
		got, err := LoadFilterState(marker, override)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got != (FilterState{Off: off, Temporary: temporary}) {
			t.Errorf("%s: %+v, want off=%v temporary=%v", step, got, off, temporary)
		}
	}
	set := func(off, persistent bool) {
		t.Helper()
		if err := SetFilter(marker, override, off, persistent); err != nil {
			t.Fatal(err)
		}
	}

	want("fresh", false, false)
	set(true, false)
	want("disabled until the VM stops", true, true)
	set(false, false)
	want("enabled again, as saved", false, false)

	set(true, true)
	want("disabled persistently, which ends the override", true, false)
	set(false, false)
	want("enabled until the VM stops despite the persistent setting", false, true)
	set(false, true)
	want("enabled persistently", false, false)
}

func TestFilterStateWithoutOverrideUsesOnlyTheMarker(t *testing.T) {
	dir := t.TempDir()
	marker, override := filepath.Join(dir, "network.off"), filepath.Join(dir, "filter")
	SetFilter(marker, override, true, true)
	SetFilter(marker, override, false, false)
	if got, err := LoadFilterState(marker, ""); err != nil || got != (FilterState{Off: true}) {
		t.Errorf("got %+v, %v; want the persistent off", got, err)
	}
}
