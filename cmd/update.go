package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	releaseRepo = "trapiche-cloud/cli"
	binName     = "trapiche"
)

var (
	executablePath  = os.Executable
	releaseAPIBase  = "https://api.github.com"
	releaseDownBase = "https://github.com"
)

func newUpdateCommand() *cobra.Command {
	var force bool

	command := &cobra.Command{
		Use:   "update",
		Short: "Update the CLI to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.OutOrStdout(), force)
		},
	}

	command.Flags().BoolVar(&force, "force", false, "Install the latest release even if the version is the same")
	return command
}

func sameVersion(a, b string) bool {
	return strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v")
}

func releaseAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin":
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported architecture: %s", goarch)
	}
	return fmt.Sprintf("%s_%s_%s", binName, goos, goarch), nil
}

type releaseAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

func (r releaseInfo) asset(name string) (releaseAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return releaseAsset{}, false
}

// parseSemver reads vX.Y.Z (extra suffixes ignored). ok is false for "dev" or unknown text.
func parseSemver(v string) (parts [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	if len(fields) != 3 {
		return parts, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

// isOlder reports whether tag is strictly older than current. False if either is not a version.
func isOlder(tag, current string) bool {
	a, okA := parseSemver(tag)
	b, okB := parseSemver(current)
	if !okA || !okB {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	// Same numbers: a pre-release tag is older than the stable version.
	return isPrerelease(tag) && !isPrerelease(current)
}

func isPrerelease(v string) bool {
	return strings.Contains(strings.TrimSpace(v), "-")
}

func latestRelease(client *http.Client) (releaseInfo, error) {
	var info releaseInfo
	url := fmt.Sprintf("%s/repos/%s/releases/latest", releaseAPIBase, releaseRepo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", binName+"-cli")
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("failed to check latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return info, fmt.Errorf("GitHub rate limit reached (%d). Try again later, or run the install script", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("failed to check latest release (%d)", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return info, fmt.Errorf("failed to read latest release: %w", err)
	}
	if strings.TrimSpace(info.TagName) == "" {
		return info, fmt.Errorf("latest release has no tag")
	}
	return info, nil
}

func runUpdate(out io.Writer, force bool) error {
	asset, err := releaseAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	release, err := latestRelease(client)
	if err != nil {
		return err
	}
	tag := release.TagName
	if !force && sameVersion(tag, Version) {
		fmt.Fprintf(out, "Already up to date (%s)\n", Version)
		return nil
	}
	if !force && isOlder(tag, Version) {
		fmt.Fprintf(out, "Current version %s is newer than the latest release %s. Use --force to install it.\n", Version, tag)
		return nil
	}
	assetInfo, found := release.asset(asset)
	if !found {
		return fmt.Errorf("release %s has no file for this platform (%s)", tag, asset)
	}
	wantDigest, ok := strings.CutPrefix(assetInfo.Digest, "sha256:")
	if !ok || len(wantDigest) != 64 || assetInfo.Size <= 0 {
		return fmt.Errorf("release %s has no valid checksum for %s; update refused", tag, asset)
	}

	exe, err := executablePath()
	if err != nil {
		return fmt.Errorf("failed to find the CLI path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	if _, ok := parseSemver(Version); ok {
		fmt.Fprintf(out, "Updating %s -> %s\n", Version, tag)
	} else {
		fmt.Fprintf(out, "Installing %s\n", tag)
	}
	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", releaseDownBase, releaseRepo, tag, asset)
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed (%d): %s", resp.StatusCode, url)
	}

	// Write next to the current file so the rename stays on one disk.
	tmp, err := os.CreateTemp(filepath.Dir(exe), "."+binName+"-update-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write to %s (try: sudo %s update): %w", filepath.Dir(exe), exe, err)
		}
		return fmt.Errorf("cannot write to %s: %w", filepath.Dir(exe), err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, assetInfo.Size+1))
	if err != nil {
		tmp.Close()
		return fmt.Errorf("download failed: %w", err)
	}
	if written != assetInfo.Size {
		tmp.Close()
		return fmt.Errorf("download is incomplete (%d bytes, expected %d)", written, assetInfo.Size)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(got, wantDigest) {
		tmp.Close()
		return fmt.Errorf("checksum mismatch: got %s, expected %s", got, wantDigest)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("failed to replace %s: %w", exe, err)
	}

	fmt.Fprintf(out, "Updated to %s\n", tag)
	return nil
}
