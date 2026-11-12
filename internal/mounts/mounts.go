// Package mounts describes host directories shared into the VM: parsing the
// `mount add` argument, the guest paths that may not be covered, and the list
// of mounts that persist across restarts.
package mounts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Mount is one host directory shared into the guest.
type Mount struct {
	HostPath  string `toml:"host_path" json:"hostPath"`
	GuestPath string `toml:"guest_path" json:"guestPath"`
	ReadOnly  bool   `toml:"read_only" json:"readOnly"`
}

// Tag is the virtiofs tag of the share, derived from the guest path so that a
// path has one tag across restarts. virtiofs tags are at most 36 bytes.
func (m Mount) Tag() string {
	sum := sha256.Sum256([]byte(m.GuestPath))
	return "t3jail-m-" + hex.EncodeToString(sum[:8])
}

// DeviceID is the cloud-hypervisor device id of the share.
func (m Mount) DeviceID() string { return "fs-" + m.Tag() }

// Parse parses `<host_dir>[:<guest_dir>]`. A relative host path is taken from
// cwd; the guest path defaults to the absolute host path.
func Parse(arg, cwd string, readOnly bool) (Mount, error) {
	host, guest, hasGuest := strings.Cut(arg, ":")
	if strings.Contains(guest, ":") {
		return Mount{}, fmt.Errorf("%q: paths containing ':' are not supported", arg)
	}
	if host == "" || (hasGuest && guest == "") {
		return Mount{}, fmt.Errorf("%q: expected <host_dir>[:<guest_dir>]", arg)
	}
	if !filepath.IsAbs(host) {
		host = filepath.Join(cwd, host)
	}
	host = filepath.Clean(host)
	if !hasGuest {
		guest = host
	}
	if !filepath.IsAbs(guest) {
		return Mount{}, fmt.Errorf("guest path %q must be absolute", guest)
	}
	m := Mount{HostPath: host, GuestPath: filepath.Clean(guest), ReadOnly: readOnly}
	return m, CheckGuestPath(m.GuestPath)
}

// Guest paths whose contents the system or t3jail itself needs. Mounting over
// them, or anything inside them, would break the guest or the host's access.
var reservedTrees = []string{
	"/nix", "/etc", "/boot", "/run", "/proc", "/sys", "/dev", "/usr", "/bin",
	"/sbin", "/lib", "/lib64", "/var", "/root", "/home/agent/.t3", "/home/agent/.ssh",
}

// Paths that may contain mounts but not be one.
var reservedExact = []string{"/", "/home", "/home/agent", "/tmp"}

func CheckGuestPath(p string) error {
	if slices.Contains(reservedExact, p) {
		return fmt.Errorf("cannot mount over %s in the VM", p)
	}
	for _, tree := range reservedTrees {
		if p == tree || strings.HasPrefix(p, tree+"/") {
			return fmt.Errorf("cannot mount at %s: %s is reserved in the VM", p, tree)
		}
	}
	return nil
}

// CheckHostDir verifies that the host side exists and is a directory, and
// returns it with symlinks resolved (virtiofsd shares the real directory).
func CheckHostDir(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	return real, nil
}

// Persisted is the list of mounts restored at every start (mounts.toml).
type Persisted struct {
	Mounts []Mount `toml:"mount"`
}

func Load(path string) (Persisted, error) {
	var p Persisted
	md, err := toml.DecodeFile(path, &p)
	if errors.Is(err, fs.ErrNotExist) {
		return Persisted{}, nil
	}
	if err != nil {
		return Persisted{}, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Persisted{}, fmt.Errorf("%s: unknown keys %v", path, undecoded)
	}
	for _, m := range p.Mounts {
		if err := CheckGuestPath(m.GuestPath); err != nil {
			return Persisted{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return p, nil
}

func (p Persisted) Save(path string) error {
	var b strings.Builder
	b.WriteString("# Mounts restored at every start. Managed by `t3jail mount add --persist`.\n")
	if err := toml.NewEncoder(&b).Encode(p); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Find returns the index of the mount at guestPath, or -1.
func (p Persisted) Find(guestPath string) int {
	return slices.IndexFunc(p.Mounts, func(m Mount) bool { return m.GuestPath == guestPath })
}
