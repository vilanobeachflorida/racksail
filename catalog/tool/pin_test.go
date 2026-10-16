package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const pinRecipe = `format: 1
kind: app
id: hello-site
name: Hello site
version: "1.1.0" # bumped by a dependency update
summary: A static page that says hello.
license: MIT
licenseClass: open-source
icon: icon.svg
maintainers: [{name: Example maintainers, github: example}]
inputs:
  - {name: domain, type: domain, label: Domain, required: true}
resources:
  - name: site
    kind: web.site
    spec: {domains: ["${inputs.domain}"]}
downloads:
  - url: URL/hello-site-1.1.0.tar.gz
    sha256: f92b7faf2625dbd94e941242e8720b1948fa0e2193da3c99d0afcdbd20dc6e61 # the old release
    size: 2048
  - {url: "URL/extras.tar.gz", sha256: "SUM", size: SIZE}
`

func TestPin(t *testing.T) {
	release := bytes.Repeat([]byte("hello "), 1000)
	extras := []byte("extras")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hello-site-1.1.0.tar.gz":
			w.Write(release)
		case "/extras.tar.gz":
			w.Write(extras)
		case "/moved.tar.gz":
			http.Redirect(w, r, "http://downloads.example.com/plain.tar.gz", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	doc := strings.NewReplacer("URL", srv.URL, "SUM", sum(extras), "SIZE", strconv.Itoa(len(extras))).Replace(pinRecipe)
	file := filepath.Join(t.TempDir(), "recipe.yaml")
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Pin(t.Context(), srv.Client(), file, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Changed || results[1].Changed {
		t.Fatalf("results = %+v", results)
	}
	if got, _ := os.ReadFile(file); string(got) != doc {
		t.Error("pinning without -write changed the recipe")
	}
	var out bytes.Buffer
	if err := pinFiles(t.Context(), srv.Client(), []string{file}, false, &out); err == nil {
		t.Errorf("a mismatch without -write must fail:\n%s", out.String())
	}

	if _, err := Pin(t.Context(), srv.Client(), file, true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file)
	want := strings.Replace(strings.Replace(doc,
		"f92b7faf2625dbd94e941242e8720b1948fa0e2193da3c99d0afcdbd20dc6e61 # the old release", sum(release)+" # the old release", 1),
		"size: 2048", "size: "+strconv.Itoa(len(release)), 1)
	if string(got) != want {
		t.Errorf("pinning changed more than the checksum and size:\n%s", got)
	}
	results, err = Pin(t.Context(), srv.Client(), file, false)
	if err != nil || results[0].Changed || results[1].Changed {
		t.Errorf("after -write everything should match: %+v, %v", results, err)
	}

	for _, path := range []string{"/missing.tar.gz", "/moved.tar.gz"} {
		broken := strings.Replace(doc, "/hello-site-1.1.0.tar.gz", path, 1)
		os.WriteFile(file, []byte(broken), 0o644)
		if _, err := Pin(t.Context(), srv.Client(), file, true); err == nil {
			t.Errorf("%s: pinning should fail", path)
		}
	}
}

func TestScalarText(t *testing.T) {
	for in, want := range map[string]string{
		"f92b7faf2625dbd94e941242e8720b19": "f92b7faf2625dbd94e941242e8720b19",
		"1234567890":                       `"1234567890"`,
		"12e45":                            `"12e45"`,
	} {
		if got := scalarText(in); got != want {
			t.Errorf("scalarText(%s) = %s, want %s", in, got, want)
		}
	}
}
