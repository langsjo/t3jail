package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langsjo/t3jail/internal/gitrepos"
)

// gitFixture is a host checkout and a "VM" repository in a fake home,
// reached through a fake ssh that runs git's remote command locally there.
type gitFixture struct {
	t       *testing.T
	ctx     context.Context
	vmHome  string
	vmRepo  string // ~/.t3/projects/proj in the fake home
	host    string // host checkout, also named proj
	sshCmd  string
	noBound gitrepos.Bindings
}

func newGitFixture(t *testing.T) *gitFixture {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	f := &gitFixture{t: t, ctx: context.Background(), vmHome: filepath.Join(dir, "vm")}
	f.vmRepo = filepath.Join(f.vmHome, gitrepos.ProjectsDir, "proj")
	f.host = filepath.Join(dir, "host", "proj")

	f.sshCmd = filepath.Join(dir, "fake-ssh")
	// git runs `<sshCommand> [options] t3jail '<remote command>'`.
	script := "#!/bin/sh\nfor last; do :; done\ncd \"$FAKE_VM_HOME\" && exec sh -c \"$last\"\n"
	if err := os.WriteFile(f.sshCmd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_VM_HOME", f.vmHome)

	f.git(filepath.Dir(f.vmRepo), "init", "-q", "-b", "main", f.vmRepo)
	f.commit(f.vmRepo, "a")
	f.git(filepath.Dir(f.host), "clone", "-q", f.vmRepo, f.host)
	return f
}

func (f *gitFixture) git(dir string, args ...string) string {
	f.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *gitFixture) commit(dir, name string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", name)
	f.git(dir, "commit", "-q", "-m", name)
}

func (f *gitFixture) sync(repo, branch string, push bool) error {
	f.t.Helper()
	t, err := resolveGitTarget(f.ctx, f.host, f.noBound, repo, branch)
	if err != nil {
		return err
	}
	return t.sync(f.ctx, f.sshCmd, push)
}

func TestGitPullFastForwards(t *testing.T) {
	f := newGitFixture(t)
	f.commit(f.vmRepo, "b")
	if err := f.sync("", "", false); err != nil {
		t.Fatal(err)
	}
	if got, want := f.git(f.host, "rev-parse", "HEAD"), f.git(f.vmRepo, "rev-parse", "HEAD"); got != want {
		t.Errorf("host HEAD %s, want the VM's %s", got, want)
	}
	if got := f.git(f.host, "rev-parse", "refs/remotes/t3jail/main"); got != f.git(f.vmRepo, "rev-parse", "HEAD") {
		t.Errorf("tracking ref not updated: %s", got)
	}
}

func TestGitPushUpdatesCheckedOutBranch(t *testing.T) {
	f := newGitFixture(t)
	f.commit(f.host, "b")
	if err := f.sync("", "", true); err != nil {
		t.Fatal(err)
	}
	if got, want := f.git(f.vmRepo, "rev-parse", "HEAD"), f.git(f.host, "rev-parse", "HEAD"); got != want {
		t.Errorf("VM HEAD %s, want %s", got, want)
	}
	// updateInstead updated the VM's work tree too.
	if _, err := os.Stat(filepath.Join(f.vmRepo, "b")); err != nil {
		t.Errorf("VM work tree not updated: %v", err)
	}
	if got := f.git(f.vmRepo, "status", "--porcelain"); got != "" {
		t.Errorf("VM work tree dirty after push: %q", got)
	}
}

func TestGitPushRefusedOverDirtyVMTree(t *testing.T) {
	f := newGitFixture(t)
	if err := os.WriteFile(filepath.Join(f.vmRepo, "a"), []byte("edited in the VM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit(f.host, "b")
	if err := f.sync("", "", true); err == nil {
		t.Fatal("push over uncommitted VM changes succeeded")
	}
}

func TestGitMissingBranchNeedsFlag(t *testing.T) {
	f := newGitFixture(t)
	f.git(f.host, "switch", "-q", "-c", "feature")
	f.commit(f.host, "b")
	for _, push := range []bool{false, true} {
		err := f.sync("", "", push)
		if err == nil || !strings.Contains(err.Error(), "--branch") {
			t.Errorf("push=%v: err = %v, want a hint to pass --branch", push, err)
		}
	}
	// An explicit branch still has to exist for pull, but push creates it.
	if err := f.sync("", "feature", false); err == nil {
		t.Error("pull of a missing explicit branch succeeded")
	}
	if err := f.sync("", "feature", true); err != nil {
		t.Fatal(err)
	}
	if got, want := f.git(f.vmRepo, "rev-parse", "feature"), f.git(f.host, "rev-parse", "HEAD"); got != want {
		t.Errorf("VM feature %s, want %s", got, want)
	}
}

func TestGitExplicitBranchPullsIntoCurrent(t *testing.T) {
	f := newGitFixture(t)
	f.git(f.vmRepo, "branch", "other")
	f.git(f.host, "switch", "-q", "-c", "local")
	if err := f.sync("", "other", false); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.host, "rev-parse", "--abbrev-ref", "HEAD"); got != "local" {
		t.Errorf("current branch changed to %s", got)
	}
}

func TestResolveGitTarget(t *testing.T) {
	f := newGitFixture(t)
	sub := filepath.Join(f.host, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveGitTarget(f.ctx, sub, f.noBound, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.hostRoot != f.host || got.repo != "proj" || got.guestPath != ".t3/projects/proj" || got.branch != "main" || got.explicitBranch {
		t.Errorf("defaults: %+v", got)
	}

	var bound gitrepos.Bindings
	bound.Set(f.host, "/srv/repo")
	if got, err := resolveGitTarget(f.ctx, f.host, bound, "", ""); err != nil || got.guestPath != "/srv/repo" {
		t.Errorf("binding not used: %+v, %v", got, err)
	}
	if got, err := resolveGitTarget(f.ctx, f.host, bound, "other", "x"); err != nil || got.guestPath != ".t3/projects/other" || got.branch != "x" || !got.explicitBranch {
		t.Errorf("flags not preferred: %+v, %v", got, err)
	}

	for _, branch := range []string{"-x", "a..b", "a b"} {
		if _, err := resolveGitTarget(f.ctx, f.host, f.noBound, "", branch); err == nil {
			t.Errorf("branch %q accepted", branch)
		}
	}
	if _, err := resolveGitTarget(f.ctx, f.host, f.noBound, "a/b", ""); err == nil {
		t.Error("repo a/b accepted")
	}

	f.git(f.host, "switch", "-q", "--detach")
	if _, err := resolveGitTarget(f.ctx, f.host, f.noBound, "", ""); err == nil {
		t.Error("detached HEAD accepted")
	}
	if _, err := resolveGitTarget(f.ctx, t.TempDir(), f.noBound, "", ""); err == nil {
		t.Error("directory outside a checkout accepted")
	}
}
