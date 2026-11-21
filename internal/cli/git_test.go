package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langsjo/t3jail/internal/gitrepos"
)

// envFakeSSH makes the test binary act as git-remote-t3jail, reaching the
// "VM" through the fake ssh it names.
const envFakeSSH = "T3JAIL_TEST_FAKE_SSH"

func TestMain(m *testing.M) {
	if ssh := os.Getenv(envFakeSSH); ssh != "" {
		os.Exit(fakeGitRemoteHelper(ssh))
	}
	os.Exit(m.Run())
}

// fakeGitRemoteHelper is __git-remote without the instance and VM checks.
func fakeGitRemoteHelper(ssh string) int {
	err := func() error {
		if len(os.Args) != 3 {
			return fmt.Errorf("usage: %s <remote> <repo>", os.Args[0])
		}
		guestPath, err := gitrepos.GuestPath(os.Args[2])
		if err != nil {
			return err
		}
		return serveGitRemote(context.Background(), os.Stdin, os.Stdout, []string{ssh}, guestPath)
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "t3jail:", err)
		return 1
	}
	return 0
}

// gitFixture is a host checkout and a "VM" repository in a fake home. git
// reaches the VM through the remote helper, which runs the remote command
// locally in the fake home instead of over ssh.
type gitFixture struct {
	t      *testing.T
	ctx    context.Context
	dir    string
	vmRepo string // ~/.t3/projects/proj in the fake home
	host   string // host checkout, also named proj
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

	vmHome := filepath.Join(dir, "vm")
	f := &gitFixture{t: t, ctx: context.Background(), dir: dir}
	f.vmRepo = filepath.Join(vmHome, gitrepos.ProjectsDir, "proj")
	f.host = filepath.Join(dir, "host", "proj")

	// The helper runs `<ssh> '<remote command>'`.
	fakeSSH := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\nfor last; do :; done\ncd \"$FAKE_VM_HOME\" && exec sh -c \"$last\"\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_VM_HOME", vmHome)
	t.Setenv(envFakeSSH, fakeSSH)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(bin, gitRemoteHelper)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	f.git(filepath.Dir(f.vmRepo), "init", "-q", "-b", "main", f.vmRepo)
	f.commit(f.vmRepo, "a")
	f.git(filepath.Dir(f.host), "init", "-q", "-b", "main", f.host)
	if err := gitRemoteAdd(f.ctx, f.host, "t3jail", ""); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *gitFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := f.tryGit(dir, args...)
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func (f *gitFixture) tryGit(dir string, args ...string) (string, error) {
	f.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (f *gitFixture) commit(dir, name string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", name)
	f.git(dir, "commit", "-q", "-m", name)
}

func TestGitRemoteFetchAndClone(t *testing.T) {
	f := newGitFixture(t)
	f.git(f.vmRepo, "tag", "v1")
	f.git(f.vmRepo, "branch", "other")

	f.git(f.host, "fetch", "-q", "t3jail")
	vmHead := f.git(f.vmRepo, "rev-parse", "HEAD")
	for _, ref := range []string{"t3jail/main", "t3jail/other", "v1"} {
		if got := f.git(f.host, "rev-parse", ref); got != vmHead {
			t.Errorf("%s = %s, want %s", ref, got, vmHead)
		}
	}

	clone := filepath.Join(f.dir, "clone")
	f.git(f.dir, "clone", "-q", "t3jail::proj", clone)
	if got := f.git(clone, "rev-parse", "HEAD"); got != vmHead {
		t.Errorf("clone HEAD %s, want %s", got, vmHead)
	}
}

func TestGitRemotePushUpdatesCheckedOutBranch(t *testing.T) {
	f := newGitFixture(t)
	f.git(f.host, "pull", "-q", "t3jail", "main")
	f.commit(f.host, "b")
	f.git(f.host, "push", "-q", "t3jail", "main")
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

	f.git(f.host, "push", "-q", "t3jail", "HEAD:feature")
	f.git(f.vmRepo, "rev-parse", "--verify", "-q", "refs/heads/feature")
	f.git(f.host, "push", "-q", "t3jail", ":feature")
	if _, err := f.tryGit(f.vmRepo, "rev-parse", "--verify", "-q", "refs/heads/feature"); err == nil {
		t.Error("branch not deleted in the VM")
	}
}

func TestGitRemotePushRefusedOverDirtyVMTree(t *testing.T) {
	f := newGitFixture(t)
	f.git(f.host, "pull", "-q", "t3jail", "main")
	if err := os.WriteFile(filepath.Join(f.vmRepo, "a"), []byte("edited in the VM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit(f.host, "b")
	if out, err := f.tryGit(f.host, "push", "t3jail", "main"); err == nil {
		t.Fatalf("push over uncommitted VM changes succeeded:\n%s", out)
	}
}

func TestGitRemoteMasksVMOutput(t *testing.T) {
	f := newGitFixture(t)
	f.git(f.host, "pull", "-q", "t3jail", "main")
	// git masks C0 controls in remote: messages itself, but not an 8-bit
	// CSI encoded as UTF-8.
	hook := filepath.Join(f.vmRepo, ".git", "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'evil \\302\\2332J\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.commit(f.host, "b")
	out, err := f.tryGit(f.host, "push", "t3jail", "main")
	if err == nil {
		t.Fatal("push past a rejecting hook succeeded")
	}
	if !strings.Contains(out, "evil ??2J") || strings.Contains(out, "\u009b") {
		t.Errorf("hook output not masked:\n%q", out)
	}
}

func TestGitRemoteHelperTakesNoFlags(t *testing.T) {
	for _, args := range [][]string{{"origin", "--help"}, {"-vm", "-x"}} {
		root := NewRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"__git-remote"}, args...))
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "repository") || out.Len() > 0 {
			t.Errorf("%v: err %v, output %q; want the repository rejected and nothing on stdout", args, err, out.String())
		}
	}
}

func TestGitRemoteAdd(t *testing.T) {
	f := newGitFixture(t)
	if got := f.git(f.host, "remote", "get-url", "t3jail"); got != "t3jail::proj" {
		t.Errorf("default remote URL %q", got)
	}

	sub := filepath.Join(f.host, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitRemoteAdd(f.ctx, sub, "vm", "/etc/nixos"); err != nil {
		t.Fatal(err)
	}
	if got := f.git(f.host, "remote", "get-url", "vm"); got != "t3jail::/etc/nixos" {
		t.Errorf("--repo remote URL %q", got)
	}

	for name, call := range map[string]func() error{
		"existing remote": func() error { return gitRemoteAdd(f.ctx, f.host, "t3jail", "") },
		"bad repo":        func() error { return gitRemoteAdd(f.ctx, f.host, "x", "a/b") },
		"bad name":        func() error { return gitRemoteAdd(f.ctx, f.host, "-x", "") },
		"no checkout":     func() error { return gitRemoteAdd(f.ctx, t.TempDir(), "x", "") },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
