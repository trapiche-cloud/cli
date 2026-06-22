package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type fakeEntry struct {
	name  string
	isDir bool
}

func (f fakeEntry) Name() string               { return f.name }
func (f fakeEntry) IsDir() bool                { return f.isDir }
func (f fakeEntry) Type() os.FileMode          { return 0 }
func (f fakeEntry) Info() (os.FileInfo, error) { return fakeFileInfo{name: f.name, isDir: f.isDir}, nil }

type fakeFileInfo struct {
	name  string
	isDir bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() any           { return nil }

func makeTempProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "src", "index.js"), "console.log('hello');")
	mustWriteFile(t, filepath.Join(root, "README.md"), "# app")
	mustWriteFile(t, filepath.Join(root, "node_modules", "lodash", "x"), "x")
	return root
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTarEntries(t *testing.T, archivePath string) []string {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var entries []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, hdr.Name)
	}
	sort.Strings(entries)
	return entries
}

func TestShouldExclude(t *testing.T) {
	got := shouldExclude("", fakeEntry{name: "node_modules", isDir: true})
	if !got {
		t.Fatal("expected node_modules excluded")
	}
	got = shouldExclude("", fakeEntry{name: "main.go", isDir: false})
	if got {
		t.Fatal("expected main.go included")
	}
}

func TestCreateTarGz(t *testing.T) {
	projectDir := makeTempProject(t)
	archivePath, fileCount, err := createTarGz(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)

	entries := readTarEntries(t, archivePath)
	expected := []string{"README.md", "src/index.js"}
	if !reflect.DeepEqual(entries, expected) {
		t.Fatalf("entries = %v, want %v", entries, expected)
	}
	if fileCount != 2 {
		t.Fatalf("fileCount = %d, want 2", fileCount)
	}
}

func TestApiPath(t *testing.T) {
	original := apiBase
	defer func() { apiBase = original }()
	apiBase = "https://example.com/"
	got := apiPath("/api/deploy")
	if got != "https://example.com/api/deploy" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeRepoArg(t *testing.T) {
	got, err := normalizeRepoArg("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/owner/repo" {
		t.Fatalf("got %q", got)
	}
}

func TestQueueAuthenticatedDeploy(t *testing.T) {
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
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("repoURL") == "" || r.FormValue("appName") == "" {
			t.Fatalf("missing form fields")
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_test", Name: "app-wolf", URL: "https://app-wolf.trapiche.site"})
	}))
	defer srv.Close()

	apiBase = srv.URL
	got, err := queueAuthenticatedDeploy(creds, archivePath, "https://github.com/o/r", "app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "dep_test" {
		t.Fatalf("got %#v", got)
	}
}

func TestRepoURLsMatch(t *testing.T) {
	a := "https://github.com/user/repo"
	b := "git@github.com:user/repo.git"
	if !repoURLsMatch(a, b) {
		t.Fatal("expected match")
	}
	if repoURLsMatch(a, "https://github.com/other/repo") {
		t.Fatal("expected no match")
	}
}

func captureOutput(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	f()
	_ = w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestPrintNewLogs(t *testing.T) {
	printed := 0
	out := captureOutput(func() {
		printNewLogs("hello world", &printed)
	})
	if out != "hello world" || printed != 11 {
		t.Fatalf("out=%q printed=%d", out, printed)
	}
}

func TestRunDeployRequiresAuth(t *testing.T) {
	orig := credentialsPath
	defer func() {
		credentialsPath = orig
	}()
	credentialsPath = func() (string, error) {
		return filepath.Join(t.TempDir(), "missing.json"), nil
	}

	err := runDeploy(deployOptions{dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected auth error, got %v", err)
	}
}
