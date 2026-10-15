package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeCatalog runs a catalog target of the repository's Makefile with its
// output in out and the given environment on top of a clean one.
func makeCatalog(t *testing.T, target, out, public string, env ...string) (string, error) {
	t.Helper()
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make isn't installed")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go isn't on the PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(makeBin, "-C", root, "--no-print-directory", target,
		"GO="+goBin, "BIN="+out, "CATALOG_OUT="+filepath.Join(out, "catalog"), "CATALOG_PUBLIC="+public)
	// The make running these tests, if any, mustn't hand its jobs or
	// variables to this one.
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEOVERRIDES", "CATALOG_SIGNING_KEY":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// CI keeps the signing key as the whole key file keygen writes, comment line
// included, so the secret make gets from the environment holds newlines.
func TestMakeCatalogPublish(t *testing.T) {
	if testing.Short() {
		t.Skip("runs make and builds the tool")
	}
	keys := t.TempDir()
	if _, err := GenerateKeys(filepath.Join(keys, "k"), filepath.Join(keys, "k.pub")); err != nil {
		t.Fatal(err)
	}
	secret := mustRead(t, filepath.Join(keys, "k"))
	if !strings.HasPrefix(secret, "#") || strings.Count(secret, "\n") < 2 {
		t.Fatalf("keygen's file changed shape, so this test no longer covers it:\n%s", secret)
	}
	public := filepath.Join(keys, "k.pub")

	out := t.TempDir()
	output, err := makeCatalog(t, "catalog-publish", out, public, "CATALOG_SIGNING_KEY="+secret)
	if err != nil {
		t.Fatalf("make catalog-publish: %v\n%s", err, output)
	}
	if !strings.Contains(output, "items, signed") || !strings.Contains(output, "signature good") {
		t.Errorf("the index wasn't signed and verified:\n%s", output)
	}
	if strings.Contains(output, strings.TrimSpace(strings.Split(secret, "\n")[1])) {
		t.Error("make printed the signing key")
	}
	for _, f := range []string{"catalog/index.json", "catalog/index.json.sig", "catalog.tar.gz"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}

	output, err = makeCatalog(t, "catalog-publish", t.TempDir(), public)
	if err == nil || !strings.Contains(output, "CATALOG_SIGNING_KEY, which isn't set") {
		t.Errorf("publishing without a key must refuse: %v\n%s", err, output)
	}

	// Without a key, make catalog builds the index unsigned.
	out = t.TempDir()
	if output, err := makeCatalog(t, "catalog", out, public); err != nil || !strings.Contains(output, "items, unsigned") {
		t.Fatalf("make catalog: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(out, "catalog", "index.json.sig")); err == nil {
		t.Error("an index built without a key has a signature")
	}
}
