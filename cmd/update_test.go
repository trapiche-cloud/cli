package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSameVersion(t *testing.T) {
	if !sameVersion("v1.2.3", "1.2.3") {
		t.Fatal("expected equal")
	}
	if sameVersion("v1.2.4", "v1.2.3") {
		t.Fatal("expected different")
	}
	if sameVersion("v1.0.0", "dev") {
		t.Fatal("dev must not match")
	}
}

func TestReleaseAssetName(t *testing.T) {
	got, err := releaseAssetName("darwin", "arm64")
	if err != nil || got != "trapiche_darwin_arm64" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := releaseAssetName("windows", "amd64"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := releaseAssetName("linux", "386"); err == nil {
		t.Fatal("expected error")
	}
}

func TestIsOlder(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.2.0", "v0.3.0", true},
		{"v0.3.0", "v0.2.0", false},
		{"v0.10.0", "v0.9.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "dev", false},
	}
	for _, c := range cases {
		if got := isOlder(c.tag, c.cur); got != c.want {
			t.Fatalf("isOlder(%q,%q)=%v", c.tag, c.cur, got)
		}
	}
}

func TestIsOlderPrerelease(t *testing.T) {
	if !isOlder("v1.2.3-rc.1", "v1.2.3") {
		t.Fatal("rc must be older than stable")
	}
	if isOlder("v1.2.3", "v1.2.3-4-gabc") {
		t.Fatal("stable is not older than a later build")
	}
}

func runUpdateServer(t *testing.T, body []byte, digest string, size int64) (string, string, error) {
	t.Helper()
	asset, _ := releaseAssetName(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+releaseRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v9.0.0","assets":[{"name":%q,"size":%d,"digest":%q}]}`, asset, size, digest)
	})
	mux.HandleFunc("/"+releaseRepo+"/releases/download/v9.0.0/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldAPI, oldDown, oldExe, oldVer := releaseAPIBase, releaseDownBase, executablePath, Version
	t.Cleanup(func() { releaseAPIBase, releaseDownBase, executablePath, Version = oldAPI, oldDown, oldExe, oldVer })
	releaseAPIBase, releaseDownBase, Version = srv.URL, srv.URL, "v1.0.0"

	exe := filepath.Join(t.TempDir(), "trapiche")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return exe, nil }

	err := runUpdate(io.Discard, false)
	got, _ := os.ReadFile(exe)
	return string(got), exe, err
}

func TestRunUpdateInstalls(t *testing.T) {
	body := []byte("new-binary")
	sum := sha256.Sum256(body)
	got, _, err := runUpdateServer(t, body, "sha256:"+hex.EncodeToString(sum[:]), int64(len(body)))
	if err != nil || got != "new-binary" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRunUpdateRefusals(t *testing.T) {
	body := []byte("new-binary")
	sum := sha256.Sum256(body)
	good := "sha256:" + hex.EncodeToString(sum[:])
	bad := "sha256:" + strings.Repeat("0", 64)
	cases := map[string]struct {
		digest string
		size   int64
	}{
		"missing digest": {"", int64(len(body))},
		"bad digest":     {bad, int64(len(body))},
		"short size":     {good, int64(len(body)) + 5},
		"long body":      {good, int64(len(body)) - 2},
	}
	for name, c := range cases {
		got, _, err := runUpdateServer(t, body, c.digest, c.size)
		if err == nil || got != "old" {
			t.Fatalf("%s: got %q, %v", name, got, err)
		}
	}
}
