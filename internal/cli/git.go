package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/gitrepos"
	"github.com/langsjo/t3jail/internal/termsafe"
)

// gitRemoteHelper is the executable git runs for t3jail::<repo> URLs.
const gitRemoteHelper = "git-remote-t3jail"

// gitServices maps the services git asks the remote helper for to the
// command run in the VM. A push updates the branch the VM has checked out, as
// long as its work tree is clean, without changing the VM repository's config.
var gitServices = map[string]string{
	"git-upload-pack":    "git-upload-pack",
	"git-upload-archive": "git-upload-archive",
	"git-receive-pack":   "git -c receive.denyCurrentBranch=updateInstead receive-pack",
}

func newGitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Use repositories in the VM as git remotes",
		Long: `A repository in the VM is a git remote with the URL t3jail::<repo>, where
<repo> is a folder name in the VM's ~/.t3/projects (where T3 Code puts
projects) or an absolute path in the VM. git reaches it through ` + gitRemoteHelper + `,
which comes with t3jail and uses t3jail's own SSH connection; your git config
and ~/.ssh are not involved.

    t3jail git remote add            # in a checkout: add the remote "t3jail"
    git fetch t3jail
    git push t3jail HEAD:fix-typo
    git clone t3jail::myproject

A push to the branch the VM has checked out updates its work tree, and is
refused while that has uncommitted changes.

The commits come from the VM. Review them before building or running them on
the host.`,
	}
	remote := &cobra.Command{
		Use:   "remote",
		Short: "Manage git remotes for repositories in the VM",
	}
	remote.AddCommand(newGitRemoteAddCmd())
	cmd.AddCommand(remote)
	return cmd
}

func newGitRemoteAddCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "add [NAME]",
		Short: "Add a remote (default name: t3jail) for a repository in the VM",
		Long: `Add a git remote to this checkout for a repository in the VM: --repo (a
folder name in the VM's ~/.t3/projects, or an absolute path in the VM), else
the folder in ~/.t3/projects with the same name as this checkout.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "t3jail"
			if len(args) == 1 {
				name = args[0]
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			return gitRemoteAdd(cmd.Context(), wd, name, repo)
		},
	}
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "VM repository: folder name in ~/.t3/projects, or absolute path")
	return cmd
}

func gitRemoteAdd(ctx context.Context, dir, name, repo string) error {
	root, err := hostGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("not in a git checkout")
	}
	if repo == "" {
		repo = filepath.Base(root)
	}
	guestPath, err := gitrepos.GuestPath(repo)
	if err != nil {
		return fmt.Errorf("%w; pass --repo", err)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid remote name %q", name)
	}
	if err := runGit(ctx, root, "remote", "add", name, "t3jail::"+repo); err != nil {
		return err
	}
	fmt.Printf("Added remote %s for %s in the VM.\n", name, gitrepos.Display(guestPath))
	if _, err := exec.LookPath(gitRemoteHelper); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s is not on PATH, and git needs it for this remote. Install t3jail's package (nix run does not put it on PATH).\n", gitRemoteHelper)
	}
	return nil
}

func newGitRemoteHelperCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__git-remote <remote> <repo>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// git passes the URL without its "t3jail::" prefix.
			guestPath, err := gitrepos.GuestPath(args[1])
			if err != nil {
				return err
			}
			inst, err := instance()
			if err != nil {
				return err
			}
			if err := inst.Ensure(); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			st, err := control.NewClient(inst.ControlSocket()).Status(ctx)
			cancel()
			if err != nil {
				return errors.New("the VM is not running; start it with `t3jail start`")
			}
			if st.Rescue {
				return errors.New("the VM is running the rescue system")
			}
			ssh, err := sshArgv(inst)
			if err != nil {
				return err
			}
			return serveGitRemote(cmd.Context(), os.Stdin, os.Stdout, ssh, guestPath)
		},
	}
}

// serveGitRemote speaks git's remote helper protocol (gitremote-helpers(7))
// on in and out. It offers only "connect": git asks for a service such as
// git-upload-pack, and from then on in and out are connected to that service
// running in the VM, reached by running the ssh argv with the command
// appended. git itself then does everything a fetch or push needs. What the
// service sends goes through filterGitWire.
func serveGitRemote(ctx context.Context, in, out *os.File, ssh []string, guestPath string) error {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) && line == "" {
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSuffix(line, "\n")
		switch service, isConnect := strings.CutPrefix(line, "connect "); {
		case line == "":
			return nil
		case line == "capabilities":
			if _, err := io.WriteString(out, "connect\n\n"); err != nil {
				return err
			}
		case isConnect:
			remoteCmd, ok := gitServices[service]
			if !ok {
				return fmt.Errorf("unsupported service %q", service)
			}
			// The service reads in directly, so nothing may sit in r's buffer.
			// git sends no data before the reply below.
			if r.Buffered() > 0 {
				return errors.New("git sent data before the connection was set up")
			}
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, ssh[0], append(ssh[1:], remoteCmd+" "+shellQuote(guestPath))...)
			cmd.Stdin = in
			cmd.Stderr = termsafe.Writer{W: os.Stderr}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return err
			}
			if err := cmd.Start(); err != nil {
				return err
			}
			filterErr := filterGitWire(out, stdout, service != "git-receive-pack")
			if filterErr != nil {
				_ = cmd.Process.Kill()
			}
			if err := cmd.Wait(); filterErr == nil && err != nil {
				filterErr = err
			}
			if filterErr != nil {
				return fmt.Errorf("%s in the VM (%s): %w", service, gitrepos.Display(guestPath), filterErr)
			}
			return nil
		default:
			return fmt.Errorf("unsupported remote helper command %q", line)
		}
	}
}

// hostGit runs a git command in the host checkout and returns its trimmed
// output.
func hostGit(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
