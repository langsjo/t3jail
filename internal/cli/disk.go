package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/diskimg"
)

func newDiskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "disk", Short: "Manage the VM's disk"}
	cmd.AddCommand(&cobra.Command{
		Use:   "resize <size>",
		Short: "Grow the VM's disk (VM stopped); the guest grows its filesystem at boot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var size config.ByteSize
			if err := size.Set(args[0]); err != nil {
				return err
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			if _, err := control.NewClient(inst.ControlSocket()).Status(cmd.Context()); !errors.Is(err, control.ErrNotRunning) {
				return errors.New("stop the VM first (t3jail stop)")
			}
			// Holding the daemon's lock keeps a start from racing this.
			unlock, err := tryLockRuntime(inst, "daemon.lock")
			if err != nil {
				return errors.New("the VM is starting or running; stop it first")
			}
			defer unlock()
			before, err := os.Stat(inst.Disk())
			if err != nil {
				return err
			}
			if err := diskimg.Grow(inst.Disk(), int64(size)); err != nil {
				return err
			}
			fmt.Printf("Grew the disk from %v to %v; the VM uses the space from its next start.\n",
				config.ByteSize(before.Size()), size)
			return nil
		},
	})
	return cmd
}
