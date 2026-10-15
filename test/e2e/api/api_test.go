package api

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/webui"
)

// panel returns the address of the panel under test and a client for it,
// or skips the test when there is none.
func panel(t *testing.T) (string, *http.Client) {
	t.Helper()
	base := strings.TrimSuffix(os.Getenv("RACKSAIL_E2E_URL"), "/")
	if base == "" {
		t.Skip("RACKSAIL_E2E_URL isn't set; make e2e-local runs these tests against racksail dev")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if os.Getenv("RACKSAIL_E2E_INSECURE") != "" {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return base, client
}

func get(t *testing.T, client *http.Client, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

type problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func readProblem(t *testing.T, resp *http.Response, body []byte) problem {
	t.Helper()
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", resp.Header.Get("Content-Type"))
	}
	var p problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if p.Status != resp.StatusCode || p.Detail == "" {
		t.Errorf("problem = %+v for status %d", p, resp.StatusCode)
	}
	return p
}

func TestHealth(t *testing.T) {
	base, client := panel(t)
	resp, body := get(t, client, base+"/api/v1/health")
	var report struct {
		Status string `json:"status"`
		Checks []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if resp.StatusCode != http.StatusOK || report.Status != "ok" {
		t.Errorf("health = %d %s", resp.StatusCode, body)
	}
	for _, c := range report.Checks {
		if c.Name == "" || c.Status != "ok" {
			t.Errorf("check %+v isn't passing", c)
		}
	}
}

func TestVersion(t *testing.T) {
	base, client := panel(t)
	resp, body := get(t, client, base+"/api/v1/version")
	var v struct {
		Version   string `json:"version"`
		Channel   string `json:"channel"`
		GoVersion string `json:"goVersion"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
	}
	if err := json.Unmarshal(body, &v); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("version = %d %s %v", resp.StatusCode, body, err)
	}
	if !slices.Contains([]string{"stable", "beta", "nightly", "dev"}, v.Channel) || v.Version == "" || !strings.HasPrefix(v.GoVersion, "go") {
		t.Errorf("version = %+v", v)
	}
	if want := os.Getenv("RACKSAIL_E2E_VERSION"); want != "" && strings.TrimPrefix(want, "v") != v.Version {
		t.Errorf("the panel runs %s, want %s", v.Version, want)
	}
	if os.Getenv("RACKSAIL_E2E_DEV") == "" && (v.OS != "linux" || !slices.Contains([]string{"amd64", "arm64"}, v.Arch)) {
		t.Errorf("an installed server runs on linux/amd64 or linux/arm64, not %s/%s", v.OS, v.Arch)
	}
}

// The panel serves the API document it was built with, the one committed in
// spec/openapi.json.
func TestOpenAPIDocument(t *testing.T) {
	base, client := panel(t)
	resp, body := get(t, client, base+"/api/v1/openapi.json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi.json = %d", resp.StatusCode)
	}
	var served, committed map[string]any
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &committed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(served["openapi"].(string), "3.1") {
		t.Errorf("openapi = %v", served["openapi"])
	}
	if !reflect.DeepEqual(served["paths"], committed["paths"]) || !reflect.DeepEqual(served["components"], committed["components"]) {
		t.Error("the served API document differs from spec/openapi.json; the panel under test was built from other code")
	}
}

func TestUnknownPaths(t *testing.T) {
	base, client := panel(t)
	resp, body := get(t, client, base+"/api/v1/no-such-thing")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if p := readProblem(t, resp, body); p.Code != "core.not_found" {
		t.Errorf("code = %q", p.Code)
	}

	req, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/health", nil)
	del, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer del.Body.Close()
	delBody, _ := io.ReadAll(del.Body)
	if del.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(del.Header.Get("Allow"), http.MethodGet) {
		t.Errorf("DELETE health = %d, Allow %q", del.StatusCode, del.Header.Get("Allow"))
	}
	if p := readProblem(t, del, delBody); p.Code != "core.method_not_allowed" {
		t.Errorf("code = %q", p.Code)
	}
}

// The web UI is served with headers that keep it from loading anything from
// elsewhere or being framed.
func TestUIHeaders(t *testing.T) {
	base, client := panel(t)
	for _, path := range []string{"/", "/settings/something"} {
		resp, body := get(t, client, base+path)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("%s = %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if !strings.Contains(string(body), "<title>Racksail</title>") {
			t.Errorf("%s doesn't look like the panel", path)
		}
		for header, want := range map[string]string{
			"Content-Security-Policy": webui.SecurityPolicy,
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "same-origin",
		} {
			if got := resp.Header.Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", path, header, got, want)
			}
		}
	}
}
