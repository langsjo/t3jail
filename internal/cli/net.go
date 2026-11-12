package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

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
allowed until the entry that caused them is removed or the VM restarts.`,
	}
	cmd.AddCommand(newNetAllowCmd(), newNetRemoveCmd(), newNetListCmd(), newNetReloadCmd(), newNetLogCmd())
	return cmd
}

func newNetAllowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "allow <entry>...",
		Short:   "Add entries to the allowlist",
		Example: "  t3jail net allow example.com '*.npmjs.org' github.com:22",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editAndApply(cmd.Context(), args, func(l *netrules.List, e netrules.Entry) error {
				if !l.Add(e) {
					fmt.Printf("%s is already allowed\n", e)
				}
				return nil
			})
		},
	}
}

func newNetRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <entry>...",
		Short: "Remove entries from the allowlist",
		Long:  "Remove entries from the allowlist. Entries must be given as listed, ports included.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editAndApply(cmd.Context(), args, func(l *netrules.List, e netrules.Entry) error {
				if !l.Remove(e) {
					return fmt.Errorf("%s is not in the allowlist", e)
				}
				return nil
			})
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
			data, err := os.ReadFile(inst.NetworkList())
			if err != nil {
				return err
			}
			l, err := netrules.ParseList(data)
			if err != nil {
				return err
			}
			for _, e := range l.Entries() {
				fmt.Println(e)
			}
			return nil
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

// editAndApply parses all args first, so that one typo changes nothing, then
// edits the list under a lock and applies it to the running VM.
func editAndApply(ctx context.Context, args []string, edit func(*netrules.List, netrules.Entry) error) error {
	var entries []netrules.Entry
	for _, arg := range args {
		e, err := netrules.Parse(arg)
		if err != nil {
			return fmt.Errorf("%q: %w", arg, err)
		}
		entries = append(entries, e)
	}
	inst, err := instance()
	if err != nil {
		return err
	}
	err = editNetworkList(inst, func(l *netrules.List) error {
		for _, e := range entries {
			if err := edit(l, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = control.NewClient(inst.ControlSocket()).ReloadNetwork(ctx)
	if errors.Is(err, control.ErrNotRunning) {
		fmt.Println("Saved; the VM is not running, the change applies when it starts.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("saved %s, but applying it failed: %w", inst.NetworkList(), err)
	}
	return nil
}

// editNetworkList applies edit to network.list while holding a lock, and
// replaces the file atomically so the helper never reads half a list.
func editNetworkList(inst paths.Instance, edit func(*netrules.List) error) error {
	if err := inst.Ensure(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(inst.Runtime, "network.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}

	if err := ensureNetworkList(inst); err != nil {
		return err
	}
	data, err := os.ReadFile(inst.NetworkList())
	if err != nil {
		return err
	}
	l, err := netrules.ParseList(data)
	if err != nil {
		return err
	}
	if err := edit(l); err != nil {
		return err
	}
	tmp := inst.NetworkList() + ".tmp"
	if err := os.WriteFile(tmp, l.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, inst.NetworkList())
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
