package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/netrules"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/termsafe"
)

func newNetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "net",
		Short: "Edit which destinations the VM may reach",
		Long: `The VM can only reach destinations on the allowlist (network.list in the
config directory). Entries:

  github.com          exactly this name
  *.github.com        this name and all names below it
  192.0.2.7           an address
  198.51.100.0/24     a network; overrides the default block of private,
                      link-local and host networks
  [2001:db8::/32]     IPv6 networks and addresses may be bracketed

Each entry may end in :PORTS, e.g. :22, :80,443 or :8000-8100. Without it,
TCP ports 80 and 443 are allowed. Only TCP is forwarded.

Changes apply immediately to a running VM. Already resolved addresses stay
allowed until the entry that caused them is removed or the VM restarts.

` + "`t3jail net disable`" + ` lets the VM reach any internet destination for a while
without changing the allowlist; ` + "`t3jail net enable`" + ` applies it again.`,
	}
	cmd.AddCommand(newNetAllowCmd(), newNetRemoveCmd(), newNetListCmd(), newNetReloadCmd(), newNetLogCmd(),
		newNetSwitchCmd(true), newNetSwitchCmd(false))
	return cmd
}

func newNetAllowCmd() *cobra.Command {
	var persistent bool
	cmd := &cobra.Command{
		Use:   "allow <entry>...",
		Short: "Allow destinations until the VM stops, or permanently with -p",
		Example: `  t3jail net allow example.com '*.npmjs.org'
  t3jail net allow -p github.com:22`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := parseEntries(args)
			if err != nil {
				return err
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			client := control.NewClient(inst.ControlSocket())
			if !persistent {
				if _, err := client.Status(cmd.Context()); err != nil {
					if errors.Is(err, control.ErrNotRunning) {
						return errors.New("the VM is not running; use --persistent to allow from the next start on")
					}
					return err
				}
			}

			var added []netrules.Entry
			err = withNetworkLists(inst, func(lists networkLists) error {
				target := lists.ephemeral
				if persistent {
					target = lists.persistent
				}
				for _, e := range entries {
					if lists.persistent.Contains(e) {
						fmt.Printf("%s is already allowed permanently\n", e)
					} else if !target.Add(e) {
						fmt.Printf("%s is already allowed\n", e)
					} else {
						added = append(added, e)
					}
				}
				return nil
			})
			if err != nil || len(added) == 0 {
				return err
			}
			running, err := applyNetwork(cmd.Context(), client)
			if err != nil {
				return err
			}
			if !running && !persistent {
				// The VM stopped after the check above. The entry is discarded
				// when the VM next starts.
				return errors.New("the VM stopped before the change applied; nothing was allowed")
			}
			until := "until the VM stops"
			if persistent {
				until = "permanently"
			}
			for _, e := range added {
				fmt.Printf("Allowed %s %s\n", e, until)
			}
			printIfStopped(running)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&persistent, "persistent", "p", false, "save the entry in network.list so it survives restarts")
	return cmd
}

func newNetRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <entry>...",
		Short: "Remove entries from the allowlist",
		Long: `Remove entries from the allowlist, whether they were allowed permanently or
until the VM stops. Entries must be given as listed, ports included.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := parseEntries(args)
			if err != nil {
				return err
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			err = withNetworkLists(inst, func(lists networkLists) error {
				for _, e := range entries {
					if !lists.persistent.Contains(e) && !lists.ephemeral.Contains(e) {
						return fmt.Errorf("%s is not in the allowlist (see `t3jail net list`)", e)
					}
				}
				for _, e := range entries {
					lists.persistent.Remove(e)
					lists.ephemeral.Remove(e)
					fmt.Printf("Removed %s\n", e)
				}
				return nil
			})
			if err != nil {
				return err
			}
			running, err := applyNetwork(cmd.Context(), control.NewClient(inst.ControlSocket()))
			if err != nil {
				return err
			}
			printIfStopped(running)
			return nil
		},
	}
}

func newNetListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print the allowlist",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			// After an unclean exit the ephemeral list outlives the VM. It is
			// discarded at the next start, so it does not apply now.
			_, statusErr := control.NewClient(inst.ControlSocket()).Status(cmd.Context())
			running := !errors.Is(statusErr, control.ErrNotRunning)
			state, err := filterState(inst, running)
			if err != nil {
				return err
			}
			if line := describeFilterState(state); line != "" {
				fmt.Printf("# %s\n", line)
			}
			return withNetworkLists(inst, func(lists networkLists) error {
				for _, e := range lists.persistent.Entries() {
					fmt.Println(e)
				}
				if !running {
					return nil
				}
				for _, e := range lists.ephemeral.Entries() {
					fmt.Printf("%s\t(until the VM stops)\n", e)
				}
				return nil
			})
		},
	}
}

func newNetReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Apply a hand-edited allowlist to the running VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			if err := control.NewClient(inst.ControlSocket()).ReloadNetwork(cmd.Context()); err != nil {
				return err
			}
			fmt.Println("Applied", inst.NetworkList())
			return nil
		},
	}
}

func newNetLogCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show the VM's DNS queries and whether they were allowed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			// Query names are chosen by the guest.
			return printFile(inst.DNSLog(), termsafe.Writer{W: os.Stdout}, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	return cmd
}

func parseEntries(args []string) ([]netrules.Entry, error) {
	var entries []netrules.Entry
	for _, arg := range args {
		e, err := netrules.Parse(arg)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", arg, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// applyNetwork makes a running VM use the saved lists. It reports whether a VM
// was running.
func applyNetwork(ctx context.Context, client *control.Client) (running bool, err error) {
	err = client.ReloadNetwork(ctx)
	if errors.Is(err, control.ErrNotRunning) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("saved, but applying the change to the running VM failed: %w", err)
	}
	return true, nil
}

func printIfStopped(running bool) {
	if !running {
		fmt.Println("The VM is not running; the change applies when it starts.")
	}
}

type networkLists struct {
	persistent *netrules.List
	ephemeral  *netrules.List
}

// withNetworkLists runs fn on both allowlists while holding a lock, then
// saves them if fn succeeds. Files are replaced atomically so the helper never
// reads half a list.
func withNetworkLists(inst paths.Instance, fn func(networkLists) error) error {
	unlock, err := lockRuntime(inst, "network.lock")
	if err != nil {
		return err
	}
	defer unlock()

	if err := ensureNetworkList(inst); err != nil {
		return err
	}
	persistent, err := readList(inst.NetworkList())
	if err != nil {
		return err
	}
	ephemeral, err := readList(inst.EphemeralNetworkList())
	if errors.Is(err, fs.ErrNotExist) {
		ephemeral, err = netrules.ParseList(nil)
	}
	if err != nil {
		return err
	}

	before := [2]string{string(persistent.Bytes()), string(ephemeral.Bytes())}
	if err := fn(networkLists{persistent, ephemeral}); err != nil {
		return err
	}
	if string(persistent.Bytes()) != before[0] {
		if err := writeAtomic(inst.NetworkList(), persistent.Bytes()); err != nil {
			return err
		}
	}
	if string(ephemeral.Bytes()) != before[1] {
		return writeAtomic(inst.EphemeralNetworkList(), ephemeral.Bytes())
	}
	return nil
}

func readList(path string) (*netrules.List, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	l, err := netrules.ParseList(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ensureNetworkList creates the default allowlist if there is none.
func ensureNetworkList(inst paths.Instance) error {
	f, err := os.OpenFile(inst.NetworkList(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Printf("Created the default network allowlist at %s:\n  %s\n", inst.NetworkList(),
		strings.Join(defaultEntries(), "\n  "))
	_, err = f.WriteString(netrules.DefaultList)
	return err
}

func defaultEntries() []string {
	l, _ := netrules.ParseList([]byte(netrules.DefaultList))
	var out []string
	for _, e := range l.Entries() {
		out = append(out, e.String())
	}
	return out
}
