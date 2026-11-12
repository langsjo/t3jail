package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/diskimg"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/sshx"
)

func newInitCmd() *cobra.Command {
	var diskSize config.ByteSize
	var image string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the VM disk, SSH key and default configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			return initInstance(inst, diskSize, image)
		},
	}
	cmd.Flags().Var(&diskSize, "disk-size", "size of the VM disk (default from config.toml)")
	cmd.Flags().StringVar(&image, "image", "", "use this base disk image instead of building it with nix")
	return cmd
}

// initInstance prepares everything `start` needs. Existing files are kept.
func initInstance(inst paths.Instance, diskSize config.ByteSize, image string) error {
	if err := inst.Ensure(); err != nil {
		return err
	}
	if err := config.WriteDefault(inst.ConfigFile()); err != nil {
		return err
	}
	cfg, err := config.Load(inst.ConfigFile())
	if err != nil {
		return err
	}
	if diskSize == 0 {
		diskSize = cfg.DiskSize
	}
	if _, err := sshx.EnsureKey(inst.SSHKey()); err != nil {
		return fmt.Errorf("creating SSH key: %w", err)
	}

	if _, err := os.Stat(inst.Disk()); err == nil {
		fmt.Printf("VM disk already exists at %s\n", inst.Disk())
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if image == "" {
		if image, err = buildImage(); err != nil {
			return err
		}
	}
	fmt.Printf("Creating %v VM disk from %s\n", diskSize, image)
	if err := diskimg.CopySparse(image, inst.Disk(), int64(diskSize)); err != nil {
		return err
	}
	// A new disk means a new guest host key.
	os.Remove(inst.KnownHosts())
	return nil
}

func buildImage() (string, error) {
	flake := os.Getenv(envFlake)
	if flake == "" {
		return "", fmt.Errorf("%s is not set; pass --image or run t3jail through its Nix package", envFlake)
	}
	fmt.Println("Building the base VM image (this takes a while the first time)...")
	cmd := exec.Command("nix", "build", "--no-link", "--print-out-paths", flake+"#guest-image")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("building the VM image: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "nixos.img"), nil
}
