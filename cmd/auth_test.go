package cmd

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupCredsFile points the credentials path to a temp file with a token.
func setupCredsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	orig := credentialsPath
	t.Cleanup(func() { credentialsPath = orig })
	credentialsPath = func() (string, error) { return path, nil }
	if err := saveCredentials(&credentials{Token: "trp_testtoken", Username: "ana"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func useAPI(t *testing.T, url string) {
	t.Helper()
	orig := apiBase
	t.Cleanup(func() { apiBase = orig })
	apiBase = url
}

func TestLogoutRevokesAndClears(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusUnauthorized} {
		path := setupCredsFile(t)
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.Method != http.MethodDelete || r.URL.Path != "/api/auth/cli/token" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer trp_testtoken" {
				t.Errorf("missing auth header")
			}
			w.WriteHeader(status)
		}))
		useAPI(t, srv.URL)

		var err error
		out := captureOutput(func() { err = runAuthLogout(false) })
		srv.Close()
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if !called {
			t.Fatalf("status %d: server not called", status)
		}
		if !strings.Contains(out, "Token revoked.") || !strings.Contains(out, "Logged out.") {
			t.Fatalf("status %d: out = %q", status, out)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("status %d: credentials file still there", status)
		}
	}
}

func TestLogoutKeepsFileWhenRevokeFails(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		path := setupCredsFile(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		useAPI(t, srv.URL)

		var err error
		out := captureOutput(func() { err = runAuthLogout(false) })
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "could not revoke") {
			t.Fatalf("status %d: expected revoke error, got %v", status, err)
		}
		if strings.Contains(out, "Token revoked.") || strings.Contains(out, "Logged out.") {
			t.Fatalf("status %d: false success message: %q", status, out)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("status %d: credentials file was removed", status)
		}
	}
}

func TestLogoutServerDownKeepsFile(t *testing.T) {
	path := setupCredsFile(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	useAPI(t, srv.URL)
	srv.Close() // server is down

	if err := runAuthLogout(false); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("credentials file was removed")
	}
}

func TestLogoutForceClearsFile(t *testing.T) {
	path := setupCredsFile(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	var err error
	out := captureOutput(func() { err = runAuthLogout(true) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Token revoked.") {
		t.Fatalf("false success message: %q", out)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("credentials file still there")
	}
}

func TestLogoutSendsExpiredToken(t *testing.T) {
	path := setupCredsFile(t)
	expired := &credentials{Token: "trp_testtoken", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := saveCredentials(expired); err != nil {
		t.Fatal(err)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	if err := runAuthLogout(false); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expired token was not sent for revocation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("credentials file still there")
	}
}

func TestLogoutWithoutCredentials(t *testing.T) {
	orig := credentialsPath
	defer func() { credentialsPath = orig }()
	credentialsPath = func() (string, error) {
		return filepath.Join(t.TempDir(), "missing.json"), nil
	}
	if err := runAuthLogout(false); err != nil {
		t.Fatal(err)
	}
}

func TestStatusVerifiesToken(t *testing.T) {
	setupCredsFile(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// Same shape as the real API: the user model has no JSON tags.
		_, _ = w.Write([]byte(`{"user":{"ID":"u1","Username":"bob","Plan":"free"},"hasGithubConnected":false}`))
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	var err error
	out := captureOutput(func() { err = runAuthStatus() })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Logged in as bob") {
		t.Fatalf("out = %q", out)
	}
}

func TestStatusRejectedToken(t *testing.T) {
	setupCredsFile(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	err := runAuthStatus()
	if err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("expected invalid token error, got %v", err)
	}
}

func TestStatusServerDownFails(t *testing.T) {
	setupCredsFile(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	useAPI(t, srv.URL)
	srv.Close()

	err := runAuthStatus()
	if err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("expected verify error, got %v", err)
	}
}

func TestStatusUnexpectedBodyFails(t *testing.T) {
	setupCredsFile(t)
	for _, body := range []string{`not json`, `{}`, `{"user":{}}`, `{"user":{"Username":""}}`, `{"username":"flat"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		useAPI(t, srv.URL)

		var err error
		out := captureOutput(func() { err = runAuthStatus() })
		srv.Close()
		if err == nil {
			t.Fatalf("body %q: expected error", body)
		}
		if strings.Contains(out, "Logged in as") {
			t.Fatalf("body %q: false success: %q", body, out)
		}
	}
}

func TestStatusServerErrorFails(t *testing.T) {
	setupCredsFile(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	if err := runAuthStatus(); err == nil {
		t.Fatal("expected error")
	}
}

func TestStatusWithoutCredentialsFails(t *testing.T) {
	orig := credentialsPath
	defer func() { credentialsPath = orig }()
	credentialsPath = func() (string, error) {
		return filepath.Join(t.TempDir(), "missing.json"), nil
	}
	err := runAuthStatus()
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected not logged in error, got %v", err)
	}
}

func TestUploadArchiveStreamsFileBytes(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	payload := make([]byte, 3<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "big.tar.gz")
	if err := os.WriteFile(archivePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength <= int64(len(payload)) {
			t.Errorf("content length %d not set", r.ContentLength)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("repoURL") != "https://github.com/o/r" {
			t.Errorf("repoURL = %q", r.FormValue("repoURL"))
		}
		f, _, err := r.FormFile("archive")
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		got, _ := io.ReadAll(f)
		if !bytes.Equal(got, payload) {
			t.Errorf("archive bytes differ (%d vs %d)", len(got), len(payload))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"dep_1"}`))
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	body, err := uploadArchive(creds, "/api/deployments/upload", [][2]string{{"repoURL", "https://github.com/o/r"}}, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "dep_1") {
		t.Fatalf("body = %s", body)
	}
}

func TestUploadArchiveReportsAPIError(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	archivePath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archivePath, []byte{0x1f, 0x8b}, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"limit reached"}`))
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	_, err := uploadArchive(creds, "/api/deployments/upload", nil, archivePath)
	if err == nil || !isHTTPStatus(err, http.StatusPaymentRequired) {
		t.Fatalf("expected 402 error, got %v", err)
	}
}
