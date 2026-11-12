package cli

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/langsjo/t3jail/internal/paths"
)

// lockRuntime takes an exclusive lock on a file in the runtime directory,
// waiting for other t3jail commands that hold it.
func lockRuntime(inst paths.Instance, name string) (unlock func(), err error) {
	if err := inst.Ensure(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(inst.Runtime, name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
