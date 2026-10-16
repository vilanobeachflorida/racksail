package addons

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// An add-on allowed to change some fields of a kind changes those and
// nothing else: it can't delete a resource, run its actions or relabel it,
// whichever way it asks, until its permissions say so.
func TestLimitedWriteScopes(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.install(t)
	running := func() *auth.Actor {
		t.Helper()
		resource, man, grants, _, ok := e.runtime.Running("site-guard")
		if !ok {
			t.Fatal("site-guard isn't running")
		}
		return grants.Actor(resource, man.ID)
	}
	actor := running()
	acct := store.AccountScope(store.NewID("acct"))
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{"shop.example"}})})
	if err != nil {
		t.Fatal(err)
	}
	siteID := cs.Changes[0].ID
	site, _ := siteKind.Get(ctx, e.store, siteID)

	if _, err := e.submit(actor, changeset.Change{Op: changeset.OpDelete, Kind: "web.site", ID: siteID}); !i18n.ErrForbidden.Is(err) {
		t.Errorf("deleting a site with a permission for its cdn: %v", err)
	}
	if _, err := e.submit(actor, changeset.Change{Op: changeset.OpAction, Kind: "web.site", ID: siteID, Action: "purge"}); !i18n.ErrForbidden.Is(err) {
		t.Errorf("running an action with a permission for the cdn: %v", err)
	}
	relabel := changeset.Change{Op: changeset.OpUpdate, Kind: "web.site", ID: siteID, Spec: spec(t, site.Spec), Labels: map[string]string{"racksail.dev/installation": "inst_forged"}}
	if _, err := e.submit(actor, relabel); !errors.Is(err, hooks.ErrDenied) || !strings.Contains(i18n.From(err).Message, "labels") {
		t.Errorf("relabeling a site with a permission for its cdn: %v", err)
	}
	if got, err := siteKind.Get(ctx, e.store, siteID); err != nil || got.Deleted() || len(got.Labels) != 0 {
		t.Fatalf("the site after all that = %+v, %v", got, err)
	}
	changed := site.Spec
	changed.CDN = "on"
	if _, err := e.submit(actor, changeset.Change{Op: changeset.OpUpdate, Kind: "web.site", ID: siteID, Spec: spec(t, changed)}); err != nil {
		t.Errorf("changing the cdn: %v", err)
	}

	// Asking for them, and having them approved, allows them.
	manifest := strings.Replace(guardManifest, "write: [{kind: web.site, fields: [cdn]}]",
		"write: [{kind: web.site, fields: [cdn], labels: true, delete: true, actions: [purge]}]", 1)
	wider := guard(true)
	man, err := hooks.ParseAddonManifest([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	wider.Manifest, wider.Approved, wider.Catalog, wider.Item = manifest, man.Permissions, "", ""
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: id, Spec: spec(t, wider)}); err != nil {
		t.Fatal(err)
	}
	actor = running()
	if _, err := e.submit(actor, changeset.Change{Op: changeset.OpAction, Kind: "web.site", ID: siteID, Action: "purge"}); err != nil {
		t.Errorf("an action it may run: %v", err)
	}
	site, _ = siteKind.Get(ctx, e.store, siteID)
	relabel.Spec = spec(t, site.Spec)
	if _, err := e.submit(actor, relabel); err != nil {
		t.Errorf("labels it may change: %v", err)
	}
	if _, err := e.submit(actor, changeset.Change{Op: changeset.OpDelete, Kind: "web.site", ID: siteID}); err != nil {
		t.Errorf("a site it may delete: %v", err)
	}
}

func TestWriteScopeChecks(t *testing.T) {
	acct := store.AccountScope(store.NewID("acct"))
	site := store.NewID("site")
	limited := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}}}}, store.ServerScope())
	whole := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site"}}}, store.ServerScope())
	actionOnly := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site", Actions: []string{"purge"}}}}, store.ServerScope())
	for _, tc := range []struct {
		name   string
		grants Grants
		change changeset.ResourceChange
		code   i18n.Code
	}{
		{"a field it may change", limited, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: site, Scope: acct, Diff: []changeset.FieldDiff{{Path: "cdn"}}}, ""},
		{"a field it may not", limited, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: site, Scope: acct, Diff: []changeset.FieldDiff{{Path: "domains[0]"}}}, ErrFieldDenied.Code},
		{"a delete, which has no diff", limited, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: site, Scope: acct}, ErrDeleteDenied.Code},
		{"an action that leaves the spec", limited, changeset.ResourceChange{Op: changeset.OpAction, Action: "purge", Kind: "web.site", ID: site, Scope: acct}, ErrActionDenied.Code},
		{"new labels", limited, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: site, Scope: acct, Labels: map[string]string{"a": "b"}}, ErrLabelsDenied.Code},
		{"labels on a new site", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Labels: map[string]string{"a": "b"}, Diff: []changeset.FieldDiff{{Path: "cdn"}}}, ErrLabelsDenied.Code},
		{"labels kept as they were", limited, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: site, Scope: acct, Labels: map[string]string{"a": "b"}, LabelsBefore: map[string]string{"a": "b"}, Diff: []changeset.FieldDiff{{Path: "cdn"}}}, ""},
		{"another kind", limited, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "dns.zone", Scope: acct}, ErrFieldDenied.Code},
		{"a delete with the whole kind", whole, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: site, Scope: acct}, ""},
		{"an action with the whole kind", whole, changeset.ResourceChange{Op: changeset.OpAction, Action: "purge", Kind: "web.site", ID: site, Scope: acct}, ""},
		{"labels with the whole kind", whole, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: site, Scope: acct, Labels: map[string]string{"a": "b"}}, ""},
		{"the action it may run", actionOnly, changeset.ResourceChange{Op: changeset.OpAction, Action: "purge", Kind: "web.site", ID: site, Scope: acct}, ""},
		{"another action", actionOnly, changeset.ResourceChange{Op: changeset.OpAction, Action: "deploy", Kind: "web.site", ID: site, Scope: acct}, ErrActionDenied.Code},
		{"any field with only an action", actionOnly, changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: site, Scope: acct, Diff: []changeset.FieldDiff{{Path: "cdn"}}}, ErrFieldDenied.Code},
	} {
		err := tc.grants.CheckWrite("Site guard", tc.change)
		if got := i18n.CodeOf(err); got != tc.code {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.code)
		}
	}

	// The API grants follow: deleting and actions only with the whole kind
	// or when the scope names them.
	authz := auth.NewRoleAuthorizer(nil)
	may := func(g Grants, perm auth.Permission) bool {
		return authz.Authorize(context.Background(), g.Actor(store.NewID("addon"), "site-guard"), perm, auth.Target{Scope: acct}) == nil
	}
	if !may(limited, "web.site.update") || may(limited, "web.site.delete") || may(limited, "web.site.purge") {
		t.Error("a scope of one field gives changing it, and neither deleting nor actions")
	}
	if !may(actionOnly, "web.site.purge") || may(actionOnly, "web.site.update") || may(actionOnly, "web.site.delete") {
		t.Error("a scope of one action gives that action alone")
	}
	if fields, ok := actionOnly.CanWrite("web.site"); !ok || fields == nil || len(fields) != 0 {
		t.Errorf("a scope of one action changes no field: %v %v", fields, ok)
	}
}

// What a modify hook's answer adds is held to the add-on's permissions as
// its own requests are.
func TestModifyRepliesStayWithinTheirScopes(t *testing.T) {
	acct := store.AccountScope(store.NewID("acct"))
	site := changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", ID: store.NewID("site"), Scope: acct, After: json.RawMessage(`{"domains":["a.example"]}`)}
	limited := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}}}}, store.ServerScope())
	deleting := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}, Delete: true}}}, store.ServerScope())
	for _, tc := range []struct {
		name   string
		grants Grants
		added  changeset.ResourceChange
		ok     bool
	}{
		{"a delete it may not make", limited, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: store.NewID("site"), Scope: acct}, false},
		{"a delete it may make", deleting, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: store.NewID("site"), Scope: acct}, true},
		{"labels", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Labels: map[string]string{"a": "b"}, After: json.RawMessage(`{"cdn":"on"}`)}, false},
		{"a field it may set", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, After: json.RawMessage(`{"cdn":"on"}`)}, true},
		{"a field it may not", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, After: json.RawMessage(`{"domains":["b.example"]}`)}, false},
	} {
		work := &changeset.ChangeSet{Changes: []changeset.ResourceChange{site.Clone()}}
		// The add-on answers with what it was sent, and what it adds.
		reply := tc.grants.FilterChangeSet(work)
		reply.Changes = append(reply.Changes, tc.added)
		err := tc.grants.Merge(work, reply, []int{0})
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
		if err == nil && len(work.Changes) != 2 {
			t.Errorf("%s: the change wasn't added", tc.name)
		}
	}
}

// An add-on allowed some fields of a kind sees those of its resources and
// nothing else, through every answer of the API: not their status beyond
// its phase, not their labels, not the specs of their history, and no
// search or list that would tell it what the other fields hold.
func TestLimitedReadScopes(t *testing.T) {
	e := newEnv(t)
	e.install(t)
	token, _ := os.ReadFile(filepath.Join(e.dir, "tokens", "site-guard.token"))
	site, zone := store.NewID("site"), store.NewID("zone")
	answers := map[string]string{
		"/api/v1/web/sites": `{"items":[` +
			`{"id":"` + string(site) + `","kind":"web.site","labels":{"team":"red"},"spec":{"domains":["a.example"],"notes":"private"},"status":{"phase":"ready","message":"Serving a.example from /srv/private","conditions":[{"type":"Ready"}]}},` +
			`{"id":"` + string(zone) + `","kind":"dns.zone","labels":{"team":"red"},"spec":{"name":"a.example"},"status":{"phase":"ready","serial":7}}]}`,
		"/api/v1/web/sites/" + string(site) + "/history": `{"versions":[{"generation":1,"spec":{"domains":["a.example"],"notes":"private"},"labels":{"team":"red"}}]}`,
		"/api/v1/search": `{"results":[{"id":"` + string(site) + `","kind":"web.site","title":"private","phase":"ready"}]}`,
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(answers[r.URL.Path]))
	})
	g := &Gateway{Runtime: e.runtime, API: api, Store: e.store}
	get := func(path string) *recorder {
		t.Helper()
		req, _ := http.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+string(token))
		rec := newRecorder()
		g.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/api/v1/web/sites")
	body := rec.body.String()
	if rec.status != http.StatusOK || !strings.Contains(body, "a.example") || !strings.Contains(body, `"phase":"ready"`) {
		t.Fatalf("list = %d %s", rec.status, body)
	}
	for _, leak := range []string{"private", "team", "conditions", "serial"} {
		if strings.Contains(body, leak) {
			t.Errorf("the list shows %s: %s", leak, body)
		}
	}
	if body := get("/api/v1/web/sites/" + string(site) + "/history").body.String(); strings.Contains(body, "private") || strings.Contains(body, "team") || !strings.Contains(body, "a.example") {
		t.Errorf("the history keeps only what the add-on may read: %s", body)
	}
	if body := get("/api/v1/search?q=priv").body.String(); strings.Contains(body, "private") || strings.Contains(body, string(site)) {
		t.Errorf("a search shows a site whose other fields it matched: %s", body)
	}
	for _, query := range []string{"?q=priv", "?note=private", "?sort=-note", "?label=team%3Dred"} {
		if rec := get("/api/v1/web/sites" + query); rec.status != http.StatusForbidden || !strings.Contains(rec.body.String(), "addons.query_denied") {
			t.Errorf("listing with %s = %d %s", query, rec.status, rec.body.String())
		}
	}
	if rec := get("/api/v1/web/sites?domain=a.example&sort=domain"); rec.status != http.StatusOK {
		t.Errorf("filtering by a field it may read = %d %s", rec.status, rec.body.String())
	}
}

func TestScopesCoverWhatIsAsked(t *testing.T) {
	cdn := hooks.DataScope{Kind: "web.site", Fields: []string{"cdn"}}
	for _, tc := range []struct {
		name            string
		asked, approved []hooks.DataScope
		missing         string
	}{
		{"the same", []hooks.DataScope{cdn}, []hooks.DataScope{cdn}, ""},
		{"the whole kind for a field", []hooks.DataScope{cdn}, []hooks.DataScope{{Kind: "web.site"}}, ""},
		{"a field for the whole kind", []hooks.DataScope{{Kind: "web.site"}}, []hooks.DataScope{cdn}, "changing web.site"},
		{"a parent field", []hooks.DataScope{{Kind: "web.site", Fields: []string{"tls.mode"}}}, []hooks.DataScope{{Kind: "web.site", Fields: []string{"tls"}}}, ""},
		{"deleting", []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}, Delete: true}}, []hooks.DataScope{cdn}, "deleting web.site"},
		{"an action", []hooks.DataScope{{Kind: "web.site", Actions: []string{"purge"}}}, []hooks.DataScope{cdn}, "running actions of web.site"},
		{"labels", []hooks.DataScope{{Kind: "web.site", Labels: true}}, []hooks.DataScope{cdn}, "changing the labels of web.site"},
		{"spread over two scopes", []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}, Actions: []string{"purge"}}}, []hooks.DataScope{cdn, {Kind: "web.site", Actions: []string{"purge"}}}, ""},
	} {
		if got := Missing(hooks.Permissions{Write: tc.asked}, hooks.Permissions{Write: tc.approved}); got != tc.missing {
			t.Errorf("%s: missing %q, want %q", tc.name, got, tc.missing)
		}
	}
	if _, err := hooks.ParseAddonManifest([]byte(strings.Replace(guardManifest, "read: [{kind: web.site, fields: [domains]}]", "read: [{kind: web.site, fields: [domains], delete: true}]", 1))); err == nil {
		t.Error("deleting needs a permission to write")
	}
}
