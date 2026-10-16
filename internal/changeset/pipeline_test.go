package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// A ticket's comments are sent from the panel itself, so its action applies
// in the panel and returns an output.
type ticketSpec struct {
	Subject string `json:"subject" minLength:"1"`
}

type commentInput struct {
	Text string `json:"text" minLength:"1"`
}

type commentOutput struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

var ticketKind = store.Define[ticketSpec, pageStatus](store.KindInfo{
	Name: "test.ticket", Prefix: "ticket", Scope: store.ScopeAccount, Title: "Ticket", Plural: "tickets",
	Description: "A support ticket, for actions that run in the panel.",
	Actions:     []store.Action{{Name: "comment", Title: "Comment", Summary: "Add a comment.", Risk: store.RiskLow, Input: commentInput{}, Output: commentOutput{}}},
})

type ticketReconciler struct{}

var (
	commentsMu sync.Mutex
	comments   []string
)

func (ticketReconciler) Plan(ctx context.Context, obj *store.Object[ticketSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (ticketReconciler) Apply(ctx context.Context, obj *store.Object[ticketSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

func (ticketReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (ticketReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	var in commentInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return nil, err
	}
	commentsMu.Lock()
	defer commentsMu.Unlock()
	comments = append(comments, in.Text)
	return commentOutput{Number: len(comments), Text: in.Text}, nil
}

// Labels have no reconciler: they only keep settings.
type labelSpec struct {
	Name string `json:"name"`
}

var labelKind = store.Define[labelSpec, store.Status](store.KindInfo{
	Name: "test.label", Prefix: "label", Scope: store.ScopeAccount, Title: "Label", Plural: "labels",
	Description: "A label, for kinds without a reconciler.",
})

func (e *env) create(kind string, spec any) Change {
	raw, _ := json.Marshal(spec)
	return Change{Op: OpCreate, Kind: kind, Scope: store.AccountScope(e.acct), Spec: raw}
}

func TestApplyingAPreview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	preview := e.submit(t, true, e.createPage(pageSpec{Path: "about.html", Content: "About"}))
	applied, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, IdempotencyKey: "apply-1"})
	if err != nil {
		t.Fatal(err)
	}
	if applied.ID != preview.ID || applied.DryRun || applied.Job == "" {
		t.Fatalf("the preview becomes the applied change set: %+v", applied)
	}
	if again, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, IdempotencyKey: "apply-1"}); err != nil || again.ID != applied.ID {
		t.Errorf("a retry with the same key returns the same change set: %+v, %v", again, err)
	}
	final := e.wait(t, applied)
	if final.State != StateApplied || final.Changes[0].ID != preview.Changes[0].ID {
		t.Errorf("applied = %+v", final)
	}
	if got := e.file(pagesDir + "/about.html"); got != "About" {
		t.Errorf("page = %q", got)
	}
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID}); !errors.Is(err, ErrNotPreview) {
		t.Errorf("applying it a second time: %v", err)
	}
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: store.NewID("chg")}); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("a preview that doesn't exist: %v", err)
	}
}

func TestStalePreviewsAreNotApplied(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	id := e.submit(t, false, e.createPage(pageSpec{Path: "news.html", Content: "v1"})).Changes[0].ID
	raw, _ := json.Marshal(pageSpec{Path: "news.html", Content: "v2"})
	preview := e.submit(t, true, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw})

	// Someone else changes the page's labels, which the preview would have
	// kept; applying it now would do something else than it showed.
	current, _ := e.store.Get(ctx, id)
	other := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: current.Spec, Labels: map[string]string{"team": "news"}})
	if other.State != StateApplied {
		t.Fatal(other)
	}
	_, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID})
	if !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("a stale preview: %v", err)
	}
	fresh := store.ID(i18n.From(err).Params["preview"].(string))
	newPreview, err := e.engine.Get(ctx, fresh)
	if err != nil || !newPreview.DryRun || newPreview.Changes[0].Labels["team"] != "news" {
		t.Fatalf("the new preview shows what applying does now: %+v, %v", newPreview, err)
	}
	if got, _ := e.store.Get(ctx, id); strings.Contains(string(got.Spec), "v2") {
		t.Error("nothing was applied")
	}
	applied, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: fresh})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, applied)
	if got := e.file(pagesDir + "/news.html"); got != "v2" {
		t.Errorf("page = %q", got)
	}
}

func TestPreviewsWithSecretsAreSentAgain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	ch := e.createPage(pageSpec{Path: "vault.html", Content: "x"})
	ch.Secrets = map[string]string{"secret": "hunter2"}
	preview := e.submit(t, true, ch)
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID}); !errors.Is(err, ErrPreviewHasSecrets) {
		t.Errorf("a preview whose secret values weren't kept: %v", err)
	}
}

func TestApplyingAnUndoPreview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	created := e.submit(t, false, e.createPage(pageSpec{Path: "gone.html", Content: "x"}))
	preview, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: created.ID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	undo, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	if undo = e.wait(t, undo); undo.State != StateApplied || undo.UndoOf != created.ID {
		t.Fatalf("undo = %+v", undo)
	}
	if orig, _ := e.engine.Get(ctx, created.ID); orig.State != StateUndone {
		t.Errorf("the original is marked as undone: %s", orig.State)
	}
	if _, err := e.store.Get(ctx, created.Changes[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the page is gone: %v", err)
	}
}

func TestListingChangeSets(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testListingChangeSets(t, newEnv(t, Retention{}, nil)) })
	t.Run("sqlite", func(t *testing.T) { testListingChangeSets(t, buildEnv(t, envConfig{db: openDB(t)})) })
}

func testListingChangeSets(t *testing.T, e *env) {
	ctx := context.Background()
	first := e.submit(t, false, e.createPage(pageSpec{Path: "a.html", Content: "a"}))
	id := first.Changes[0].ID
	raw, _ := json.Marshal(pageSpec{Path: "a.html", Content: "b"})
	second := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw})
	other := e.submit(t, false, e.createPage(pageSpec{Path: "c.html", Content: "c"}))
	preview := e.submit(t, true, e.createPage(pageSpec{Path: "d.html", Content: "d"}))
	ids := func(q ListQuery) []store.ID {
		t.Helper()
		list, _, err := e.engine.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.ID
		for _, cs := range list {
			out = append(out, cs.ID)
		}
		return out
	}
	if got := ids(ListQuery{}); !slices.Equal(got, []store.ID{other.ID, second.ID, first.ID}) {
		t.Errorf("applied change sets, newest first = %v", got)
	}
	if got := ids(ListQuery{Resource: id}); !slices.Equal(got, []store.ID{second.ID, first.ID}) {
		t.Errorf("by resource = %v", got)
	}
	if got := ids(ListQuery{DryRuns: true}); !slices.Equal(got, []store.ID{preview.ID}) {
		t.Errorf("previews = %v", got)
	}
	if got := ids(ListQuery{Actor: e.owner.ID, State: StateApplied, Limit: 1}); !slices.Equal(got, []store.ID{other.ID}) {
		t.Errorf("by actor and state = %v", got)
	}
	_, next, _ := e.engine.List(ctx, ListQuery{Limit: 2})
	if got := ids(ListQuery{Limit: 2, Cursor: next}); !slices.Equal(got, []store.ID{first.ID}) {
		t.Errorf("second page = %v", got)
	}

	// What someone may see: their own change sets, and those whose every
	// resource they reach.
	admin := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	label, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: []Change{e.create("test.label", labelSpec{Name: "x"})}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, label)
	otherAcct := store.AccountScope(store.NewID("acct"))
	elsewhere, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: []Change{{Op: OpCreate, Kind: "test.label", Scope: otherAcct, Spec: json.RawMessage(`{"name":"y"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, elsewhere)
	mine := store.AccountScope(e.acct)
	if got := ids(ListQuery{Visible: &Visibility{Person: e.owner.ID}}); !slices.Equal(got, []store.ID{other.ID, second.ID, first.ID}) {
		t.Errorf("their own = %v", got)
	}
	if got := ids(ListQuery{Visible: &Visibility{Scopes: []store.Scope{mine}}}); !slices.Equal(got, []store.ID{label.ID, other.ID, second.ID, first.ID}) {
		t.Errorf("in an account they reach = %v", got)
	}
	if got := ids(ListQuery{Visible: &Visibility{Objects: []store.ID{elsewhere.Changes[0].ID}}}); !slices.Equal(got, []store.ID{elsewhere.ID}) {
		t.Errorf("of a resource they hold a grant on = %v", got)
	}
	if got := ids(ListQuery{Visible: &Visibility{Person: store.NewID("user")}}); len(got) != 0 {
		t.Errorf("someone with no reach = %v", got)
	}
}

func TestActionsThatRunInThePanel(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"test.ticket.commented"}}, 4)
	defer sub.Close()
	ticket := e.submit(t, false, e.create("test.ticket", ticketSpec{Subject: "Printer on fire"}))
	id := ticket.Changes[0].ID
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpAction, Kind: "test.ticket", ID: id, Action: "comment", Input: json.RawMessage(`{"text":""}`)}}}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("inputs are checked against their schema: %v", err)
	}
	cs := e.submit(t, false, Change{Op: OpAction, Kind: "test.ticket", ID: id, Action: "comment", Input: json.RawMessage(`{"text":"Have you tried water?"}`)})
	var out commentOutput
	if err := json.Unmarshal(cs.Changes[0].Output, &out); err != nil || out.Text != "Have you tried water?" || out.Number == 0 {
		t.Errorf("the action's output is kept with the change set: %s, %v", cs.Changes[0].Output, err)
	}
	select {
	case ev := <-sub.C:
		if !strings.Contains(string(ev.Data), "Have you tried water?") {
			t.Errorf("the action's event carries its output: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Error("no event")
	}
	job, _ := e.jobs.Get(ctx, cs.Job)
	if !strings.Contains(string(job.Result), "outputs") {
		t.Errorf("the job's result has the output too: %s", job.Result)
	}
}

func TestKindsWithoutReconcilersAreReadyOnceStored(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	cs := e.submit(t, false, e.create("test.label", labelSpec{Name: "urgent"}))
	got, err := labelKind.Get(ctx, e.store, cs.Changes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != store.PhaseReady || got.Status.ObservedGeneration != 1 || !got.Status.IsTrue(reconcile.ConditionReady) {
		t.Errorf("status = %+v", got.Status)
	}
	del := e.submit(t, false, Change{Op: OpDelete, Kind: "test.label", ID: got.ID})
	if del.State != StateApplied {
		t.Fatal(del)
	}
	if _, err := e.store.Get(ctx, got.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a deleted label is purged at once: %v", err)
	}
}

func TestChangeSetEvents(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"core.changeset.*"}}, 8)
	defer sub.Close()
	cs := e.submit(t, false, e.createPage(pageSpec{Path: "e.html", Content: "e"}))
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: cs.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, undo)
	var got []string
	var undone events.ChangeSetData
	for len(got) < 3 {
		select {
		case ev := <-sub.C:
			got = append(got, ev.Type)
			if ev.Type == events.ChangeSetUndone.Name {
				if err := json.Unmarshal(ev.Data, &undone); err != nil {
					t.Fatal(err)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("events = %v", got)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"core.changeset.applied", "core.changeset.applied", "core.changeset.undone"}) {
		t.Errorf("events = %v", got)
	}
	if undone.UndoneBy != undo.ID || undone.Resources[0] != cs.Changes[0].ID {
		t.Errorf("undone = %+v", undone)
	}

	// A change that fails says so as a whole too.
	failing := e.createPage(pageSpec{Path: "broken.html", Content: "x"})
	e.sim.FailNext("files.write", pagesDir+"/broken.html", errors.New("disk full"))
	broken := e.submit(t, false, failing)
	if broken.State != StateFailed {
		t.Fatalf("change set = %+v", broken)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != events.ChangeSetFailed.Name || ev.Severity != events.SeverityError || ev.ChangeSet != broken.ID {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Error("no failure event")
	}
}

// orderedHooks records every hook call of the pipeline in order.
type orderedHooks struct {
	mu    sync.Mutex
	calls []string
}

func (o *orderedHooks) record(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, s)
}

func (o *orderedHooks) Validate(ctx context.Context, point string, cs *ChangeSet, change *ResourceChange) error {
	o.record("validate " + point + " " + string(change.Op))
	return nil
}

func (o *orderedHooks) Modify(ctx context.Context, point string, cs *ChangeSet, changes []int) error {
	var idx []string
	for _, i := range changes {
		idx = append(idx, cs.Changes[i].Kind)
	}
	o.record("modify " + point + " " + strings.Join(idx, ","))
	return nil
}

func (o *orderedHooks) Applied(ctx context.Context, point string, cs *ChangeSet, changes []int) {
	if cs.State != StateApplied {
		o.record("applied before the change set was applied")
	}
	o.record("applied " + point)
}

// The stages run in a fixed order: every change's validate hooks, as the
// changes are planned; then each plan point's modify hooks once, with its
// changes, in the order the points first appear; then, once the apply job
// has carried everything out, the applied hooks of each point.
func TestHookStagesRunInOrder(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	hooks := &orderedHooks{}
	e.engine.opts.Hooks = hooks
	pageID := store.NewID("page")
	page := e.createPage(pageSpec{Path: "home.html", Content: "home"})
	page.ID = pageID
	raw, _ := json.Marshal(menuSpec{Pages: []store.ID{pageID}})
	second := e.createPage(pageSpec{Path: "other.html", Content: "other"})
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{
		page, {Op: OpCreate, Kind: "test.menu", Scope: store.AccountScope(e.acct), Spec: raw}, second,
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, cs)
	deadline := time.Now().Add(2 * time.Second)
	for {
		hooks.mu.Lock()
		n := len(hooks.calls)
		hooks.mu.Unlock()
		if n >= 7 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	want := []string{
		"validate test.page.create.validate create",
		"validate test.menu.create.validate create",
		"validate test.page.create.validate create",
		"modify test.page.create.plan test.page,test.page",
		"modify test.menu.create.plan test.menu",
		"applied test.page.create.applied",
		"applied test.menu.create.applied",
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if !slices.Equal(hooks.calls, want) {
		t.Errorf("calls =\n%s\nwant\n%s", strings.Join(hooks.calls, "\n"), strings.Join(want, "\n"))
	}
}

// The panel keeps change sets, previews and idempotency keys in its
// database, so they outlive a restart: previews from before can be applied,
// keys still answer, and change sets from before can still be undone.
func TestChangeSetsOutliveARestart(t *testing.T) {
	db, err := store.OpenDB(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	machine := sim.New(sim.Options{})
	ctx1, stop := context.WithCancel(context.Background())
	first := buildEnv(t, envConfig{db: db, machine: machine, ctx: ctx1})
	created := first.submit(t, false, first.createPage(pageSpec{Path: "kept.html", Content: "v1"}))
	preview := first.submit(t, true, Change{Op: OpUpdate, Kind: "test.page", ID: created.Changes[0].ID, Spec: json.RawMessage(`{"path":"kept.html","content":"v2"}`)})
	keyed, err := first.engine.Submit(context.Background(), Request{Actor: first.owner, IdempotencyKey: "k-1", Changes: []Change{first.createPage(pageSpec{Path: "once.html", Content: "once"})}})
	if err != nil {
		t.Fatal(err)
	}
	first.wait(t, keyed)
	stop()

	ctx2, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := buildEnv(t, envConfig{db: db, machine: machine, ctx: ctx2, vault: first.secrets})
	second.owner, second.acct = first.owner, first.acct
	if again, err := second.engine.Submit(context.Background(), Request{Actor: first.owner, IdempotencyKey: "k-1", Changes: []Change{first.createPage(pageSpec{Path: "once.html", Content: "once"})}}); err != nil || again.ID != keyed.ID {
		t.Errorf("an idempotency key outlives the restart: %+v, %v", again, err)
	}
	applied, err := second.engine.Apply(context.Background(), ApplyRequest{Actor: first.owner, ChangeSet: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	second.wait(t, applied)
	if got := second.file(pagesDir + "/kept.html"); got != "v2" {
		t.Errorf("a preview from before the restart is applied: %q", got)
	}
	undo, err := second.engine.Undo(context.Background(), UndoRequest{Actor: first.owner, ChangeSet: applied.ID})
	if err != nil {
		t.Fatal(err)
	}
	second.wait(t, undo)
	if got := second.file(pagesDir + "/kept.html"); got != "v1" {
		t.Errorf("undo works across the restart: %q", got)
	}
	audit, _, _ := second.bus.QueryAudit(context.Background(), events.Query{Subject: created.Changes[0].ID})
	if len(audit) < 3 {
		t.Errorf("audit records = %d", len(audit))
	}
	actorEvents, _, _ := second.bus.QueryEvents(context.Background(), events.Query{Subject: created.Changes[0].ID})
	if len(actorEvents) < 3 || actorEvents[len(actorEvents)-1].Actor == nil || actorEvents[len(actorEvents)-1].Actor.Type != auth.ActorUser {
		t.Errorf("events = %+v", actorEvents)
	}
}

// Change sets kept before their resources' owners were recorded get them
// from what they changed.
func TestResourceScopesMigration(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	all, err := store.LoadMigrations(MigrationOwner, Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationOwner, fstest.MapFS{"0001_create_change_sets.sql": {Data: []byte(all[0].SQL)}}); err != nil {
		t.Fatal(err)
	}
	acct := store.AccountScope(store.NewID("acct"))
	cs := &ChangeSet{ID: store.NewID(store.PrefixChangeSet), Title: "Create page", State: StateApplied, CreatedAt: time.Now().UTC(),
		Changes: []ResourceChange{{Op: OpCreate, Kind: "test.page", ID: store.NewID("page"), Scope: acct}}}
	body, _ := json.Marshal(cs)
	if _, err := db.Exec(ctx, `INSERT INTO change_sets (id, title, state, dry_run, created_at, body) VALUES (?, ?, 'applied', 0, ?, ?)`,
		cs.ID, cs.Title, store.FormatTime(cs.CreatedAt), string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO change_set_resources (change_set, resource) VALUES (?, ?)`, cs.ID, cs.Changes[0].ID); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLStorage(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := s.List(ctx, ListQuery{Visible: &Visibility{Scopes: []store.Scope{acct}}})
	if err != nil || len(list) != 1 || list[0].ID != cs.ID {
		t.Errorf("found by its resource's owner = %v, %v", list, err)
	}
}
