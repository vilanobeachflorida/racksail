package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// compact removes the whitespace of indented JSON.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

func TestGeneratedDocument(t *testing.T) {
	s := newStack(t, RateLimit{})
	raw, err := s.server.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for path, methods := range map[string][]string{
		"/api/v1/test/sites":                               {"get", "post"},
		"/api/v1/test/sites/{id}":                          {"get", "put", "delete"},
		"/api/v1/test/sites/{id}/history":                  {"get"},
		"/api/v1/test/sites/{id}/actions/deploy":           {"post"},
		"/api/v1/test/sites/{id}/actions/ping":             {"post"},
		"/api/v1/core/webhooks/{id}/actions/test":          {"post"},
		"/api/v1/core/webhooks/{id}/deliveries":            {"get"},
		"/api/v1/changesets/{id}/apply":                    {"post"},
		"/api/v1/jobs/{id}/stream":                         {"get"},
		"/api/v1/events/stream":                            {"get"},
		"/api/v1/resources/stream":                         {"get"},
		"/api/v1/operations":                               {"get"},
		"/api/v1/kinds/{name}":                             {"get"},
		"/api/v1/audit/export":                             {"get"},
		"/api/v1/core/webhooks/{id}/deliveries/{delivery}": {"get"},
	} {
		for _, m := range methods {
			if doc.Paths[path][m] == nil {
				t.Errorf("%s %s isn't documented", m, path)
			}
		}
	}
	for _, name := range []string{"TestSite", "TestSiteList", "TestSiteCreateRequest", "TestSiteUpdateRequest", "SiteSpec", "SiteStatus", "DeployInput", "PingOutput", "CoreWebhook", "WebhookSpec", "ChangeSet", "Problem", "Job", "Event"} {
		if doc.Components.Schemas[name] == nil {
			t.Errorf("schema %s is missing", name)
		}
	}
	var create struct {
		OperationID string         `json:"operationId"`
		Extensions  map[string]any `json:"-"`
		Parameters  []struct {
			Name string `json:"name"`
		} `json:"parameters"`
		RequestBody struct {
			Content map[string]struct {
				Schema struct {
					Ref string `json:"$ref"`
				} `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	json.Unmarshal(doc.Paths["/api/v1/test/sites"]["post"], &create)
	var ext map[string]any
	json.Unmarshal(doc.Paths["/api/v1/test/sites"]["post"], &ext)
	if create.OperationID != "createTestSite" || ext[ExtRisk] != "low" || ext[ExtPermission] != "test.site.create" || ext[ExtDryRun] != true ||
		create.RequestBody.Content["application/json"].Schema.Ref != "#/components/schemas/TestSiteCreateRequest" {
		t.Errorf("create = %+v %+v", create, ext)
	}
	var names []string
	for _, p := range create.Parameters {
		names = append(names, p.Name)
	}
	if !slices.Contains(names, "dryRun") || !slices.Contains(names, "Idempotency-Key") {
		t.Errorf("create takes the change parameters: %v", names)
	}
	var list map[string]any
	json.Unmarshal(doc.Paths["/api/v1/test/sites"]["get"], &list)
	if !strings.Contains(compact(doc.Paths["/api/v1/test/sites"]["get"]), `"name":"domain"`) {
		t.Error("lists take their kind's indexes as parameters")
	}
	var deploy map[string]any
	json.Unmarshal(doc.Paths["/api/v1/test/sites/{id}/actions/deploy"]["post"], &deploy)
	if deploy["operationId"] != "deployTestSite" || deploy[ExtRisk] != "medium" || deploy[ExtPermission] != "test.site.deploy" {
		t.Errorf("deploy = %+v", deploy)
	}
	var del map[string]any
	json.Unmarshal(doc.Paths["/api/v1/test/sites/{id}"]["delete"], &del)
	if del[ExtRisk] != "high" || del[ExtDestructive] != true {
		t.Errorf("delete = %+v", del)
	}
	if !strings.Contains(compact(doc.Components.Schemas["SiteSpec"]), `"minItems":1`) {
		t.Errorf("the spec's schema comes from its Go type: %s", doc.Components.Schemas["SiteSpec"])
	}
}

// Requests that describe things only read the document's schemas, so they
// can run side by side with each other and with the document itself.
func TestDescriptionsReadSideBySide(t *testing.T) {
	s := newStack(t, RateLimit{})
	var wg sync.WaitGroup
	for i := range 12 {
		path := []string{"/api/v1/events/types", "/api/v1/openapi.json", "/api/v1/kinds/core.webhook"}[i%3]
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, s.http.URL+path, nil)
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("X-Test-Actor", "owner")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s = %d", path, resp.StatusCode)
			}
		}()
	}
	wg.Wait()
}

func TestOperationsCatalog(t *testing.T) {
	s := newStack(t, RateLimit{})
	var out struct {
		Operations []Operation `json:"operations"`
	}
	if resp := s.call("nobody", "GET", "/api/v1/operations", nil, &out); resp.StatusCode != http.StatusOK || len(out.Operations) == 0 {
		t.Fatalf("catalog = %d", resp.StatusCode)
	}
	byID := map[string]Operation{}
	for _, op := range out.Operations {
		if _, dup := byID[op.ID]; dup {
			t.Errorf("operation %s appears twice", op.ID)
		}
		byID[op.ID] = op
		if !op.Risk.Valid() || op.Summary == "" || op.Description == "" {
			t.Errorf("%s needs a risk, a summary and a description: %+v", op.ID, op)
		}
	}
	for id, want := range map[string]Operation{
		"listTestSites":   {Kind: "test.site", Operation: "list", Risk: store.RiskRead, Permission: "test.site.read"},
		"rebuildTestSite": {Kind: "test.site", Operation: "rebuild", Risk: store.RiskHigh, Long: true, Mutating: true, DryRun: true},
		"getHealth":       {Risk: store.RiskRead, Public: true},
		"streamEvents":    {Risk: store.RiskRead, Stream: true},
		"testCoreWebhook": {Kind: "core.webhook", Operation: "test", Risk: store.RiskLow, Mutating: true, DryRun: true},
	} {
		got := byID[id]
		if got.Kind != want.Kind || got.Operation != want.Operation || got.Risk != want.Risk || got.Long != want.Long ||
			got.Public != want.Public || got.Stream != want.Stream || (want.Permission != "" && got.Permission != want.Permission) {
			t.Errorf("%s = %+v", id, got)
		}
	}
	if byID["createTestSite"].Request != "TestSiteCreateRequest" || byID["getTestSite"].Response != "TestSite" || byID["pingTestSite"].Output != "PingOutput" {
		t.Errorf("schemas = %+v %+v %+v", byID["createTestSite"], byID["getTestSite"], byID["pingTestSite"])
	}

	// Kinds describe themselves, with standalone schemas forms can use.
	var kinds struct {
		Kinds []KindDescription `json:"kinds"`
	}
	s.call("nobody", "GET", "/api/v1/kinds", nil, &kinds)
	if !slices.ContainsFunc(kinds.Kinds, func(k KindDescription) bool { return k.Name == "test.site" }) ||
		!slices.ContainsFunc(kinds.Kinds, func(k KindDescription) bool { return k.Name == "core.webhook" }) {
		t.Fatalf("kinds = %+v", kinds)
	}
	var site KindDescription
	if resp := s.call("nobody", "GET", "/api/v1/kinds/test.site", nil, &site); resp.StatusCode != http.StatusOK {
		t.Fatal(bodyOf(resp))
	}
	if site.Path != "/api/v1/test/sites" || len(site.Actions) != 3 || site.Actions[0].Event != "test.site.deployed" || !slices.Contains(site.Events, "test.site.drifted") {
		t.Errorf("site = %+v", site)
	}
	var spec map[string]any
	if err := json.Unmarshal(site.Spec, &spec); err != nil || spec["$schema"] == nil || spec["type"] != "object" {
		t.Errorf("spec schema = %s", site.Spec)
	}
	if !strings.Contains(string(site.Inputs["deploy"]), `"minLength":1`) || !strings.Contains(string(site.Outputs["ping"]), "pong") {
		t.Errorf("action schemas = %s / %s", site.Inputs["deploy"], site.Outputs["ping"])
	}
	var webhooks KindDescription
	s.call("nobody", "GET", "/api/v1/kinds/core.webhook", nil, &webhooks)
	if !strings.Contains(string(webhooks.Spec), "#/$defs/") && !strings.Contains(string(webhooks.Spec), `"$defs"`) {
		t.Errorf("schemas that refer to others carry them along: %s", webhooks.Spec)
	}
	if resp := s.call("nobody", "GET", "/api/v1/kinds/nope.thing", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown kind = %d", resp.StatusCode)
	}
	var types struct {
		Types []EventType `json:"types"`
	}
	s.call("nobody", "GET", "/api/v1/events/types", nil, &types)
	var drift bool
	for _, et := range types.Types {
		drift = drift || (et.Name == "test.site.drifted" && et.Data != nil)
	}
	if !drift {
		t.Errorf("event types = %+v", types.Types)
	}
}

// Every operation checks permissions: signed in without any grant, nothing
// that needs one succeeds, and without signing in nothing protected does.
func TestEveryOperationIsAuthorized(t *testing.T) {
	s := newStack(t, RateLimit{})
	// Operations that only describe the API, and searches whose answers
	// are filtered to what the caller may see, need nothing more than a
	// sign-in.
	open := []string{"listOperations", "listKinds", "getKind", "listEventTypes", "streamResources", "listChangeSets", "listJobs", "listRoles", "getSession"}
	// IDs that look right for each path, of things that exist or not.
	prefixOf := func(path string) string {
		switch {
		case strings.HasPrefix(path, Prefix+"/core/webhooks"):
			return "whk"
		case strings.HasPrefix(path, Prefix+"/changesets"):
			return store.PrefixChangeSet
		case strings.HasPrefix(path, Prefix+"/jobs"):
			return store.PrefixJob
		}
		return "tsite"
	}
	for _, op := range s.server.Catalog() {
		if op.Public {
			continue
		}
		path := strings.NewReplacer(
			"{id}", string(store.NewID(prefixOf(op.Path))),
			"{name}", "test.site",
			"{delivery}", string(store.NewID(webhook.DeliveryPrefix)),
		).Replace(op.Path)
		var body any
		if op.Method == http.MethodPost || op.Method == http.MethodPut {
			body = map[string]any{"scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"x.test"}, "content": "x"}}
			if op.ID == "submitChangeSet" {
				body = map[string]any{"changes": []map[string]any{{"op": "create", "kind": "test.site", "scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"x.test"}, "content": "x"}}}}
			}
		}
		if resp := s.call("", op.Method, path+"?dryRun=true", body, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without signing in = %d", op.ID, resp.StatusCode)
		}
		if op.Stream && slices.Contains(open, op.ID) {
			continue
		}
		if _, self := SignedInOperations[op.ID]; self && !slices.Contains(open, op.ID) {
			// What people do about themselves is tested with real users.
			continue
		}
		resp := s.call("nobody", op.Method, path+"?dryRun=true", body, nil)
		if slices.Contains(open, op.ID) {
			if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s = %d %s", op.ID, resp.StatusCode, bodyOf(resp))
			}
			continue
		}
		if resp.StatusCode < 400 {
			t.Errorf("%s succeeded without a grant: %d %s", op.ID, resp.StatusCode, bodyOf(resp))
		}
	}
}

// Every operation that changes a kind has its hook points, so hooks can
// reach every change.
func TestEveryChangeHasHookPoints(t *testing.T) {
	s := newStack(t, RateLimit{})
	for _, op := range s.server.Catalog() {
		if op.Kind == "" || !op.Mutating {
			continue
		}
		for _, stage := range []changeset.Stage{changeset.StageValidate, changeset.StagePlan, changeset.StageApplied} {
			if p := hooks.KindPoint(op.Kind, op.Operation, stage); !s.hooks.Known(p) {
				t.Errorf("%s has no hook point %s", op.ID, p)
			}
		}
	}
}

func TestRateLimits(t *testing.T) {
	s := newStack(t, RateLimit{PerActor: 4, PerAddress: 4})
	// A bucket holds a quarter of a minute's requests and ten more, which
	// is what the headers count: eleven, and how many of them are left.
	var limited *http.Response
	for i := range 12 {
		resp := s.call("owner", "GET", "/api/v1/kinds", nil, nil)
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = resp
			break
		}
		if got := resp.Header.Get("RateLimit-Limit"); got != "11" {
			t.Errorf("limit header = %q", got)
		}
		if got := resp.Header.Get("RateLimit-Remaining"); got != strconv.Itoa(10-i) {
			t.Errorf("remaining after %d requests = %q", i+1, got)
		}
		if reset, err := strconv.Atoi(resp.Header.Get("RateLimit-Reset")); err != nil || reset < 1 {
			t.Errorf("reset header = %q", resp.Header.Get("RateLimit-Reset"))
		}
	}
	if limited == nil {
		t.Fatal("the limit never applied")
	}
	if limited.Header.Get("Retry-After") == "" || s.problem(limited) != i18n.ErrRateLimited.Code {
		t.Errorf("limited = %v %s", limited.Header, bodyOf(limited))
	}
	// Others have buckets of their own, and the health is never limited.
	if resp := s.call("admin", "GET", "/api/v1/kinds", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("another user = %d", resp.StatusCode)
	}
	for range 20 {
		if resp := s.call("", "GET", "/api/v1/health", nil, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("health = %d", resp.StatusCode)
		}
	}
	// Without credentials, the limit is per address.
	var anonymous int
	for range 20 {
		if resp := s.call("", "GET", "/api/v1/version", nil, nil); resp.StatusCode == http.StatusTooManyRequests {
			anonymous++
		}
	}
	if anonymous == 0 {
		t.Error("addresses are limited too")
	}

	l := newLimiter(RateLimit{PerActor: 60}, func() time.Time { return time.Unix(0, 0) })
	if ok, q, _ := l.allow(nil, "203.0.113.1:1"); !ok || q.limit != 0 {
		t.Error("a limit of 0 is no limit")
	}
	// A full bucket holds a quarter of a minute and ten more, and refills
	// at the steady rate.
	now := time.Unix(0, 0)
	l = newLimiter(RateLimit{PerActor: 600}, func() time.Time { return now })
	user := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user")}
	if ok, q, _ := l.allow(user, ""); !ok || q.limit != 160 || q.remaining != 159 || q.reset != 100*time.Millisecond {
		t.Errorf("after one request = %+v", q)
	}
	for range 159 {
		l.allow(user, "")
	}
	ok, q, wait := l.allow(user, "")
	if ok || q.remaining != 0 || wait != 100*time.Millisecond || q.reset != 16*time.Second {
		t.Errorf("over the limit = %v %+v %v", ok, q, wait)
	}
	now = now.Add(time.Second)
	if _, q, _ := l.allow(user, ""); q.remaining != 9 {
		t.Errorf("a second later = %+v", q)
	}
	if refused, callers := l.stats(); refused != 1 || callers != 1 {
		t.Errorf("stats = %d refused, %d callers", refused, callers)
	}
	if a, _ := l.key(nil, "[2001:db8::1]:443"); a != "address:2001:db8::" {
		t.Errorf("IPv6 addresses share a bucket per /64: %s", a)
	}
}

func TestWebhookDeliveriesThroughTheAPI(t *testing.T) {
	s := newStack(t, RateLimit{})
	received := make(chan string, 64)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if webhook.Verify([]byte("hook-secret"), store.ID(r.Header.Get("webhook-id")), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), body, time.Now(), time.Minute) {
			select {
			case received <- string(body):
			default:
			}
		}
	}))
	defer receiver.Close()
	var cs changeset.ChangeSet
	resp := s.call("admin", "POST", "/api/v1/core/webhooks", map[string]any{
		"spec": map[string]any{"url": receiver.URL, "events": []string{"*"}}, "secrets": map[string]string{"secret": "hook-secret"},
	}, &cs)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", &cs)
	id := cs.Changes[0].ID
	if strings.Contains(bodyOf(s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id), nil, nil)), "hook-secret") {
		t.Error("the secret's value never comes back")
	}
	var tested changeset.ChangeSet
	if resp := s.call("admin", "POST", "/api/v1/core/webhooks/"+string(id)+"/actions/test", nil, &tested); resp.StatusCode != http.StatusOK {
		t.Fatalf("test = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var delivery webhook.Delivery
	if err := json.Unmarshal(tested.Changes[0].Output, &delivery); err != nil || delivery.State != webhook.DeliverySucceeded {
		t.Fatalf("test output = %s, %v", tested.Changes[0].Output, err)
	}
	// The webhook also gets the events of its own creation and test, which
	// "*" asks for; the ping is among them.
	for pinged := false; !pinged; {
		select {
		case body := <-received:
			pinged = strings.Contains(body, webhook.PingType)
		case <-time.After(2 * time.Second):
			t.Fatal("the receiver never got the ping")
		}
	}
	var list struct {
		Deliveries []webhook.Delivery `json:"deliveries"`
	}
	if resp := s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id)+"/deliveries", nil, &list); resp.StatusCode != http.StatusOK || len(list.Deliveries) == 0 {
		t.Fatalf("deliveries = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var one webhook.Delivery
	if resp := s.call("admin", "GET", "/api/v1/core/webhooks/"+string(id)+"/deliveries/"+string(delivery.ID), nil, &one); resp.StatusCode != http.StatusOK || len(one.Attempts) != 1 {
		t.Errorf("delivery = %d %+v", resp.StatusCode, one)
	}
	if resp := s.call("owner", "GET", "/api/v1/core/webhooks/"+string(id)+"/deliveries", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an account owner can't see the server's webhooks: %d", resp.StatusCode)
	}
}
