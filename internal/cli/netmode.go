package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/netrules"
	"github.com/langsjo/t3jail/internal/paths"
)

func newNetModeCmd() *cobra.Command {
	var persistent bool
	cmd := &cobra.Command{
		Use:   "mode [allowlist|blocklist|open]",
		Short: "Show or switch which entries the network filter enforces",
		Long: `Show the network mode, or switch it until the VM stops, or permanently with -p:

  allowlist  only what entries allow; blocks win (the default)
  blocklist  any name that is not blocked, on every TCP and UDP port, but
             only at the addresses the VM's DNS returned for it, so
             hard-coded addresses and other resolvers do not get through.
             An address stays open for a day after its last lookup.
  open       any internet destination, on any port and protocol; neither
             allows nor blocks apply, and other resolvers work

In every mode this machine, your LAN and the other private ranges stay
blocked unless an address entry allows them. Leaving open mode cuts the
connections the new mode does not allow.`,
		ValidArgs: []string{string(netrules.Allowlist), string(netrules.Blocklist), string(netrules.Open)},
		Args:      cobra.MaximumNArgs(1),
	}
	cmd.Flags().BoolVarP(&persistent, "persistent", "p", false, "keep the mode across restarts")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		inst, err := instance()
		if err != nil {
			return err
		}
		client := control.NewClient(inst.ControlSocket())
		_, statusErr := client.Status(cmd.Context())
		running := !errors.Is(statusErr, control.ErrNotRunning)
		if len(args) == 0 {
			if persistent {
				return errors.New("--persistent needs a mode to save")
			}
			state, err := modeState(inst, running)
			if err != nil {
				return err
			}
			if state.Temporary() {
				fmt.Printf("%s (until the VM stops, then %s)\n", state.Mode, state.Saved)
			} else {
				fmt.Println(state.Mode)
			}
			return nil
		}
		mode, err := netrules.ParseMode(args[0])
		if err != nil {
			return err
		}
		if !running && !persistent {
			return errors.New("the VM is not running; use --persistent to change the mode for the next start")
		}

		unlock, err := lockRuntime(inst, "network.lock")
		if err != nil {
			return err
		}
		before, err := modeState(inst, running)
		if err == nil {
			err = inst.NetworkMode().Set(mode, persistent)
		}
		unlock()
		if err != nil {
			return err
		}
		if _, err := applyNetwork(cmd.Context(), client); err != nil {
			return err
		}

		switch {
		case before.Mode == mode && !persistent:
			fmt.Printf("The network is already in %s mode.\n", mode)
		case persistent:
			fmt.Printf("The network is in %s mode, also after restarts.\n", mode)
		default:
			fmt.Printf("The network is in %s mode until the VM stops.\n", mode)
		}
		switch mode {
		case netrules.Blocklist:
			fmt.Println("The VM can reach any name that is not blocked; this machine and your LAN stay blocked.")
		case netrules.Open:
			fmt.Println("The VM can reach any internet destination; this machine and your LAN stay blocked.")
		}
		if !running {
			fmt.Println("The VM is not running; the change applies when it starts.")
		}
		return nil
	}
	return cmd
}

// modeState is the current network mode. An override left behind by a VM
// that is not running is discarded at its next start, so it is ignored.
func modeState(inst paths.Instance, running bool) (netrules.ModeState, error) {
	files := inst.NetworkMode()
	if !running {
		files.Override = ""
	}
	return files.Load()
}

// describeMode returns a line for `net list` and `status`, or "" in the
// default, persistent allowlist mode.
func describeMode(s netrules.ModeState) string {
	switch {
	case s.Temporary():
		return fmt.Sprintf("%s mode until the VM stops, then %s (t3jail net mode)", s.Mode, s.Saved)
	case s.Mode != netrules.Allowlist:
		return fmt.Sprintf("%s mode (t3jail net mode -p allowlist)", s.Mode)
	}
	return ""
}
