package cli

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/portfwd"
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
and end when the VM stops unless opened with --persist.`,
	}
	cmd.AddCommand(newPortOpenCmd(), newPortCloseCmd(), newPortListCmd())
	return cmd
}

func newPortOpenCmd() *cobra.Command {
	var bind string
	var persist bool
	cmd := &cobra.Command{
		Use:   "open to|from PORT[:TARGET_PORT]...",
		Short: "Forward ports until closed or the VM stops",
		Long: `Forward ports until closed or the VM stops. PORT is where the forward listens:
on the host for "to", in the VM for "from". TARGET_PORT is the port it
connects to on the other side, the same as PORT by default.

--persist also opens them, with the same bind address, at every start and
after the VM reboots, until closed. The bind address may be one the host
does not have yet, such as a Tailscale IP; the forward works once it appears.`,
		Example: `  t3jail port open to 3000                 # host 127.0.0.1:3000 -> VM localhost:3000
  t3jail port open to 8080:3000 5173       # several at once
  t3jail port open to --bind 0.0.0.0 3000  # reachable from other machines
  t3jail port open from 5432               # VM localhost:5432 -> host localhost:5432
  t3jail port open to --bind 100.64.1.2 --persist 3000  # at every start too`,
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
				if f, err = f.Normalized(); err != nil {
					return fmt.Errorf("%s: %w", s, err)
				}
				fwds = append(fwds, f)
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			return openPorts(cmd.Context(), inst, fwds, persist)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "", "host address to listen on, e.g. 0.0.0.0 (default 127.0.0.1)")
	cmd.Flags().BoolVar(&persist, "persist", false, "also open them at every start")
	return cmd
}

// openPorts opens fwds, which are normalized. With persist it saves those
// that opened, or all of them while the VM is stopped.
func openPorts(ctx context.Context, inst paths.Instance, fwds []control.PortForward, persist bool) error {
	client := control.NewClient(inst.ControlSocket())
	active, err := client.Ports(ctx)
	running := !errors.Is(err, control.ErrNotRunning)
	if running && err != nil {
		return err
	}
	if !running && !persist {
		return errors.New("the VM is not running; start it with `t3jail start`, or use --persist to open them from the next start on")
	}
	saved, err := portfwd.Load(inst.PortsFile())
	if err != nil {
		return err
	}

	var failed bool
	for _, f := range fwds {
		if i := saved.Find(f.Direction, f.Listen); persist && i >= 0 && saved.Forwards[i] != f {
			fmt.Printf("%s is already persisted; close it first\n", saved.Forwards[i])
			failed = true
			continue
		}
		if running && !slices.Contains(active, f) {
			opened, err := client.OpenPort(ctx, f)
			if err != nil {
				// The error can carry text from the guest's sshd.
				fmt.Printf("%s: %s\n", f, termsafe.Escape([]byte(err.Error())))
				failed = true
				continue
			}
			fmt.Println("Opened", opened)
		} else if running {
			fmt.Println("Already open:", f)
		}
		if !persist {
			continue
		}
		if err := persistPort(inst, f); err != nil {
			fmt.Println(err)
			failed = true
		} else if running {
			fmt.Println("Persisted", f)
		} else {
			fmt.Printf("Persisted %s; it opens at the next start\n", f)
		}
	}
	if failed {
		return errors.New("some ports were not opened")
	}
	return nil
}

// persistPort saves f unless it is saved already. A different forward on the
// same port must be closed first.
func persistPort(inst paths.Instance, f control.PortForward) error {
	return editPersistedPorts(inst, func(p *portfwd.Persisted) error {
		i := p.Find(f.Direction, f.Listen)
		switch {
		case i < 0:
			p.Forwards = append(p.Forwards, f)
		case p.Forwards[i] != f:
			return fmt.Errorf("%s is already persisted; close it first", p.Forwards[i])
		}
		return nil
	})
}

func editPersistedPorts(inst paths.Instance, edit func(*portfwd.Persisted) error) error {
	unlock, err := lockRuntime(inst, "ports.lock")
	if err != nil {
		return err
	}
	defer unlock()
	p, err := portfwd.Load(inst.PortsFile())
	if err != nil {
		return err
	}
	before := slices.Clone(p.Forwards)
	if err := edit(&p); err != nil {
		return err
	}
	if slices.Equal(p.Forwards, before) {
		return nil
	}
	return p.Save(inst.PortsFile())
}

func newPortCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "close to|from PORT...",
		Short:   "Close forwards by the port they listen on, persisted ones too",
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
			inst, err := instance()
			if err != nil {
				return err
			}
			var failed bool
			for _, p := range ports {
				if err := closePort(cmd.Context(), inst, dir, p); err != nil {
					fmt.Println(err)
					failed = true
				}
			}
			if failed {
				return errors.New("some ports were not closed")
			}
			return nil
		},
	}
}

// closePort closes the forward listening on listen and stops opening it at
// start.
func closePort(ctx context.Context, inst paths.Instance, dir control.PortDirection, listen uint16) error {
	client := control.NewClient(inst.ControlSocket())
	active, err := client.Ports(ctx)
	if err != nil && !errors.Is(err, control.ErrNotRunning) {
		return err
	}
	isOpen := slices.ContainsFunc(active, func(f control.PortForward) bool {
		return f.Direction == dir && f.Listen == listen
	})
	if isOpen {
		if err := client.ClosePort(ctx, control.ClosePortRequest{Direction: dir, Listen: listen}); err != nil {
			return err
		}
		fmt.Printf("Closed %s %d\n", dir, listen)
	}

	wasPersisted := false
	err = editPersistedPorts(inst, func(p *portfwd.Persisted) error {
		if i := p.Find(dir, listen); i >= 0 {
			p.Forwards = slices.Delete(p.Forwards, i, i+1)
			wasPersisted = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if wasPersisted {
		fmt.Printf("%s %d is no longer opened at start\n", dir, listen)
	}
	if !isOpen && !wasPersisted {
		return fmt.Errorf("no forward %s port %d is open or persisted (see `t3jail port list`)", dir, listen)
	}
	return nil
}

func newPortListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List forwards, open and persisted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			active, err := control.NewClient(inst.ControlSocket()).Ports(cmd.Context())
			running := !errors.Is(err, control.ErrNotRunning)
			if running && err != nil {
				return err
			}
			persisted, err := portfwd.Load(inst.PortsFile())
			if err != nil {
				return err
			}
			rows := portRows(active, persisted.Forwards, running)
			if len(rows) == 0 {
				fmt.Println("No ports are open or persisted.")
			}
			for _, row := range rows {
				fmt.Println(row)
			}
			return nil
		},
	}
}

// portRows describes each forward once, whether it is open now, opened at
// start, or both.
func portRows(active, persisted []control.PortForward, running bool) []string {
	var rows []string
	describe := func(f control.PortForward, state string) string {
		return fmt.Sprintf("%-4s  %s\t%s", f.Direction, f, state)
	}
	for _, f := range active {
		state := "open"
		if slices.Contains(persisted, f) {
			state = "open, persisted"
		}
		rows = append(rows, describe(f, state))
	}
	for _, f := range persisted {
		switch {
		case slices.Contains(active, f):
		case running:
			rows = append(rows, describe(f, "persisted, not open (see `t3jail logs --log daemon`)"))
		default:
			rows = append(rows, describe(f, "at next start"))
		}
	}
	return rows
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
