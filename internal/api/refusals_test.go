package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// refusals returns the audit records of what an actor was refused.
func (s *stack) refusals(actor string) []events.AuditRecord {
	s.t.Helper()
	list, _, err := s.log.QueryAudit(context.Background(), events.Query{Actor: s.actors[actor].ID, Outcome: events.OutcomeDenied, Limit: 100})
	if err != nil {
		s.t.Fatal(err)
	}
	return list
}

// refused reports whether the audit log has a refusal of action to actor,
// about subject when it isn't empty.
func (s *stack) refused(actor, action string, subject store.ID) bool {
	return slices.ContainsFunc(s.refusals(actor), func(r events.AuditRecord) bool {
		return r.Action == action && (subject == "" || r.Subject == subject)
	})
}

// Every request that is refused, a read or a job's cancel as much as a
// change, is in the audit log; what lists leave out isn't.
func TestRefusalsAreAudited(t *testing.T) {
	s := newStack(t, RateLimit{})
	site := s.createSite("owner", "refused.test")
	var cs changeset.ChangeSet
	s.call("owner", "PUT", "/api/v1/test/sites/"+string(site), map[string]any{"spec": map[string]any{"domains": []string{"refused.test"}, "content": "v2"}}, &cs)
	s.finish("owner", &cs)
	s.actors["jobwatcher"] = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "jobwatcher", Source: auth.SourceUI,
		Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.AccountScope(s.account), Permissions: []auth.Permission{PermJobRead}}}}

	for _, tc := range []struct {
		actor, method, path string
		status              int
		action              string
		subject             store.ID
	}{
		{"stranger", "GET", "/api/v1/test/sites/" + string(site), http.StatusNotFound, "test.site.read", site},
		{"stranger", "GET", "/api/v1/resources/" + string(site), http.StatusNotFound, "test.site.read", site},
		{"stranger", "GET", "/api/v1/test/sites/" + string(site) + "/history", http.StatusNotFound, "test.site.read", site},
		{"nobody", "GET", "/api/v1/test/sites", http.StatusForbidden, "test.site.read", ""},
		{"stranger", "GET", "/api/v1/jobs/" + string(cs.Job), http.StatusNotFound, "core.job.read", cs.Job},
		{"jobwatcher", "POST", "/api/v1/jobs/" + string(cs.Job) + "/cancel", http.StatusForbidden, "core.job.cancel", cs.Job},
		{"jobwatcher", "POST", "/api/v1/jobs/" + string(cs.Job) + "/retry", http.StatusForbidden, "core.job.retry", cs.Job},
		{"stranger", "GET", "/api/v1/changesets/" + string(cs.ID), http.StatusNotFound, "core.changeset.read", cs.ID},
		{"stranger", "POST", "/api/v1/changesets/" + string(cs.ID) + "/undo", http.StatusNotFound, "core.changeset.read", cs.ID},
		{"nobody", "GET", "/api/v1/audit", http.StatusForbidden, "core.audit.read", ""},
		{"nobody", "GET", "/api/v1/events", http.StatusForbidden, "core.event.read", ""},
		{"owner", "GET", "/api/v1/metrics", http.StatusForbidden, "core.metrics.read", ""},
	} {
		if resp := s.call(tc.actor, tc.method, tc.path, nil, nil); resp.StatusCode != tc.status {
			t.Errorf("%s %s as %s = %d, want %d", tc.method, tc.path, tc.actor, resp.StatusCode, tc.status)
		}
		if !s.refused(tc.actor, tc.action, tc.subject) {
			t.Errorf("%s %s as %s isn't recorded as a refused %s", tc.method, tc.path, tc.actor, tc.action)
		}
	}
	// The record of a refused read belongs to the resource's owner, so its
	// audit log shows who tried.
	var audit struct {
		Records []events.AuditRecord `json:"records"`
	}
	if resp := s.call("owner", "GET", "/api/v1/audit?outcome=denied&subject="+string(site), nil, &audit); resp.StatusCode != http.StatusOK || len(audit.Records) == 0 {
		t.Errorf("the owner's view of refusals = %d %+v", resp.StatusCode, audit.Records)
	}
	for _, r := range audit.Records {
		if r.Actor.ID != s.actors["stranger"].ID || r.Reason == "" {
			t.Errorf("refusal = %+v", r)
		}
	}

	// Lists leave out what someone may not see without refusing anything.
	before := len(s.refusals("owner"))
	other := store.AccountScope(s.newAccount(""))
	var theirs changeset.ChangeSet
	s.call("admin", "POST", "/api/v1/test/sites", map[string]any{"scope": other, "spec": map[string]any{"domains": []string{"theirs.test"}, "content": "x"}}, &theirs)
	s.finish("admin", &theirs)
	for _, path := range []string{"/api/v1/test/sites", "/api/v1/changesets", "/api/v1/jobs", "/api/v1/events"} {
		if resp := s.call("owner", "GET", path, nil, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
	s.server.opts.Services.Authorizer = plainAuthorizer{s.authz}
	for _, path := range []string{"/api/v1/test/sites", "/api/v1/changesets", "/api/v1/jobs"} {
		if resp := s.call("owner", "GET", path, nil, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("%s with a plain authorizer = %d", path, resp.StatusCode)
		}
	}
	if after := len(s.refusals("owner")); after != before {
		t.Errorf("lists recorded %d refusals", after-before)
	}
}

// A refusal that keeps happening counts on its first record, which the list
// and the export of the audit log show with the number of repeats.
func TestRepeatedRefusalsCountOnce(t *testing.T) {
	s := newStack(t, RateLimit{})
	site := s.createSite("owner", "again.test")
	for range 5 {
		if resp := s.call("stranger", "GET", "/api/v1/test/sites/"+string(site), nil, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("read = %d", resp.StatusCode)
		}
	}
	var audit struct {
		Records []events.AuditRecord `json:"records"`
	}
	s.call("admin", "GET", "/api/v1/audit?outcome=denied&subject="+string(site), nil, &audit)
	if len(audit.Records) != 1 || audit.Records[0].Repeats != 4 || audit.Records[0].LastTime == nil {
		t.Fatalf("refused reads = %+v", audit.Records)
	}
	resp := s.call("admin", "GET", "/api/v1/audit/export?outcome=denied&subject="+string(site), nil, nil)
	rows, err := csv.NewReader(strings.NewReader(bodyOf(resp))).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("export = %v, %v", rows, err)
	}
	repeats, last := slices.Index(rows[0], "repeats"), slices.Index(rows[0], "last time")
	if repeats < 0 || last < 0 || rows[1][repeats] != "4" || rows[1][last] == "" {
		t.Errorf("export = %v", rows)
	}
}

// A change to a resource someone can't see answers as if the resource
// didn't exist, as reading it does; one they can see but not change answers
// that they may not. Both are recorded.
func TestChangesToUnseenResources(t *testing.T) {
	s := newStack(t, RateLimit{})
	site := s.createSite("owner", "unseen.test")
	s.actors["reader"] = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "reader", Source: auth.SourceUI,
		Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(s.account)}}}
	update := map[string]any{"changes": []map[string]any{{
		"op": "update", "kind": "test.site", "id": site, "spec": map[string]any{"domains": []string{"unseen.test"}, "content": "mine now"},
	}}}
	resp := s.call("stranger", "POST", "/api/v1/changesets", update, nil)
	if resp.StatusCode != http.StatusNotFound || s.problem(resp) != i18n.ErrNotFound.Code {
		t.Errorf("another account's site = %d %s", resp.StatusCode, bodyOf(resp))
	}
	missing := map[string]any{"changes": []map[string]any{{
		"op": "update", "kind": "test.site", "id": store.NewID("tsite"), "spec": map[string]any{"domains": []string{"x.test"}, "content": "x"},
	}}}
	if resp := s.call("stranger", "POST", "/api/v1/changesets", missing, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a site that doesn't exist = %d", resp.StatusCode)
	}
	if !s.refused("stranger", "test.site.update", site) {
		t.Error("the stranger's attempt isn't recorded")
	}
	resp = s.call("reader", "POST", "/api/v1/changesets", update, nil)
	if resp.StatusCode != http.StatusForbidden || s.problem(resp) != i18n.ErrForbidden.Code {
		t.Errorf("a reader of the account = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if !s.refused("reader", "test.site.update", site) {
		t.Error("the reader's attempt isn't recorded")
	}
	if got, _ := siteKind.Get(context.Background(), s.store, site); got.Spec.Content != "unseen.test" {
		t.Errorf("the site changed: %+v", got.Spec)
	}
}

// A failed job is retried once: a second request is refused, unless it
// repeats the first one's idempotency key.
func TestRetryingAJobOnce(t *testing.T) {
	s := newStack(t, RateLimit{})
	ctx := context.Background()
	if err := s.jobs.Handle(jobs.Type{Name: "apitest.broken", Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		return nil, i18n.ErrUnavailable.New("what", "The mirror")
	}}); err != nil {
		t.Fatal(err)
	}
	failed, err := s.jobs.Enqueue(ctx, "apitest.broken", nil, jobs.Options{Actor: s.actors["owner"], Scope: store.AccountScope(s.account)})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.jobs.Wait(ctx, failed.ID); got.State != jobs.StateFailed {
		t.Fatalf("job = %+v", got)
	}
	path := "/api/v1/jobs/" + string(failed.ID) + "/retry"
	var first, again jobs.Job
	if resp := s.call("owner", "POST", path, nil, &first, "Idempotency-Key", "retry-1"); resp.StatusCode != http.StatusAccepted || first.RetryOf != failed.ID {
		t.Fatalf("retry = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("owner", "POST", path, nil, &again, "Idempotency-Key", "retry-1"); resp.StatusCode != http.StatusAccepted || again.ID != first.ID {
		t.Errorf("the same request again = %d %+v", resp.StatusCode, again)
	}
	resp := s.call("owner", "POST", path, nil, nil)
	if resp.StatusCode != http.StatusConflict || s.problem(resp) != jobs.ErrAlreadyRetried.Code {
		t.Errorf("a second retry = %d %s", resp.StatusCode, bodyOf(resp))
	}
	list, _, err := s.jobs.List(ctx, jobs.Filter{Type: "apitest.broken"})
	if err != nil || len(list) != 2 {
		t.Errorf("jobs = %d, %v", len(list), err)
	}
	records, _, _ := s.log.QueryAudit(ctx, events.Query{Actor: s.actors["owner"].ID, Types: []string{"core.job.retry"}})
	outcomes := map[events.Outcome]int{}
	for _, r := range records {
		outcomes[r.Outcome]++
	}
	if outcomes[events.OutcomeSuccess] < 1 || outcomes[events.OutcomeFailed] != 1 {
		t.Errorf("retry records = %v", outcomes)
	}
	// Canceling takes a key too, and canceling again changes nothing.
	for range 2 {
		if resp := s.call("owner", "POST", "/api/v1/jobs/"+string(first.ID)+"/cancel", nil, nil, "Idempotency-Key", "cancel-1"); resp.StatusCode != http.StatusOK {
			t.Errorf("cancel = %d %s", resp.StatusCode, bodyOf(resp))
		}
	}
}

// Someone with a grant on the whole server, but not core.changeset.read,
// sees the change sets whose resources they may read, whoever made them.
func TestServerWideReadersSeeChangeSets(t *testing.T) {
	s := newStack(t, RateLimit{})
	elsewhere := store.AccountScope(s.newAccount(""))
	var cs changeset.ChangeSet
	s.call("admin", "POST", "/api/v1/test/sites", map[string]any{"scope": elsewhere, "spec": map[string]any{"domains": []string{"seen.test"}, "content": "x"}}, &cs)
	s.finish("admin", &cs)
	s.actors["siteauditor"] = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "siteauditor", Source: auth.SourceUI,
		Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.ServerScope(), Permissions: []auth.Permission{"test.site.read"}}}}
	s.actors["jobauditor"] = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "jobauditor", Source: auth.SourceUI,
		Grants: []auth.Grant{{Role: auth.RoleCollaborator, Scope: store.ServerScope(), Permissions: []auth.Permission{PermJobRead}}}}
	if ids, _ := s.pages("siteauditor", "/api/v1/changesets", "changeSets", 10); !slices.Equal(ids, []store.ID{cs.ID}) {
		t.Errorf("change sets = %v", ids)
	}
	if ids, _ := s.pages("jobauditor", "/api/v1/changesets", "changeSets", 10); len(ids) != 0 {
		t.Errorf("someone who can't read sites sees %v", ids)
	}
	s.server.opts.Services.Authorizer = plainAuthorizer{s.authz}
	if ids, _ := s.pages("siteauditor", "/api/v1/changesets", "changeSets", 10); !slices.Equal(ids, []store.ID{cs.ID}) {
		t.Errorf("change sets with a plain authorizer = %v", ids)
	}
}

func TestGrantScopesSayWhenTheyReachEverything(t *testing.T) {
	ctx := context.Background()
	acct := store.AccountScope(store.NewID("acct"))
	collaborator := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{
		{Role: auth.RoleCollaborator, Scope: store.ServerScope(), Permissions: []auth.Permission{"web.site.read"}},
		{Role: auth.RoleOwner, Scope: acct},
	}}
	authz := auth.NewRoleAuthorizer(nil)
	for name, a := range map[string]auth.Authorizer{"listing": authz, "plain": plainAuthorizer{authz}} {
		r, err := grantScopes(ctx, a, collaborator, "")
		if err != nil || !r.all {
			t.Errorf("%s, any permission = %+v, %v", name, r, err)
		}
		if r, _ := grantScopes(ctx, a, collaborator, "web.site.read"); !r.all {
			t.Errorf("%s, a permission the server grant carries = %+v", name, r)
		}
		if r, _ := grantScopes(ctx, a, collaborator, "web.site.delete"); r.all || !slices.Equal(r.scopes, []store.Scope{acct}) {
			t.Errorf("%s, a permission only the account grant carries = %+v", name, r)
		}
	}
	if _, err := grantScopes(ctx, authz, nil, ""); !errors.Is(err, i18n.ErrUnauthenticated) {
		t.Errorf("without an actor: %v", err)
	}
}
