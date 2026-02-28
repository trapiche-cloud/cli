package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEntry implements os.DirEntry for testing shouldExclude.
type fakeEntry struct {
	name  string
	isDir bool
}

func (f fakeEntry) Name() string      { return f.name }
func (f fakeEntry) IsDir() bool       { return f.isDir }
func (f fakeEntry) Type() os.FileMode { return 0 }
func (f fakeEntry) Info() (os.FileInfo, error) {
	return fakeFileInfo{name: f.name, isDir: f.isDir}, nil
}

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

// captureOutput redirects os.Stdout and returns what was printed.
func captureOutput(f func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return ""
	}

	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	f()

	_ = w.Close()
	os.Stdout = old

	return <-done
}

// makeTempProject creates a temp dir tree and registers cleanup with t.Cleanup.
func makeTempProject(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "project")
	mustMkdirAll(t, root)

	// Included files
	mustWriteFile(t, filepath.Join(root, "src", "index.js"), "console.log('hello');")
	mustWriteFile(t, filepath.Join(root, "src", "app.go"), "package main")
	mustWriteFile(t, filepath.Join(root, "public", "style.css"), "body{}")
	mustWriteFile(t, filepath.Join(root, "README.md"), "# app")

	// Excluded paths
	mustWriteFile(t, filepath.Join(root, "node_modules", "lodash", "x"), "x")
	mustWriteFile(t, filepath.Join(root, ".git", "config"), "[core]")
	mustWriteFile(t, filepath.Join(root, "dist", "bundle.js"), "bundle")
	mustWriteFile(t, filepath.Join(root, ".DS_Store"), "ignored")
	mustWriteFile(t, filepath.Join(root, "server.log"), "logs")
	mustWriteFile(t, filepath.Join(root, ".env"), "SECRET=1")
	mustWriteFile(t, filepath.Join(root, ".env.local"), "SECRET=2")

	return root
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir failed for %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write failed for %s: %v", path, err)
	}
}

// readTarEntries opens a tar.gz and returns the list of entry names.
func readTarEntries(t *testing.T, archivePath string) []string {
	t.Helper()

	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive failed: %v", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
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
			t.Fatalf("reading tar failed: %v", err)
		}
		entries = append(entries, hdr.Name)
	}

	sort.Strings(entries)
	return entries
}

// serveStatusSequence returns an http.Handler replying with each status in turn.
func serveStatusSequence(responses []deployStatusResponse) http.Handler {
	if len(responses) == 0 {
		responses = []deployStatusResponse{{Status: "building"}}
	}

	var mu sync.Mutex
	index := 0

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		resp := responses[index]
		if index < len(responses)-1 {
			index++
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// mustMakeTinyArchive returns path to a minimal valid tar.gz temp file.
func mustMakeTinyArchive(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "tiny.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tiny archive failed: %v", err)
	}

	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	body := []byte("hello")

	if err := tw.WriteHeader(&tar.Header{
		Name: "index.html",
		Mode: 0o644,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatalf("write tar header failed: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write tar body failed: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer failed: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer failed: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file failed: %v", err)
	}

	return path
}

func TestShouldExclude(t *testing.T) {
	tests := []struct {
		name     string
		isDir    bool
		expected bool
	}{
		{name: "node_modules", isDir: true, expected: true},
		{name: ".git", isDir: true, expected: true},
		{name: ".next", isDir: true, expected: true},
		{name: "dist", isDir: true, expected: true},
		{name: "out", isDir: true, expected: true},
		{name: "build", isDir: true, expected: true},
		{name: "coverage", isDir: true, expected: true},
		{name: "src", isDir: true, expected: false},
		{name: "components", isDir: true, expected: false},
		{name: ".DS_Store", isDir: false, expected: true},
		{name: "error.log", isDir: false, expected: true},
		{name: "debug.log", isDir: false, expected: true},
		{name: ".env", isDir: false, expected: true},
		{name: ".env.local", isDir: false, expected: true},
		{name: ".env.production", isDir: false, expected: true},
		{name: ".envrc", isDir: false, expected: false},
		{name: "myapp.env", isDir: false, expected: false},
		{name: "main.go", isDir: false, expected: false},
		{name: "index.js", isDir: false, expected: false},
		{name: "README.md", isDir: false, expected: false},
	}

	for _, tc := range tests {
		got := shouldExclude("", fakeEntry{name: tc.name, isDir: tc.isDir})
		if got != tc.expected {
			t.Fatalf("shouldExclude(%q, isDir=%v) = %v, want %v", tc.name, tc.isDir, got, tc.expected)
		}
	}
}

func TestCreateTarGz(t *testing.T) {
	projectDir := makeTempProject(t)

	archivePath, fileCount, err := createTarGz(projectDir)
	if err != nil {
		t.Fatalf("createTarGz failed: %v", err)
	}
	defer os.Remove(archivePath)

	t.Run("includes_expected_files", func(t *testing.T) {
		entries := readTarEntries(t, archivePath)
		expected := []string{"README.md", "public/style.css", "src/app.go", "src/index.js"}
		sort.Strings(expected)

		if !reflect.DeepEqual(entries, expected) {
			t.Fatalf("archive entries mismatch\ngot:  %v\nwant: %v", entries, expected)
		}
	})

	t.Run("excludes_excluded_paths", func(t *testing.T) {
		entries := readTarEntries(t, archivePath)
		for _, entry := range entries {
			if strings.HasPrefix(entry, "node_modules/") ||
				strings.HasPrefix(entry, ".git/") ||
				strings.HasPrefix(entry, "dist/") ||
				entry == ".DS_Store" ||
				entry == "server.log" ||
				entry == ".env" ||
				entry == ".env.local" {
				t.Fatalf("unexpected excluded entry in archive: %s", entry)
			}
		}
	})

	t.Run("file_count_matches", func(t *testing.T) {
		if fileCount != 4 {
			t.Fatalf("fileCount = %d, want 4", fileCount)
		}
	})

	t.Run("archive_is_valid_gzip", func(t *testing.T) {
		file, err := os.Open(archivePath)
		if err != nil {
			t.Fatalf("open archive failed: %v", err)
		}
		defer file.Close()

		header := make([]byte, 2)
		if _, err := io.ReadFull(file, header); err != nil {
			t.Fatalf("read gzip header failed: %v", err)
		}

		if header[0] != 0x1f || header[1] != 0x8b {
			t.Fatalf("invalid gzip magic bytes: %#x %#x", header[0], header[1])
		}
	})

	t.Run("archive_cleaned_up_on_error", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)

		_, _, err := createTarGz(filepath.Join(tmp, "does-not-exist"))
		if err == nil {
			t.Fatal("expected error for missing root path")
		}

		matches, err := filepath.Glob(filepath.Join(tmp, "trapiche-*.tar.gz"))
		if err != nil {
			t.Fatalf("glob failed: %v", err)
		}
		if len(matches) != 0 {
			t.Fatalf("expected no leftover temp archives, found: %v", matches)
		}
	})
}

func TestPrintNewLogs(t *testing.T) {
	tests := []struct {
		name       string
		allLogs    string
		before     int
		wantOut    string
		wantLength int
	}{
		{name: "nothing to print", allLogs: "", before: 0, wantOut: "", wantLength: 0},
		{name: "first chunk", allLogs: "hello", before: 0, wantOut: "hello", wantLength: 5},
		{name: "incremental", allLogs: "hello world", before: 5, wantOut: " world", wantLength: 11},
		{name: "no new content", allLogs: "hello world", before: 11, wantOut: "", wantLength: 11},
		{name: "reset (shorter)", allLogs: "hi", before: 11, wantOut: "hi", wantLength: 2},
	}

	for _, tc := range tests {
		printedLen := tc.before
		out := captureOutput(func() {
			printNewLogs(tc.allLogs, &printedLen)
		})

		if out != tc.wantOut {
			t.Fatalf("%s: output = %q, want %q", tc.name, out, tc.wantOut)
		}
		if printedLen != tc.wantLength {
			t.Fatalf("%s: printedLen = %d, want %d", tc.name, printedLen, tc.wantLength)
		}
	}
}

func TestApiPath(t *testing.T) {
	original := apiBase
	defer func() { apiBase = original }()

	tests := []struct {
		base string
		path string
		want string
	}{
		{base: "https://example.com", path: "/api/deploy", want: "https://example.com/api/deploy"},
		{base: "https://example.com/", path: "/api/deploy", want: "https://example.com/api/deploy"},
		{base: "https://example.com///", path: "/api/deploy", want: "https://example.com/api/deploy"},
	}

	for _, tc := range tests {
		apiBase = tc.base
		got := apiPath(tc.path)
		if got != tc.want {
			t.Fatalf("apiPath(%q) with base %q = %q, want %q", tc.path, tc.base, got, tc.want)
		}
	}
}

func TestQueueAnonymousDeploy(t *testing.T) {
	archivePath := mustMakeTinyArchive(t)
	original := apiBase
	defer func() { apiBase = original }()

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"dep_abc","name":"brave-wolf-1234","url":"https://brave-wolf-1234.trapiche.site"}`))
		}))
		defer srv.Close()

		apiBase = srv.URL
		got, err := queueAnonymousDeploy(archivePath)
		if err != nil {
			t.Fatalf("queueAnonymousDeploy failed: %v", err)
		}
		if got.ID != "dep_abc" || got.Name != "brave-wolf-1234" || got.URL != "https://brave-wolf-1234.trapiche.site" {
			t.Fatalf("unexpected response: %#v", got)
		}
	})

	t.Run("missing_id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"name":"x","url":"y"}`))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := queueAnonymousDeploy(archivePath)
		if err == nil || !strings.Contains(err.Error(), "missing deployment id") {
			t.Fatalf("expected missing id error, got: %v", err)
		}
	})

	t.Run("rate_limited", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limit exceeded"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := queueAnonymousDeploy(archivePath)
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 error, got: %v", err)
		}
	})

	t.Run("server_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := queueAnonymousDeploy(archivePath)
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("expected 500 error, got: %v", err)
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("not-json"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := queueAnonymousDeploy(archivePath)
		if err == nil || !strings.Contains(err.Error(), "failed to parse") {
			t.Fatalf("expected parse error, got: %v", err)
		}
	})

	t.Run("multipart_field_received", func(t *testing.T) {
		handlerResult := make(chan error, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				handlerResult <- err
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			file, _, err := r.FormFile("archive")
			if err != nil {
				handlerResult <- err
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = file.Close()
			handlerResult <- nil

			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"dep_field","name":"ok","url":"https://ok.trapiche.site"}`))
		}))
		defer srv.Close()

		apiBase = srv.URL
		if _, err := queueAnonymousDeploy(archivePath); err != nil {
			t.Fatalf("queueAnonymousDeploy failed: %v", err)
		}

		if err := <-handlerResult; err != nil {
			t.Fatalf("handler validation failed: %v", err)
		}
	})
}

func TestGetAnonymousDeployStatus(t *testing.T) {
	original := apiBase
	defer func() { apiBase = original }()

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"dep_1","name":"app","status":"building","url":"https://app.trapiche.site","expiresAt":"2026-01-01T00:00:00Z","logs":"line1"}`))
		}))
		defer srv.Close()

		apiBase = srv.URL
		resp, err := getAnonymousDeployStatus("dep_1")
		if err != nil {
			t.Fatalf("getAnonymousDeployStatus failed: %v", err)
		}
		if resp.ID != "dep_1" || resp.Name != "app" || resp.Status != "building" || resp.URL != "https://app.trapiche.site" || resp.Logs != "line1" {
			t.Fatalf("unexpected response: %#v", resp)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := getAnonymousDeployStatus("dep_404")
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("expected 404 error, got: %v", err)
		}
	})

	t.Run("server_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("oops"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := getAnonymousDeployStatus("dep_500")
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("expected 500 error, got: %v", err)
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		_, err := getAnonymousDeployStatus("dep_bad")
		if err == nil || !strings.Contains(err.Error(), "failed to parse") {
			t.Fatalf("expected parse error, got: %v", err)
		}
	})
}

func TestPollUntilDone(t *testing.T) {
	original := apiBase
	defer func() { apiBase = original }()

	t.Run("deployed_immediately", func(t *testing.T) {
		srv := httptest.NewServer(serveStatusSequence([]deployStatusResponse{
			{Status: "deployed", URL: "https://x.trapiche.site"},
		}))
		defer srv.Close()

		apiBase = srv.URL
		var err error
		out := captureOutput(func() {
			err = pollUntilDone(context.Background(), "dep", "https://fallback.trapiche.site")
		})
		if err != nil {
			t.Fatalf("pollUntilDone failed: %v", err)
		}
		if !strings.Contains(out, "https://x.trapiche.site") {
			t.Fatalf("expected output to contain response URL, got: %q", out)
		}
	})

	t.Run("failed_immediately", func(t *testing.T) {
		srv := httptest.NewServer(serveStatusSequence([]deployStatusResponse{
			{Status: "failed"},
		}))
		defer srv.Close()

		apiBase = srv.URL
		err := pollUntilDone(context.Background(), "dep", "https://fallback.trapiche.site")
		if err == nil || !strings.Contains(err.Error(), "deployment failed") {
			t.Fatalf("expected deployment failed error, got: %v", err)
		}
	})

	t.Run("deployed_after_building", func(t *testing.T) {
		srv := httptest.NewServer(serveStatusSequence([]deployStatusResponse{
			{Status: "building", Logs: "line1\n"},
			{Status: "building", Logs: "line1\nline2\n"},
			{Status: "deployed", URL: "https://done.trapiche.site", Logs: "line1\nline2\n"},
		}))
		defer srv.Close()

		apiBase = srv.URL
		start := time.Now()
		err := pollUntilDone(context.Background(), "dep", "https://fallback.trapiche.site")
		if err != nil {
			t.Fatalf("pollUntilDone failed: %v", err)
		}
		if time.Since(start) < 3500*time.Millisecond {
			t.Fatalf("expected polling delay around 4s, got %v", time.Since(start))
		}
	})

	t.Run("context_cancelled", func(t *testing.T) {
		srv := httptest.NewServer(serveStatusSequence([]deployStatusResponse{
			{Status: "building"},
		}))
		defer srv.Close()

		apiBase = srv.URL
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := pollUntilDone(ctx, "dep", "https://fallback.trapiche.site")
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("expected timeout error, got: %v", err)
		}
	})

	t.Run("poll_error_propagated", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}))
		defer srv.Close()

		apiBase = srv.URL
		err := pollUntilDone(context.Background(), "dep", "https://fallback.trapiche.site")
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("expected 500 error, got: %v", err)
		}
	})

	t.Run("uses_response_url", func(t *testing.T) {
		srv := httptest.NewServer(serveStatusSequence([]deployStatusResponse{
			{Status: "deployed", URL: "https://response.trapiche.site"},
		}))
		defer srv.Close()

		apiBase = srv.URL
		var err error
		out := captureOutput(func() {
			err = pollUntilDone(context.Background(), "dep", "https://default.trapiche.site")
		})
		if err != nil {
			t.Fatalf("pollUntilDone failed: %v", err)
		}
		if !strings.Contains(out, "https://response.trapiche.site") {
			t.Fatalf("expected response URL in output, got: %q", out)
		}
		if strings.Contains(out, "https://default.trapiche.site") {
			t.Fatalf("did not expect default URL in output, got: %q", out)
		}
	})

	t.Run("falls_back_to_default_url", func(t *testing.T) {
		srv := httptest.NewServer(serveStatusSequence([]deployStatusResponse{
			{Status: "deployed", URL: ""},
		}))
		defer srv.Close()

		apiBase = srv.URL
		var err error
		out := captureOutput(func() {
			err = pollUntilDone(context.Background(), "dep", "https://default.trapiche.site")
		})
		if err != nil {
			t.Fatalf("pollUntilDone failed: %v", err)
		}
		if !strings.Contains(out, "https://default.trapiche.site") {
			t.Fatalf("expected fallback URL in output, got: %q", out)
		}
	})
}

func TestRunDeploy(t *testing.T) {
	original := apiBase
	defer func() { apiBase = original }()

	t.Run("nonexistent_dir", func(t *testing.T) {
		err := runDeploy("/does/not/exist")
		if err == nil || !strings.Contains(err.Error(), "failed to read directory") {
			t.Fatalf("expected read directory error, got: %v", err)
		}
	})

	t.Run("path_is_file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "file.txt")
		if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
			t.Fatalf("write file failed: %v", err)
		}

		err := runDeploy(filePath)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("expected not a directory error, got: %v", err)
		}
	})

	t.Run("success_full_flow", func(t *testing.T) {
		projectDir := makeTempProject(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/api/anonymous/deploy":
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"id":"dep_ok","name":"happy-river-3821","url":"https://happy-river-3821.trapiche.site"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/api/anonymous/deploy/dep_ok":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"dep_ok","name":"happy-river-3821","status":"deployed","url":"https://happy-river-3821.trapiche.site","logs":"done"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		apiBase = srv.URL
		var err error
		out := captureOutput(func() {
			err = runDeploy(projectDir)
		})
		if err != nil {
			t.Fatalf("runDeploy failed: %v", err)
		}
		if !strings.Contains(out, "trapiche.site") {
			t.Fatalf("expected trapiche.site in output, got: %q", out)
		}
	})

	t.Run("server_rejects_upload", func(t *testing.T) {
		projectDir := makeTempProject(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/anonymous/deploy" {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("rate limit exceeded"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		apiBase = srv.URL
		err := runDeploy(projectDir)
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("expected 429 error, got: %v", err)
		}
	})
}
