package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	cases := map[string]ByteSize{
		"8G": 8 * GiB, "8g": 8 * GiB, "8GiB": 8 * GiB, "8GB": 8 * GiB,
		"512M": 512 * MiB, "1T": TiB, "4096": 4096, " 2K ": 2 * KiB, "0": 0,
	}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "G", "1.5G", "-1G", "8X", "99999999999T"} {
		if _, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) succeeded, want error", bad)
		}
	}
}

func TestByteSizeString(t *testing.T) {
	cases := map[ByteSize]string{8 * GiB: "8G", 1536 * MiB: "1536M", 1000: "1000", 0: "0"}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", uint64(in), got, want)
		}
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CPUs != Defaults().CPUs || cfg.Memory != Defaults().Memory {
		t.Fatalf("got %+v", cfg)
	}
}

func TestDefaultFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	if cfg.CPUs != want.CPUs || cfg.Memory != want.Memory || cfg.DiskSize != want.DiskSize || cfg.T3 != want.T3 {
		t.Fatalf("default file loads as %+v, want %+v", cfg, want)
	}
}

func TestWriteDefaultKeepsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("cpus = 2\n"), 0o600)
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.CPUs != 2 {
		t.Fatalf("existing file was replaced: %+v %v", cfg, err)
	}
}

func TestLoadRejectsUnknownKeysAndBadValues(t *testing.T) {
	for name, content := range map[string]string{
		"typo":      "cpu = 2\n",
		"bad size":  "memory = \"lots\"\n",
		"zero cpus": "cpus = 0\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, []byte(content), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: Load succeeded, want error", name)
		} else if name == "typo" && !strings.Contains(err.Error(), "cpu") {
			t.Errorf("unknown-key error does not name the key: %v", err)
		}
	}
}
