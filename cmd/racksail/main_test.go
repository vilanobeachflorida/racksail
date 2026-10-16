package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/system"
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
		{[]string{"openapi", "--addon"}, "addon-protocol.json"},
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
	// The simulated server has Racksail where its package puts it.
	out, err = run(t, "install", "--answers", answers, "--sim")
	if err != nil || !strings.Contains(out, "[5/5] Start the agent, the panel and the edge's socket") {
		t.Errorf("simulated conversion = %s, %v", out, err)
	}
	if _, err := run(t, "install", "--answers", answers, "--sim", "--binary", "/srv/racksail/racksail"); err == nil {
		t.Error("the services can't run an executable the panel could replace")
	}
	wipe := filepath.Join(dir, "wipe.yaml")
	os.WriteFile(wipe, []byte("format: 1\nmode: install\nnetwork: {mode: dhcp}\ndisks: {layout: single, devices: [/dev/sda], confirmWipe: true}\naccount: {username: admin, githubKeys: someone}\n"), 0o644)
	if _, err := run(t, "install", "--answers", wipe); err == nil || !strings.Contains(err.Error(), "installer image") {
		t.Errorf("install mode on a running system: %v", err)
	}
	if _, err := run(t, "install", "--convert", "--answers", wipe); err == nil || !strings.Contains(err.Error(), "for the install mode") {
		t.Errorf("--convert with an install answers file: %v", err)
	}
	if _, err := run(t, "install"); err == nil || !strings.Contains(err.Error(), "--convert") {
		t.Errorf("install without answers: %v", err)
	}
	out, err = run(t, "install", "--convert", "--dry-run", "--binary", "/usr/local/bin/racksail")
	if err != nil || !strings.Contains(out, "Install Racksail's services") || strings.Contains(out, "Set the hostname") {
		t.Errorf("--convert without answers = %s, %v", out, err)
	}
}

func TestDevRefusesRemoteAddresses(t *testing.T) {
	for _, args := range [][]string{
		{"--addr", "0.0.0.0:7070"},
		{"--edge-http", "192.0.2.1:80"},
		{"--edge-https", "[::]:443"},
	} {
		if _, err := run(t, append([]string{"dev", "--data", t.TempDir()}, args...)...); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestSimulatedAgent(t *testing.T) {
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "agent.sock")
	root := newRootCommand()
	root.SetArgs([]string{"agent", "--sim", "--socket", sock, "--config", filepath.Join(dir, "missing.toml")})
	root.SetOut(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	client := agent.DialUnix(sock)
	var facts system.MachineFacts
	for range 100 {
		if facts, err = agent.GetFacts.Call(ctx, client, agent.Empty{}, agent.CallOptions{}); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || facts.Hostname == "" {
		t.Errorf("the simulated agent answers on its socket: %+v, %v", facts, err)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("only its user reaches a simulated agent: %v, %v", fi, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("agent = %v", err)
	}

	t.Setenv("XDG_RUNTIME_DIR", dir)
	path, err := simSocket()
	if err != nil || path != filepath.Join(dir, "racksail", "agent.sock") {
		t.Errorf("default socket = %s, %v", path, err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("socket directory = %v, %v", fi, err)
	}
}

func TestDryRunOnlyForChanges(t *testing.T) {
	if _, err := run(t, "status", "--dry-run"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("status changes nothing, so it has no --dry-run: %v", err)
	}
	cmd := mutating(&cobra.Command{Use: "restart", RunE: func(*cobra.Command, []string) error { return nil }})
	addClientFlags(cmd, &ClientOptions{})
	if cmd.Flags().Lookup("dry-run") == nil || cmd.Flags().Lookup("server") == nil {
		t.Error("commands that change something get --dry-run")
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

	// The checks' states line up after the longest name, however long.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"degraded","checks":[{"name":"agent","status":"ok"},{"name":"notifications","status":"ok"},` +
				`{"name":"backups.destinations","status":"failing","message":"The destination doesn't answer."}]}`))
		case "/api/v1/version":
			w.Write([]byte(`{"version":"1.2.3","channel":"stable","goVersion":"go1.27","os":"linux","arch":"amd64"}`))
		}
	}))
	defer failing.Close()
	out, err = run(t, "status", "--server", failing.URL)
	var exit exitError
	if !errors.As(err, &exit) || exit.code != 2 {
		t.Errorf("a degraded server = %v", err)
	}
	want := []string{
		"  agent                ok",
		"  notifications        ok",
		"  backups.destinations failing: The destination doesn't answer.",
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 4 || !slices.Equal(lines[1:], want) {
		t.Errorf("status of a degraded server =\n%s", out)
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
