package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSigstoreRootPin(t *testing.T) {
	script, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := SigstoreRootPin(script)
	if err != nil || len(pin) != 64 {
		t.Fatalf("pin = %q, %v", pin, err)
	}
	other := strings.Repeat("ab", 32)
	updated, err := UpdateSigstoreRootPin(script, other)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := SigstoreRootPin(updated); got != other {
		t.Errorf("updated pin = %s", got)
	}
	if !bytes.Equal(bytes.Replace(updated, []byte(other), []byte(pin), 1), script) {
		t.Error("updating the pin changed more than the checksum")
	}
	if _, err := UpdateSigstoreRootPin(script, "not a checksum"); err == nil {
		t.Error("a pin that isn't a checksum must be refused")
	}
	if _, err := SigstoreRootPin([]byte("#!/bin/sh\n")); err == nil {
		t.Error("a script without the pin must fail")
	}
	if _, err := UpdateSigstoreRootPin([]byte("#!/bin/sh\n"), other); err == nil {
		t.Error("a script without the pin can't be updated")
	}
}

// sigstoreRepository serves TUF metadata the way Sigstore's repository
// does, with root's checksum for trusted_root.json, and cosign's checksums.
func sigstoreRepository(t *testing.T, root string, cosignSums map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tuf/timestamp.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"signed":{"_type":"timestamp","meta":{"snapshot.json":{"version":166}},"version":809}}`)
	})
	mux.HandleFunc("GET /tuf/166.snapshot.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"signed":{"_type":"snapshot","meta":{"rekor.json":{"version":3},"targets.json":{"version":14}}}}`)
	})
	mux.HandleFunc("GET /tuf/14.targets.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"signed":{"_type":"targets","targets":{"rekor.pub":{"hashes":{"sha256":"%s"},"length":178},`+
			`"trusted_root.json":{"hashes":{"sha256":"%s"},"length":6787}}}}`, strings.Repeat("0", 64), root)
	})
	mux.HandleFunc("GET /cosign/{version}/cosign_checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		for name, sum := range cosignSums {
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCurrentTrustedRoot(t *testing.T) {
	root := strings.Repeat("6", 64)
	srv := sigstoreRepository(t, root, nil)
	got, err := CurrentTrustedRoot(context.Background(), srv.Client(), srv.URL+"/tuf")
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != root || got.Length != 6787 {
		t.Errorf("trusted root = %+v", got)
	}
	if want := srv.URL + "/tuf/targets/" + root + ".trusted_root.json"; got.URL(srv.URL+"/tuf") != want {
		t.Errorf("URL = %s, want %s", got.URL(srv.URL+"/tuf"), want)
	}
	if _, err := CurrentTrustedRoot(context.Background(), srv.Client(), srv.URL+"/elsewhere"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a repository that isn't there must fail: %v", err)
	}
	broken := sigstoreRepository(t, "not a checksum", nil)
	if _, err := CurrentTrustedRoot(context.Background(), broken.Client(), broken.URL+"/tuf"); err == nil || !strings.Contains(err.Error(), "without a SHA-256 checksum") {
		t.Errorf("a target without a checksum must fail: %v", err)
	}
}

func TestCheckPins(t *testing.T) {
	original, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, sums, err := CosignPins(original)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := SigstoreRootPin(original)
	script := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(script, original, 0o755); err != nil {
		t.Fatal(err)
	}
	cosignSums := map[string]string{"cosign-linux-amd64": sums["amd64"], "cosign-linux-arm64": sums["arm64"]}
	check := func(srv *httptest.Server, write bool) (string, error) {
		var out bytes.Buffer
		err := checkPins(context.Background(), srv.Client(), script, srv.URL+"/cosign", srv.URL+"/tuf", write, &out)
		return out.String(), err
	}

	current := sigstoreRepository(t, pin, cosignSums)
	if out, err := check(current, false); err != nil || !strings.Contains(out, "pins Sigstore's current trusted root") {
		t.Fatalf("check-pins = %q, %v", out, err)
	}

	newRoot := strings.Repeat("7", 64)
	rotated := sigstoreRepository(t, newRoot, cosignSums)
	_, err = check(rotated, false)
	if err == nil || !strings.Contains(err.Error(), "Sigstore's current one is "+newRoot) || !strings.Contains(err.Error(), "check-pins -write") {
		t.Errorf("a new trusted root must be reported: %v", err)
	}
	if _, err := check(rotated, true); err != nil {
		t.Fatal(err)
	}
	if _, err := check(rotated, false); err != nil {
		t.Errorf("after -write: %v", err)
	}
	data, _ := os.ReadFile(script)
	if got, _ := SigstoreRootPin(data); got != newRoot {
		t.Errorf("-write pinned %s", got)
	}
	if !bytes.Equal(bytes.Replace(data, []byte(newRoot), []byte(pin), 1), original) {
		t.Error("-write changed more than the pin")
	}
}
