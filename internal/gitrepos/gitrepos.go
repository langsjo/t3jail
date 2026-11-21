// Package gitrepos names git repositories in the VM, as in t3jail::<repo>
// remote URLs.
package gitrepos

import (
	"fmt"
	"path"
	"strings"
	"unicode"
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
