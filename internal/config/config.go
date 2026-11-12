// Package config loads the user-editable config.toml of a t3jail instance.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Defaults for `t3jail start`; flags override them per run.
	CPUs   int      `toml:"cpus"`
	Memory ByteSize `toml:"memory"`
	// Upper bounds for live resizing. Zero means the host's CPU count / RAM.
	MaxCPUs   int      `toml:"max_cpus"`
	MaxMemory ByteSize `toml:"max_memory"`
	// Size of the VM disk created by `t3jail init`.
	DiskSize ByteSize `toml:"disk_size"`

	T3  T3Config  `toml:"t3"`
	GUI GUIConfig `toml:"gui"`
}

type T3Config struct {
	// Port on 127.0.0.1 forwarded to the VM's T3 server.
	HostPort uint16 `toml:"host_port"`
}

type GUIConfig struct {
	// Command that starts the T3 desktop app. Empty means the bundled one.
	Command []string `toml:"command"`
}

func Defaults() Config {
	return Config{
		CPUs:     4,
		Memory:   8 * GiB,
		DiskSize: 64 * GiB,
		T3:       T3Config{HostPort: 37730},
	}
}

// Load reads path on top of the defaults. A missing file yields the defaults;
// unknown keys are an error so typos do not go unnoticed.
func Load(path string) (Config, error) {
	cfg := Defaults()
	md, err := toml.DecodeFile(path, &cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	switch {
	case c.CPUs < 1:
		return errors.New("cpus must be at least 1")
	case c.MaxCPUs < 0:
		return errors.New("max_cpus must not be negative")
	case c.T3.HostPort == 0:
		return errors.New("t3.host_port must be set")
	}
	return nil
}

// WriteDefault creates a commented config file at path unless one exists.
func WriteDefault(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	d := Defaults()
	_, err = fmt.Fprintf(f, `# t3jail configuration. Flags given to commands override these values.

# vCPUs and RAM given to the VM by `+"`t3jail start`"+`.
cpus = %d
memory = %q

# Upper bounds for `+"`t3jail resize`"+` while the VM runs. 0 means the host's
# CPU count / total RAM. Changes take effect on the next start.
max_cpus = 0
max_memory = "0"

# Size of the VM disk created by `+"`t3jail init`"+`.
disk_size = %q

[t3]
# The T3 server is reachable at http://127.0.0.1:<host_port>.
host_port = %d

[gui]
# Command for `+"`t3jail gui`"+`; empty uses the bundled T3 desktop app.
command = []
`, d.CPUs, d.Memory, d.DiskSize, d.T3.HostPort)
	return err
}
