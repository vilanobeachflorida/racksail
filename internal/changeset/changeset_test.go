package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func TestDiff(t *testing.T) {
	before := json.RawMessage(`{"domains":["a.test","b.test"],"php":{"version":"8.3","memory":128},"labels":{"x.y":"1"},"gone":true}`)
	after := json.RawMessage(`{"domains":["a.test"],"php":{"version":"8.4","memory":128},"labels":{"x.y":"2"},"new":"yes"}`)
	diffs, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DiffOp{}
	for _, d := range diffs {
		got[d.Path] = d.Op
	}
	want := map[string]DiffOp{"domains[1]": DiffRemove, "php.version": DiffReplace, `labels["x.y"]`: DiffReplace, "gone": DiffRemove, "new": DiffAdd}
	if len(got) != len(want) {
		t.Fatalf("diffs = %+v", diffs)
	}
	for p, op := range want {
		if got[p] != op {
			t.Errorf("%s: %s, want %s", p, got[p], op)
		}
	}
	if d, _ := Diff(nil, json.RawMessage(`{"a":1}`)); len(d) != 1 || d[0].Op != DiffAdd || d[0].Path != "a" {
		t.Errorf("create diff = %+v", d)
	}
	if d, _ := Diff(before, before); len(d) != 0 {
		t.Errorf("no change should mean no diffs: %+v", d)
	}
	red := Redact([]FieldDiff{{Path: "password", After: json.RawMessage(`"x"`)}, {Path: "passwordHint", After: json.RawMessage(`"y"`)}, {Path: "db.password", After: json.RawMessage(`"z"`)}}, []string{"password", "db"})
	if !red[0].Sensitive || red[0].After != nil || red[1].Sensitive || !red[2].Sensitive {
		t.Errorf("redact = %+v", red)
	}
}

type pageSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Secret  string `json:"secret,omitempty"`
}

type pageStatus struct {
	store.Status
}

type publishInput struct {
	Message string `json:"message"`
}

func (in publishInput) Validate() error {
	if in.Message == "" {
		return i18n.Invalid(i18n.Field("message", i18n.ErrRequired))
	}
	return nil
}

var pageKind = store.Define[pageSpec, pageStatus](store.KindInfo{
	Name: "test.page", Prefix: "page", Scope: store.ScopeAccount, Title: "Page", Plural: "pages",
	Description: "A static page, for pipeline tests.",
	Actions: []store.Action{
		{Name: "publish", Title: "Publish", Summary: "Publish the page with a message.", Risk: store.RiskLow, Input: publishInput{}},
	},
	UI: store.UIHints{Fields: map[string]store.FieldHint{"secret": {Sensitive: true}}},
}).WithValidator(func(ctx context.Context, obj, old *store.Object[pageSpec, pageStatus]) error {
	if obj.Spec.Path == "" {
		return i18n.Invalid(i18n.Field("spec.path", i18n.ErrRequired))
	}
	return nil
})

type pageReconciler struct{}

func (pageReconciler) Plan(ctx context.Context, obj *store.Object[pageSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	target := "/srv/racksail/pages/" + obj.Spec.Path
	if obj.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: target})}}, nil
	}
	return reconcile.Plan{
		Steps: []agent.Step{
			agent.EnsureDir.Step(agent.EnsureDirInput{Path: "/srv/racksail/pages", Mode: 0o755}),
			agent.WriteFile.Step(agent.WriteFileInput{Path: target, Content: []byte(obj.Spec.Content), Mode: 0o644}),
		},
		Effects: []system.Effect{{Type: system.EffectService, Action: "estimate", Target: "pages", MemoryBytes: 1 << 20}},
	}, nil
}

func (pageReconciler) Apply(ctx context.Context, obj *store.Object[pageSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

func (pageReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	var in publishInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return reconcile.ActionPlan{}, err
	}
	return reconcile.ActionPlan{Plan: reconcile.Plan{Steps: []agent.Step{
		agent.WriteFile.Step(agent.WriteFileInput{Path: "/srv/racksail/pages/.published", Content: []byte(in.Message), Mode: 0o644}),
	}}}, nil
}

type fakeHooks struct {
	deny     bool
	validate []string
	applied  []string
}

func (f *fakeHooks) Validate(ctx context.Context, point string, cs *ChangeSet, change *ResourceChange) error {
	f.validate = append(f.validate, point)
	if f.deny {
		return i18n.ErrForbidden.New("action", "publish pages on Fridays")
	}
	return nil
}

func (f *fakeHooks) Modify(ctx context.Context, point string, cs *ChangeSet) error {
	cs.Effects = append(cs.Effects, system.Effect{Type: system.EffectFile, Action: "write", Target: "/etc/racksail/generated/cdn.conf", Contributor: "addon:cdn"})
	cs.Contributions = append(cs.Contributions, Contribution{Source: Source{Type: "addon", Name: "cdn", Hook: point}, Summary: "Purge the CDN", Effects: []int{len(cs.Effects) - 1}})
	return nil
}

func (f *fakeHooks) Applied(ctx context.Context, point string, cs *ChangeSet) {
	f.applied = append(f.applied, point)
}

type env struct {
	engine *Engine
	store  *store.Memory
	sim    *sim.Machine
	bus    *events.Memory
	jobs   *jobs.Memory
	hooks  *fakeHooks
	owner  *auth.Actor
	acct   store.ID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(pageKind); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(kinds)
	m := sim.New(sim.Options{})
	recs := reconcile.NewRegistry()
	if err := recs.Register(reconcile.Typed(pageKind, pageReconciler{})); err != nil {
		t.Fatal(err)
	}
	bus := events.NewMemory(100)
	q := jobs.NewMemory(ctx, 2, nil)
	hooks := &fakeHooks{}
	engine, err := NewEngine(Options{
		Store: st, Kinds: kinds, Jobs: q, Events: bus, Audit: bus, Hooks: hooks,
		Runner:     &reconcile.Runner{Store: st, Agent: agent.NewLocal(m.Backend(), nil), Registry: recs},
		Authorizer: auth.NewRoleAuthorizer(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	acct := store.NewID("acct")
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(acct)}}}
	return &env{engine: engine, store: st, sim: m, bus: bus, jobs: q, hooks: hooks, owner: owner, acct: acct}
}

func (e *env) create(t *testing.T, spec pageSpec, dryRun bool) *ChangeSet {
	t.Helper()
	raw, _ := json.Marshal(spec)
	cs, err := e.engine.Submit(context.Background(), Request{Actor: e.owner, Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: raw, DryRun: dryRun})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func (e *env) wait(t *testing.T, cs *ChangeSet) *ChangeSet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.jobs.Wait(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final, err := e.engine.Get(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func TestDryRunPreview(t *testing.T) {
	e := newEnv(t)
	cs := e.create(t, pageSpec{Path: "about.html", Content: "About us", Secret: "hunter2"}, true)
	if !cs.DryRun || cs.State != StatePlanned || cs.Job != "" {
		t.Errorf("dry run = %+v", cs)
	}
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpCreate || cs.Changes[0].ID.Prefix() != "page" {
		t.Fatalf("changes = %+v", cs.Changes)
	}
	for _, d := range cs.Changes[0].Diff {
		if d.Path == "secret" && (!d.Sensitive || d.After != nil) {
			t.Errorf("the secret leaked into the diff: %+v", d)
		}
	}
	var files, contributed int
	for _, eff := range cs.Effects {
		if eff.Type == system.EffectFile && eff.Contributor == "" {
			files++
		}
		if eff.Contributor == "addon:cdn" {
			contributed++
		}
	}
	if files < 2 || contributed != 1 || len(cs.Contributions) != 1 || cs.Estimate.MemoryBytes != 1<<20 {
		t.Errorf("effects = %+v, contributions = %+v, estimate = %+v", cs.Effects, cs.Contributions, cs.Estimate)
	}
	if page, _ := e.store.List(context.Background(), store.Query{Kind: "test.page"}); len(page.Items) != 0 {
		t.Error("a dry run must not store anything")
	}
	if len(e.sim.Calls()) != 0 {
		for _, c := range e.sim.Calls() {
			if c.Op == "write" || c.Op == "mkdir" {
				t.Errorf("a dry run changed the machine: %v", c)
			}
		}
	}
}

func TestApplyUpdateUndo(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"test.page.*"}}, 16)
	defer sub.Close()

	cs := e.wait(t, e.create(t, pageSpec{Path: "index.html", Content: "v1"}, false))
	if cs.State != StateApplied {
		t.Fatalf("change set = %+v", cs)
	}
	id := cs.Changes[0].ID
	data, _ := e.sim.Backend().Files.ReadFile(ctx, "/srv/racksail/pages/index.html", 0)
	if string(data) != "v1" {
		t.Errorf("page = %q", data)
	}
	if ev := <-sub.C; ev.Type != "test.page.created" || ev.Subject != id || ev.ChangeSet != cs.ID {
		t.Errorf("event = %+v", ev)
	}
	got, _ := pageKind.Get(ctx, e.store, id)
	if got.Status.Phase != store.PhaseReady {
		t.Errorf("status = %+v", got.Status)
	}

	raw, _ := json.Marshal(pageSpec{Path: "index.html", Content: "v2"})
	update, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw, Revision: got.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if update = e.wait(t, update); update.State != StateApplied {
		t.Fatalf("update = %+v", update)
	}
	if len(update.Changes[0].Diff) != 1 || update.Changes[0].Diff[0].Path != "content" {
		t.Errorf("update diff = %+v", update.Changes[0].Diff)
	}
	data, _ = e.sim.Backend().Files.ReadFile(ctx, "/srv/racksail/pages/index.html", 0)
	if string(data) != "v2" {
		t.Errorf("page after update = %q", data)
	}

	stale, _ := json.Marshal(pageSpec{Path: "index.html", Content: "v3"})
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpUpdate, Kind: "test.page", ID: id, Spec: stale, Revision: got.Revision}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a stale revision should conflict: %v", err)
	}

	undo, err := e.engine.Undo(ctx, e.owner, update.ID)
	if err != nil {
		t.Fatal(err)
	}
	if undo = e.wait(t, undo); undo.State != StateApplied || undo.UndoOf != update.ID {
		t.Fatalf("undo = %+v", undo)
	}
	data, _ = e.sim.Backend().Files.ReadFile(ctx, "/srv/racksail/pages/index.html", 0)
	if string(data) != "v1" {
		t.Errorf("page after undo = %q", data)
	}
	if orig, _ := e.engine.Get(ctx, update.ID); orig.State != StateUndone {
		t.Errorf("the undone change set should say so: %s", orig.State)
	}
	if _, err := e.engine.Undo(ctx, e.owner, update.ID); !errors.Is(err, ErrNotApplied) {
		t.Errorf("undoing twice: %v", err)
	}

	del, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpDelete, Kind: "test.page", ID: id})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, del)
	if _, err := e.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the page should be purged after its files are gone: %v", err)
	}
	if _, err := e.sim.Backend().Files.Stat(ctx, "/srv/racksail/pages/index.html"); !errors.Is(err, system.ErrNotFound) {
		t.Error("the page file should be removed")
	}
	records, _, _ := e.bus.QueryAudit(ctx, events.Query{Subject: id})
	if len(records) < 3 {
		t.Errorf("audit records = %d", len(records))
	}
}

func TestActionsAndValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cs := e.wait(t, e.create(t, pageSpec{Path: "news.html", Content: "news"}, false))
	id := cs.Changes[0].ID
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpAction, Kind: "test.page", ID: id, Action: "publish", Input: json.RawMessage(`{}`)}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("missing action input: %v", err)
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpAction, Kind: "test.page", ID: id, Action: "explode"}); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("unknown action: %v", err)
	}
	pub, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpAction, Kind: "test.page", ID: id, Action: "publish", Input: json.RawMessage(`{"message":"hello"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if pub.Title != "Publish page" {
		t.Errorf("title = %q", pub.Title)
	}
	e.wait(t, pub)
	data, _ := e.sim.Backend().Files.ReadFile(ctx, "/srv/racksail/pages/.published", 0)
	if string(data) != "hello" {
		t.Errorf("the action's step didn't run: %q", data)
	}
	events, _, _ := e.bus.QueryEvents(ctx, eventsQuery("test.page.published"))
	if len(events) != 1 {
		t.Errorf("published events = %d", len(events))
	}

	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: json.RawMessage(`{"content":"x"}`)}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("kind validation: %v", err)
	}
	stranger := &auth.Actor{Type: auth.ActorUser, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(store.NewID("acct"))}}}
	if _, err := e.engine.Submit(ctx, Request{Actor: stranger, Op: OpDelete, Kind: "test.page", ID: id}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("another account's owner: %v", err)
	}
	e.hooks.deny = true
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Op: OpDelete, Kind: "test.page", ID: id}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("a validate hook should deny: %v", err)
	}
	if e.hooks.validate[len(e.hooks.validate)-1] != "test.page.delete.validate" {
		t.Errorf("hook points = %v", e.hooks.validate)
	}
}

func eventsQuery(typ string) events.Query { return events.Query{Types: []string{typ}} }

func TestIdempotency(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := Request{Actor: e.owner, Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: json.RawMessage(`{"path":"a.html","content":"a"}`), IdempotencyKey: "retry-1"}
	first, err := e.engine.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.engine.Submit(ctx, req)
	if err != nil || second.ID != first.ID {
		t.Errorf("a retried request should return the first change set: %v, %v", second, err)
	}
	e.wait(t, first)
	page, _ := e.store.List(ctx, store.Query{Kind: "test.page"})
	if len(page.Items) != 1 {
		t.Errorf("pages = %d", len(page.Items))
	}
}
