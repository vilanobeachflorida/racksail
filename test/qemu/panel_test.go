package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The suites write their reports into the logs, which CI uploads, whatever
// directory they run from.
func TestRunSuitesWritesIntoTheLogs(t *testing.T) {
	record := `#!/bin/sh
printf '%s\n%s\n%s\n' "$(pwd)" "$RACKSAIL_E2E_REPORT" "$RACKSAIL_E2E_RESULTS" >"$RECORD/$(basename "$0")"
`
	installStubs(t, map[string]string{"go": record, "npx": record})
	recorded := t.TempDir()
	t.Setenv("RECORD", recorded)
	work := t.TempDir()
	t.Chdir(work)
	repo := filepath.Join(work, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "test", "e2e"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join("build", "e2e", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RunSuites(context.Background(), repo, "https://127.0.0.1:18443", "1.2.3", filepath.Join("build", "e2e", "logs"), []string{"api", "ui"}); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(work, "build", "e2e", "logs")
	data, err := os.ReadFile(filepath.Join(recorded, "npx"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	resolve := func(p string) string {
		p, _ = filepath.EvalSymlinks(p)
		return p
	}
	if len(lines) != 3 || resolve(lines[0]) != resolve(filepath.Join(repo, "test", "e2e")) {
		t.Fatalf("Playwright ran with %q", lines)
	}
	if lines[1] != filepath.Join(logs, "playwright-report") || lines[2] != filepath.Join(logs, "playwright-results") {
		t.Errorf("Playwright writes its report to %s and its results to %s, outside %s", lines[1], lines[2], logs)
	}
	for _, suite := range []string{"api", "ui"} {
		if _, err := os.Stat(filepath.Join(logs, suite+".txt")); err != nil {
			t.Errorf("the %s suite's output isn't in the logs: %v", suite, err)
		}
	}
}
