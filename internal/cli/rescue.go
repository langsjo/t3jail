package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/langsjo/t3jail/internal/daemon"
	"github.com/langsjo/t3jail/internal/paths"
)

// buildRescue builds the rescue system, kept alive by a GC root in the state
// directory. Like the base image, it is only built when first needed.
func buildRescue(inst paths.Instance) (*daemon.RescueBoot, error) {
	drv := os.Getenv(envRescueDrv)
	if drv == "" {
		return nil, fmt.Errorf("%s is not set; run t3jail through its Nix package", envRescueDrv)
	}
	if err := os.MkdirAll(inst.GCRoots(), 0o700); err != nil {
		return nil, err
	}
	// The package wrapper passes a derivation; tests pass an already built
	// output.
	installable := drv
	if strings.HasSuffix(drv, ".drv") {
		installable += "^out"
	}
	fmt.Println("Building the rescue system...")
	cmd := exec.Command("nix", "build", "--out-link", filepath.Join(inst.GCRoots(), "rescue"), "--print-out-paths", installable)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("building the rescue system: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return nil, err
	}
	return &daemon.RescueBoot{
		Kernel:    filepath.Join(dir, "kernel"),
		Initramfs: filepath.Join(dir, "initrd"),
		Cmdline:   string(cmdline),
	}, nil
}
