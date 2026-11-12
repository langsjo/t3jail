package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/mounts"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/termsafe"
)

func newMountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Share host directories with the VM",
		Long: `Share host directories with the VM over virtiofs. Files the VM creates are
owned by you on the host.

Shares are writable unless --ro is given; read-only is enforced on the host.
Anything the VM writes into a share can end up running on the host (git
hooks, .envrc, flake.nix), so review shared repositories before using them
on the host.`,
	}
	cmd.AddCommand(newMountAddCmd(), newMountRemoveCmd(), newMountListCmd())
	return cmd
}

func newMountAddCmd() *cobra.Command {
	var readOnly, persist bool
	cmd := &cobra.Command{
		Use:   "add <host_dir>[:<guest_dir>]",
		Short: "Share a directory; the guest path defaults to the host path",
		Example: `  t3jail mount add ~/src/project
  t3jail mount add --ro ~/datasets:/data --persist`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			m, err := mounts.Parse(args[0], cwd, readOnly)
			if err != nil {
				return err
			}
			if m.HostPath, err = mounts.CheckHostDir(m.HostPath); err != nil {
				return err
			}
			return addMount(cmd.Context(), inst, m, persist)
		},
	}
	cmd.Flags().BoolVar(&readOnly, "ro", false, "share read-only")
	cmd.Flags().BoolVar(&persist, "persist", false, "also mount it at every start")
	return cmd
}

func addMount(ctx context.Context, inst paths.Instance, m mounts.Mount, persist bool) error {
	client := control.NewClient(inst.ControlSocket())
	_, statusErr := client.Status(ctx)
	running := !errors.Is(statusErr, control.ErrNotRunning)
	if !running && !persist {
		return errors.New("the VM is not running; use --persist to mount from the next start on")
	}

	if persist {
		err := editPersistedMounts(inst, func(p *mounts.Persisted) error {
			if i := p.Find(m.GuestPath); i >= 0 {
				return fmt.Errorf("%s is already a persisted mount (of %s)", m.GuestPath, p.Mounts[i].HostPath)
			}
			p.Mounts = append(p.Mounts, m)
			return nil
		})
		if err != nil {
			return err
		}
	}
	if !running {
		fmt.Printf("Saved; %s will be mounted at %s when the VM starts.\n", m.HostPath, m.GuestPath)
		return nil
	}

	undoPersist := func() {
		if persist {
			editPersistedMounts(inst, func(p *mounts.Persisted) error {
				if i := p.Find(m.GuestPath); i >= 0 {
					p.Mounts = append(p.Mounts[:i], p.Mounts[i+1:]...)
				}
				return nil
			})
		}
	}
	attached, err := client.AddMount(ctx, m)
	if err != nil {
		undoPersist()
		return err
	}
	mode := "rw"
	if m.ReadOnly {
		mode = "ro"
	}
	if err := guestRun(ctx, inst, "sudo", "t3jail-guest", "mount", attached.Tag(), attached.GuestPath, mode); err != nil {
		client.RemoveMount(ctx, attached.GuestPath)
		undoPersist()
		return fmt.Errorf("mounting in the VM: %w", err)
	}
	fmt.Printf("Mounted %s at %s in the VM (%s)\n", attached.HostPath, attached.GuestPath, mode)
	return nil
}

func newMountRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <guest_dir>",
		Short: "Unmount a share and stop restoring it at start",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			guestPath := filepath.Clean(args[0])
			return removeMount(cmd.Context(), inst, guestPath, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "unmount even if processes in the VM use it (lazy unmount)")
	return cmd
}

func removeMount(ctx context.Context, inst paths.Instance, guestPath string, force bool) error {
	client := control.NewClient(inst.ControlSocket())
	active, err := client.Mounts(ctx)
	running := !errors.Is(err, control.ErrNotRunning)
	if running && err != nil {
		return err
	}
	isActive := false
	for _, m := range active {
		isActive = isActive || m.GuestPath == guestPath
	}

	if isActive {
		args := []string{"sudo", "t3jail-guest", "umount", guestPath}
		if force {
			args = append(args, "--lazy")
		}
		if err := guestRun(ctx, inst, args...); err != nil {
			return fmt.Errorf("unmounting in the VM (use --force to detach it anyway): %w", err)
		}
		if err := client.RemoveMount(ctx, guestPath); err != nil {
			return err
		}
		fmt.Printf("Unmounted %s\n", guestPath)
	}

	wasPersisted := false
	err = editPersistedMounts(inst, func(p *mounts.Persisted) error {
		if i := p.Find(guestPath); i >= 0 {
			p.Mounts = append(p.Mounts[:i], p.Mounts[i+1:]...)
			wasPersisted = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if wasPersisted {
		fmt.Printf("%s is no longer mounted at start\n", guestPath)
	}
	if !isActive && !wasPersisted {
		return fmt.Errorf("nothing is mounted at %s (see `t3jail mount list`)", guestPath)
	}
	return nil
}

func newMountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List shares, active and persisted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			active, err := control.NewClient(inst.ControlSocket()).Mounts(cmd.Context())
			if err != nil && !errors.Is(err, control.ErrNotRunning) {
				return err
			}
			persisted, err := mounts.Load(inst.MountsFile())
			if err != nil {
				return err
			}
			for _, row := range mountRows(active, persisted.Mounts) {
				fmt.Println(row)
			}
			return nil
		},
	}
}

// mountRows describes each share once, whether it is mounted now, restored at
// start, or both.
func mountRows(active, persisted []mounts.Mount) []string {
	var rows []string
	describe := func(m mounts.Mount, state string) string {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		return fmt.Sprintf("%s -> %s\t%s\t%s", m.HostPath, m.GuestPath, mode, state)
	}
	for _, m := range active {
		state := "mounted"
		for _, p := range persisted {
			if p.GuestPath == m.GuestPath {
				state = "mounted, persisted"
			}
		}
		rows = append(rows, describe(m, state))
	}
	for _, p := range persisted {
		mounted := false
		for _, m := range active {
			mounted = mounted || m.GuestPath == p.GuestPath
		}
		if !mounted {
			rows = append(rows, describe(p, "at next start"))
		}
	}
	return rows
}

func editPersistedMounts(inst paths.Instance, edit func(*mounts.Persisted) error) error {
	unlock, err := lockRuntime(inst, "mounts.lock")
	if err != nil {
		return err
	}
	defer unlock()
	p, err := mounts.Load(inst.MountsFile())
	if err != nil {
		return err
	}
	before := len(p.Mounts)
	if err := edit(&p); err != nil {
		return err
	}
	if len(p.Mounts) == before {
		return nil
	}
	return p.Save(inst.MountsFile())
}

// guestRun runs a command in the VM over SSH. Its output is guest-controlled,
// so it is escaped before reaching the terminal.
func guestRun(ctx context.Context, inst paths.Instance, args ...string) error {
	cmd, err := guestCmd(ctx, inst, args...)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		os.Stderr.Write(termsafe.Escape(out))
	}
	return err
}

// guestCmd prepares args to run in the VM over SSH, quoted for its shell.
func guestCmd(ctx context.Context, inst paths.Instance, args ...string) (*exec.Cmd, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	argv, err := sshArgv(inst, strings.Join(quoted, " "))
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...), nil
}

// shellQuote quotes s for the remote shell that runs SSH commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
