package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestListenAddrs(t *testing.T) {
	t3 := T3Config{HostPort: 37730, Listen: []string{"127.0.0.1", "100.64.1.2", "100.64.1.2:8080", "::1", "[fd7a::1]:9000", "::ffff:10.0.0.1"}}
	got, err := t3.ListenAddrs()
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.AddrPort{
		netip.MustParseAddrPort("127.0.0.1:37730"),
		netip.MustParseAddrPort("100.64.1.2:37730"),
		netip.MustParseAddrPort("100.64.1.2:8080"),
		netip.MustParseAddrPort("[::1]:37730"),
		netip.MustParseAddrPort("[fd7a::1]:9000"),
		netip.MustParseAddrPort("10.0.0.1:37730"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListenAddrs = %v, want %v", got, want)
	}
	for _, bad := range [][]string{
		nil,
		{"0.0.0.0"},
		{"[::]:80"},
		{"localhost"},
		{"10.0.0.1:0"},
		{"127.0.0.1", "127.0.0.1:37730"},
		{"fd7a::1:9000x"},
	} {
		if got, err := (T3Config{HostPort: 37730, Listen: bad}).ListenAddrs(); err == nil {
			t.Errorf("ListenAddrs(%q) = %v, want an error", bad, got)
		}
	}
}

func TestLoadListenReplacesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[t3]\nlisten = [\"100.64.1.2\"]\n"), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.T3.Listen, []string{"100.64.1.2"}) {
		t.Errorf("Listen = %q", cfg.T3.Listen)
	}
	os.WriteFile(path, []byte("[t3]\nlisten = [\"0.0.0.0\"]\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("a wildcard listen address loaded")
	}
}

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
	if cfg.CPUs != want.CPUs || cfg.Memory != want.Memory || cfg.DiskSize != want.DiskSize ||
		cfg.T3.HostPort != want.T3.HostPort || !slices.Equal(cfg.T3.Listen, want.T3.Listen) {
		t.Fatalf("default file loads as %+v, want %+v", cfg, want)
	}
}

func TestWriteDefaultKeepsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("cpu = 2\n"), 0o600)
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.CPUs != 2 {
		t.Fatalf("existing file was replaced: %+v %v", cfg, err)
	}
}

func TestLoadIgnoresRemovedGUISection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("cpu = 2\n\n[gui]\ncommand = [\"/opt/t3code\"]\n"), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CPUs != 2 {
		t.Errorf("CPUs = %d, want 2", cfg.CPUs)
	}
}

func TestLoadRejectsUnknownKeysAndBadValues(t *testing.T) {
	for name, content := range map[string]string{
		"typo":              "cpus = 2\n",
		"bad size":          "mem = \"lots\"\n",
		"zero cpus":         "cpu = 0\n",
		"negative reminder": "[net]\nrequest_reminder_secs = -1\n",
		"tiny reminder":     "[net]\nrequest_reminder_secs = 1\n",
		"huge reminder":     "[net]\nrequest_reminder_secs = 10000000000\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, []byte(content), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: Load succeeded, want error", name)
		} else if name == "typo" && !strings.Contains(err.Error(), "cpus") {
			t.Errorf("unknown-key error does not name the key: %v", err)
		}
	}
}
