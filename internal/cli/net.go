package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
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

Each entry may end in :PORTS, e.g. :22, :80,443 or :8000-8100. Ports are
TCP unless they end in /udp, e.g. :53/udp or :443,443/udp (HTTP/3). Without
:PORTS, TCP ports 80 and 443 are allowed.

Blocks (` + "`t3jail net block`" + `, lines starting with ! in network.list) win
over any entry that allows the same name or address:

  !ads.example        this name; also refused when another name is a CNAME
                      alias for it
  !*.ads.example      this name and all names below it
  !198.51.100.0/24    an address or network, on every port and protocol

Changes apply immediately to a running VM. Already resolved addresses stay
allowed until the entry that caused them is removed or the VM restarts.

` + "`t3jail net disable`" + ` lets the VM reach any internet destination for a while
without changing the allowlist; ` + "`t3jail net enable`" + ` applies it again.`,
	}
	cmd.AddCommand(newNetAllowCmd(), newNetBlockCmd(), newNetRemoveCmd(), newNetUnblockCmd(), newNetListCmd(), newNetReloadCmd(), newNetLogCmd(), newNetRequestsCmd(),
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
			for _, e := range entries {
				if e.Block {
					return fmt.Errorf("%s is a block; use `t3jail net block`", e)
				}
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			return allowEntries(cmd.Context(), inst, entries, persistent)
		},
	}
	cmd.Flags().BoolVarP(&persistent, "persistent", "p", false, "save the entry in network.list so it survives restarts")
	return cmd
}

// allowEntries adds entries to the allowlist, applies it, and answers the
// guest's pending requests they cover.
func allowEntries(ctx context.Context, inst paths.Instance, entries []netrules.Entry, persistent bool) error {
	_, lists, err := addEntries(ctx, inst, entries, persistent, "allowed")
	if err != nil || lists == nil {
		return err
	}
	var unblocked []netrules.Entry
	for _, e := range entries {
		if e.Kind != netrules.KindPrefix && (lists.persistent.Blocked(e.Name) || lists.ephemeral.Blocked(e.Name)) {
			fmt.Printf("%s stays blocked by a block entry (see `t3jail net list`)\n", e)
		} else {
			unblocked = append(unblocked, e)
		}
	}
	answer := control.AnswerAllowed
	if persistent {
		answer = control.AnswerAllowedPerm
	}
	answerPending(ctx, control.NewClient(inst.ControlSocket()), answer, func(req netrules.Entry) bool {
		return slices.ContainsFunc(unblocked, func(e netrules.Entry) bool { return e.Covers(req) })
	})
	return nil
}

func newNetBlockCmd() *cobra.Command {
	var persistent bool
	cmd := &cobra.Command{
		Use:   "block <entry>...",
		Short: "Block destinations until the VM stops, or permanently with -p",
		Long: `Block names or addresses, whatever else allows them. A blocked name is
refused by the VM's DNS; a blocked address on every port and protocol.
Connections to newly blocked addresses are cut.`,
		Example: `  t3jail net block -p '*.ads.example'
  t3jail net block 198.51.100.0/24`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := parseBlocks(args)
			if err != nil {
				return err
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			added, lists, err := addEntries(cmd.Context(), inst, entries, persistent, "blocked")
			if err != nil || lists == nil {
				return err
			}
			answerPending(cmd.Context(), control.NewClient(inst.ControlSocket()), control.AnswerBlocked, func(req netrules.Entry) bool {
				return slices.ContainsFunc(added, func(e netrules.Entry) bool { return e.MatchesName(req.Name) })
			})
			return nil
		},
	}
	cmd.Flags().BoolVarP(&persistent, "persistent", "p", false, "save the block in network.list so it survives restarts")
	return cmd
}

// parseBlocks parses block arguments, with or without the leading !.
func parseBlocks(args []string) ([]netrules.Entry, error) {
	blocks := make([]string, len(args))
	for i, arg := range args {
		blocks[i] = "!" + strings.TrimPrefix(strings.TrimSpace(arg), "!")
	}
	return parseEntries(blocks)
}

// addEntries adds entries to the persistent or the ephemeral list and applies
// the lists. It returns the entries it added and the lists after the change,
// or nil lists if no running VM could be told (the reason is printed).
func addEntries(ctx context.Context, inst paths.Instance, entries []netrules.Entry, persistent bool, verb string) ([]netrules.Entry, *networkLists, error) {
	client := control.NewClient(inst.ControlSocket())
	if !persistent {
		if _, err := client.Status(ctx); err != nil {
			if errors.Is(err, control.ErrNotRunning) {
				return nil, nil, fmt.Errorf("the VM is not running; use --persistent to keep it %s from the next start on", verb)
			}
			return nil, nil, err
		}
	}

	var added []netrules.Entry
	var after networkLists
	err := withNetworkLists(inst, func(lists networkLists) error {
		after = lists
		target := lists.ephemeral
		if persistent {
			target = lists.persistent
		}
		for _, e := range entries {
			if lists.persistent.Contains(e) {
				fmt.Printf("%s is already %s permanently\n", e, verb)
			} else if !target.Add(e) {
				fmt.Printf("%s is already %s\n", e, verb)
			} else {
				added = append(added, e)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	running := true
	if len(added) > 0 {
		if running, err = applyNetwork(ctx, client); err != nil {
			return nil, nil, err
		}
		if !running && !persistent {
			// The VM stopped after the check above. The entry is discarded
			// when the VM next starts.
			return nil, nil, fmt.Errorf("the VM stopped before the change applied; nothing was %s", verb)
		}
	}
	until := "until the VM stops"
	if persistent {
		until = "permanently"
	}
	for _, e := range added {
		fmt.Printf("%s %s %s\n", strings.ToUpper(verb[:1])+verb[1:], e, until)
	}
	printIfStopped(running)
	if !running {
		return added, nil, nil
	}
	return added, &after, nil
}

// answerPending gives answer to the pending requests that match. Best effort:
// the list change is what matters.
func answerPending(ctx context.Context, client *control.Client, answer string, match func(netrules.Entry) bool) {
	reqs, err := client.NetRequests(ctx)
	if err != nil {
		return
	}
	answers := map[string]string{}
	for _, r := range reqs {
		if re, err := netrules.Parse(r.Entry); err == nil && match(re) {
			answers[r.Entry] = answer
		}
	}
	if len(answers) > 0 {
		client.AnswerNetRequests(ctx, answers)
	}
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
			return removeEntries(cmd.Context(), entries)
		},
	}
}

func newNetUnblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <entry>...",
		Short: "Remove blocks",
		Long: `Remove blocks, whether they were added permanently or until the VM stops.
Entries must be given as listed, with or without the leading !.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := parseBlocks(args)
			if err != nil {
				return err
			}
			return removeEntries(cmd.Context(), entries)
		},
	}
}

func removeEntries(ctx context.Context, entries []netrules.Entry) error {
	inst, err := instance()
	if err != nil {
		return err
	}
	err = withNetworkLists(inst, func(lists networkLists) error {
		for _, e := range entries {
			if !lists.persistent.Contains(e) && !lists.ephemeral.Contains(e) {
				return fmt.Errorf("%s is not in the list (see `t3jail net list`)", e)
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
	running, err := applyNetwork(ctx, control.NewClient(inst.ControlSocket()))
	if err != nil {
		return err
	}
	printIfStopped(running)
	return nil
}

func newNetListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print the allowlist and blocks",
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
