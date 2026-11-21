// Package paths resolves where t3jail keeps its files, following the XDG base
// directory spec. Everything for one VM lives under an instance directory
// ("default" for now), so named VMs can be added later without moving data.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/langsjo/t3jail/internal/netrules"
)

const (
	appName         = "t3jail"
	DefaultInstance = "default"
)

// Instance holds the directories of one VM.
type Instance struct {
	Name string
	// Config holds user-editable files (config.toml, network.list).
	Config string
	// State holds the disk, keys and other generated, persistent data.
	State string
	// Runtime holds sockets and pid files; it is a tmpfs that vanishes on logout.
	Runtime string
}

// Resolve returns the directories for the named instance from the environment.
func Resolve(name string) (Instance, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Instance{}, err
	}
	configHome := envDir("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	stateHome := envDir("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" || !filepath.IsAbs(runtimeDir) {
		return Instance{}, errors.New("XDG_RUNTIME_DIR is not set; t3jail needs a private per-user runtime directory for its sockets")
	}
	return Instance{
		Name:    name,
		Config:  filepath.Join(configHome, appName, name),
		State:   filepath.Join(stateHome, appName, name),
		Runtime: filepath.Join(runtimeDir, appName, name),
	}, nil
}

// envDir returns $key if it is an absolute path (as the XDG spec requires),
// otherwise fallback.
func envDir(key, fallback string) string {
	if v := os.Getenv(key); filepath.IsAbs(v) {
		return v
	}
	return fallback
}

// Ensure creates all instance directories, private to the current user.
func (i Instance) Ensure() error {
	for _, dir := range []string{i.Config, i.State, i.Runtime, i.Logs(), i.SSHDir(), i.MetaDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		// MkdirAll does not change existing directories; tighten them.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("securing %s: %w", dir, err)
		}
	}
	return nil
}

// Config files.
func (i Instance) ConfigFile() string  { return filepath.Join(i.Config, "config.toml") }
func (i Instance) NetworkList() string { return filepath.Join(i.Config, "network.list") }

// State files.
func (i Instance) Disk() string        { return filepath.Join(i.State, "disk.raw") }
func (i Instance) SSHDir() string      { return filepath.Join(i.State, "ssh") }
func (i Instance) SSHKey() string      { return filepath.Join(i.SSHDir(), "id_ed25519") }
func (i Instance) SSHConfig() string   { return filepath.Join(i.SSHDir(), "config") }
func (i Instance) KnownHosts() string  { return filepath.Join(i.SSHDir(), "known_hosts") }
func (i Instance) MountsFile() string  { return filepath.Join(i.State, "mounts.toml") }
func (i Instance) PortsFile() string   { return filepath.Join(i.State, "ports.toml") }
func (i Instance) NixOSConfig() string { return filepath.Join(i.State, "nixos-config") }
func (i Instance) GCRoots() string     { return filepath.Join(i.State, "gcroots") }
func (i Instance) Logs() string        { return filepath.Join(i.State, "logs") }
func (i Instance) DaemonLog() string   { return filepath.Join(i.Logs(), "daemon.log") }
func (i Instance) SerialLog() string   { return filepath.Join(i.Logs(), "serial.log") }
func (i Instance) DNSLog() string      { return filepath.Join(i.Logs(), "dns.log") }

// RescueKnownHosts is separate from KnownHosts because the rescue system
// makes new host keys at every boot.
func (i Instance) RescueKnownHosts() string { return filepath.Join(i.SSHDir(), "known_hosts.rescue") }

// MetaDir is shared read-only into the guest (SSH key, persisted mounts).
func (i Instance) MetaDir() string { return filepath.Join(i.State, "meta") }

// Runtime sockets.
func (i Instance) ControlSocket() string { return filepath.Join(i.Runtime, "control.sock") }
func (i Instance) NetnsSocket() string   { return filepath.Join(i.Runtime, "netns.sock") }

// EphemeralNetworkList holds `net allow` entries that end when the VM stops.
func (i Instance) EphemeralNetworkList() string {
	return filepath.Join(i.Runtime, "network.ephemeral.list")
}
func (i Instance) CHAPISocket() string { return filepath.Join(i.Runtime, "ch.sock") }
func (i Instance) VsockSocket() string { return filepath.Join(i.Runtime, "vsock.sock") }

// VsockHostSocket is where cloud-hypervisor connects when the guest dials
// the host's vsock port.
func (i Instance) VsockHostSocket(port uint32) string {
	return fmt.Sprintf("%s_%d", i.VsockSocket(), port)
}
func (i Instance) SerialSocket() string  { return filepath.Join(i.Runtime, "serial.sock") }
func (i Instance) ConsoleSocket() string { return filepath.Join(i.Runtime, "console.sock") }
func (i Instance) T3Socket() string      { return filepath.Join(i.Runtime, "t3.sock") }
func (i Instance) DaemonLock() string    { return filepath.Join(i.Runtime, "daemon.lock") }
func (i Instance) SSHControlPath() string {
	return filepath.Join(i.Runtime, "ssh-control")
}

// FSSocket is the virtiofsd socket for the share with the given tag.
func (i Instance) FSSocket(tag string) string {
	return filepath.Join(i.Runtime, "fs-"+tag+".sock")
}

// NetworkModeOverride holds the network mode set until the VM stops.
func (i Instance) NetworkModeOverride() string { return filepath.Join(i.Runtime, "network.mode") }

// NetworkMode returns the files that keep the network mode.
func (i Instance) NetworkMode() netrules.ModeFiles {
	return netrules.ModeFiles{
		Saved:     filepath.Join(i.Config, "network.mode"),
		Override:  i.NetworkModeOverride(),
		LegacyOff: filepath.Join(i.Config, "network.off"),
	}
}
