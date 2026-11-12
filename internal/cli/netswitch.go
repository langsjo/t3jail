package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/netrules"
	"github.com/langsjo/t3jail/internal/paths"
)

// newNetSwitchCmd builds `net disable` (off) and `net enable`.
func newNetSwitchCmd(off bool) *cobra.Command {
	var persistent bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Apply the allowlist again",
		Long: `Apply the allowlist again, until the VM stops, or permanently with -p.
Connections made while it was off are cut.`,
		Args: cobra.NoArgs,
	}
	if off {
		cmd.Use = "disable"
		cmd.Short = "Let the VM reach any internet destination, without changing the allowlist"
		cmd.Long = `Switch the allowlist off until the VM stops, or permanently with -p: the VM
may then reach any internet destination, on any port and protocol. This
machine, your LAN and other private ranges stay blocked unless an address
entry allows them. The allowlist itself is kept for ` + "`t3jail net enable`."
	}
	cmd.Flags().BoolVarP(&persistent, "persistent", "p", false, "keep the setting across restarts")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		inst, err := instance()
		if err != nil {
			return err
		}
		client := control.NewClient(inst.ControlSocket())
		_, statusErr := client.Status(cmd.Context())
		running := !errors.Is(statusErr, control.ErrNotRunning)
		if !running && !persistent {
			return errors.New("the VM is not running; use --persistent to change the setting for the next start")
		}

		unlock, err := lockRuntime(inst, "network.lock")
		if err != nil {
			return err
		}
		before, err := filterState(inst, running)
		if err == nil {
			err = netrules.SetFilter(inst.NetworkOffMarker(), inst.NetworkFilterOverride(), off, persistent)
		}
		unlock()
		if err != nil {
			return err
		}
		if _, err := applyNetwork(cmd.Context(), client); err != nil {
			return err
		}

		state := "on"
		if off {
			state = "off"
		}
		switch {
		case before.Off == off && !persistent:
			fmt.Printf("The allowlist is already %s.\n", state)
		case persistent:
			fmt.Printf("The allowlist is %s, also after restarts.\n", state)
		default:
			fmt.Printf("The allowlist is %s until the VM stops.\n", state)
		}
		if off {
			fmt.Println("The VM can reach any internet destination; this machine and your LAN stay blocked.")
		}
		if !running {
			fmt.Println("The VM is not running; the change applies when it starts.")
		}
		return nil
	}
	return cmd
}

// filterState is the allowlist's current state. An override left behind by
// a VM that is not running is discarded at its next start, so it is ignored.
func filterState(inst paths.Instance, running bool) (netrules.FilterState, error) {
	override := inst.NetworkFilterOverride()
	if !running {
		override = ""
	}
	return netrules.LoadFilterState(inst.NetworkOffMarker(), override)
}

// describeFilterState returns a line for `net list` and `status`, or "" when
// the allowlist applies normally.
func describeFilterState(s netrules.FilterState) string {
	switch {
	case s.Off && s.Temporary:
		return "allowlist off until the VM stops (t3jail net enable)"
	case s.Off:
		return "allowlist off (t3jail net enable -p)"
	case s.Temporary:
		return "allowlist on until the VM stops; off after a restart"
	}
	return ""
}
