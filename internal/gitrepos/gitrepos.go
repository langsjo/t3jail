// Package gitrepos names git repositories in the VM for `t3jail git pull/push`
// and remembers which one each host checkout was last synced with
// (git-repos.toml).
package gitrepos

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// ProjectsDir is where T3 Code creates and clones projects, relative to the
// guest user's home (the server's base directory is ~/.t3).
const ProjectsDir = ".t3/projects"

// GuestPath returns the VM path of repo: a folder name in ProjectsDir, or an
// absolute path. A relative result is relative to the guest user's home,
// which is where git over SSH resolves it.
func GuestPath(repo string) (string, error) {
	if strings.ContainsFunc(repo, unicode.IsControl) {
		return "", fmt.Errorf("repository %q contains control characters", repo)
	}
	if path.IsAbs(repo) {
		if path.Clean(repo) != repo {
			return "", fmt.Errorf("repository path %q is not clean (use %s)", repo, path.Clean(repo))
		}
		return repo, nil
	}
	if repo == "" || repo == "." || repo == ".." || strings.Contains(repo, "/") || strings.HasPrefix(repo, "-") {
		return "", fmt.Errorf("repository %q must be a project folder name in ~/%s or an absolute path in the VM", repo, ProjectsDir)
	}
	return ProjectsDir + "/" + repo, nil
}

// Display returns how to show a GuestPath result to the user.
func Display(guestPath string) string {
	if path.IsAbs(guestPath) {
		return guestPath
	}
	return "~/" + guestPath
}

// Binding links a host checkout (its absolute top-level directory) to a
// repository in the VM, as given to --repo.
type Binding struct {
	HostPath string `toml:"host_path"`
	Repo     string `toml:"repo"`
}

// Bindings is the content of git-repos.toml.
type Bindings struct {
	Repos []Binding `toml:"repo"`
}

func Load(file string) (Bindings, error) {
	var b Bindings
	md, err := toml.DecodeFile(file, &b)
	if errors.Is(err, fs.ErrNotExist) {
		return Bindings{}, nil
	}
	if err != nil {
		return Bindings{}, fmt.Errorf("%s: %w", file, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Bindings{}, fmt.Errorf("%s: unknown keys %v", file, undecoded)
	}
	for _, r := range b.Repos {
		if !path.IsAbs(r.HostPath) {
			return Bindings{}, fmt.Errorf("%s: host path %q is not absolute", file, r.HostPath)
		}
		if _, err := GuestPath(r.Repo); err != nil {
			return Bindings{}, fmt.Errorf("%s: %w", file, err)
		}
	}
	return b, nil
}

func (b Bindings) Save(file string) error {
	var s strings.Builder
	s.WriteString("# The VM repository each host checkout syncs with. Managed by `t3jail git pull/push`.\n")
	if err := toml.NewEncoder(&s).Encode(b); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(s.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Lookup returns the repository bound to hostPath.
func (b Bindings) Lookup(hostPath string) (string, bool) {
	i := slices.IndexFunc(b.Repos, func(r Binding) bool { return r.HostPath == hostPath })
	if i < 0 {
		return "", false
	}
	return b.Repos[i].Repo, true
}

// Set binds hostPath to repo and reports whether that changed anything.
func (b *Bindings) Set(hostPath, repo string) bool {
	i := slices.IndexFunc(b.Repos, func(r Binding) bool { return r.HostPath == hostPath })
	switch {
	case i < 0:
		b.Repos = append(b.Repos, Binding{HostPath: hostPath, Repo: repo})
	case b.Repos[i].Repo == repo:
		return false
	default:
		b.Repos[i].Repo = repo
	}
	return true
}
