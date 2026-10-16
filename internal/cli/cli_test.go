package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var (
	docOnce sync.Once
	docRaw  []byte
	docErr  error
)

// apiDocument is the API document the core's operations make, which the
// commands under test are built from.
func apiDocument() ([]byte, error) {
	docOnce.Do(func() {
		docRaw, docErr = api.New(api.Options{Modules: modules.Default(), Services: &modules.Services{}}).OpenAPI()
	})
	return docRaw, docErr
}

func kindInfo(name string) (*store.KindInfo, bool) {
	k, ok := store.Kinds().Get(name)
	if !ok {
		return nil, false
	}
	return k.Info(), true
}

// isolate keeps a test's profiles and environment to itself.
func isolate(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "racksail", "cli.json")
	t.Setenv(ConfigEnv, path)
	for _, env := range []string{ServerEnv, TokenEnv, ProfileEnv} {
		t.Setenv(env, "")
		os.Unsetenv(env)
	}
	return path
}

// run runs the command line and returns what it wrote.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "racksail", SilenceErrors: true, SilenceUsage: true}
	root.AddGroup(&cobra.Group{ID: "client", Title: "Manage a server:"})
	if err := AddCommands(root, Config{Document: apiDocument, Kind: kindInfo, Options: &Options{}, Group: "client"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// panel stands in for a panel: it answers with the handlers a test gives
// and records what it was sent.
type panel struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*sent
	routes   map[string]func(w http.ResponseWriter, r *sent)
}

type sent struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
	ctx    context.Context
}

func newPanel(t *testing.T) *panel {
	p := &panel{routes: map[string]func(http.ResponseWriter, *sent){}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &sent{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), ctx: r.Context()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&s.Body); err != nil {
				t.Errorf("%s %s sent %s: %v", r.Method, r.URL.Path, raw, err)
			}
		}
		p.mu.Lock()
		p.requests = append(p.requests, s)
		handle := p.routes[r.Method+" "+r.URL.Path]
		p.mu.Unlock()
		if handle == nil {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"status":404,"code":"core.not_found","detail":"Nothing is at %s."}`, r.URL.Path)
			return
		}
		handle(w, s)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *panel) on(route string, handle func(w http.ResponseWriter, r *sent)) {
	p.routes[route] = handle
}

// json answers with a JSON document.
func (p *panel) json(route string, status int, body string) {
	p.on(route, func(w http.ResponseWriter, _ *sent) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (p *panel) last(method, path string) *sent {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.requests) - 1; i >= 0; i-- {
		if r := p.requests[i]; r.Method == method && r.Path == path {
			return r
		}
	}
	return nil
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

const changeSetJSON = `{"id":"chg_1","title":"Create webhook","changes":[{"op":"create","kind":"core.webhook","id":"whk_1",
	"scope":{"level":"server"},"diff":[{"path":"url","op":"add","after":"https://hooks.example.com/x"},{"path":"secret","op":"add","sensitive":true}],
	"source":{"type":"request"}}],"effects":[{"type":"package","action":"install","target":"curl","downloadBytes":2048}],
	"risk":"medium","estimate":{"downloadBytes":2048,"diskBytes":0,"memoryBytes":0},"state":"%s","dryRun":%t,"job":"%s","createdAt":"2026-10-15T10:00:00Z"}`

// Flags fill the request body as the kind's schema says: the spec's fields
// by their own names, labels as key=value, secret fields in secrets, and
// --set for anything else.
func TestCreateFromFlags(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("POST /api/v1/core/webhooks", http.StatusOK, fmt.Sprintf(changeSetJSON, "planned", true, ""))
	out, err := run(t, "", "webhook", "create", "--server", p.URL,
		"--url", "https://hooks.example.com/x", "--events", "web.site.*,*.failed", "--events", "core.*",
		"--min-severity", "warning", "--owner", "acct_123", "--labels", "team=ops", "--labels", "env=prod",
		"--secret", "s3cr3t", "--on-server", "srv_1", "--set", "spec.description=from --set", "--dry-run")
	if err != nil {
		t.Fatal(err, out)
	}
	r := p.last("POST", "/api/v1/core/webhooks")
	if r.Query.Get("dryRun") != "true" {
		t.Errorf("query = %v", r.Query)
	}
	want := `{"labels":{"env":"prod","team":"ops"},"secrets":{"secret":"s3cr3t"},"server":"srv_1","spec":{"description":"from --set",` +
		`"events":["web.site.*","*.failed","core.*"],"minSeverity":"warning","owner":{"id":"acct_123","level":"account"},"url":"https://hooks.example.com/x"}}`
	if got := jsonOf(r.Body); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
	for _, s := range []string{"Create webhook (medium risk)", "create core.webhook whk_1", `+ url: "https://hooks.example.com/x"`,
		"+ secret: (secret)", "install package curl, downloading 2 KiB", "This is a preview: nothing was changed."} {
		if !strings.Contains(out, s) {
			t.Errorf("preview lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "s3cr3t") {
		t.Error("the preview shows the secret")
	}
}

// An update starts from what the panel has, with its revision, so flags
// change only what they name.
func TestUpdateStartsFromTheCurrentSettings(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("GET /api/v1/core/webhooks/whk_1", http.StatusOK, `{"id":"whk_1","kind":"core.webhook","scope":{"level":"server"},"revision":7,
		"labels":{"team":"ops"},"spec":{"url":"https://a.example","events":["*"],"description":"old"},"status":{"phase":"ready"}}`)
	p.json("PUT /api/v1/core/webhooks/whk_1", http.StatusOK, fmt.Sprintf(changeSetJSON, "planned", true, ""))
	if out, err := run(t, "", "webhook", "update", "whk_1", "--server", p.URL, "--description", "new", "--dry-run"); err != nil {
		t.Fatal(err, out)
	}
	got := jsonOf(p.last("PUT", "/api/v1/core/webhooks/whk_1").Body)
	want := `{"labels":{"team":"ops"},"revision":7,"spec":{"description":"new","events":["*"],"url":"https://a.example"}}`
	if got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	// --replace sends only what's given.
	if out, err := run(t, "", "webhook", "update", "whk_1", "--server", p.URL, "--replace", "--url", "https://b.example", "--events", "*", "--dry-run"); err != nil {
		t.Fatal(err, out)
	}
	if got := jsonOf(p.last("PUT", "/api/v1/core/webhooks/whk_1").Body); got != `{"spec":{"events":["*"],"url":"https://b.example"}}` {
		t.Errorf("replace body = %s", got)
	}

	// An update that takes its revision as a parameter gets the one it
	// started from.
	p.json("GET /api/v1/users/user_1", http.StatusOK, `{"id":"user_1","username":"ada","name":"Ada","home":{"level":"server"},
		"grants":[{"role":"admin","scope":{"level":"server"}}],"allowFrom":null,"passwordSet":true,"twoFactor":false,"revision":4,
		"createdAt":"2026-10-15T10:00:00Z","updatedAt":"2026-10-15T10:00:00Z"}`)
	p.json("PUT /api/v1/users/user_1", http.StatusOK, `{"id":"user_1","username":"ada","name":"Ada Lovelace","home":{"level":"server"},"grants":[],"revision":5}`)
	if out, err := run(t, "", "user", "update", "user_1", "--server", p.URL, "--name", "Ada Lovelace"); err != nil {
		t.Fatal(err, out)
	}
	r := p.last("PUT", "/api/v1/users/user_1")
	if r.Query.Get("revision") != "4" {
		t.Errorf("revision = %q", r.Query.Get("revision"))
	}
	if got := jsonOf(r.Body); got != `{"allowFrom":null,"grants":[{"role":"admin","scope":{"level":"server"}}],"home":{"level":"server"},"name":"Ada Lovelace","username":"ada"}` {
		t.Errorf("user body = %s", got)
	}
}

// A body can come from a YAML file, which flags change further.
func TestBodyFromAFile(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("POST /api/v1/core/webhooks", http.StatusOK, fmt.Sprintf(changeSetJSON, "planned", true, ""))
	file := filepath.Join(t.TempDir(), "hook.yaml")
	os.WriteFile(file, []byte("spec:\n  url: https://file.example\n  events: ['*']\nlabels:\n  from: file\n"), 0o600)
	if out, err := run(t, "", "webhook", "create", "--server", p.URL, "-f", file, "--description", "flag", "--dry-run"); err != nil {
		t.Fatal(err, out)
	}
	if got := jsonOf(p.last("POST", "/api/v1/core/webhooks").Body); got != `{"labels":{"from":"file"},"spec":{"description":"flag","events":["*"],"url":"https://file.example"}}` {
		t.Errorf("body = %s", got)
	}
	if out, err := run(t, `{"spec":{"url":"https://stdin.example","events":["*"]}}`, "webhook", "create", "--server", p.URL, "-f", "-", "--dry-run"); err != nil {
		t.Fatal(err, out)
	}
	if got := jsonOf(p.last("POST", "/api/v1/core/webhooks").Body); got != `{"spec":{"events":["*"],"url":"https://stdin.example"}}` {
		t.Errorf("stdin body = %s", got)
	}
}

// Sizes, owners and grants read the way people write them, and bad values
// are refused before anything is sent.
func TestValuesPeopleWrite(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("POST /api/v1/core/accounts/open", http.StatusOK, `{"changeSet":`+fmt.Sprintf(changeSetJSON, "planned", true, "")+`}`)
	if out, err := run(t, "", "account", "open", "--server", p.URL, "--slug", "acme", "--name", "Acme", "--limits-memory-max", "2G",
		"--limits-memory-high", "1.5GB", "--limits-cpu", "150", "--owner-username", "ada", "--dry-run"); err != nil {
		t.Fatal(err, out)
	}
	got := jsonOf(p.last("POST", "/api/v1/core/accounts/open").Body)
	if want := `{"limits":{"cpu":150,"memoryHigh":1500000000,"memoryMax":2147483648},"name":"Acme","owner":{"username":"ada"},"slug":"acme"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	for _, args := range [][]string{
		{"account", "open", "--limits-cpu", "lots"},
		{"account", "open", "--limits-memory-max", "two gigs"},
		{"webhook", "create", "--min-severity", "loud"},
		{"webhook", "create", "--owner", "someone"},
		{"webhook", "create", "--disabled=maybe"},
	} {
		before := len(p.requests)
		if _, err := run(t, "", append(args, "--server", p.URL)...); err == nil {
			t.Errorf("%v was taken", args)
		}
		if len(p.requests) != before {
			t.Errorf("%v sent a request", args)
		}
	}
}

// Operations the panel can't preview show the request they'd send, with
// secrets hidden, and send nothing.
func TestDryRunWithoutAPreview(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	out, err := run(t, "", "user", "create", "--server", p.URL, "--username", "ada", "--grants", "owner@acct_9", "--grants", "admin", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 0 {
		t.Errorf("sent %+v", p.requests)
	}
	for _, s := range []string{"This would send POST " + p.URL + "/api/v1/users", `"role": "owner"`, `"id": "acct_9"`, `"level": "server"`} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if hidden := hideSecrets(map[string]any{"secrets": map[string]any{"a": "b"}, "spec": map[string]any{"password": "pw", "name": "x"}}, ""); jsonOf(hidden) !=
		`{"secrets":{"a":"(hidden)"},"spec":{"name":"x","password":"(hidden)"}}` {
		t.Errorf("hidden = %s", jsonOf(hidden))
	}
}

// Problems name the flag that set the field they're about.
func TestProblemsNameFlags(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.on("POST /api/v1/core/webhooks", func(w http.ResponseWriter, _ *sent) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"status":422,"code":"core.invalid","detail":"Some values aren't valid.","errors":[
			{"location":"body.spec.url","code":"core.bad_value","message":"use https"},{"location":"body.spec.unknown","code":"core.bad_value","message":"no such field"}]}`)
	})
	_, err := run(t, "", "webhook", "create", "--server", p.URL, "--url", "http://x", "--events", "*")
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	for _, s := range []string{"Some values aren't valid. (core.invalid)", "--url: use https", "spec.unknown: no such field"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error lacks %q: %s", s, msg)
		}
	}
	p.json("GET /api/v1/core/webhooks", http.StatusOK, `{}`)
	p.on("GET /api/v1/core/webhooks", func(w http.ResponseWriter, _ *sent) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401,"code":"core.unauthenticated","detail":"Sign in to continue."}`)
	})
	if _, err := run(t, "", "webhook", "list", "--server", p.URL); err == nil || !strings.Contains(err.Error(), "racksail login") {
		t.Errorf("unauthenticated = %v", err)
	}
}

// Applying a change follows its job: its log as it comes, and how it
// ended.
func TestFollowingTheJob(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("POST /api/v1/core/webhooks", http.StatusAccepted, fmt.Sprintf(changeSetJSON, "applying", false, "job_1"))
	attempts := 0
	p.on("GET /api/v1/jobs/job_1/stream", func(w http.ResponseWriter, r *sent) {
		w.Header().Set("Content-Type", "text/event-stream")
		attempts++
		if attempts == 1 {
			// The stream drops after the first line, as when the panel
			// restarts; the command picks it up after that line.
			io.WriteString(w, "event: job\ndata: {\"id\":\"job_1\",\"title\":\"Create webhook\",\"state\":\"running\",\"progress\":{\"done\":0,\"total\":2}}\n\n")
			io.WriteString(w, "id: 1\nevent: log\ndata: {\"seq\":1,\"time\":\"2026-10-15T10:00:00Z\",\"level\":\"INFO\",\"message\":\"Install curl\",\"attrs\":{\"step\":1}}\n\n")
			return
		}
		if r.Header.Get("Last-Event-ID") != "1" {
			t.Errorf("resumed after %q", r.Header.Get("Last-Event-ID"))
		}
		io.WriteString(w, ": keep-alive\n\n")
		io.WriteString(w, "id: 2\nevent: log\ndata: {\"seq\":2,\"time\":\"2026-10-15T10:00:01Z\",\"level\":\"INFO\",\"message\":\"Write the unit\"}\n\n")
		io.WriteString(w, "event: job\ndata: {\"id\":\"job_1\",\"title\":\"Create webhook\",\"state\":\"succeeded\",\"progress\":{\"done\":2,\"total\":2},"+
			"\"startedAt\":\"2026-10-15T10:00:00Z\",\"finishedAt\":\"2026-10-15T10:00:02Z\"}\n\n")
	})
	out, err := run(t, "", "webhook", "create", "--server", p.URL, "--url", "https://x.example", "--events", "*")
	if err != nil {
		t.Fatal(err, out)
	}
	for _, s := range []string{"Following job job_1", "INFO  Install curl step=1", "INFO  Write the unit", "Done in 2s."} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Count(out, "Install curl") != 1 {
		t.Errorf("a line came twice:\n%s", out)
	}

	// A job that fails fails the command, with its reason.
	p.on("GET /api/v1/jobs/job_1/stream", func(w http.ResponseWriter, r *sent) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: job\ndata: {\"id\":\"job_1\",\"title\":\"Create webhook\",\"state\":\"failed\",\"error\":{\"code\":\"agent.failed\",\"message\":\"the disk is full\"}}\n\n")
	})
	if _, err := run(t, "", "webhook", "create", "--server", p.URL, "--url", "https://x.example", "--events", "*"); err == nil ||
		err.Error() != "Create webhook failed: the disk is full (agent.failed)" {
		t.Errorf("failed job = %v", err)
	}
	// --detach doesn't wait, and JSON output is the change set itself.
	before := attempts
	out, err = run(t, "", "webhook", "create", "--server", p.URL, "--url", "https://x.example", "--events", "*", "--detach", "-o", "json")
	if err != nil || attempts != before {
		t.Fatalf("detach = %v, %d streams", err, attempts-before)
	}
	var cs map[string]any
	if err := json.Unmarshal([]byte(out), &cs); err != nil || cs["job"] != "job_1" {
		t.Errorf("json = %s, %v", out, err)
	}
}

// What an action returns is shown once it's applied.
func TestActionOutput(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("POST /api/v1/core/webhooks/whk_1/actions/test", http.StatusAccepted, `{"id":"chg_2","title":"Send a test webhook","risk":"low","state":"applied",
		"changes":[{"op":"action","action":"test","kind":"core.webhook","id":"whk_1","source":{"type":"request"},"output":{"id":"dlv_1","state":"delivered","attemptCount":1}}],
		"effects":[],"estimate":{}}`)
	out, err := run(t, "", "webhook", "test", "whk_1", "--server", p.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"test core.webhook whk_1", "test returned:", "state:", "delivered"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// --all reads every page into one list.
func TestEveryPage(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.on("GET /api/v1/jobs", func(w http.ResponseWriter, r *sent) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Query.Get("cursor") {
		case "":
			io.WriteString(w, `{"jobs":[{"id":"job_1","title":"One","state":"succeeded"}],"next":"c2"}`)
		case "c2":
			io.WriteString(w, `{"jobs":[{"id":"job_2","title":"Two","state":"failed"}],"next":"c3"}`)
		default:
			io.WriteString(w, `{"jobs":[{"id":"job_3","title":"Three","state":"running"}],"next":""}`)
		}
	})
	out, err := run(t, "", "job", "list", "--server", p.URL, "--all", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Jobs []struct{ ID string } `json:"jobs"`
		Next string                `json:"next"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Jobs) != 3 || page.Next != "" || page.Jobs[2].ID != "job_3" {
		t.Errorf("all = %s, %v", out, err)
	}
	out, err = run(t, "", "job", "list", "--server", p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "job_1") || strings.Contains(out, "job_2") || !strings.Contains(out, "--cursor c2") {
		t.Errorf("first page = %s", out)
	}
}

// Tables put the most telling columns first; YAML and JSON keep the
// panel's order.
func TestOutputFormats(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("GET /api/v1/core/accounts", http.StatusOK, `{"items":[{"id":"acct_1","kind":"core.account","scope":{"level":"account","id":"acct_1"},
		"spec":{"slug":"acme","name":"Acme Inc.","limits":{"memoryMax":2147483648}},"status":{"phase":"ready"},"createdAt":"2026-10-15T10:00:00Z"}],"next":""}`)
	out, err := run(t, "", "account", "list", "--server", p.URL)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "NAME") || !strings.Contains(lines[0], "SLUG") ||
		!strings.Contains(lines[1], "Acme Inc.") || !strings.Contains(lines[1], "ready") {
		t.Errorf("table =\n%s", out)
	}
	out, err = run(t, "", "account", "list", "--server", p.URL, "-o", "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "items:\n  - id: acct_1\n    kind: core.account\n") || !strings.Contains(out, "memoryMax: 2147483648") {
		t.Errorf("yaml =\n%s", out)
	}
	out, err = run(t, "", "account", "list", "--server", p.URL, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "{\n  \"items\": [\n    {\n      \"id\": \"acct_1\"") {
		t.Errorf("json =\n%s", out)
	}
	if _, err := run(t, "", "account", "list", "--server", p.URL, "-o", "xml"); err == nil {
		t.Error("an unknown format was taken")
	}
}

// Login checks the token with the panel and keeps it in a profile only its
// user can read; commands then call that panel with it, and never send it
// anywhere else.
func TestProfiles(t *testing.T) {
	path := isolate(t)
	p := newPanel(t)
	p.on("GET /api/v1/session", func(w http.ResponseWriter, r *sent) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer rsk_good" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"status":401,"code":"auth.token_invalid","detail":"The API token isn't valid."}`)
			return
		}
		io.WriteString(w, `{"user":{"id":"user_1","name":"Ada Lovelace","username":"ada","home":{"level":"server"}},"permissions":["*.read"],
			"modules":{"web":"active"},"token":{"id":"tok_1","name":"laptop"},"stepUpMethods":[],"accounts":[{"id":"acct_1","name":"Acme","slug":"acme"}]}`)
	})
	if _, err := run(t, "rsk_bad\n", "login", "--server", p.URL, "--token-stdin"); err == nil || !strings.Contains(err.Error(), "isn't valid") {
		t.Errorf("a bad token = %v", err)
	}
	if _, err := run(t, "not-a-token\n", "login", "--server", p.URL, "--token-stdin"); err == nil || !strings.Contains(err.Error(), "rsk_") {
		t.Errorf("not a token = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a failed login kept a profile")
	}
	out, err := run(t, "rsk_good\n", "login", "--server", p.URL, "--token-stdin", "--profile", "work")
	if err != nil || !strings.Contains(out, `as ada with the token "laptop"`) {
		t.Fatalf("login = %s, %v", out, err)
	}
	if fi, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("profiles file = %v, %v", fi, err)
	}
	out, err = run(t, "", "whoami")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ada (Ada Lovelace)", "(profile work)", `the API token "laptop"`, "*.read", "acme (acct_1)", "web (active)"} {
		if !strings.Contains(out, s) {
			t.Errorf("whoami lacks %q:\n%s", s, out)
		}
	}
	// Another server never gets the profile's token.
	other := newPanel(t)
	other.json("GET /api/v1/session", http.StatusOK, `{"user":{"id":"user_2","name":"x","home":{"level":"server"}},"permissions":[],"modules":{},"stepUpMethods":[],"accounts":[]}`)
	if _, err := run(t, "", "whoami", "--server", other.URL); err != nil {
		t.Fatal(err)
	}
	if auth := other.last("GET", "/api/v1/session").Header.Get("Authorization"); auth != "" {
		t.Errorf("another server got %q", auth)
	}
	// The environment's token goes wherever it's sent.
	t.Setenv(TokenEnv, "rsk_env")
	run(t, "", "whoami", "--server", other.URL)
	if auth := other.last("GET", "/api/v1/session").Header.Get("Authorization"); auth != "Bearer rsk_env" {
		t.Errorf("environment token = %q", auth)
	}
	os.Unsetenv(TokenEnv)
	if ua := p.last("GET", "/api/v1/session").Header.Get("User-Agent"); !strings.HasPrefix(ua, "racksail/") {
		t.Errorf("user agent %q doesn't say the requests come from the CLI", ua)
	}

	out, err = run(t, "", "profile", "list")
	if err != nil || !strings.Contains(out, "*  work") || !strings.Contains(out, p.URL) {
		t.Errorf("profiles = %s, %v", out, err)
	}
	if _, err := run(t, "", "profile", "use", "nope"); err == nil {
		t.Error("switched to a missing profile")
	}
	if _, err := run(t, "", "whoami", "--profile", "nope"); err == nil || !strings.Contains(err.Error(), "no profile called nope") {
		t.Errorf("missing profile = %v", err)
	}
	// Others mustn't read the tokens.
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0o644)
		if _, err := run(t, "", "whoami"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("a readable profiles file = %v", err)
		}
		os.Chmod(path, 0o600)
	}
	if out, err := run(t, "", "logout"); err != nil || !strings.Contains(out, "Removed the profile work") {
		t.Errorf("logout = %s, %v", out, err)
	}
	if out, _ := run(t, "", "profile", "list"); !strings.Contains(out, "No profiles yet") {
		t.Errorf("after logout = %s", out)
	}
}

// Arguments complete with what the panel lists.
func TestCompletingIDs(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.json("GET /api/v1/core/webhooks", http.StatusOK, `{"items":[{"id":"whk_1","spec":{"url":"https://a.example"}},{"id":"whk_2","spec":{"url":"https://b.example"}}],"next":""}`)
	p.json("GET /api/v1/users", http.StatusOK, `{"users":[{"id":"user_1","username":"ada"}]}`)
	p.json("GET /api/v1/users/user_1/tokens", http.StatusOK, `{"tokens":[{"id":"tok_1","name":"ci"}]}`)
	t.Setenv(ServerEnv, p.URL)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "webhook", "get", "whk_2"}, "whk_2\thttps://b.example\n:4"},
		{[]string{"__complete", "user", "revoke-token", ""}, "user_1\tada\n:4"},
		{[]string{"__complete", "user", "revoke-token", "user_1", ""}, "tok_1\tci\n:4"},
		{[]string{"__complete", "webhook", "create", "--min-severity", ""}, "info\nnotice\nwarning\nerror\ncritical\n:4"},
	} {
		out, err := run(t, "", tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.SplitN(out, "\nCompletion ended", 2)[0]; got != tc.want {
			t.Errorf("%v = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// Streams print each event as it comes, until they're stopped.
func TestWatch(t *testing.T) {
	isolate(t)
	p := newPanel(t)
	p.on("GET /api/v1/events/stream", func(w http.ResponseWriter, r *sent) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "id: 7\nevent: event\ndata: {\"id\":\"evt_1\",\"type\":\"core.webhook.created\",\"subject\":\"whk_1\",\"message\":\"made\",\"time\":\"2026-10-15T10:00:00Z\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.ctx.Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	c, err := NewClient(p.URL, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- watch(ctx, c, Request{Method: http.MethodGet, Path: "/api/v1/events/stream"}, &syncWriter{w: &out, onWrite: cancel}, "json")
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); !strings.HasPrefix(got, `{"event":"event","id":"7","data":{"id":"evt_1"`) {
		t.Errorf("event = %s", got)
	}
}

// syncWriter calls onWrite after each write, as a test's signal.
type syncWriter struct {
	mu      sync.Mutex
	w       io.Writer
	onWrite func()
}

func (s *syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.w.Write(b)
	s.onWrite()
	return n, err
}
