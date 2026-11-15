package cli

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/termsafe"
)

func newPortCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Forward TCP ports into or out of the VM",
		Long: `Forward TCP ports between the host and the VM while it runs.

"to" listens on a host port and connects to localhost in the VM; "from"
listens on localhost in the VM and connects to localhost on the host. The
VM's address exists only inside its network namespace, so forwards go through
SSH over vsock. They bypass the network allowlist, for the given ports only,
and end when the VM stops.`,
	}
	cmd.AddCommand(newPortOpenCmd(), newPortCloseCmd(), newPortListCmd())
	return cmd
}

func newPortOpenCmd() *cobra.Command {
	var bind string
	cmd := &cobra.Command{
		Use:   "open to|from PORT[:TARGET_PORT]...",
		Short: "Forward ports until closed or the VM stops",
		Long: `Forward ports until closed or the VM stops. PORT is where the forward listens:
on the host for "to", in the VM for "from". TARGET_PORT is the port it
connects to on the other side, the same as PORT by default.`,
		Example: `  t3jail port open to 3000                 # host 127.0.0.1:3000 -> VM localhost:3000
  t3jail port open to 8080:3000 5173       # several at once
  t3jail port open to --bind 0.0.0.0 3000  # reachable from other machines
  t3jail port open from 5432               # VM localhost:5432 -> host localhost:5432`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := parseDirection(args[0])
			if err != nil {
				return err
			}
			var addr netip.Addr
			if bind != "" {
				if dir != control.PortToVM {
					return errors.New("--bind applies only to forwards to the VM")
				}
				if addr, err = netip.ParseAddr(bind); err != nil {
					return fmt.Errorf("--bind: %w", err)
				}
			}
			var fwds []control.PortForward
			for _, s := range args[1:] {
				f, err := parsePortForward(s)
				if err != nil {
					return err
				}
				f.Direction, f.Bind = dir, addr
				fwds = append(fwds, f)
			}
			return openPorts(cmd.Context(), fwds)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "", "host address to listen on, e.g. 0.0.0.0 (default 127.0.0.1)")
	return cmd
}

func openPorts(ctx context.Context, fwds []control.PortForward) error {
	client, err := runningClient(ctx)
	if err != nil {
		return err
	}
	var failed bool
	for _, f := range fwds {
		opened, err := client.OpenPort(ctx, f)
		if err != nil {
			// The error can carry text from the guest's sshd.
			fmt.Printf("%s: %s\n", f, termsafe.Escape([]byte(err.Error())))
			failed = true
			continue
		}
		fmt.Println("Opened", opened)
	}
	if failed {
		return errors.New("some ports were not opened")
	}
	return nil
}

func newPortCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "close to|from PORT...",
		Short:   "Close forwards by the port they listen on",
		Example: `  t3jail port close to 3000 5173`,
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := parseDirection(args[0])
			if err != nil {
				return err
			}
			var ports []uint16
			for _, s := range args[1:] {
				p, err := parsePort(s)
				if err != nil {
					return fmt.Errorf("%q is not a port number (1-65535)", s)
				}
				ports = append(ports, p)
			}
			client, err := runningClient(cmd.Context())
			if err != nil {
				return err
			}
			var failed bool
			for _, p := range ports {
				if err := client.ClosePort(cmd.Context(), control.ClosePortRequest{Direction: dir, Listen: p}); err != nil {
					fmt.Println(err)
					failed = true
					continue
				}
				fmt.Printf("Closed %s %d\n", dir, p)
			}
			if failed {
				return errors.New("some ports were not closed")
			}
			return nil
		},
	}
}

func newPortListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List open forwards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			fwds, err := control.NewClient(inst.ControlSocket()).Ports(cmd.Context())
			if errors.Is(err, control.ErrNotRunning) {
				fmt.Println("The VM is not running.")
				return nil
			}
			if err != nil {
				return err
			}
			if len(fwds) == 0 {
				fmt.Println("No ports are open.")
			}
			for _, f := range fwds {
				fmt.Printf("%-4s  %s\n", f.Direction, f)
			}
			return nil
		},
	}
}

func runningClient(ctx context.Context) (*control.Client, error) {
	inst, err := instance()
	if err != nil {
		return nil, err
	}
	client := control.NewClient(inst.ControlSocket())
	if _, err := client.Status(ctx); err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return nil, errors.New("the VM is not running; start it with `t3jail start`")
		}
		return nil, err
	}
	return client, nil
}

func parseDirection(s string) (control.PortDirection, error) {
	switch d := control.PortDirection(s); d {
	case control.PortToVM, control.PortFromVM:
		return d, nil
	}
	return "", fmt.Errorf("%q: expected \"to\" (host to VM) or \"from\" (VM to host)", s)
}

// parsePortForward parses PORT[:TARGET_PORT].
func parsePortForward(s string) (control.PortForward, error) {
	listen, target, hasTarget := strings.Cut(s, ":")
	if !hasTarget {
		target = listen
	}
	l, errL := parsePort(listen)
	t, errT := parsePort(target)
	if errL != nil || errT != nil {
		return control.PortForward{}, fmt.Errorf("%q: expected PORT or PORT:TARGET_PORT, ports 1-65535", s)
	}
	return control.PortForward{Listen: l, Target: t}, nil
}

func parsePort(s string) (uint16, error) {
	n, err := strconv.ParseUint(s, 10, 16)
	if err == nil && n == 0 {
		err = errors.New("port 0")
	}
	return uint16(n), err
}
