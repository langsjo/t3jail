package netrules

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// The allowlist can be switched off without changing it: persistently, by a
// marker file next to network.list, or until the VM stops, by an override
// file saying "on" or "off" that wins while it exists.

// FilterState says whether the allowlist applies.
type FilterState struct {
	Off bool
	// Temporary means the override decides, until the VM stops.
	Temporary bool
}

// LoadFilterState reads the state files. An empty override path ignores the
// override.
func LoadFilterState(marker, override string) (FilterState, error) {
	if override != "" {
		data, err := os.ReadFile(override)
		if err == nil {
			switch v := strings.TrimSpace(string(data)); v {
			case "on", "off":
				return FilterState{Off: v == "off", Temporary: true}, nil
			default:
				return FilterState{}, fmt.Errorf("%s: want on or off, got %q", override, v)
			}
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return FilterState{}, err
		}
	}
	_, err := os.Stat(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return FilterState{}, nil
	}
	return FilterState{Off: err == nil}, err
}

// SetFilter switches the allowlist off or on, persistently or until the VM
// stops. An override exists only while it differs from the persistent
// setting, so a persistent change ends it.
func SetFilter(marker, override string, off, persistent bool) error {
	if !persistent {
		saved, err := LoadFilterState(marker, "")
		if err != nil {
			return err
		}
		if saved.Off == off {
			return removeIfExists(override)
		}
		state := "on"
		if off {
			state = "off"
		}
		return os.WriteFile(override, []byte(state+"\n"), 0o600)
	}
	var err error
	if off {
		err = os.WriteFile(marker, []byte("The network allowlist is off; `t3jail net enable -p` turns it on.\n"), 0o600)
	} else {
		err = removeIfExists(marker)
	}
	if err != nil {
		return err
	}
	return removeIfExists(override)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
