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

func TestParseEnvArgs(t *testing.T) {
	pairs, err := parseEnvArgs([]string{"A=1", "URL=https://x.test/?a=b", " B =two words"})
	if err != nil {
		t.Fatal(err)
	}
	want := []envPair{{"A", "1"}, {"URL", "https://x.test/?a=b"}, {"B", "two words"}}
	if len(pairs) != len(want) {
		t.Fatalf("pairs = %#v", pairs)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Fatalf("pair %d = %#v, want %#v", i, pairs[i], want[i])
		}
	}

	for _, bad := range []string{"NOEQUALS", "=value", "EMPTY="} {
		if _, err := parseEnvArgs([]string{bad}); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestParseEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nA=1\nexport B=\"two words\"\nC='single'\nEMPTY=\nD=has=equals\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	pairs, skipped, err := parseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []envPair{{"A", "1"}, {"B", "two words"}, {"C", "single"}, {"D", "has=equals"}}
	if len(pairs) != len(want) {
		t.Fatalf("pairs = %#v", pairs)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Fatalf("pair %d = %#v, want %#v", i, pairs[i], want[i])
		}
	}
	if len(skipped) != 1 || skipped[0] != "EMPTY" {
		t.Fatalf("skipped = %v", skipped)
	}

	bad := filepath.Join(t.TempDir(), "bad.env")
	if err := os.WriteFile(bad, []byte("not a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseEnvFile(bad); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("expected line error, got %v", err)
	}
}

func TestParseEnvValue(t *testing.T) {
	cases := map[string]string{
		"plain":                          "plain",
		"  spaced  ":                     "spaced",
		"postgres://db/app # local":      "postgres://db/app",
		"value\t# tab comment":           "value",
		"http://x.test/#fragment":        "http://x.test/#fragment",
		`"secret" # prod`:                "secret",
		`'secret' # prod`:                "secret",
		`"with # inside" # real comment`: "with # inside",
		`"a b"`:                          "a b",
		`"a\"b" # prod`:                  `a"b`,
		`"line1\nline2"`:                 "line1\nline2",
		`"back\\slash"`:                  `back\slash`,
		`"keep \d"`:                      `keep \d`,
		`'literal\n'`:                    `literal\n`,
		`"unterminated`:                  `"unterminated`,
		" # only a comment":              "",
		"#nospace":                       "#nospace",
		`""`:                             "",
		"":                               "",
	}
	for in, want := range cases {
		if got := parseEnvValue(in); got != want {
			t.Fatalf("parseEnvValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEnvFileTrailingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "DATABASE_URL=postgres://db/app # local\nKEY=\"secret\" # prod\nURL=http://x.test/#top\nONLY_COMMENT= # nothing\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	pairs, skipped, err := parseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []envPair{{"DATABASE_URL", "postgres://db/app"}, {"KEY", "secret"}, {"URL", "http://x.test/#top"}}
	if len(pairs) != len(want) {
		t.Fatalf("pairs = %#v", pairs)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Fatalf("pair %d = %#v, want %#v", i, pairs[i], want[i])
		}
	}
	if len(skipped) != 1 || skipped[0] != "ONLY_COMMENT" {
		t.Fatalf("skipped = %v", skipped)
	}
}

func TestResolveEnvDeployment(t *testing.T) {
	if id, _, err := resolveEnvDeployment(t.TempDir(), " dep_flag "); err != nil || id != "dep_flag" {
		t.Fatalf("flag: %q, %v", id, err)
	}

	dir := t.TempDir()
	if _, _, err := resolveEnvDeployment(dir, ""); err == nil || !strings.Contains(err.Error(), "--deployment") {
		t.Fatalf("expected help error, got %v", err)
	}

	dep := &deploymentResponse{ID: "dep_cfg", Name: "app", DeploymentType: "ssr"}
	if err := refreshProjectConfig(dir, dep, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}
	id, typ, err := resolveEnvDeployment(dir, "")
	if err != nil || id != "dep_cfg" || typ != "ssr" {
		t.Fatalf("config: %q %q %v", id, typ, err)
	}
}

func TestEnvSetListUnsetAgainstAPI(t *testing.T) {
	setupCredsFile(t)
	store := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer trp_testtoken" {
			t.Errorf("missing auth header")
		}
		const base = "/api/deployments/dep_1/env"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base:
			var req struct{ Key, Value string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			store[req.Key] = req.Value
			_ = json.NewEncoder(w).Encode(map[string]string{"key": req.Key, "value": "***"})
		case r.Method == http.MethodGet && r.URL.Path == base:
			var out []envEntry
			for k, v := range store {
				if r.URL.Query().Get("reveal") != "true" {
					v = "***"
				}
				out = append(out, envEntry{Key: k, Value: v})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
			key := strings.TrimPrefix(r.URL.Path, base+"/")
			if _, ok := store[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(store, key)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	dir := t.TempDir()
	dep := &deploymentResponse{ID: "dep_1", Name: "app", DeploymentType: "ssr"}
	if err := refreshProjectConfig(dir, dep, "https://github.com/o/r", "main"); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureOutput(func() { err = runEnvSet(dir, "", "", []string{"A=1", "B=2"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "✓ set A") || !strings.Contains(out, "next deploy") {
		t.Fatalf("set out = %q", out)
	}
	if store["A"] != "1" || store["B"] != "2" {
		t.Fatalf("store = %v", store)
	}

	out = captureOutput(func() { err = runEnvList(dir, "", false) })
	if err != nil || strings.Contains(out, "=1") || !strings.Contains(out, "A=***") {
		t.Fatalf("list out = %q, err %v", out, err)
	}
	out = captureOutput(func() { err = runEnvList(dir, "", true) })
	if err != nil || !strings.Contains(out, "A=1") {
		t.Fatalf("reveal out = %q, err %v", out, err)
	}

	out = captureOutput(func() { err = runEnvUnset(dir, "", []string{"A"}) })
	if err != nil || !strings.Contains(out, "✓ removed A") {
		t.Fatalf("unset out = %q, err %v", out, err)
	}
	if _, ok := store["A"]; ok {
		t.Fatal("A was not removed")
	}

	err = runEnvUnset(dir, "", []string{"MISSING"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

// A relative --file is read from --dir, not from the working directory.
func TestEnvSetFileIsRelativeToDir(t *testing.T) {
	setupCredsFile(t)
	store := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Key, Value string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		store[req.Key] = req.Value
		_ = json.NewEncoder(w).Encode(map[string]string{"key": req.Key, "value": "***"})
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_DIR=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureOutput(func() { err = runEnvSet(dir, "dep_1", ".env", nil) })
	if err != nil {
		t.Fatal(err)
	}
	if store["FROM_DIR"] != "yes" {
		t.Fatalf("store = %v", store)
	}
}

func TestEnvSetStopsOnAPIError(t *testing.T) {
	setupCredsFile(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid key format."}`))
	}))
	defer srv.Close()
	useAPI(t, srv.URL)

	err := runEnvSet(t.TempDir(), "dep_1", "", []string{"1BAD=x", "GOOD=y"})
	if err == nil || !strings.Contains(err.Error(), "Invalid key format") || !strings.Contains(err.Error(), "0 of 2") {
		t.Fatalf("got %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestEnvSetNeedsInput(t *testing.T) {
	if err := runEnvSet(t.TempDir(), "dep_1", "", nil); err == nil {
		t.Fatal("expected error")
	}
}
