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

	"github.com/langsjo/t3jail/internal/gitrepos"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/sshx"
	"github.com/langsjo/t3jail/internal/termsafe"
)

// vmRefPrefix holds the host's view of the VM branches it synced with.
const vmRefPrefix = "refs/remotes/t3jail/"

// receivePack lets a push update the branch the VM has checked out, as long
// as its work tree is clean, without changing the VM repository's config.
const receivePack = "git -c receive.denyCurrentBranch=updateInstead receive-pack"

func newGitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Pull or push a branch between this checkout and a repository in the VM",
		Long: `Run these in a git checkout on the host. They use t3jail's own SSH
connection; your git config and ~/.ssh are not changed.

The VM repository is --repo (a folder name in the VM's ~/.t3/projects, where
T3 Code puts projects, or an absolute path in the VM), else the one this
checkout last synced with, else the folder in ~/.t3/projects with the same
name as this checkout. The choice is remembered for this checkout.

The branch is --branch, else the current branch, which the VM repository must
then have.

pull fetches the VM branch into refs/remotes/t3jail/<branch> and fast-forwards
the current branch to it. push sends HEAD to the VM branch; only --branch can
create it. If the VM has that branch checked out, its work tree is updated,
and the push is refused while it has uncommitted changes.

The commits come from the VM. Review them before building or running them on
the host.`,
	}
	var repo, branch string
	cmd.PersistentFlags().StringVarP(&repo, "repo", "r", "", "VM repository: folder name in ~/.t3/projects, or absolute path")
	cmd.PersistentFlags().StringVarP(&branch, "branch", "b", "", "VM branch (default: the current branch)")
	for _, push := range []bool{false, true} {
		use, short := "pull", "Fast-forward the current branch to the VM branch"
		if push {
			use, short = "push", "Push HEAD to the VM branch"
		}
		cmd.AddCommand(&cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runGitSync(cmd.Context(), repo, branch, push)
			},
		})
	}
	return cmd
}

func runGitSync(ctx context.Context, repo, branch string, push bool) error {
	inst, err := instance()
	if err != nil {
		return err
	}
	if err := inst.Ensure(); err != nil {
		return err
	}
	bindings, err := gitrepos.Load(inst.GitRepos())
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	t, err := resolveGitTarget(ctx, wd, bindings, repo, branch)
	if err != nil {
		return err
	}
	sshCmd, err := gitSSH(inst)
	if err != nil {
		return err
	}
	if err := t.sync(ctx, sshCmd, push); err != nil {
		return err
	}
	return saveGitBinding(inst, t.hostRoot, t.repo)
}

// gitTarget is what one pull or push syncs.
type gitTarget struct {
	hostRoot       string // top-level directory of the host checkout
	repo           string // as given to --repo
	guestPath      string // gitrepos.GuestPath(repo)
	branch         string // VM branch
	explicitBranch bool   // branch came from --branch
}

func resolveGitTarget(ctx context.Context, dir string, bindings gitrepos.Bindings, repo, branch string) (gitTarget, error) {
	root, err := hostGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitTarget{}, errors.New("not in a git checkout")
	}
	root = filepath.Clean(root)
	current, err := hostGit(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return gitTarget{}, errors.New("HEAD is detached; check out the branch to pull into or push from")
	}

	if repo == "" {
		if bound, ok := bindings.Lookup(root); ok {
			repo = bound
		} else {
			repo = filepath.Base(root)
		}
	}
	guestPath, err := gitrepos.GuestPath(repo)
	if err != nil {
		return gitTarget{}, fmt.Errorf("%w; pass --repo", err)
	}

	t := gitTarget{hostRoot: root, repo: repo, guestPath: guestPath, branch: branch, explicitBranch: branch != ""}
	if !t.explicitBranch {
		t.branch = current
	}
	if strings.HasPrefix(t.branch, "-") {
		return gitTarget{}, fmt.Errorf("invalid branch name %q", t.branch)
	}
	if _, err := hostGit(ctx, root, "check-ref-format", "refs/heads/"+t.branch); err != nil {
		return gitTarget{}, fmt.Errorf("invalid branch name %q", t.branch)
	}
	return t, nil
}

func (t gitTarget) sync(ctx context.Context, sshCmd string, push bool) error {
	url := sshx.HostAlias + ":" + t.guestPath
	sshOpt := "core.sshCommand=" + sshCmd
	where := gitrepos.Display(t.guestPath)
	fmt.Printf("VM repository %s, branch %s\n", where, t.branch)

	exists, err := vmHasBranch(ctx, t.hostRoot, sshOpt, url, t.branch)
	if err != nil {
		return fmt.Errorf("cannot read %s in the VM (is the VM running, and is it a git repository? Otherwise pass --repo): %w", where, err)
	}
	if !exists && (!push || !t.explicitBranch) {
		hint := ""
		if !t.explicitBranch {
			hint = "; pass --branch"
		}
		return fmt.Errorf("%s in the VM has no branch %q%s", where, t.branch, hint)
	}

	tracking := vmRefPrefix + t.branch
	if push {
		if err := runGit(ctx, t.hostRoot, "-c", sshOpt, "push", "--receive-pack="+receivePack, url, "HEAD:refs/heads/"+t.branch); err != nil {
			return err
		}
		return runGit(ctx, t.hostRoot, "update-ref", tracking, "HEAD")
	}
	if err := runGit(ctx, t.hostRoot, "-c", sshOpt, "fetch", url, "+refs/heads/"+t.branch+":"+tracking); err != nil {
		return err
	}
	return runGit(ctx, t.hostRoot, "merge", "--ff-only", tracking)
}

// vmHasBranch asks the VM repository whether it has branch.
func vmHasBranch(ctx context.Context, dir, sshOpt, url, branch string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "-c", sshOpt, "ls-remote", "--heads", url, "refs/heads/"+branch)
	cmd.Stderr = termsafe.Writer{W: os.Stderr}
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(string(out)) {
		if _, ref, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && ref == "refs/heads/"+branch {
			return true, nil
		}
	}
	return false, nil
}

// hostGit runs a git command in the host checkout and returns its trimmed
// output.
func hostGit(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

func saveGitBinding(inst paths.Instance, hostRoot, repo string) error {
	unlock, err := lockRuntime(inst, "git-repos.lock")
	if err != nil {
		return err
	}
	defer unlock()
	b, err := gitrepos.Load(inst.GitRepos())
	if err != nil {
		return err
	}
	if !b.Set(hostRoot, repo) {
		return nil
	}
	if err := b.Save(inst.GitRepos()); err != nil {
		return err
	}
	fmt.Printf("This checkout now syncs with %s in the VM by default.\n", repo)
	return nil
}
