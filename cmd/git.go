package cmd

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var (
	githubSSHRemote = regexp.MustCompile(`^git@github\.com:([^/]+)/(.+?)(?:\.git)?$`)
	githubHTTPS     = regexp.MustCompile(`^https?://github\.com/([^/]+)/(.+?)(?:\.git)?/?$`)
)

func gitCurrentBranch() (string, error) {
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("could not determine current git branch")
	}
	return branch, nil
}

func gitOriginRemote() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no git remote 'origin' found — pass --repo owner/name")
	}
	remote := strings.TrimSpace(string(out))
	if remote == "" {
		return "", fmt.Errorf("empty git remote 'origin'")
	}
	return normalizeGitHubRepoURL(remote)
}

func normalizeGitHubRepoURL(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if matches := githubSSHRemote.FindStringSubmatch(remote); len(matches) == 3 {
		return fmt.Sprintf("https://github.com/%s/%s", matches[1], matches[2]), nil
	}
	if matches := githubHTTPS.FindStringSubmatch(remote); len(matches) == 3 {
		return fmt.Sprintf("https://github.com/%s/%s", matches[1], matches[2]), nil
	}
	return "", fmt.Errorf("unsupported git remote (expected GitHub HTTPS or SSH): %s", remote)
}

func normalizeRepoArg(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("repo is required")
	}
	if strings.Contains(repo, "github.com") {
		return normalizeGitHubRepoURL(repo)
	}
	parts := strings.Split(strings.Trim(repo, "/"), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repo format — use owner/name or a GitHub URL")
	}
	return fmt.Sprintf("https://github.com/%s/%s", parts[0], parts[1]), nil
}

func repoNameFromURL(repoURL string) string {
	repoURL = strings.TrimSuffix(strings.TrimSuffix(repoURL, "/"), ".git")
	parts := strings.Split(repoURL, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return repoURL
}

func repoURLsMatch(a, b string) bool {
	na, errA := normalizeGitHubRepoURL(a)
	nb, errB := normalizeGitHubRepoURL(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return strings.EqualFold(na, nb)
}
