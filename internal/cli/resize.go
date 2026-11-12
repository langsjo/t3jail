package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/control"
)

func newResizeCmd() *cobra.Command {
	var cpus int
	var memory config.ByteSize
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "resize [--cpus N] [--memory SIZE]",
		Short: "Change the running VM's vCPUs and RAM",
		Long: `Change the running VM's vCPUs and RAM without restarting it, within the
maximums set at start. Memory cannot go below the part fixed at start (a
quarter of the start size, at least 1G). Shrinking memory is best effort and can
take minutes, as the guest first moves data out of the memory being removed.
Memory grown beyond about four times the fixed part may not be removable
until the next start.

To change the size for future starts, edit config.toml.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cpus == 0 && memory == 0 {
				return errors.New("give --cpus and/or --memory")
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			client := control.NewClient(inst.ControlSocket())
			req := control.ResizeRequest{CPUs: cpus, Memory: uint64(memory)}
			if err := client.Resize(cmd.Context(), req); err != nil {
				return err
			}
			st, reached := waitForSize(cmd.Context(), client, req, wait)
			fmt.Printf("vCPUs: %d, memory: %v\n", st.CPUs, config.ByteSize(st.Memory))
			if !reached {
				fmt.Println("The guest is still adjusting; check `t3jail status` later.")
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&cpus, "cpus", 0, "number of vCPUs")
	cmd.Flags().Var(&memory, "memory", "RAM, e.g. 12G (a multiple of 128M)")
	cmd.Flags().DurationVar(&wait, "wait", time.Minute, "how long to wait for the guest to reach the new size")
	return cmd
}

func waitForSize(ctx context.Context, client *control.Client, req control.ResizeRequest, timeout time.Duration) (control.Status, bool) {
	deadline := time.Now().Add(timeout)
	for {
		st, err := client.Status(ctx)
		reached := err == nil &&
			(req.CPUs == 0 || st.CPUs == req.CPUs) &&
			(req.Memory == 0 || st.Memory == req.Memory)
		if reached || time.Now().After(deadline) {
			return st, reached
		}
		select {
		case <-ctx.Done():
			return st, false
		case <-time.After(time.Second):
		}
	}
}
