package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/termsafe"
)

func newPairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Create a one-time link that signs a T3 client in to the VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			url, err := pairingURL(cmd.Context(), inst)
			if err != nil {
				return err
			}
			fmt.Println(url)
			return nil
		},
	}
}

// pairingURL asks the T3 server in the VM for a one-time pairing link that
// points at the host-side forwarded port.
func pairingURL(ctx context.Context, inst paths.Instance) (string, error) {
	status, err := control.NewClient(inst.ControlSocket()).Status(ctx)
	if errors.Is(err, control.ErrNotRunning) {
		return "", errors.New("the VM is not running; start it with `t3jail start`")
	}
	if err != nil {
		return "", err
	}
	argv, err := sshArgv(inst, "t3", "auth", "pairing", "create",
		"--base-dir", `"$HOME/.t3"`, "--label", "t3jail", "--base-url", status.T3URL, "--json")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Guest output: escape before it reaches the terminal.
		return "", fmt.Errorf("creating a pairing link in the VM: %w: %s", err, termsafe.Escape([]byte(strings.TrimSpace(stderr.String()))))
	}
	return parsePairingOutput(out, status.T3URL)
}

// parsePairingOutput extracts the link from `t3 auth pairing create --json`.
// The guest is untrusted, so the link must point at our own forwarded port.
func parsePairingOutput(out []byte, baseURL string) (string, error) {
	var issued struct {
		PairURL string `json:"pairUrl"`
	}
	if err := json.Unmarshal(out, &issued); err != nil {
		return "", fmt.Errorf("unexpected output from t3 in the VM: %w", err)
	}
	if !strings.HasPrefix(issued.PairURL, strings.TrimSuffix(baseURL, "/")+"/pair#") {
		return "", fmt.Errorf("t3 in the VM returned an unexpected pairing link: %s", termsafe.Escape([]byte(issued.PairURL)))
	}
	for _, c := range []byte(issued.PairURL) {
		if c <= 0x20 || c >= 0x7f {
			return "", fmt.Errorf("t3 in the VM returned a pairing link with unexpected characters: %s", termsafe.Escape([]byte(issued.PairURL)))
		}
	}
	return issued.PairURL, nil
}
