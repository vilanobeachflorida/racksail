package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

type siteResource struct {
	store.Meta
	Spec   siteSpec   `json:"spec"`
	Status siteStatus `json:"status"`
}

type siteList struct {
	Items []siteResource `json:"items"`
	Next  string         `json:"next"`
}

// A kind registered in test code gets its whole API: create, update, delete
// and actions, each previewed with dryRun and applied through the change
// pipeline against the simulated server, with history, events, audit
// records and undo.
func TestKindOperations(t *testing.T) {
	s := newStack(t, RateLimit{})
	scope := map[string]any{"level": "account", "id": s.account}
	sites := "/api/v1/test/sites"

	var empty siteList
	if resp := s.call("owner", "GET", sites+"?scope=account:"+string(s.account), nil, &empty); resp.StatusCode != 200 || len(empty.Items) != 0 {
		t.Fatalf("an empty list = %d %+v", resp.StatusCode, empty)
	}

	// A dry run answers with the preview and changes nothing.
	create := map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"Example.com"}, "content": "v1"}, "labels": map[string]string{"env": "prod"}}
	var preview changeset.ChangeSet
	resp := s.call("owner", "POST", sites+"?dryRun=true", create, &preview)
	if resp.StatusCode != http.StatusOK || !preview.DryRun || preview.Job != "" || preview.Changes[0].Op != changeset.OpCreate {
		t.Fatalf("preview = %d %+v", resp.StatusCode, preview)
	}
	var wrote bool
	for _, eff := range preview.Effects {
		wrote = wrote || (eff.Type == system.EffectFile && strings.HasSuffix(eff.Target, ".html"))
	}
	if !wrote || preview.Risk != store.RiskLow {
		t.Errorf("the preview lists the page it writes: %+v", preview.Effects)
	}
	if s.call("owner", "GET", sites+"?scope=account:"+string(s.account), nil, &empty); len(empty.Items) != 0 {
		t.Error("a dry run stores nothing")
	}

	// Applying it answers 202 with the change set; its job writes the page.
	var cs changeset.ChangeSet
	resp = s.call("owner", "POST", sites, create, &cs, "Idempotency-Key", "create-1", "X-Request-Id", "req-create-0001")
	if resp.StatusCode != http.StatusAccepted || cs.Job == "" || cs.DryRun {
		t.Fatalf("create = %d %s", resp.StatusCode, bodyOf(resp))
	}
	id := cs.Changes[0].ID
	var again changeset.ChangeSet
	if s.call("owner", "POST", sites, create, &again, "Idempotency-Key", "create-1"); again.ID != cs.ID {
		t.Errorf("a retry with the same key returns the first change set: %s, %s", again.ID, cs.ID)
	}
	if final := s.finish("owner", &cs); final.State != changeset.StateApplied {
		t.Fatalf("applied = %+v", final)
	}
	if got := s.file(sitePath(id)); got != "v1" {
		t.Errorf("page = %q", got)
	}
	var site siteResource
	if resp := s.call("owner", "GET", sites+"/"+string(id), nil, &site); resp.StatusCode != 200 || site.Status.Phase != store.PhaseReady || site.Labels["env"] != "prod" || site.Status.Path == "" {
		t.Fatalf("site = %d %+v", resp.StatusCode, site)
	}

	// Others can't see it, and can't find out it exists.
	if resp := s.call("stranger", "GET", sites+"/"+string(id), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another account's owner = %d", resp.StatusCode)
	}
	if resp := s.call("stranger", "PUT", sites+"/"+string(id), map[string]any{"spec": map[string]any{"domains": []string{"x.test"}, "content": "x"}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another account's owner changing it = %d", resp.StatusCode)
	}
	if resp := s.call("stranger", "GET", sites+"?scope=account:"+string(s.account), nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another account's owner listing it = %d", resp.StatusCode)
	}
	if resp := s.call("", "GET", sites, nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without signing in = %d", resp.StatusCode)
	}

	// An update with the revision that was read.
	update := map[string]any{"spec": map[string]any{"domains": []string{"example.com", "www.example.com"}, "content": "v2"}, "revision": site.Revision}
	resp = s.call("owner", "PUT", sites+"/"+string(id), update, &cs)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("update = %d %s", resp.StatusCode, bodyOf(resp))
	}
	updated := s.finish("owner", &cs)
	if s.file(sitePath(id)) != "v2" || len(updated.Changes[0].Diff) == 0 || updated.Changes[0].LabelsBefore["env"] != "prod" {
		t.Errorf("after the update: %q %+v", s.file(sitePath(id)), updated.Changes[0])
	}
	if resp := s.call("owner", "PUT", sites+"/"+string(id), update, nil); resp.StatusCode != http.StatusConflict || s.problem(resp) != i18n.ErrConflict.Code {
		t.Errorf("a stale revision = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var history historyBody
	if s.call("owner", "GET", sites+"/"+string(id)+"/history", nil, &history); len(history.Versions) != 2 || history.Versions[1].ChangeSet != updated.ID || !strings.Contains(history.Versions[0].Actor, "user:") {
		t.Errorf("history = %+v", history)
	}

	// A short action is waited for; the answer is the finished change set.
	var deployed changeset.ChangeSet
	resp = s.call("owner", "POST", sites+"/"+string(id)+"/actions/deploy", map[string]string{"message": "release 2"}, &deployed)
	if resp.StatusCode != http.StatusOK || deployed.State != changeset.StateApplied {
		t.Fatalf("deploy = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if got := s.file(sitePath(id) + ".release"); got != "release 2" {
		t.Errorf("the action's step ran: %q", got)
	}
	var pinged changeset.ChangeSet
	if resp := s.call("owner", "POST", sites+"/"+string(id)+"/actions/ping", nil, &pinged); resp.StatusCode != http.StatusOK || !strings.Contains(string(pinged.Changes[0].Output), `"pong":true`) {
		t.Errorf("an action's output = %d %s", resp.StatusCode, pinged.Changes[0].Output)
	}
	var rebuilding changeset.ChangeSet
	if resp := s.call("owner", "POST", sites+"/"+string(id)+"/actions/rebuild", nil, &rebuilding); resp.StatusCode != http.StatusAccepted {
		t.Errorf("a long action answers 202 at once: %d", resp.StatusCode)
	} else {
		s.finish("owner", &rebuilding)
	}
	if resp := s.call("owner", "POST", sites+"/"+string(id)+"/actions/deploy", map[string]string{"message": ""}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an input that doesn't match its schema = %d %s", resp.StatusCode, bodyOf(resp))
	}

	// Events and audit records name the change set and the request.
	var timeline struct {
		Events []events.Event `json:"events"`
	}
	s.call("owner", "GET", "/api/v1/events?subject="+string(id), nil, &timeline)
	var types []string
	for _, e := range timeline.Events {
		types = append(types, e.Type)
	}
	for _, want := range []string{"test.site.created", "test.site.updated", "test.site.deployed", "test.site.pinged"} {
		if !slices.Contains(types, want) {
			t.Errorf("events = %v, missing %s", types, want)
		}
	}
	var audit struct {
		Records []events.AuditRecord `json:"records"`
	}
	s.call("admin", "GET", "/api/v1/audit?subject="+string(id), nil, &audit)
	var fromRequest bool
	for _, r := range audit.Records {
		fromRequest = fromRequest || (r.Action == "test.site.create" && r.RequestID == "req-create-0001" && r.Actor.Name == "owner")
	}
	if len(audit.Records) < 4 || !fromRequest {
		t.Errorf("audit = %+v", audit.Records)
	}
	if resp := s.call("stranger", "GET", "/api/v1/audit?scope=account:"+string(s.account), nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another account's audit records = %d", resp.StatusCode)
	}

	// Undo the update through the API.
	var undo changeset.ChangeSet
	if resp := s.call("owner", "POST", "/api/v1/changesets/"+string(updated.ID)+"/undo?dryRun=true", nil, &undo); resp.StatusCode != http.StatusOK || !undo.DryRun || undo.UndoOf != updated.ID {
		t.Fatalf("undo preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("owner", "POST", "/api/v1/changesets/"+string(updated.ID)+"/undo", nil, &undo); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("undo = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if s.finish("owner", &undo); s.file(sitePath(id)) != "v1" {
		t.Errorf("after undo: %q", s.file(sitePath(id)))
	}
	var list struct {
		ChangeSets []changeset.ChangeSet `json:"changeSets"`
	}
	s.call("owner", "GET", "/api/v1/changesets?resource="+string(id), nil, &list)
	if len(list.ChangeSets) < 5 || list.ChangeSets[0].ID != undo.ID {
		t.Errorf("the resource's change sets = %d", len(list.ChangeSets))
	}
	if resp := s.call("stranger", "GET", "/api/v1/changesets/"+string(undo.ID), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another account's change set = %d", resp.StatusCode)
	}

	// Delete: a preview first, then for real.
	var del changeset.ChangeSet
	if resp := s.call("owner", "DELETE", sites+"/"+string(id)+"?dryRun=true", nil, &del); resp.StatusCode != http.StatusOK || !del.DryRun || del.Risk != store.RiskHigh {
		t.Fatalf("delete preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("owner", "DELETE", sites+"/"+string(id), nil, &del); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("owner", &del)
	if resp := s.call("owner", "GET", sites+"/"+string(id), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted site = %d", resp.StatusCode)
	}
	if s.file(sitePath(id)) != "" {
		t.Error("the page is gone from the server")
	}

	// And back, by undoing the delete.
	if resp := s.call("owner", "POST", "/api/v1/changesets/"+string(del.ID)+"/undo", nil, &undo); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("undoing the delete = %d %s", resp.StatusCode, bodyOf(resp))
	}
	s.finish("owner", &undo)
	if resp := s.call("owner", "GET", sites+"/"+string(id), nil, &site); resp.StatusCode != http.StatusOK || site.Spec.Content != "v1" {
		t.Errorf("the site is back: %d %+v", resp.StatusCode, site)
	}
}

func TestKindValidationAndHooks(t *testing.T) {
	s := newStack(t, RateLimit{})
	sites := "/api/v1/test/sites"
	scope := map[string]any{"level": "account", "id": s.account}
	for name, tc := range map[string]struct {
		body  any
		code  i18n.Code
		field string
	}{
		"missing domains": {map[string]any{"scope": scope, "spec": map[string]any{"content": "x"}}, i18n.ErrInvalid.Code, "spec.domains"},
		"unknown field":   {map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"a.test"}, "content": "x", "colour": "red"}}, i18n.ErrInvalid.Code, "spec.colour"},
		"long note":       {map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"a.test"}, "content": "x", "note": strings.Repeat("n", 41)}}, i18n.ErrInvalid.Code, "spec.note"},
		"no scope":        {map[string]any{"spec": map[string]any{"domains": []string{"a.test"}, "content": "x"}}, i18n.ErrInvalid.Code, "body.scope"},
		"no spec":         {map[string]any{"scope": scope}, i18n.ErrInvalid.Code, "body.spec"},
		"unknown key":     {map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"a.test"}, "content": "x"}, "color": 1}, i18n.ErrBadRequest.Code, ""},
		"hook denies":     {map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"shop.invalid"}, "content": "x"}}, "hooks.denied", ""},
	} {
		resp := s.call("owner", "POST", sites+"?dryRun=true", tc.body, nil)
		var p struct {
			Code   i18n.Code `json:"code"`
			Errors []struct {
				Location string `json:"location"`
			} `json:"errors"`
		}
		json.Unmarshal([]byte(bodyOf(resp)), &p)
		if p.Code != tc.code || (tc.field != "" && (len(p.Errors) == 0 || p.Errors[0].Location != tc.field)) {
			t.Errorf("%s: %d %+v", name, resp.StatusCode, p)
		}
	}
	// A unique index refuses a value another site holds, with the kind's
	// own error.
	create := map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"taken.test"}, "content": "x"}}
	var cs changeset.ChangeSet
	s.call("owner", "POST", sites, create, &cs)
	s.finish("owner", &cs)
	if resp := s.call("owner", "POST", sites+"?dryRun=true", create, nil); resp.StatusCode != http.StatusConflict || s.problem(resp) != errDomainTaken.Code {
		t.Errorf("a taken domain = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("nobody", "POST", sites+"?dryRun=true", map[string]any{"scope": scope, "spec": map[string]any{"domains": []string{"n.test"}, "content": "x"}}, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("without a grant = %d", resp.StatusCode)
	}
}

func TestListingKinds(t *testing.T) {
	s := newStack(t, RateLimit{})
	sites := "/api/v1/test/sites"
	scope := map[string]any{"level": "account", "id": s.account}
	var ids []store.ID
	for i, d := range []string{"charlie.test", "alpha.test", "bravo.test"} {
		var cs changeset.ChangeSet
		labels := map[string]string{"team": "web"}
		if i == 1 {
			labels["team"] = "shop"
		}
		s.call("owner", "POST", sites, map[string]any{"scope": scope, "labels": labels, "spec": map[string]any{"domains": []string{d}, "content": d}}, &cs)
		s.finish("owner", &cs)
		ids = append(ids, cs.Changes[0].ID)
	}
	list := func(query string) []store.ID {
		t.Helper()
		var out siteList
		resp := s.call("owner", "GET", sites+"?scope=account:"+string(s.account)+"&"+query, nil, &out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, bodyOf(resp))
		}
		var got []store.ID
		for _, r := range out.Items {
			got = append(got, r.ID)
		}
		return got
	}
	for query, want := range map[string][]store.ID{
		"":                     ids,
		"sort=-id":             {ids[2], ids[1], ids[0]},
		"sort=domain":          {ids[1], ids[2], ids[0]},
		"domain=ALPHA.test":    {ids[1]},
		"label=team%3Dweb":     {ids[0], ids[2]},
		"q=bravo":              {ids[2]},
		"phase=ready":          ids,
		"phase=failed,pending": nil,
	} {
		if got := list(query); !slices.Equal(got, want) {
			t.Errorf("%q = %v, want %v", query, got, want)
		}
	}
	// Every page is the same size and follows on.
	var page siteList
	s.call("owner", "GET", sites+"?scope=account:"+string(s.account)+"&limit=2&sort=domain", nil, &page)
	var rest siteList
	s.call("owner", "GET", sites+"?scope=account:"+string(s.account)+"&limit=2&sort=domain&cursor="+url.QueryEscape(page.Next), nil, &rest)
	if len(page.Items) != 2 || page.Next == "" || len(rest.Items) != 1 || rest.Items[0].ID != ids[0] {
		t.Errorf("pages = %+v / %+v", page, rest)
	}
	// Field selection.
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	s.call("owner", "GET", sites+"?scope=account:"+string(s.account)+"&fields=spec.domains,status.phase", nil, &raw)
	if len(raw.Items) != 3 {
		t.Fatalf("items = %+v", raw)
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range raw.Items[0] {
			if !yield(k) {
				return
			}
		}
	})
	if fmt.Sprint(keys) != "[id kind spec status]" || raw.Items[0]["spec"].(map[string]any)["content"] != nil {
		t.Errorf("selected fields = %+v", raw.Items[0])
	}
	for query, code := range map[string]i18n.Code{
		"sort=colour":        i18n.ErrBadRequest.Code,
		"fields=colour":      i18n.ErrInvalid.Code,
		"label=team":         i18n.ErrInvalid.Code,
		"phase=sleepy":       i18n.ErrBadRequest.Code,
		"limit=0":            i18n.ErrInvalid.Code,
		"scope=account:nope": i18n.ErrInvalid.Code,
		"cursor=nope":        i18n.ErrBadRequest.Code,
	} {
		resp := s.call("owner", "GET", sites+"?"+query, nil, nil)
		if got := s.problem(resp); got != code {
			t.Errorf("%s = %d %s", query, resp.StatusCode, got)
		}
	}
	// Without a scope, a list covers the accounts the caller's grants
	// reach.
	if got := list(""); len(got) != 3 {
		t.Errorf("by grants = %v", got)
	}
	var fromGrants siteList
	s.call("owner", "GET", sites, nil, &fromGrants)
	if len(fromGrants.Items) != 3 {
		t.Errorf("an owner's list without scope = %+v", fromGrants)
	}
	var other siteList
	if resp := s.call("stranger", "GET", sites, nil, &other); resp.StatusCode != http.StatusOK || len(other.Items) != 0 {
		t.Errorf("another owner without scope sees nothing of this account: %d %+v", resp.StatusCode, other)
	}
	// Any resource by ID.
	var generic map[string]any
	if resp := s.call("owner", "GET", "/api/v1/resources/"+string(ids[0])+"?fields=spec.domains", nil, &generic); resp.StatusCode != http.StatusOK || generic["kind"] != "test.site" {
		t.Errorf("by ID = %d %+v", resp.StatusCode, generic)
	}
	if resp := s.call("stranger", "GET", "/api/v1/resources/"+string(ids[0]), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another's resource by ID = %d", resp.StatusCode)
	}
}

func TestSubmittingAndApplyingChangeSets(t *testing.T) {
	s := newStack(t, RateLimit{})
	site := store.NewID("tsite")
	body := map[string]any{"title": "Two sites", "changes": []map[string]any{
		{"op": "create", "kind": "test.site", "id": site, "scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"one.test"}, "content": "1"}},
		{"op": "create", "kind": "test.site", "scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"two.test"}, "content": "2"}},
	}}
	var preview changeset.ChangeSet
	if resp := s.call("owner", "POST", "/api/v1/changesets?dryRun=true", body, &preview); resp.StatusCode != http.StatusOK || len(preview.Changes) != 2 || preview.Title != "Two sites" {
		t.Fatalf("preview = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var applied changeset.ChangeSet
	if resp := s.call("owner", "POST", "/api/v1/changesets/"+string(preview.ID)+"/apply", nil, &applied); resp.StatusCode != http.StatusAccepted || applied.ID != preview.ID {
		t.Fatalf("apply = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if final := s.finish("owner", &applied); final.State != changeset.StateApplied || s.file(sitePath(site)) != "1" {
		t.Errorf("applied = %+v", final)
	}
	if resp := s.call("owner", "POST", "/api/v1/changesets/"+string(preview.ID)+"/apply", nil, nil); s.problem(resp) != changeset.ErrNotPreview.Code {
		t.Errorf("applying twice = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var previews struct {
		ChangeSets []changeset.ChangeSet `json:"changeSets"`
	}
	s.call("owner", "GET", "/api/v1/changesets?previews=true", nil, &previews)
	if len(previews.ChangeSets) != 0 {
		t.Errorf("an applied preview is no longer one: %+v", previews.ChangeSets)
	}
	if resp := s.call("owner", "POST", "/api/v1/changesets", map[string]any{"changes": []any{}}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("no changes = %d", resp.StatusCode)
	}

	// Jobs of the change set, their logs, and retrying and canceling.
	var jobList struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	s.call("owner", "GET", "/api/v1/jobs?changeSet="+string(applied.ID), nil, &jobList)
	if len(jobList.Jobs) != 1 || jobList.Jobs[0].State != jobs.StateSucceeded {
		t.Fatalf("jobs = %+v", jobList)
	}
	job := jobList.Jobs[0]
	var logs struct {
		Lines []jobs.LogLine `json:"lines"`
	}
	if s.call("owner", "GET", "/api/v1/jobs/"+string(job.ID)+"/logs", nil, &logs); len(logs.Lines) == 0 {
		t.Error("the job has a log")
	}
	if resp := s.call("owner", "POST", "/api/v1/jobs/"+string(job.ID)+"/retry", nil, nil); s.problem(resp) != jobs.ErrNotRetryable.Code {
		t.Errorf("retrying a job that succeeded = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := s.call("owner", "POST", "/api/v1/jobs/"+string(job.ID)+"/cancel", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("canceling a finished job = %d", resp.StatusCode)
	}
	if resp := s.call("stranger", "GET", "/api/v1/jobs/"+string(job.ID), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another's job = %d", resp.StatusCode)
	}
	var adminJobs struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if s.call("admin", "GET", "/api/v1/jobs", nil, &adminJobs); len(adminJobs.Jobs) == 0 {
		t.Error("the admin sees every job")
	}
}
