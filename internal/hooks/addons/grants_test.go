package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// sites counts the sites tests make up names for.
var sites atomic.Int64

// newDomain is a domain no other site has.
func newDomain() string { return fmt.Sprintf("site%d.example", sites.Add(1)) }

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

// siteKinds is a registry of kinds with sites, for a store of them.
func siteKinds(t *testing.T) *store.KindRegistry {
	t.Helper()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(siteKind); err != nil {
		t.Fatal(err)
	}
	return kinds
}

// What a modify hook's answer adds is held to the add-on's permissions as
// its own requests are: deletes, actions and labels only with the grant
// for them, an update only in the fields it may write of the resource as
// the change set leaves it, and nothing outside an account add-on's
// account, whatever scope the answer claims.
func TestModifyRepliesStayWithinTheirScopes(t *testing.T) {
	ctx := context.Background()
	acct := store.AccountScope(store.NewID("acct"))
	other := store.AccountScope(store.NewID("acct"))
	st := store.NewMemory(siteKinds(t))
	stored := func(scope store.Scope, spec string, labels map[string]string) store.ID {
		t.Helper()
		id := store.NewID("site")
		if err := st.Write(ctx, store.WriteMeta{Actor: "system:tests"}, func(tx store.Tx) error {
			return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: id, Kind: "web.site", Scope: scope, Labels: labels}, Spec: json.RawMessage(spec)})
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	labelled := stored(acct, `{"domains":["kept.example"],"notes":"keep"}`, map[string]string{"racksail.dev/installation": "inst_real"})
	theirs := stored(other, `{"domains":["theirs.example"]}`, nil)
	planned := changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", ID: store.NewID("site"), Scope: acct, After: json.RawMessage(`{"domains":["a.example"],"notes":"new"}`), Source: changeset.RequestSource}
	writes := func(scope store.Scope, read []hooks.DataScope, write ...hooks.DataScope) Grants {
		return NewGrants(hooks.Permissions{Read: read, Write: write}, scope)
	}
	limited := writes(store.ServerScope(), nil, hooks.DataScope{Kind: "web.site", Fields: []string{"cdn"}})
	reading := writes(store.ServerScope(), []hooks.DataScope{{Kind: "web.site", Fields: []string{"domains"}}}, hooks.DataScope{Kind: "web.site", Fields: []string{"cdn"}})
	twoFields := writes(store.ServerScope(), nil, hooks.DataScope{Kind: "web.site", Fields: []string{"cdn", "domains"}})
	deleting := writes(store.ServerScope(), nil, hooks.DataScope{Kind: "web.site", Fields: []string{"cdn"}, Delete: true})
	relabeling := writes(store.ServerScope(), nil, hooks.DataScope{Kind: "web.site", Fields: []string{"cdn"}, Labels: true})
	account := writes(acct, nil, hooks.DataScope{Kind: "web.site"})
	update := func(id store.ID, after string, labels map[string]string) changeset.ResourceChange {
		c := changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: id, Labels: labels}
		if after != "" {
			c.After = json.RawMessage(after)
		}
		return c
	}
	for _, tc := range []struct {
		name   string
		grants Grants
		added  changeset.ResourceChange
		// spec is what the added change's spec must be once taken, when it
		// is taken.
		ok   bool
		spec string
	}{
		{"a delete it may not make", limited, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: labelled, Scope: acct}, false, ""},
		{"a delete it may make", deleting, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: labelled, Scope: acct}, true, ""},
		{"a delete of what isn't there", deleting, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: store.NewID("site"), Scope: acct}, false, ""},
		{"an action", limited, changeset.ResourceChange{Op: changeset.OpAction, Action: "purge", Kind: "web.site", ID: labelled}, false, ""},
		{"labels on a new site", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Labels: map[string]string{"a": "b"}, After: json.RawMessage(`{"cdn":"on"}`)}, false, ""},
		{"a field it may set", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, After: json.RawMessage(`{"cdn":"on"}`)}, true, `{"cdn":"on"}`},
		{"a field it may not", limited, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, After: json.RawMessage(`{"domains":["b.example"]}`)}, false, ""},
		{"a create of what exists", account, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", ID: labelled, Scope: acct, After: json.RawMessage(`{"domains":["b.example"]}`)}, false, ""},
		{"empty labels over a site's own", limited, update(labelled, "", map[string]string{}), false, ""},
		{"other labels", limited, update(labelled, "", map[string]string{"racksail.dev/installation": "inst_forged"}), false, ""},
		{"the labels it had", limited, update(labelled, "", map[string]string{"racksail.dev/installation": "inst_real"}), true, ""},
		{"labels it may change", relabeling, update(labelled, "", map[string]string{}), true, ""},
		{"the field it may write", limited, update(labelled, `{"cdn":"on"}`, nil), true, `{"cdn":"on","domains":["kept.example"],"notes":"keep"}`},
		{"a field it can read left out", reading, update(labelled, `{"cdn":"on"}`, nil), false, ""},
		{"what it can read, with its field changed", reading, update(labelled, `{"cdn":"on","domains":["kept.example"]}`, nil), true, `{"cdn":"on","domains":["kept.example"],"notes":"keep"}`},
		{"a field it can read changed", reading, update(labelled, `{"cdn":"on","domains":["stolen.example"]}`, nil), false, ""},
		{"one of its fields, the others kept", twoFields, update(labelled, `{"domains":["kept.example"]}`, nil), true, `{"domains":["kept.example"],"notes":"keep"}`},
		{"a field it may not write, set", twoFields, update(labelled, `{"domains":["kept.example"],"notes":"gone"}`, nil), false, ""},
		{"a site the change set creates", limited, update(planned.ID, `{"cdn":"on"}`, nil), true, `{"cdn":"on","domains":["a.example"],"notes":"new"}`},
		{"another account's site, as its own", account, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: theirs, Scope: acct}, false, ""},
		{"an update in another account", account, update(theirs, `{"domains":["mine.example"]}`, nil), false, ""},
		{"a site in another account", account, changeset.ResourceChange{Op: changeset.OpCreate, Kind: "web.site", Scope: other, After: json.RawMessage(`{"domains":["b.example"]}`)}, false, ""},
		{"a delete in its account", account, changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: labelled, Scope: other}, true, ""},
	} {
		work := &changeset.ChangeSet{Changes: []changeset.ResourceChange{planned.Clone()}}
		// The add-on answers with what it was sent, and what it adds.
		reply := tc.grants.FilterChangeSet(work)
		reply.Changes = append(reply.Changes, tc.added)
		err := tc.grants.Merge(ctx, st, work, reply, []int{0})
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if err != nil {
			continue
		}
		if len(work.Changes) != 2 {
			t.Errorf("%s: the change wasn't added", tc.name)
			continue
		}
		taken := work.Changes[1]
		if tc.spec != "" && !bytes.Equal(normalizeJSON(taken.After), normalizeJSON(json.RawMessage(tc.spec))) {
			t.Errorf("%s: the spec taken = %s, want %s", tc.name, taken.After, tc.spec)
		}
		if taken.Op != changeset.OpCreate {
			if r, err := st.Get(ctx, taken.ID); err == nil && taken.Scope != r.Scope {
				t.Errorf("%s: the change is at %v, not the site's own %v", tc.name, taken.Scope, r.Scope)
			}
		}
	}
}

// What the pipeline plans of a change a modify hook added is checked again,
// in case anything got it past Merge: with the scope, spec and labels the
// resource had, and a diff of its own.
func TestAddedChangesAsPlanned(t *testing.T) {
	acct := store.AccountScope(store.NewID("acct"))
	other := store.AccountScope(store.NewID("acct"))
	site := store.NewID("site")
	limited := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site", Fields: []string{"cdn"}}}}, store.ServerScope())
	account := NewGrants(hooks.Permissions{Write: []hooks.DataScope{{Kind: "web.site"}}}, acct)
	labels := map[string]string{"racksail.dev/installation": "inst_real"}
	planned := func(op changeset.Op, scope store.Scope, before, after string, labelsBefore, labelsAfter map[string]string) changeset.ResourceChange {
		return changeset.ResourceChange{
			Op: op, Kind: "web.site", ID: site, Scope: scope, Before: json.RawMessage(before), After: json.RawMessage(after),
			LabelsBefore: labelsBefore, Labels: labelsAfter, Source: changeset.Source{Type: hooks.OwnerAddon, Name: "site-guard"},
			// What the hook wrote here counts for nothing.
			Diff: []changeset.FieldDiff{{Path: "cdn"}},
		}
	}
	for _, tc := range []struct {
		name   string
		grants Grants
		change changeset.ResourceChange
		code   i18n.Code
	}{
		{"its field", limited, planned(changeset.OpUpdate, acct, `{"domains":["a.example"]}`, `{"domains":["a.example"],"cdn":"on"}`, labels, labels), ""},
		{"a field it left out", limited, planned(changeset.OpUpdate, acct, `{"domains":["a.example"],"notes":"keep"}`, `{"domains":["a.example"],"cdn":"on"}`, labels, labels), ErrFieldDenied.Code},
		{"the labels emptied", limited, planned(changeset.OpUpdate, acct, `{"domains":["a.example"]}`, `{"domains":["a.example"]}`, labels, map[string]string{}), ErrLabelsDenied.Code},
		{"a delete", limited, planned(changeset.OpDelete, acct, `{"domains":["a.example"]}`, `{"domains":["a.example"]}`, nil, nil), ErrDeleteDenied.Code},
		{"labels on a new site", limited, planned(changeset.OpCreate, acct, ``, `{"cdn":"on"}`, nil, labels), ErrLabelsDenied.Code},
		{"a new site with the kind's defaults", limited, planned(changeset.OpCreate, acct, ``, `{"cdn":"on","domains":["filled.in"]}`, nil, nil), ""},
		{"another account's site", account, planned(changeset.OpDelete, other, `{"domains":["a.example"]}`, `{"domains":["a.example"]}`, nil, nil), ErrFieldDenied.Code},
		{"its own account's", account, planned(changeset.OpDelete, acct, `{"domains":["a.example"]}`, `{"domains":["a.example"]}`, nil, nil), ""},
	} {
		if got := i18n.CodeOf(tc.grants.CheckAdded("Site guard", tc.change)); got != tc.code {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.code)
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

// Through the pipeline, a site guard allowed only the cdn field adds
// updates of another site to its modify hook's answers: one that empties
// its labels, one that leaves out a field it can read, and one that is its
// field alone. Only the last is taken, into the site as it was, and the
// others are the hook's failures, which its failure mode lets the change
// go ahead without.
func TestModifyHooksChangeOnlyWhatTheyMayWrite(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	id := e.install(t)
	acct := store.AccountScope(store.NewID("acct"))
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct,
		Spec:   spec(t, siteSpec{Domains: []string{"victim.example"}, Notes: "keep"}),
		Labels: map[string]string{"racksail.dev/installation": "inst_real"}})
	if err != nil {
		t.Fatal(err)
	}
	victim := cs.Changes[0].ID
	answer := func(adds ...map[string]any) {
		e.fake.mu.Lock()
		e.fake.adds = adds
		e.fake.mu.Unlock()
	}
	another := func() *changeset.ChangeSet {
		t.Helper()
		cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{newDomain()}})})
		if err != nil || cs.State != changeset.StateApplied {
			t.Fatalf("another site: %v %+v", err, cs)
		}
		return cs
	}
	victimNow := func() *store.Object[siteSpec, store.Status] {
		t.Helper()
		got, err := siteKind.Get(ctx, e.store, victim)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	answer(map[string]any{"op": "update", "kind": "web.site", "id": string(victim), "labels": map[string]any{}})
	if cs := another(); len(cs.Changes) != 1 {
		t.Errorf("the answer that empties labels was taken: %+v", cs.Changes)
	}
	if got := victimNow(); got.Labels["racksail.dev/installation"] != "inst_real" {
		t.Errorf("the site's labels = %v", got.Labels)
	}
	if e.registry.Failures("addon:site-guard", "web.site.*.plan") == 0 {
		t.Error("an answer that does more than the add-on may isn't the hook's failure")
	}

	// The site guard put the site behind its CDN as it was created.
	answer(map[string]any{"op": "update", "kind": "web.site", "id": string(victim), "after": map[string]any{"cdn": "strict"}})
	if cs := another(); len(cs.Changes) != 1 {
		t.Errorf("the answer without the domains it can read was taken: %+v", cs.Changes)
	}
	if got := victimNow(); len(got.Spec.Domains) != 1 || got.Spec.Notes != "keep" || got.Spec.CDN != "on" {
		t.Errorf("the site after an answer that drops its domains = %+v", got.Spec)
	}

	answer(map[string]any{"op": "update", "kind": "web.site", "id": string(victim), "after": map[string]any{"domains": []string{"victim.example"}, "cdn": "strict"}})
	if cs := another(); len(cs.Changes) != 2 || cs.Changes[1].ID != victim {
		t.Errorf("the answer that changes the cdn alone wasn't taken: %+v", cs.Changes)
	}
	if got := victimNow(); got.Spec.CDN != "strict" || got.Spec.Notes != "keep" || got.Labels["racksail.dev/installation"] != "inst_real" {
		t.Errorf("the site after its cdn changed = %+v %v", got.Spec, got.Labels)
	}

	// With the domains to write too, an answer that leaves out the notes it
	// can't read keeps them.
	manifest := strings.Replace(guardManifest, "write: [{kind: web.site, fields: [cdn]}]", "write: [{kind: web.site, fields: [cdn, domains]}]", 1)
	if _, err := e.submit(e.admin, changeset.Change{Op: changeset.OpUpdate, Kind: "core.addon", ID: id, Spec: spec(t, withKinds(t, manifest))}); err != nil {
		t.Fatal(err)
	}
	answer(map[string]any{"op": "update", "kind": "web.site", "id": string(victim), "after": map[string]any{"domains": []string{"victim.example", "www.victim.example"}, "cdn": "strict"}})
	another()
	if got := victimNow(); len(got.Spec.Domains) != 2 || got.Spec.Notes != "keep" {
		t.Errorf("the site after its domains changed = %+v", got.Spec)
	}
}

// An account's add-on with every permission on sites adds a delete and an
// update of another account's site to its answers, claiming its own
// account. The panel finds the site where it is and takes neither.
func TestAccountAddonsModifyOnlyTheirAccount(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	manifest := strings.NewReplacer(
		"  - {point: web.site.create.validate, kind: validate, security: true, deadline: 2s}\n  - {point: web.site.*.applied, kind: observe}\n",
		"  - {point: web.site.*.plan, kind: modify}\n",
		"hooks: [validate, observe]", "hooks: [modify]",
		"read: [{kind: web.site, fields: [domains]}]", "read: [{kind: web.site}]\n  write: [{kind: web.site}]",
	).Replace(accountManifest)
	e.publish(t, "acme", account(t, manifest))
	mine := store.AccountScope(store.NewID("acct"))
	theirs := store.AccountScope(store.NewID("acct"))
	e.places[mine.ID] = Placement{Slug: "mine", Limits: system.ResourceLimits{MemoryMaxBytes: 48 << 20, CPUQuotaPercent: 10}}
	inst := Instance(AccountAddon.Name(), "site-guard", mine)
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: theirs, Spec: spec(t, siteSpec{Domains: []string{"theirs.example"}, Notes: "theirs"})})
	if err != nil {
		t.Fatal(err)
	}
	victim := cs.Changes[0].ID
	claimed := map[string]any{"level": "account", "id": string(mine.ID)}
	fake := &fakeAddon{}
	fake.serve(t, filepath.Join(e.dir, inst+".sock"))
	if cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: AccountAddon.Name(), Scope: mine, Spec: spec(t, account(t, manifest))}); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("installing: %v %+v", err, cs)
	}
	if _, _, _, _, ok := e.runtime.Running(inst); !ok {
		t.Fatalf("the account's add-on isn't running: %v", e.runtime.Instances())
	}
	for _, add := range []map[string]any{
		{"op": "delete", "kind": "web.site", "id": string(victim), "scope": claimed},
		{"op": "update", "kind": "web.site", "id": string(victim), "scope": claimed, "after": map[string]any{"domains": []string{"theirs.example"}, "notes": "mine now"}},
	} {
		fake.mu.Lock()
		fake.adds = []map[string]any{add}
		fake.mu.Unlock()
		// A server admin changes something in the add-on's account.
		cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: mine, Spec: spec(t, siteSpec{Domains: []string{newDomain()}})})
		if err != nil || cs.State != changeset.StateApplied {
			t.Fatalf("a change in its account: %v %+v", err, cs)
		}
		if len(cs.Changes) != 1 {
			t.Errorf("the %s of another account's site was taken: %+v", add["op"], cs.Changes)
		}
		got, err := siteKind.Get(ctx, e.store, victim)
		if err != nil || got.Deleted() || got.Spec.Notes != "theirs" {
			t.Fatalf("the other account's site after a %s = %+v, %v", add["op"], got, err)
		}
	}
	fake.mu.Lock()
	modified := slices.ContainsFunc(fake.calls, func(c hooks.HookCall) bool { return c.Kind == hooks.Modify })
	fake.mu.Unlock()
	if !modified {
		t.Fatal("the add-on's modify hook was never called")
	}
}

// Whatever got a change past Merge, such as a hook of an add-on's that
// isn't called over its socket, the runtime's validate hook holds it to
// the add-on's permissions as the pipeline planned it, and the change set
// is refused.
func TestAddedChangesAreCheckedAsPlanned(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.install(t)
	acct := store.AccountScope(store.NewID("acct"))
	cs, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct,
		Spec:   spec(t, siteSpec{Domains: []string{"victim.example"}, Notes: "keep"}),
		Labels: map[string]string{"racksail.dev/installation": "inst_real"}})
	if err != nil {
		t.Fatal(err)
	}
	victim := cs.Changes[0].ID
	var adding changeset.ResourceChange
	if err := e.registry.Register(hooks.Registration{
		Point: "web.site.create.plan", Kind: hooks.Modify, Owner: "addon:site-guard", Failure: hooks.Block,
		OnModify: func(ctx context.Context, c *hooks.Context) error {
			c.ChangeSet.Changes = append(c.ChangeSet.Changes, adding)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change changeset.ResourceChange
		denied *i18n.Def
	}{
		{"emptied labels", changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: victim, Labels: map[string]string{}}, ErrLabelsDenied},
		{"a spec without its notes", changeset.ResourceChange{Op: changeset.OpUpdate, Kind: "web.site", ID: victim, After: json.RawMessage(`{"domains":["victim.example"],"cdn":"on"}`), Diff: []changeset.FieldDiff{{Path: "cdn"}}}, ErrFieldDenied},
		{"a delete", changeset.ResourceChange{Op: changeset.OpDelete, Kind: "web.site", ID: victim}, ErrDeleteDenied},
	} {
		adding = tc.change
		_, err := e.submit(e.admin, changeset.Change{Op: changeset.OpCreate, Kind: "web.site", Scope: acct, Spec: spec(t, siteSpec{Domains: []string{newDomain()}})})
		if !errors.Is(err, hooks.ErrDenied) || !errors.Is(err, tc.denied) {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.denied.Code)
		}
	}
	got, err := siteKind.Get(ctx, e.store, victim)
	if err != nil || got.Deleted() || got.Spec.Notes != "keep" || got.Labels["racksail.dev/installation"] != "inst_real" {
		t.Errorf("the site after all that = %+v, %v", got, err)
	}
}
