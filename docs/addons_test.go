package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// manifests returns the add-on manifests a page shows: its YAML blocks
// that are addon.yaml files.
func manifests(md string) []string {
	var out []string
	for _, block := range codeBlocks(md, "yaml") {
		if strings.HasPrefix(block, "format: 1\n") && strings.Contains(block, "\napiVersions:") {
			out = append(out, block)
		}
	}
	return out
}

// TestAddonManifestsAreValid checks that every addon.yaml the add-on pages
// show is one the panel installs.
func TestAddonManifestsAreValid(t *testing.T) {
	pages, err := filepath.Glob(contentDir + "/addons/*.md")
	if err != nil {
		t.Fatal(err)
	}
	shown := 0
	for _, p := range pages {
		for _, m := range manifests(read(t, p)) {
			shown++
			if _, err := hooks.ParseAddonManifest([]byte(m)); err != nil {
				t.Errorf("%s shows a manifest the panel refuses: %v\n%s", p, err, m)
			}
		}
	}
	if shown < 2 {
		t.Errorf("the add-on pages show %d manifests; the manifest and SDKs pages each have one", shown)
	}
}

// shortDir returns a new directory whose path is short enough to hold a
// Unix socket, which the temporary directory on macOS isn't.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "addon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// example is the webhook guard in one of the languages the SDKs page
// shows it in, and how to run it from its directory.
type example struct {
	lang, file string
	// prepare writes what the program needs beside it and returns the
	// command that runs it, or skips the test without the tools.
	prepare func(t *testing.T, dir string) []string
}

var examples = []example{
	{"go", "main.go", func(t *testing.T, dir string) []string {
		gobin, err := exec.LookPath("go")
		if err != nil {
			t.Skip("go isn't on the PATH")
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/webhookguard\n\ngo 1.22\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		build := exec.Command(gobin, "build", "-o", "addon", ".")
		build.Dir = dir
		build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=-mod=mod")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("the Go example doesn't build: %v\n%s", err, out)
		}
		return []string{filepath.Join(dir, "addon")}
	}},
	{"python", "guard.py", func(t *testing.T, dir string) []string {
		python := os.Getenv("PYTHON")
		if python == "" {
			var err error
			if python, err = exec.LookPath("python3"); err != nil {
				t.Skip("python3 isn't on the PATH")
			}
		}
		return []string{python, "-B", filepath.Join(dir, "guard.py")}
	}},
	{"ts", "guard.ts", func(t *testing.T, dir string) []string {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node isn't on the PATH")
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type": "module"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return []string{node, "--experimental-strip-types", "--no-warnings", filepath.Join(dir, "guard.ts")}
	}},
}

// start runs an add-on as systemd does, handing it a listening socket as
// file descriptor 3 with LISTEN_PID set to its own process ID, and returns
// a client that calls it as the panel does.
func start(t *testing.T, dir string, argv []string) *addons.Client {
	t.Helper()
	sock := filepath.Join(dir, "addon.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f, err := ln.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("/bin/sh", append([]string{"-c", `export LISTEN_PID=$$ LISTEN_FDS=1; exec "$@"`, "sh"}, argv...)...)
	cmd.Dir = dir
	cmd.ExtraFiles = []*os.File{f}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Logf("the add-on's output:\n%s", stderr.String())
		}
	})
	return addons.NewClient("Webhook guard", sock)
}

// healthy waits for an add-on to answer its health check, as the panel
// does after starting it.
func healthy(t *testing.T, c *addons.Client) hooks.AddonHealth {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		h, err := c.Health(ctx)
		cancel()
		if err == nil {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("the add-on doesn't answer its health check: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAddonExamplesAnswerThePanel runs the webhook guard the SDKs page
// shows, in each language, the way the panel runs an add-on, and calls it
// as the panel would: its health, and its validate hook with what the
// panel sends under the example manifest's permissions.
func TestAddonExamplesAnswerThePanel(t *testing.T) {
	md := read(t, page("addons/sdks"))
	var manifest *hooks.AddonManifest
	for _, m := range manifests(md) {
		if strings.Contains(m, "id: webhook-guard\n") {
			parsed, err := hooks.ParseAddonManifest([]byte(m))
			if err != nil {
				t.Fatal(err)
			}
			manifest = parsed
		}
	}
	if manifest == nil {
		t.Fatal("the SDKs page doesn't show the webhook guard's manifest")
	}
	grants := addons.NewGrants(manifest.Permissions, store.ServerScope())
	for _, ex := range examples {
		t.Run(ex.lang, func(t *testing.T) {
			var code string
			for _, block := range codeBlocks(md, ex.lang) {
				if strings.Contains(block, "LISTEN_PID") {
					code = block
				}
			}
			if code == "" {
				t.Fatalf("the SDKs page doesn't show the webhook guard in %s", ex.lang)
			}
			dir := shortDir(t)
			if err := os.WriteFile(filepath.Join(dir, ex.file), []byte(code+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			client := start(t, dir, ex.prepare(t, dir))
			if h := healthy(t, client); h.Status != "ok" || h.APIVersion != hooks.APIVersion || h.Version != manifest.Version {
				t.Errorf("health = %+v", h)
			}
			for url, deny := range map[string]bool{"http://hooks.example.com/racksail": true, "https://hooks.example.com/racksail": false} {
				spec, _ := json.Marshal(map[string]any{"url": url, "events": []string{"*.failed"}})
				cs := &changeset.ChangeSet{Title: "Create a webhook", Changes: []changeset.ResourceChange{{
					Op: changeset.OpCreate, Kind: "core.webhook", ID: store.NewID("whk"), Scope: store.ServerScope(), After: spec,
				}}}
				first := 0
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				reply, err := client.Hook(ctx, hooks.HookCall{
					Point: "core.webhook.create.validate", Kind: hooks.Validate, ChangeSet: grants.FilterChangeSet(cs), Change: &first,
				})
				cancel()
				switch {
				case err != nil:
					t.Errorf("the hook failed for %s: %v", url, err)
				case reply.Deny != deny:
					t.Errorf("for %s the hook answered deny=%v, want %v", url, reply.Deny, deny)
				case deny && reply.Reason == "":
					t.Errorf("the hook refused %s without a reason", url)
				}
			}
		})
	}
}

// TestGoAddonRunsByHand runs the Go example as the SDKs page says to try
// it, on a socket of its own.
func TestGoAddonRunsByHand(t *testing.T) {
	md := read(t, page("addons/sdks"))
	var code string
	for _, block := range codeBlocks(md, "go") {
		if strings.Contains(block, "LISTEN_PID") {
			code = block
		}
	}
	if !strings.Contains(md, "go run . -socket /tmp/guard.sock") {
		t.Error("the SDKs page no longer says how to run the Go example by hand")
	}
	dir := shortDir(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(code+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := examples[0].prepare(t, dir)
	sock := filepath.Join(dir, "guard.sock")
	cmd := exec.Command(argv[0], "-socket", sock)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Logf("the add-on's output:\n%s", out.String())
		}
	})
	if h := healthy(t, addons.NewClient("Webhook guard", sock)); h.Status != "ok" {
		t.Errorf("health = %+v", h)
	}
}
