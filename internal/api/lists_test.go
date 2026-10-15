package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// pages reads every page of a list as an actor, limit at a time, and
// returns the IDs it found and the sizes of the pages.
func (s *stack) pages(actor, path, field string, limit int) ([]store.ID, []int) {
	s.t.Helper()
	var ids []store.ID
	var sizes []int
	cursor := ""
	for range 20 {
		u := path + "?limit=" + strconv.Itoa(limit)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		var page map[string]json.RawMessage
		if resp := s.call(actor, "GET", u, nil, &page); resp.StatusCode != http.StatusOK {
			s.t.Fatalf("%s = %d %s", u, resp.StatusCode, bodyOf(resp))
		}
		var items []struct {
			ID store.ID `json:"id"`
		}
		json.Unmarshal(page[field], &items)
		sizes = append(sizes, len(items))
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		cursor = ""
		json.Unmarshal(page["next"], &cursor)
		if cursor == "" {
			return ids, sizes
		}
	}
	s.t.Fatalf("%s never ended", path)
	return nil, nil
}

// Lists of jobs and change sets hold only what the caller may see, and
// their pages stay full however much else there is.
func TestJobAndChangeSetListsStayFull(t *testing.T) {
	s := newStack(t, RateLimit{})
	// The admin makes changes in another account, which the owner can't
	// see, around the owner's own.
	other := store.AccountScope(store.NewID("acct"))
	adminChange := func(domain string) {
		var cs changeset.ChangeSet
		resp := s.call("admin", "POST", "/api/v1/test/sites", map[string]any{"scope": other, "spec": map[string]any{"domains": []string{domain}, "content": "x"}}, &cs)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("admin change = %d %s", resp.StatusCode, bodyOf(resp))
		}
		s.finish("admin", &cs)
	}
	for i := range 3 {
		adminChange("before" + strconv.Itoa(i) + ".test")
	}
	mine := []store.ID{s.createSite("owner", "one.test")}
	for i := range 3 {
		adminChange("between" + strconv.Itoa(i) + ".test")
	}
	mine = append(mine, s.createSite("owner", "two.test"))
	for i := range 3 {
		adminChange("after" + strconv.Itoa(i) + ".test")
	}

	changeSets, sizes := s.pages("owner", "/api/v1/changesets", "changeSets", 1)
	if len(changeSets) != 2 || !slices.Equal(sizes, []int{1, 1}) {
		t.Errorf("the owner's change sets = %v in pages of %v", changeSets, sizes)
	}
	for i, id := range changeSets {
		var cs changeset.ChangeSet
		if s.call("owner", "GET", "/api/v1/changesets/"+string(id), nil, &cs); len(cs.Changes) != 1 || cs.Changes[0].ID != mine[len(mine)-1-i] {
			t.Errorf("change set %d = %+v", i, cs)
		}
	}
	jobList, sizes := s.pages("owner", "/api/v1/jobs", "jobs", 1)
	if len(jobList) != 2 || !slices.Equal(sizes, []int{1, 1}) {
		t.Errorf("the owner's jobs = %v in pages of %v", jobList, sizes)
	}
	if all, _ := s.pages("admin", "/api/v1/changesets", "changeSets", 4); len(all) != 11 {
		t.Errorf("the admin sees every change set: %d", len(all))
	}
	if none, _ := s.pages("stranger", "/api/v1/changesets", "changeSets", 5); len(none) != 0 {
		t.Errorf("another account's owner = %v", none)
	}
	// Change sets and jobs anyone started are theirs to see, without any
	// grant on what they touched.
	nobody := s.actors["nobody"]
	cs, err := s.server.pipeline().Submit(context.Background(), changeset.Request{Actor: s.actors["admin"], Changes: []changeset.Change{{
		Op: changeset.OpCreate, Kind: "test.site", Scope: other, Spec: json.RawMessage(`{"domains":["for.test"],"content":"x"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.jobs.Enqueue(context.Background(), changeset.ApplyJob, cs.ID, jobs.Options{Actor: nobody, Scope: other})
	if err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.pages("nobody", "/api/v1/jobs", "jobs", 10); !slices.Equal(ids, []store.ID{j.ID}) {
		t.Errorf("a job someone started = %v", ids)
	}
}

// A reseller's grant reaches their customers' accounts in every list: of
// resources, change sets, jobs, events and audit records.
func TestResellersReachTheirCustomers(t *testing.T) {
	s := newStack(t, RateLimit{})
	site := s.createSite("owner", "customer.test")
	elsewhere := store.AccountScope(store.NewID("acct"))
	var cs changeset.ChangeSet
	s.call("admin", "POST", "/api/v1/test/sites", map[string]any{"scope": elsewhere, "spec": map[string]any{"domains": []string{"other.test"}, "content": "x"}}, &cs)
	s.finish("admin", &cs)

	var sites struct {
		Items []struct {
			ID store.ID `json:"id"`
		} `json:"items"`
	}
	if resp := s.call("reseller", "GET", "/api/v1/test/sites", nil, &sites); resp.StatusCode != http.StatusOK || len(sites.Items) != 1 || sites.Items[0].ID != site {
		t.Errorf("sites = %d %+v", resp.StatusCode, sites)
	}
	if ids, _ := s.pages("reseller", "/api/v1/changesets", "changeSets", 10); len(ids) != 1 {
		t.Errorf("change sets = %v", ids)
	}
	if ids, _ := s.pages("reseller", "/api/v1/jobs", "jobs", 10); len(ids) != 1 {
		t.Errorf("jobs = %v", ids)
	}
	var evs struct {
		Events []events.Event `json:"events"`
	}
	if resp := s.call("reseller", "GET", "/api/v1/events", nil, &evs); resp.StatusCode != http.StatusOK || len(evs.Events) == 0 {
		t.Fatalf("events = %d %s", resp.StatusCode, bodyOf(resp))
	}
	for _, e := range evs.Events {
		if e.Scope != store.AccountScope(s.account) {
			t.Errorf("an event the reseller shouldn't see: %+v", e)
		}
	}
	var audit struct {
		Records []events.AuditRecord `json:"records"`
	}
	if resp := s.call("reseller", "GET", "/api/v1/audit", nil, &audit); resp.StatusCode != http.StatusOK || len(audit.Records) == 0 {
		t.Fatalf("audit = %d %s", resp.StatusCode, bodyOf(resp))
	}
	for _, r := range audit.Records {
		if r.Scope != store.AccountScope(s.account) {
			t.Errorf("an audit record the reseller shouldn't see: %+v", r)
		}
	}

	// With an authorizer that can't name the scopes a grant reaches, lists
	// read everything and check each item, and still find them.
	s.server.opts.Services.Authorizer = plainAuthorizer{s.authz}
	if ids, _ := s.pages("reseller", "/api/v1/changesets", "changeSets", 1); len(ids) != 1 {
		t.Errorf("change sets without listed scopes = %v", ids)
	}
	if ids, _ := s.pages("reseller", "/api/v1/jobs", "jobs", 1); len(ids) != 1 {
		t.Errorf("jobs without listed scopes = %v", ids)
	}
}

// plainAuthorizer authorizes like the one it wraps but can't list scopes.
type plainAuthorizer struct{ auth.Authorizer }

// The audit export honours limit and cursor, and keeps what users typed
// from running as formulas in a spreadsheet.
func TestAuditExport(t *testing.T) {
	s := newStack(t, RateLimit{})
	var cs changeset.ChangeSet
	resp := s.call("admin", "POST", "/api/v1/changesets", map[string]any{
		"title":   `=HYPERLINK("https://evil.example","open")`,
		"changes": []any{map[string]any{"op": "create", "kind": "test.site", "scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"csv.test"}, "content": "x"}}},
	}, &cs, "X-Request-Id", "-calc-request-1")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("admin", &cs)
	s.createSite("owner", "second.test")

	export := func(query string) [][]string {
		t.Helper()
		resp := s.call("admin", "GET", "/api/v1/audit/export?"+query, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("export = %d %s", resp.StatusCode, bodyOf(resp))
		}
		rows, err := csv.NewReader(strings.NewReader(bodyOf(resp))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := export("changeSet=" + string(cs.ID))
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	summary, request := rows[1][13], rows[1][12]
	if summary != `'=HYPERLINK("https://evil.example","open")` || request != "'-calc-request-1" {
		t.Errorf("summary %q, request %q", summary, request)
	}
	if got := len(export("limit=1")); got != 2 {
		t.Errorf("limit=1 exported %d rows", got-1)
	}
	var page struct {
		Records []events.AuditRecord `json:"records"`
		Next    string               `json:"next"`
	}
	s.call("admin", "GET", "/api/v1/audit?limit=1", nil, &page)
	all, rest := export(""), export("cursor="+url.QueryEscape(page.Next))
	if len(rest) != len(all)-1 || rest[1][0] != all[2][0] {
		t.Errorf("after the first page's cursor: %d rows of %d", len(rest)-1, len(all)-1)
	}
	if resp := s.call("admin", "GET", "/api/v1/audit/export?cursor=nope", nil, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", resp.StatusCode)
	}
	// JSON keeps the text as it was.
	resp = s.call("admin", "GET", "/api/v1/audit/export?format=json&changeSet="+string(cs.ID), nil, nil)
	var rec events.AuditRecord
	if err := json.Unmarshal([]byte(strings.SplitN(bodyOf(resp), "\n", 2)[0]), &rec); err != nil || !strings.HasPrefix(rec.Summary, "=HYPERLINK") {
		t.Errorf("json record = %+v, %v", rec, err)
	}
}

func TestMetrics(t *testing.T) {
	s := newStack(t, RateLimit{PerActor: 600})
	resp := s.call("admin", "GET", "/api/v1/metrics", nil, nil)
	body := bodyOf(resp)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics = %d %s", resp.StatusCode, body)
	}
	for _, want := range []string{"# TYPE racksail_api_requests_total counter", "# TYPE racksail_api_streams gauge", "racksail_api_rate_limit_callers 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics have no %q:\n%s", want, body)
		}
	}
	if resp := s.call("owner", "GET", "/api/v1/metrics", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("an account owner = %d", resp.StatusCode)
	}
	text, err := renderMetrics([]Metric{{Name: "racksail_b", Help: "Line one\nline two", Value: 2}, {Name: "racksail_a", Counter: true, Value: 1.5}})
	if err != nil || string(text) != "# HELP racksail_a \n# TYPE racksail_a counter\nracksail_a 1.5\n# HELP racksail_b Line one\\nline two\n# TYPE racksail_b gauge\nracksail_b 2\n" {
		t.Errorf("rendered = %q, %v", text, err)
	}
	for _, bad := range [][]Metric{{{Name: "jobs queued"}}, {{Name: "racksail_x"}, {Name: "racksail_x"}}} {
		if _, err := renderMetrics(bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}
