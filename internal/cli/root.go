// Package cli implements the t3jail command-line interface.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/paths"
)

// Environment variables set by the Nix wrapper to locate runtime assets.
const (
	envFirmware = "T3JAIL_FIRMWARE"
	envImageDrv = "T3JAIL_IMAGE_DRV"
)

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "t3jail",
		Short:         "Run the T3 Code server in a network-filtered NixOS VM",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newInitCmd(),
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newSSHCmd(),
		newConsoleCmd(),
		newLogsCmd(),
		newNetCmd(),
		newPairCmd(),
		newGUICmd(),
		newDaemonCmd(),
		newNetnsHelperCmd(),
		newVsockProxyCmd(),
	)
	return root
}

// instance resolves the directories of the (single, for now) VM.
func instance() (paths.Instance, error) {
	return paths.Resolve(paths.DefaultInstance)
}

func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the t3jail executable: %w", err)
	}
	return exe, nil
}
