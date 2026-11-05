package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeProjectFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeDeployType(t *testing.T) {
	cases := map[string]string{
		"":          deployTypeAuto,
		"auto":      deployTypeAuto,
		" Static ":  deployTypeStatic,
		"ssr":       deployTypeSSR,
		"container": deployTypeSSR,
	}
	for raw, want := range cases {
		got, err := normalizeDeployType(raw)
		if err != nil || got != want {
			t.Fatalf("normalizeDeployType(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := normalizeDeployType("docker"); err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestDetectDeployType(t *testing.T) {
	t.Run("dockerfile", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "Dockerfile", "FROM node")
		got, reason := detectDeployType(dir)
		if got != deployTypeSSR || !strings.Contains(reason, "Dockerfile") {
			t.Fatalf("got %q (%s)", got, reason)
		}
	})
	t.Run("express", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"express":"^4.0.0"}}`)
		got, reason := detectDeployType(dir)
		if got != deployTypeSSR || !strings.Contains(reason, "express") {
			t.Fatalf("got %q (%s)", got, reason)
		}
	})
	t.Run("express in a comment-like string is not a dependency", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"name":"express-clone-ui","dependencies":{"react":"^18"}}`)
		if got, _ := detectDeployType(dir); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("plain html", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "index.html", "<h1>hi</h1>")
		got, _ := detectDeployType(dir)
		if got != deployTypeStatic {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("not clear", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"devDependencies":{"vite":"^5"}}`)
		if got, _ := detectDeployType(dir); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("empty folder", func(t *testing.T) {
		if got, _ := detectDeployType(t.TempDir()); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestResolveDeployType(t *testing.T) {
	cases := []struct {
		name                string
		requested, detected string
		saved               string
		isUpdate            bool
		want                string
		wantErrContains     string
	}{
		{name: "new, auto, detected", requested: "auto", detected: "ssr", want: "ssr"},
		{name: "new, auto, not clear", requested: "auto", want: "auto"},
		{name: "new, flag wins over detection", requested: "static", detected: "ssr", want: "static"},
		{name: "update uses saved type", requested: "auto", detected: "static", saved: "ssr", isUpdate: true, want: "ssr"},
		{name: "update, same flag", requested: "ssr", saved: "ssr", isUpdate: true, want: "ssr"},
		{name: "update, other flag fails", requested: "static", saved: "ssr", isUpdate: true, wantErrContains: "--new"},
		// An older trapiche.json has no type. Detection must not change a deployment
		// (a static app that later got a Dockerfile must not be sent as a container).
		{name: "update, saved unknown, detected ssr: send nothing", requested: "auto", detected: "ssr", isUpdate: true, want: ""},
		{name: "update, saved unknown, flag is sent", requested: "static", detected: "ssr", isUpdate: true, want: "static"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDeployType(tc.requested, tc.detected, tc.saved, tc.isUpdate)
			if tc.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("expected error with %q, got %v", tc.wantErrContains, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDockerfilePortWarning(t *testing.T) {
	cases := []struct {
		name       string
		dockerfile string
		wantWarn   bool
	}{
		{"port 3000", "FROM node\nEXPOSE 3000\n", false},
		{"port 3000 tcp", "FROM node\nEXPOSE 3000/tcp\n", false},
		{"several ports with 3000", "FROM node\nEXPOSE 8080 3000\n", false},
		{"other port", "FROM node\nEXPOSE 8080\n", true},
		{"lowercase", "FROM node\nexpose 5000\n", true},
		{"no expose", "FROM node\nCMD [\"node\",\"x.js\"]\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectFile(t, dir, "Dockerfile", tc.dockerfile)
			got := dockerfilePortWarning(dir)
			if (got != "") != tc.wantWarn {
				t.Fatalf("warning = %q, want warning: %v", got, tc.wantWarn)
			}
		})
	}
	if got := dockerfilePortWarning(t.TempDir()); got != "" {
		t.Fatalf("no Dockerfile should give no warning, got %q", got)
	}
}

func TestUploadSendsDeploymentType(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	archivePath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archivePath, []byte{0x1f, 0x8b}, 0o644); err != nil {
		t.Fatal(err)
	}

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		seen = r.FormValue("deploymentType")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", Name: "app", DeploymentType: "ssr"})
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	got, err := queueAuthenticatedDeploy(creds, archivePath, "https://github.com/o/r", "app", "main", "ssr")
	if err != nil {
		t.Fatal(err)
	}
	if seen != "ssr" || got.DeploymentType != "ssr" {
		t.Fatalf("seen = %q, response type = %q", seen, got.DeploymentType)
	}

	seen = "unset"
	if _, err := queueAuthenticatedDeployUpdate(creds, archivePath, "dep_1", "https://github.com/o/r", "main", ""); err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Fatalf("empty type must send no field, got %q", seen)
	}
}

func TestFollowBuildLogsPrintsUntilBuildEnds(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		dep := deploymentResponse{ID: "dep_1", DeploymentType: "ssr"}
		switch n {
		case 1:
			dep.Status, dep.Logs = "building", "cloning...\n"
		case 2:
			dep.Status, dep.Logs = "building", "cloning...\nbuilding image...\n"
		default:
			dep.Status, dep.Logs = "deployed", "cloning...\nbuilding image...\nrunning\n"
		}
		_ = json.NewEncoder(w).Encode(dep)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	out := captureOutput(func() {
		err = followBuildLogs(ctx, creds, "dep_1")
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "cloning...\nbuilding image...\nrunning\n" {
		t.Fatalf("out = %q", out)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestShowDeploymentLogsUsesBuildLogForBuildingContainer(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs/stream") {
			t.Errorf("runtime log stream must not be used while a container builds")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(deploymentResponse{
			ID: "dep_1", DeploymentType: "ssr", Status: "building", Logs: "building image...\n",
		})
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	var err error
	out := captureOutput(func() {
		err = showDeploymentLogs(context.Background(), creds, "dep_1", true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "building image...\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestShowDeploymentLogsStreamsForStaticAndRunningContainer(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	cases := []deploymentResponse{
		{ID: "dep_1", DeploymentType: "static", Status: "building"},
		{ID: "dep_1", DeploymentType: "ssr", Status: "deployed"},
	}
	for _, dep := range cases {
		streamed := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/logs/stream") {
				streamed = true
				_, _ = w.Write([]byte("data: hello\n\nevent: close\ndata: stream ended\n\n"))
				return
			}
			_ = json.NewEncoder(w).Encode(dep)
		}))
		useAPI(t, srv.URL)

		var err error
		captureOutput(func() {
			err = showDeploymentLogs(context.Background(), creds, "dep_1", false)
		})
		srv.Close()
		if err != nil {
			t.Fatalf("%s/%s: %v", dep.DeploymentType, dep.Status, err)
		}
		if !streamed {
			t.Fatalf("%s/%s: log stream was not used", dep.DeploymentType, dep.Status)
		}
	}
}

// When the type is unknown, the CLI must not use the runtime log stream.
func TestPollDeploymentUnknownTypeFollowsBuildLog(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/logs/stream") {
			t.Errorf("log stream used for unknown type")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		dep := deploymentResponse{ID: "dep_1", Status: "building", Logs: "step 1\n"}
		if atomic.AddInt32(&calls, 1) > 1 {
			dep.Status, dep.Logs, dep.URL = "deployed", "step 1\nstep 2\n", "https://app.example.com"
		}
		_ = json.NewEncoder(w).Encode(dep)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	out := captureOutput(func() {
		err = pollDeploymentUntilDone(ctx, creds, "dep_1", "https://fallback.example.com", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "step 2") || !strings.Contains(out, "https://app.example.com") {
		t.Fatalf("out = %q", out)
	}
}

// During a build, only a known static deployment may use the log stream.
func TestShowDeploymentLogsUnknownTypeBuildingUsesRecord(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	for _, typ := range []string{"", "something-new"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/logs/stream") {
				t.Errorf("type %q: log stream used while building", typ)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(deploymentResponse{
				ID: "dep_1", DeploymentType: typ, Status: "queued", Logs: "waiting...\n",
			})
		}))
		useAPI(t, srv.URL)

		var err error
		out := captureOutput(func() {
			err = showDeploymentLogs(context.Background(), creds, "dep_1", true)
		})
		srv.Close()
		if err != nil {
			t.Fatalf("type %q: %v", typ, err)
		}
		if out != "waiting...\n" {
			t.Fatalf("type %q: out = %q", typ, out)
		}
	}
}

func TestShowDeploymentLogsReturnsLookupError(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	err := showDeploymentLogs(context.Background(), creds, "dep_missing", false)
	if err == nil || !isHTTPStatus(err, http.StatusNotFound) {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestDetectDeployTypeNext(t *testing.T) {
	t.Run("server", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0","react":"18"}}`)
		got, reason := detectDeployType(dir)
		if got != deployTypeSSR || !strings.Contains(reason, "Next.js") {
			t.Fatalf("got %q (%s)", got, reason)
		}
	})
	for _, cfg := range []string{
		"module.exports = { output: 'export' }",
		`export default { output: "export" }`,
	} {
		t.Run("static export: "+cfg, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
			writeProjectFile(t, dir, "next.config.mjs", cfg)
			got, _ := detectDeployType(dir)
			if got != deployTypeStatic {
				t.Fatalf("got %q", got)
			}
		})
	}
	t.Run("next with express is a server", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0","express":"4"}}`)
		writeProjectFile(t, dir, "next.config.js", "module.exports = { output: 'export' }")
		if got, _ := detectDeployType(dir); got != deployTypeSSR {
			t.Fatalf("got %q", got)
		}
	})
}

func TestFillDeploymentType(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}

	t.Run("response type wins", func(t *testing.T) {
		dep := &deploymentResponse{ID: "dep_1", DeploymentType: "static"}
		fillDeploymentType(creds, dep, "ssr")
		if dep.DeploymentType != "static" {
			t.Fatalf("type = %q", dep.DeploymentType)
		}
	})

	t.Run("uses the type that was sent when the reply has none", func(t *testing.T) {
		dep := &deploymentResponse{ID: "dep_1"}
		fillDeploymentType(creds, dep, "ssr")
		if dep.DeploymentType != "ssr" {
			t.Fatalf("type = %q", dep.DeploymentType)
		}
	})

	t.Run("asks the server when no clear type was sent", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/deployments/dep_1" {
				t.Errorf("path = %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", DeploymentType: "ssr"})
		}))
		defer srv.Close()
		useAPI(t, srv.URL)

		for _, sent := range []string{"", deployTypeAuto} {
			dep := &deploymentResponse{ID: "dep_1"}
			fillDeploymentType(creds, dep, sent)
			if dep.DeploymentType != "ssr" {
				t.Fatalf("sent %q: type = %q", sent, dep.DeploymentType)
			}
		}
	})

	t.Run("server error leaves the type empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		useAPI(t, srv.URL)

		dep := &deploymentResponse{ID: "dep_1"}
		fillDeploymentType(creds, dep, deployTypeAuto)
		if dep.DeploymentType != "" {
			t.Fatalf("type = %q", dep.DeploymentType)
		}
	})
}

// A redeploy whose reply leaves out the type must keep the saved type in trapiche.json.
func TestRedeployKeepsSavedTypeWhenReplyOmitsIt(t *testing.T) {
	dir := t.TempDir()
	creds := &credentials{Token: "trp_testtoken"}
	archivePath := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(archivePath, []byte{0x1f, 0x8b}, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", Name: "api"}) // no type
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	saved := deployTypeSSR
	got, err := queueAuthenticatedDeployUpdate(creds, archivePath, "dep_1", "https://github.com/o/r", "main", saved)
	if err != nil {
		t.Fatal(err)
	}
	fillDeploymentType(creds, got, saved)
	if err := refreshProjectConfig(dir, got, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeploymentType != "ssr" {
		t.Fatalf("saved type was lost: %#v", cfg)
	}
}

func TestProjectConfigKeepsDeploymentType(t *testing.T) {
	dir := t.TempDir()
	dep := &deploymentResponse{ID: "dep_1", Name: "api", DeploymentType: "ssr"}
	if err := refreshProjectConfig(dir, dep, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeploymentType != "ssr" {
		t.Fatalf("cfg = %#v", cfg)
	}
}
