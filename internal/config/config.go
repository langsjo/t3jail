// Package config loads the user-editable config.toml of a t3jail instance.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Defaults for `t3jail start`; flags override them per run.
	CPUs   int      `toml:"cpu"`
	Memory ByteSize `toml:"mem"`
	// Upper bounds for live resizing. Zero means the host's CPU count / RAM.
	MaxCPUs   int      `toml:"max_cpu"`
	MaxMemory ByteSize `toml:"max_mem"`
	// Size of the VM disk created by `t3jail init`.
	DiskSize ByteSize `toml:"disk_size"`
	// Let the guest run its own VMs through nested KVM.
	NestedVirt bool `toml:"nested_virt"`
	// Let the guest hand free RAM back to the host.
	FreePageReporting bool `toml:"free_page_reporting"`

	T3  T3Config  `toml:"t3"`
	Net NetConfig `toml:"net"`
}

type NetConfig struct {
	// Seconds between desktop notifications while the VM has unanswered
	// network requests; 0 notifies only when a request arrives.
	RequestReminderSecs int `toml:"request_reminder_secs"`
}

type T3Config struct {
	// Host port forwarded to the VM's T3 server.
	HostPort uint16 `toml:"host_port"`
	// Host addresses the port listens on: "ip" (on HostPort) or "ip:port".
	// The first one is used in the URL that status and pairing links show.
	Listen []string `toml:"listen"`
}

// ListenAddrs resolves Listen against HostPort. Wildcard addresses are
// refused: they would expose T3 to every network the host joins.
func (t T3Config) ListenAddrs() ([]netip.AddrPort, error) {
	if len(t.Listen) == 0 {
		return nil, errors.New("t3.listen must list at least one address")
	}
	addrs := make([]netip.AddrPort, 0, len(t.Listen))
	for _, s := range t.Listen {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			addr, addrErr := netip.ParseAddr(s)
			if addrErr != nil {
				return nil, fmt.Errorf("t3.listen: %q is not an IP address or ip:port (IPv6 with a port is [addr]:port)", s)
			}
			ap = netip.AddrPortFrom(addr, t.HostPort)
		}
		ap = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		switch {
		case ap.Port() == 0:
			return nil, fmt.Errorf("t3.listen: %q has port 0", s)
		case ap.Addr().IsUnspecified():
			return nil, fmt.Errorf("t3.listen: %q would listen on every network; list specific addresses", s)
		case slices.Contains(addrs, ap):
			return nil, fmt.Errorf("t3.listen: %s is listed twice", ap)
		}
		addrs = append(addrs, ap)
	}
	return addrs, nil
}

func Defaults() Config {
	return Config{
		CPUs:              4,
		Memory:            8 * GiB,
		DiskSize:          64 * GiB,
		FreePageReporting: true,
		T3:                T3Config{HostPort: 37730, Listen: []string{"127.0.0.1"}},
		Net:               NetConfig{RequestReminderSecs: 60},
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
	var keys []string
	for _, k := range md.Undecoded() {
		// [gui] configured the removed `t3jail gui`; existing files have it.
		if k[0] != "gui" {
			keys = append(keys, k.String())
		}
	}
	if len(keys) > 0 {
		return Config{}, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	switch {
	case c.CPUs < 1:
		return errors.New("cpu must be at least 1")
	case c.MaxCPUs < 0:
		return errors.New("max_cpu must not be negative")
	case c.T3.HostPort == 0:
		return errors.New("t3.host_port must be set")
	case c.Net.RequestReminderSecs != 0 && (c.Net.RequestReminderSecs < 10 || c.Net.RequestReminderSecs > 86400):
		return errors.New("net.request_reminder_secs must be 0 or between 10 and 86400")
	}
	_, err := c.T3.ListenAddrs()
	return err
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
cpu = %d
mem = %q

# Upper bounds for `+"`t3jail resize`"+` while the VM runs. 0 means the host's
# CPU count / total RAM. Changes take effect on the next start.
max_cpu = 0
max_mem = "0"

# Size of the VM disk created by `+"`t3jail init`"+`.
disk_size = %q

# Give the VM /dev/kvm so it can run its own VMs. Needs nested virtualization
# enabled in the host's kvm_intel or kvm_amd module, and widens what the VM
# can reach in the host kernel. Takes effect on the next start.
nested_virt = false

# Let the VM return unused RAM to the host. Takes effect on the next start.
free_page_reporting = true

[t3]
# The T3 server is forwarded to this port on the host.
host_port = %d
# Host addresses to listen on, as "ip" (on host_port) or "ip:port"; IPv6 with
# a port is "[addr]:port". An address the host does not have yet, such as a
# Tailscale IP before Tailscale is up, works as soon as it appears. Every
# address listed can reach T3 (its pairing tokens still apply); wildcards
# like 0.0.0.0 are refused. The first one is used in status and pairing links.
listen = ["127.0.0.1"]

[net]
# While the VM has unanswered requests to reach blocked names
# (t3jail-request), remind with a desktop notification every this many
# seconds; 0 notifies only when a request arrives.
request_reminder_secs = %d
`, d.CPUs, d.Memory, d.DiskSize, d.T3.HostPort, d.Net.RequestReminderSecs)
	return err
}
