package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/diskimg"
	"github.com/langsjo/t3jail/internal/paths"
)

func newDiskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "disk", Short: "Manage the VM's disk"}
	cmd.AddCommand(&cobra.Command{
		Use:   "resize <size>",
		Short: "Grow the VM's disk and root filesystem, also while it runs",
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
			client := control.NewClient(inst.ControlSocket())
			_, err = client.Status(cmd.Context())
			if err == nil {
				return growRunningDisk(cmd.Context(), inst, client, size)
			}
			if !errors.Is(err, control.ErrNotRunning) {
				return err
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

func growRunningDisk(ctx context.Context, inst paths.Instance, client *control.Client, size config.ByteSize) error {
	if err := client.ResizeDisk(ctx, control.DiskResizeRequest{Size: uint64(size)}); err != nil {
		return err
	}
	if err := guestRun(ctx, inst, "sudo", "t3jail-guest", "grow-root", fmt.Sprint(uint64(size))); err != nil {
		return fmt.Errorf("the disk grew to %v, but growing the filesystem in the VM failed (it is retried at the next start): %w", size, err)
	}
	fmt.Printf("Grew the disk and the VM's root filesystem to %v.\n", size)
	return nil
}
