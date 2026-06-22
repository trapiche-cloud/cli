package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dep := &deploymentResponse{
		ID:     "dep_abc123",
		Name:   "my-app-wolf",
		URL:    "https://my-app-wolf.trapiche.site",
		RepoURL: "https://github.com/o/r",
		Branch: "main",
	}

	if err := refreshProjectConfig(dir, dep, dep.RepoURL, dep.Branch); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeploymentID != dep.ID || cfg.Name != dep.Name || cfg.URL != dep.URL {
		t.Fatalf("cfg = %#v", cfg)
	}

	if err := removeProjectConfig(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatal("expected nil config after unlink")
	}
}

func TestLoadProjectConfigMissingDeploymentID(t *testing.T) {
	dir := t.TempDir()
	path := projectConfigPath(dir)
	if err := os.WriteFile(path, []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadProjectConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "deploymentId") {
		t.Fatalf("expected deploymentId error, got %v", err)
	}
}

func TestQueueAuthenticatedDeployUpdate(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	archivePath := filepath.Join(t.TempDir(), "tiny.tar.gz")
	if err := os.WriteFile(archivePath, []byte{0x1f, 0x8b}, 0o644); err != nil {
		t.Fatal(err)
	}

	original := apiBase
	defer func() { apiBase = original }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer trp_testtoken" {
			t.Fatalf("missing auth header")
		}
		if r.URL.Path != "/api/deployments/dep_existing/upload" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("repoURL") == "" || r.FormValue("branch") == "" {
			t.Fatalf("missing form fields")
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(deploymentResponse{
			ID:   "dep_existing",
			Name: "app-wolf",
			URL:  "https://app-wolf.trapiche.site",
		})
	}))
	defer srv.Close()

	apiBase = srv.URL
	got, err := queueAuthenticatedDeployUpdate(creds, archivePath, "dep_existing", "https://github.com/o/r", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "dep_existing" {
		t.Fatalf("got %#v", got)
	}
}

func TestDeploymentNotFoundHelp(t *testing.T) {
	help := deploymentNotFoundHelp()
	for _, snippet := range []string{"trapiche link", "trapiche deploy --new", "trapiche unlink"} {
		if !strings.Contains(help, snippet) {
			t.Fatalf("help missing %q: %s", snippet, help)
		}
	}
}
