package cmd

import (
	"os/exec"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The git helpers must read the repository in the given folder,
// not the folder where the command runs.
func TestGitHelpersUseGivenDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "feature-x")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:acme/other-app.git")

	remote, err := gitOriginRemote(dir)
	if err != nil {
		t.Fatal(err)
	}
	if remote != "https://github.com/acme/other-app" {
		t.Fatalf("remote = %q", remote)
	}

	branch, err := gitCurrentBranch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature-x" {
		t.Fatalf("branch = %q", branch)
	}
}
