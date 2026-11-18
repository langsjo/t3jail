package netrules

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Mode says which entries the network filter enforces.
type Mode string

const (
	// Allowlist allows only what entries allow, minus blocks.
	Allowlist Mode = "allowlist"
	// Blocklist allows any name that is not blocked, on every TCP and UDP
	// port, but only at the addresses the DNS filter handed out for it.
	Blocklist Mode = "blocklist"
	// Open lets the guest reach any destination; names are neither allowed
	// nor blocked.
	Open Mode = "open"
)

var Modes = []Mode{Allowlist, Blocklist, Open}

func ParseMode(s string) (Mode, error) {
	for _, m := range Modes {
		if s == string(m) {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown network mode %q (want allowlist, blocklist or open)", s)
}

// ModeFiles keep the mode: Saved persistently (missing means allowlist), and
// Override until the VM stops. The override exists only while it differs from
// the saved mode. LegacyOff is the marker `net disable -p` left before there
// were modes; it reads as open until a mode is saved.
type ModeFiles struct {
	Saved, Override, LegacyOff string
}

type ModeState struct {
	Mode Mode
	// Saved is the persistent mode, which applies again when the VM restarts.
	Saved Mode
}

// Temporary reports whether the mode lasts only until the VM stops.
func (s ModeState) Temporary() bool { return s.Mode != s.Saved }

// Load reads the mode. An empty Override is ignored.
func (f ModeFiles) Load() (ModeState, error) {
	saved, err := f.loadSaved()
	if err != nil {
		return ModeState{}, err
	}
	state := ModeState{Mode: saved, Saved: saved}
	if f.Override == "" {
		return state, nil
	}
	m, err := readMode(f.Override)
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return ModeState{}, err
	}
	state.Mode = m
	return state, nil
}

func (f ModeFiles) loadSaved() (Mode, error) {
	m, err := readMode(f.Saved)
	if !errors.Is(err, fs.ErrNotExist) {
		return m, err
	}
	if _, err := os.Stat(f.LegacyOff); err == nil {
		return Open, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return Allowlist, nil
}

func readMode(path string) (Mode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m, err := ParseMode(strings.TrimSpace(string(data)))
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// Set switches to m, persistently or until the VM stops. A persistent change
// ends any override.
func (f ModeFiles) Set(m Mode, persistent bool) error {
	if !persistent {
		saved, err := f.loadSaved()
		if err != nil {
			return err
		}
		if saved == m {
			return removeIfExists(f.Override)
		}
		return os.WriteFile(f.Override, []byte(m+"\n"), 0o600)
	}
	if err := os.WriteFile(f.Saved, []byte(m+"\n"), 0o600); err != nil {
		return err
	}
	if err := removeIfExists(f.LegacyOff); err != nil {
		return err
	}
	return removeIfExists(f.Override)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
