package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/sshx"
	"github.com/langsjo/t3jail/internal/termsafe"
)

const (
	guestConfigRepo   = "/etc/nixos"
	guestConfigBranch = "main"
	configRemote      = "vm"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Sync the VM's NixOS configuration (/etc/nixos) with a host clone",
		Long: `The VM's /etc/nixos is a git repository that agents in the VM may change.
pull clones or updates a copy on the host; push sends host commits back.

The files are written by the VM. Review them before evaluating them with nix
or allowing them with direnv on the host.`,
	}
	var dir string
	cmd.PersistentFlags().StringVar(&dir, "dir", "", "host clone (default: in the state directory)")
	resolveDir := func(inst paths.Instance) string {
		if dir != "" {
			return dir
		}
		return inst.NixOSConfig()
	}

	pull := &cobra.Command{
		Use:   "pull",
		Short: "Clone or update the host copy from the VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			return configPull(cmd.Context(), inst, resolveDir(inst))
		},
	}
	var doSwitch bool
	push := &cobra.Command{
		Use:   "push",
		Short: "Push host commits to the VM, optionally rebuilding it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			return configPush(cmd.Context(), inst, resolveDir(inst), doSwitch)
		},
	}
	push.Flags().BoolVar(&doSwitch, "switch", false, "run nixos-rebuild switch in the VM afterwards")
	cmd.AddCommand(pull, push)
	return cmd
}

// gitSSH returns the ssh command git uses to reach the VM, refreshing the
// generated ssh_config first.
func gitSSH(inst paths.Instance) (string, error) {
	if _, err := sshArgv(inst); err != nil {
		return "", err
	}
	return "ssh -F " + shellQuote(inst.SSHConfig()), nil
}

func configPull(ctx context.Context, inst paths.Instance, dir string) error {
	sshCmd, err := gitSSH(inst)
	if err != nil {
		return err
	}
	remoteURL := sshx.HostAlias + ":" + guestConfigRepo

	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		err := runGit(ctx, "", "-c", "core.sshCommand="+sshCmd,
			"clone", "--origin", configRemote, "--branch", guestConfigBranch, remoteURL, dir)
		if err != nil {
			return err
		}
		// Kept in the clone's own config, so plain git commands work too.
		if err := runGit(ctx, dir, "config", "core.sshCommand", sshCmd); err != nil {
			return err
		}
		fmt.Printf("Cloned the VM's configuration into %s\n", dir)
	} else if err != nil {
		return err
	} else {
		// The ssh_config path is stable, but refresh in case --dir moved.
		if err := runGit(ctx, dir, "config", "core.sshCommand", sshCmd); err != nil {
			return err
		}
		if err := runGit(ctx, dir, "pull", "--ff-only", configRemote, guestConfigBranch); err != nil {
			return err
		}
	}

	if dirty, err := guestOutput(ctx, inst, "git", "-C", guestConfigRepo, "status", "--porcelain"); err == nil && strings.TrimSpace(dirty) != "" {
		fmt.Printf("Note: the VM has uncommitted changes in %s that were not pulled:\n%s", guestConfigRepo, termsafe.Escape([]byte(dirty)))
	}
	return nil
}

func configPush(ctx context.Context, inst paths.Instance, dir string, doSwitch bool) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return fmt.Errorf("no host clone at %s; run `t3jail config pull` first", dir)
	}
	sshCmd, err := gitSSH(inst)
	if err != nil {
		return err
	}
	if err := runGit(ctx, dir, "config", "core.sshCommand", sshCmd); err != nil {
		return err
	}
	// The VM updates its checked-out tree (receive.denyCurrentBranch is
	// updateInstead) and refuses if that tree has uncommitted changes.
	if err := runGit(ctx, dir, "push", configRemote, "HEAD:"+guestConfigBranch); err != nil {
		return err
	}
	if !doSwitch {
		return nil
	}
	fmt.Println("Rebuilding the VM...")
	cmd, err := guestCmd(ctx, inst, "sudo", "nixos-rebuild", "switch", "--flake", guestConfigRepo)
	if err != nil {
		return err
	}
	cmd.Stdout = termsafe.Writer{W: os.Stdout}
	cmd.Stderr = termsafe.Writer{W: os.Stderr}
	return cmd.Run()
}

func runGit(ctx context.Context, dir string, args ...string) error {
	subcommand := args[0]
	for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i += 2 {
		subcommand = args[i+2]
	}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	// Messages from the remote side come from the VM.
	cmd.Stdout = termsafe.Writer{W: os.Stdout}
	cmd.Stderr = termsafe.Writer{W: os.Stderr}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s failed: %w", subcommand, err)
	}
	return nil
}

// guestOutput runs a command in the VM and returns its (untrusted) stdout.
func guestOutput(ctx context.Context, inst paths.Instance, args ...string) (string, error) {
	cmd, err := guestCmd(ctx, inst, args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	return string(out), err
}
