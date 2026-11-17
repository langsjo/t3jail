// Package portfwd stores the port forwards opened at every start.
package portfwd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/langsjo/t3jail/internal/control"
)

// Persisted is the list of forwards opened at every start (ports.toml).
type Persisted struct {
	Forwards []control.PortForward `toml:"forward"`
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
	for i, f := range p.Forwards {
		if p.Forwards[i], err = f.Normalized(); err != nil {
			return Persisted{}, fmt.Errorf("%s: forward %s %d: %w", path, f.Direction, f.Listen, err)
		}
		if p.Find(f.Direction, f.Listen) != i {
			return Persisted{}, fmt.Errorf("%s: forward %s %d is listed twice", path, f.Direction, f.Listen)
		}
	}
	return p, nil
}

func (p Persisted) Save(path string) error {
	var b strings.Builder
	b.WriteString("# Port forwards opened at every start. Managed by `t3jail port open --persist`.\n")
	if err := toml.NewEncoder(&b).Encode(p); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Find returns the index of the forward listening on listen, or -1.
func (p Persisted) Find(dir control.PortDirection, listen uint16) int {
	return slices.IndexFunc(p.Forwards, func(f control.PortForward) bool {
		return f.Direction == dir && f.Listen == listen
	})
}
