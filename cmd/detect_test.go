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
		{"builder 3000, final 80", "FROM node AS build\nEXPOSE 3000\nFROM nginx\nEXPOSE 80\n", true},
		{"builder 80, final 3000", "FROM node AS build\nEXPOSE 80\nFROM node\nEXPOSE 3000\n", false},
		{"final stage inherits 3000", "FROM node AS base\nEXPOSE 3000\nFROM base\nCMD [\"x\"]\n", false},
		{"final stage inherits 8080", "FROM --platform=linux/amd64 node AS Base\nEXPOSE 8080\nFROM base AS run\n", true},
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

	var (
		err   error
		final *deploymentResponse
	)
	out := captureOutput(func() {
		final, err = pollDeploymentUntilDone(ctx, creds, "dep_1", "https://fallback.example.com", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "step 2") || !strings.Contains(out, "https://app.example.com") {
		t.Fatalf("out = %q", out)
	}
	if final == nil || final.URL != "https://app.example.com" {
		t.Fatalf("final = %#v", final)
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

func TestStripJSComments(t *testing.T) {
	cases := map[string]string{
		"a // gone\nb":               "a \nb",
		"a /* gone */ b":             "a   b",
		"x /* multi\nline */ y":      "x   y",
		`u = "https://x.test" // c`:  `u = "https://x.test" `,
		`s = '// not a comment'`:     `s = '// not a comment'`,
		"t = `/* kept */`":           "t = `/* kept */`",
		`q = "a \" // still string"`: `q = "a \" // still string"`,
		"no comment at all":          "no comment at all",
		"unterminated /* comment":    "unterminated  ",
	}
	for in, want := range cases {
		if got := stripJSComments(in); got != want {
			t.Fatalf("stripJSComments(%q) = %q, want %q", in, got, want)
		}
	}
}

// A line that only mentions output: 'export' in a comment must not make the app static.
// The type is saved and cannot change, so a wrong guess is costly.
func TestNextStaticExportIgnoresComments(t *testing.T) {
	configs := []string{
		"// output: 'export'\nmodule.exports = {}",
		"module.exports = {\n  // output: 'export', // uncomment for GitHub Pages\n}",
		"/* output: \"export\" */\nmodule.exports = {}",
		"/*\n * Docs: set output: 'export' to build a static site.\n */\nexport default {}",
	}
	for _, cfg := range configs {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, "next.config.js", cfg)
		got, _ := detectDeployType(dir)
		if got != deployTypeSSR {
			t.Fatalf("config %q: got %q, want ssr", cfg, got)
		}
	}
}

func TestNextStaticExportAllConfigNames(t *testing.T) {
	for _, name := range []string{
		"next.config.js", "next.config.mjs", "next.config.ts", "next.config.mts",
	} {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, name, "const site = \"https://example.com\"; // docs\nexport default { output : \"export\" }")
		got, _ := detectDeployType(dir)
		if got != deployTypeStatic {
			t.Fatalf("%s: got %q, want static", name, got)
		}
	}
}

func TestContainerPortHint(t *testing.T) {
	t.Run("no Dockerfile", func(t *testing.T) {
		if hint := containerPortHint(t.TempDir()); !strings.Contains(hint, "3000") {
			t.Fatalf("hint = %q", hint)
		}
	})
	t.Run("Dockerfile without EXPOSE", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "Dockerfile", "FROM node\nCMD [\"node\",\"x.js\"]\n")
		if hint := containerPortHint(dir); !strings.Contains(hint, "3000") {
			t.Fatalf("hint = %q", hint)
		}
	})
	t.Run("Dockerfile with EXPOSE $PORT", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "Dockerfile", "FROM node\nEXPOSE $PORT\n")
		if hint := containerPortHint(dir); !strings.Contains(hint, "3000") {
			t.Fatalf("hint = %q", hint)
		}
	})
	t.Run("Dockerfile with a number: the exact warning is used", func(t *testing.T) {
		for _, expose := range []string{"EXPOSE 3000", "EXPOSE 8080"} {
			dir := t.TempDir()
			writeProjectFile(t, dir, "Dockerfile", "FROM node\n"+expose+"\n")
			if hint := containerPortHint(dir); hint != "" {
				t.Fatalf("%s: hint = %q", expose, hint)
			}
		}
	})
}

// Next.js loads one config file. The first that exists in nextConfigNames order counts.
func TestNextOnlyTheLoadedConfigFileCounts(t *testing.T) {
	const withExport = "export default { output: 'export' }"
	const withoutExport = "export default { reactStrictMode: true }"

	cases := []struct {
		name string
		js   string
		ts   string
		want string
	}{
		{"js loaded, no export; leftover ts has it", withoutExport, withExport, deployTypeSSR},
		{"js loaded, has export; ts has none", withExport, withoutExport, deployTypeStatic},
	}
	t.Run("cjs is not loaded by Next.js", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, "next.config.cjs", withoutExport)
		writeProjectFile(t, dir, "next.config.ts", withExport)
		if got, _ := detectDeployType(dir); got != deployTypeStatic {
			t.Fatalf("got %q, want static", got)
		}
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
			writeProjectFile(t, dir, "next.config.js", tc.js)
			writeProjectFile(t, dir, "next.config.ts", tc.ts)
			if got, _ := detectDeployType(dir); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNextConditionalOutputExportIsNotStatic(t *testing.T) {
	ssrConfigs := []string{
		"module.exports = {\n  ...(process.env.PAGES ? { output: 'export' } : { output: 'standalone' }),\n}",
		"module.exports = {\n  ...(process.env.PAGES && { output: 'export' }),\n}",
		"const config = process.env.PAGES ? { output: 'export' } : {}\nexport default config",
		"export default { images: { output: 'export' } }",
		"module.exports = (phase) => {\n  if (process.env.PAGES) return { output: 'export' }\n  return {}\n}",
		"module.exports = () => {\n  if (process.env.PAGES) {\n    return { output: 'export' }\n  }\n  return {}\n}",
		"let config = {}\nif (process.env.PAGES) config = { output: 'export' };\nmodule.exports = config",
		"module.exports = () => {\n  if (a) return {}\n  else return { output: 'export' }\n}",
		"module.exports = () => {\n  switch (process.env.MODE) {\n    case 'pages':\n      return { output: 'export' }\n  }\n  return {}\n}",
		"let config = {}\nfor (const k of keys) config = { output: 'export' }\nmodule.exports = config",
	}
	for _, cfg := range ssrConfigs {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, "next.config.js", cfg)
		if got, _ := detectDeployType(dir); got != deployTypeSSR {
			t.Fatalf("config %q: got %q, want ssr", cfg, got)
		}
	}

	staticConfigs := []string{
		"module.exports = withBundleAnalyzer({ output: 'export' })",
		"const nextConfig: NextConfig = {\n  reactStrictMode: true,\n  output: 'export',\n}\nexport default nextConfig",
		"module.exports = { env: { A: process.env.A ? 'x' : 'y' }, output: 'export' }",
		"module.exports = (phase) => {\n  return { output: 'export' }\n}",
		"function config() {\n  return { output: 'export' }\n}\nmodule.exports = config",
		"module.exports = (phase) => {\n  if (phase === 'dev') return { reactStrictMode: true }\n  return { output: 'export' }\n}",
		"module.exports = (phase) => {\n  if (phase === 'dev') { return {} }\n  return { output: 'export' }\n}",
		"if (process.env.ANALYZE) require('x');\nmodule.exports = { output: 'export' }",
	}
	for _, cfg := range staticConfigs {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, "next.config.js", cfg)
		if got, _ := detectDeployType(dir); got != deployTypeStatic {
			t.Fatalf("config %q: got %q, want static", cfg, got)
		}
	}
}

func TestShowDeploymentLogsFailedContainerPrintsBuildLog(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	for _, noFollow := range []bool{true, false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/logs/stream") {
				t.Errorf("runtime log stream must not be used for a container that failed to build")
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(deploymentResponse{
				ID: "dep_1", DeploymentType: "ssr", Status: "failed", Logs: "step 3 failed: npm ERR!\n",
			})
		}))
		useAPI(t, srv.URL)

		var err error
		out := captureOutput(func() {
			err = showDeploymentLogs(context.Background(), creds, "dep_1", noFollow)
		})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if out != "step 3 failed: npm ERR!\n" {
			t.Fatalf("noFollow=%v: out = %q", noFollow, out)
		}
	}
}

func TestNextStaticExportIgnoresTextInStrings(t *testing.T) {
	ssrConfigs := []string{
		`const note = "output: 'export'"; module.exports = {}`,
		"const note = `set output: 'export' for GitHub Pages`;\nexport default {}",
		"const output = 'export'; export default {}",
		"export default { output: 'standalone' }",
	}
	for _, cfg := range ssrConfigs {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, "next.config.js", cfg)
		if got, _ := detectDeployType(dir); got != deployTypeSSR {
			t.Fatalf("config %q: got %q, want ssr", cfg, got)
		}
	}

	staticConfigs := []string{
		`module.exports = { "output": "export" }`,
		"module.exports = {\n  output:\n    'export',\n}",
	}
	for _, cfg := range staticConfigs {
		dir := t.TempDir()
		writeProjectFile(t, dir, "package.json", `{"dependencies":{"next":"14.0.0"}}`)
		writeProjectFile(t, dir, "next.config.js", cfg)
		if got, _ := detectDeployType(dir); got != deployTypeStatic {
			t.Fatalf("config %q: got %q, want static", cfg, got)
		}
	}
}

func TestKeepSavedDeployment(t *testing.T) {
	saved := &projectConfig{DeploymentID: "dep_1", Name: "app", URL: "https://app.ssr.example.com", DeploymentType: "ssr"}

	// A redeploy reply with no URL must not erase the saved URL.
	created := &deploymentResponse{ID: "dep_1", URL: ""}
	keepSavedDeployment(created, saved)
	if created.URL != saved.URL || created.Name != "app" || created.DeploymentType != "ssr" {
		t.Fatalf("created = %#v", created)
	}

	// Values from the reply win over the saved ones.
	created = &deploymentResponse{ID: "dep_1", Name: "new", URL: "https://new.example.com", DeploymentType: "static"}
	keepSavedDeployment(created, saved)
	if created.URL != "https://new.example.com" || created.Name != "new" || created.DeploymentType != "static" {
		t.Fatalf("created = %#v", created)
	}

	keepSavedDeployment(nil, saved)
	keepSavedDeployment(&deploymentResponse{}, nil)
}

// A redeploy whose reply has no URL must leave the URL in trapiche.json,
// also when the wait never succeeds (failed build, --detach).
func TestRedeployKeepsSavedURLInProjectConfig(t *testing.T) {
	dir := t.TempDir()
	first := &deploymentResponse{ID: "dep_1", Name: "app", URL: "https://app.ssr.example.com", DeploymentType: "ssr"}
	if err := refreshProjectConfig(dir, first, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}

	reply := &deploymentResponse{ID: "dep_1", Name: "app", DeploymentType: "ssr"} // "url" is empty
	keepSavedDeployment(reply, cfg)
	if err := refreshProjectConfig(dir, reply, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://app.ssr.example.com" {
		t.Fatalf("saved URL was erased: %#v", cfg)
	}
}

// A build that ends as "suspended" has a URL. The CLI must report the billing
// problem and still hand back the deployment, so that the URL is saved.
func TestPollSuspendedReturnsDeploymentAndError(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(deploymentResponse{
			ID: "dep_1", DeploymentType: "ssr", Status: "suspended", URL: "https://app.ssr.example.com", Logs: "built\n",
		})
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var (
		err   error
		final *deploymentResponse
	)
	captureOutput(func() {
		final, err = pollDeploymentUntilDone(ctx, creds, "dep_1", "", "ssr")
	})
	if err == nil || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("expected a suspended error, got %v", err)
	}
	if final == nil || final.URL != "https://app.ssr.example.com" {
		t.Fatalf("final = %#v", final)
	}

	created := &deploymentResponse{ID: "dep_1", DeploymentType: "ssr"}
	if !mergeFinalDeployment(created, final) || created.URL != final.URL {
		t.Fatalf("URL was not merged: %#v", created)
	}
}

func useNoFollowLimits(t *testing.T, idle, max time.Duration) {
	t.Helper()
	oldIdle, oldMax := noFollowIdle, noFollowMax
	noFollowIdle, noFollowMax = idle, max
	t.Cleanup(func() { noFollowIdle, noFollowMax = oldIdle, oldMax })
}

// A container that logs all the time never goes quiet. --no-follow must still stop.
func TestNoFollowStopsWhenLinesKeepComing(t *testing.T) {
	useNoFollowLimits(t, time.Second, 1500*time.Millisecond)
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs/stream") {
			_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", DeploymentType: "ssr", Status: "deployed"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = w.Write([]byte("data: tick\n\n"))
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	start := time.Now()
	var (
		err error
		out string
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		out = captureOutput(func() {
			err = showDeploymentLogs(context.Background(), creds, "dep_1", true)
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("logs --no-follow did not stop while lines kept coming")
	}
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s", elapsed)
	}
	if !strings.Contains(out, "tick") {
		t.Fatalf("out = %q", out)
	}
}

// The idle timer starts when the server answers. A slow first byte must still be printed.
func TestNoFollowPrintsLineThatArrivesAfterIdleTime(t *testing.T) {
	useNoFollowLimits(t, 300*time.Millisecond, 5*time.Second)
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs/stream") {
			_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", DeploymentType: "ssr", Status: "deployed"})
			return
		}
		select {
		case <-time.After(800 * time.Millisecond): // longer than the idle time
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: late line\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
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
	if !strings.Contains(out, "late line") {
		t.Fatalf("slow first byte was lost, out = %q", out)
	}
}

func TestMergeFinalDeployment(t *testing.T) {
	t.Run("copies the URL and type that the reply left out", func(t *testing.T) {
		created := &deploymentResponse{ID: "dep_1", DeploymentType: "ssr"}
		final := &deploymentResponse{ID: "dep_1", DeploymentType: "ssr", URL: "https://app.ssr.example.com"}
		if !mergeFinalDeployment(created, final) || created.URL != final.URL {
			t.Fatalf("created = %#v", created)
		}
	})
	t.Run("fills an empty type", func(t *testing.T) {
		created := &deploymentResponse{ID: "dep_1", URL: "https://a.example.com"}
		final := &deploymentResponse{ID: "dep_1", DeploymentType: "static", URL: "https://a.example.com"}
		if !mergeFinalDeployment(created, final) || created.DeploymentType != "static" {
			t.Fatalf("created = %#v", created)
		}
	})
	t.Run("no change when equal or empty", func(t *testing.T) {
		created := &deploymentResponse{ID: "dep_1", DeploymentType: "ssr", URL: "https://a.example.com"}
		same := &deploymentResponse{ID: "dep_1", DeploymentType: "ssr", URL: "https://a.example.com"}
		empty := &deploymentResponse{ID: "dep_1"}
		if mergeFinalDeployment(created, same) || mergeFinalDeployment(created, empty) || mergeFinalDeployment(created, nil) {
			t.Fatal("expected no change")
		}
		if created.URL != "https://a.example.com" || created.DeploymentType != "ssr" {
			t.Fatalf("known values were replaced: %#v", created)
		}
	})
}

// After a wait, trapiche.json must keep the live URL and the type.
func TestWaitSavesLiveURLInProjectConfig(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dep := deploymentResponse{ID: "dep_1", DeploymentType: "ssr", Status: "building"}
		if atomic.AddInt32(&calls, 1) > 1 {
			dep.Status, dep.URL = "deployed", "https://app.ssr.example.com"
		}
		_ = json.NewEncoder(w).Encode(dep)
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	dir := t.TempDir()
	created := &deploymentResponse{ID: "dep_1", Name: "app", DeploymentType: "ssr"} // reply: no URL yet
	if err := refreshProjectConfig(dir, created, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var (
		err   error
		final *deploymentResponse
	)
	captureOutput(func() {
		final, err = pollDeploymentUntilDone(ctx, creds, "dep_1", created.URL, created.DeploymentType)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !mergeFinalDeployment(created, final) {
		t.Fatal("expected the URL to be merged")
	}
	if err := refreshProjectConfig(dir, created, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://app.ssr.example.com" || cfg.DeploymentType != "ssr" {
		t.Fatalf("cfg = %#v", cfg)
	}
}

// --no-follow must print the logs and exit. The stream of a running container never ends.
func TestNoFollowStopsWhenStreamGoesQuiet(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs/stream") {
			_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", DeploymentType: "ssr", Status: "deployed"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: line one\n\ndata: line two\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never send "event: close"
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	done := make(chan struct{})
	var (
		err error
		out string
	)
	go func() {
		defer close(done)
		out = captureOutput(func() {
			err = showDeploymentLogs(context.Background(), creds, "dep_1", true)
		})
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("logs --no-follow did not return")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("out = %q", out)
	}
}

func TestNoFollowStopsWhenStreamSendsNothing(t *testing.T) {
	// The server never answers, so only the maximum time can stop the command.
	useNoFollowLimits(t, 500*time.Millisecond, time.Second)
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs/stream") {
			_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", DeploymentType: "ssr", Status: "deployed"})
			return
		}
		<-r.Context().Done() // not even headers
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	done := make(chan error, 1)
	go func() {
		done <- showDeploymentLogs(context.Background(), creds, "dep_1", true)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("logs --no-follow did not return")
	}
}

func TestNoFollowPrintsFinishedStaticLogsAndReturns(t *testing.T) {
	creds := &credentials{Token: "trp_testtoken"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logs/stream") {
			_ = json.NewEncoder(w).Encode(deploymentResponse{ID: "dep_1", DeploymentType: "static", Status: "deployed"})
			return
		}
		_, _ = w.Write([]byte("data: built\n\nevent: close\ndata: stream ended\n\n"))
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	var err error
	out := captureOutput(func() {
		err = showDeploymentLogs(context.Background(), creds, "dep_1", true)
	})
	if err != nil || out != "built\n" {
		t.Fatalf("out = %q, err %v", out, err)
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
