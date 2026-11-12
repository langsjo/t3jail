package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/daemon"
	"github.com/langsjo/t3jail/internal/jailnet"
	"github.com/langsjo/t3jail/internal/vsock"
)

// Hidden commands that t3jail runs itself.

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__daemon",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			spec, err := daemon.ReadRunSpec(runSpecPath(inst))
			if err != nil {
				return err
			}
			self, err := selfExe()
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			return daemon.New(inst, spec, self, log).Run(context.Background())
		},
	}
}

func newNetnsHelperCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__netns-helper <spec.json>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return jailnet.RunHelper(args[0])
		},
	}
}

func newVsockProxyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__vsock-proxy <port>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.ParseUint(args[0], 10, 32)
			if err != nil {
				return fmt.Errorf("invalid port %q", args[0])
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			conn, err := vsock.Dial(cmd.Context(), inst.VsockSocket(), uint32(port))
			if err != nil {
				return err
			}
			defer conn.Close()
			return vsock.ProxyStdio(conn, os.Stdin, os.Stdout)
		},
	}
}
