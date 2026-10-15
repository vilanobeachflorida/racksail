package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/modules/all"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestRegistriesAreClean(t *testing.T) {
	if err := checkRegistries(); err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	out, err := run(t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["version"] == "" {
		t.Errorf("version = %q, %v", out, err)
	}
}

func TestOpenAPIMatchesTheCommittedDocuments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		file string
	}{
		{[]string{"openapi"}, "openapi.json"},
		{[]string{"openapi", "--agent"}, "agent-protocol.json"},
	} {
		out, err := run(t, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join("..", "..", "spec", tc.file))
		if err != nil {
			t.Fatalf("%v (run make gen)", err)
		}
		if out != string(committed) {
			t.Errorf("spec/%s is out of date; run make gen", tc.file)
		}
	}
}

func TestThemeCSSMatchesTheUI(t *testing.T) {
	out, err := run(t, "theme", "css")
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "ui", "src", "design", "theme.css"))
	if err != nil {
		t.Fatalf("%v (run make gen)", err)
	}
	if out != string(committed) {
		t.Error("ui/src/design/theme.css is out of date; run make gen")
	}
}

func TestInstallDryRun(t *testing.T) {
	dir := t.TempDir()
	answers := filepath.Join(dir, "answers.yaml")
	os.WriteFile(answers, []byte("format: 1\nmode: convert\nhostname: web1.example.test\n"), 0o644)
	out, err := run(t, "install", "--answers", answers, "--dry-run", "--binary", "/usr/local/bin/racksail")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Install Racksail's services") || !strings.Contains(out, "racksail-panel.service") {
		t.Errorf("plan = %s", out)
	}
	out, err = run(t, "install", "--answers", answers, "--sim", "--binary", "/usr/local/bin/racksail")
	if err != nil || !strings.Contains(out, "[5/5] Start the agent and the panel") {
		t.Errorf("simulated conversion = %s, %v", out, err)
	}
	wipe := filepath.Join(dir, "wipe.yaml")
	os.WriteFile(wipe, []byte("format: 1\nmode: install\nnetwork: {mode: dhcp}\ndisks: {layout: single, devices: [/dev/sda], confirmWipe: true}\naccount: {username: admin, githubKeys: someone}\n"), 0o644)
	if _, err := run(t, "install", "--answers", wipe); err == nil || !strings.Contains(err.Error(), "installer image") {
		t.Errorf("install mode on a running system: %v", err)
	}
}

func TestDevRefusesRemoteAddresses(t *testing.T) {
	if _, err := run(t, "dev", "--addr", "0.0.0.0:7070", "--data", t.TempDir()); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("err = %v", err)
	}
}

func TestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			w.Write([]byte(`{"status":"ok","checks":[{"name":"agent","status":"ok"}]}`))
		case "/api/v1/version":
			w.Write([]byte(`{"version":"1.2.3","channel":"stable","goVersion":"go1.27","os":"linux","arch":"amd64"}`))
		}
	}))
	defer srv.Close()
	out, err := run(t, "status", "--server", srv.URL)
	if err != nil || !strings.Contains(out, "racksail 1.2.3 (stable), ok") {
		t.Errorf("status = %q, %v", out, err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"status":401,"code":"core.unauthenticated","detail":"Sign in to continue."}`))
	}))
	defer down.Close()
	if _, err := run(t, "status", "--server", down.URL); err == nil || !strings.Contains(err.Error(), "Sign in to continue.") {
		t.Errorf("problem = %v", err)
	}
}

// The UI translates the error codes the binary can return with messages
// named error.<code>, kept in the catalog of the module the code belongs to,
// or in the shell's catalog for codes of the core. Their English text must
// match the server's.
func TestErrorCodesHaveUIMessages(t *testing.T) {
	ui := filepath.Join("..", "..", "ui", "src")
	catalogs := map[string]map[string]string{}
	load := func(file string) map[string]string {
		if c, ok := catalogs[file]; ok {
			return c
		}
		c := map[string]string{}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		catalogs[file] = c
		return c
	}
	catalogOf := func(code i18n.Code) string {
		if slices.Contains(all.IDs, code.Module()) {
			return filepath.Join(ui, "modules", code.Module(), "messages", "en.json")
		}
		return filepath.Join(ui, "app", "messages", "en.json")
	}
	defined := map[string]bool{}
	for _, d := range i18n.Defs() {
		file, id := catalogOf(d.Code), "error."+string(d.Code)
		defined[id] = true
		if got, ok := load(file)[id]; !ok || got != d.Message {
			t.Errorf("%s needs %q: %q", file, id, d.Message)
		}
	}
	load(filepath.Join(ui, "app", "messages", "en.json"))
	for _, id := range all.IDs {
		load(filepath.Join(ui, "modules", id, "messages", "en.json"))
	}
	for file, c := range catalogs {
		for id := range c {
			if strings.HasPrefix(id, "error.") && !defined[id] {
				t.Errorf("%s has %q, but no error has that code", file, id)
			}
		}
	}
}
