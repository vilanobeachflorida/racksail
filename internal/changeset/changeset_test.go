package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
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
	// Secret is a reference to the page's access key.
	Secret string `json:"secret,omitempty"`
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
		{Name: "touch", Summary: "Mark the page as seen.", Risk: store.RiskLow},
	},
	Indexes: []store.Index{{Name: "path", Path: "path", Unique: true, PerScope: true}},
	UI:      store.UIHints{Fields: map[string]store.FieldHint{"secret": {Sensitive: true}}},
}).WithValidator(func(ctx context.Context, obj, old *store.Object[pageSpec, pageStatus]) error {
	if obj.Spec.Path == "" {
		return i18n.Invalid(i18n.Field("spec.path", i18n.ErrRequired))
	}
	return nil
})

const pagesDir = "/srv/racksail/pages"

type pageReconciler struct{}

func (pageReconciler) Plan(ctx context.Context, obj *store.Object[pageSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	target := pagesDir + "/" + obj.Spec.Path
	if obj.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{
			agent.RemovePath.Step(agent.RemovePathInput{Path: target}),
			agent.RemovePath.Step(agent.RemovePathInput{Path: target + ".key"}),
		}}, nil
	}
	steps := []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: pagesDir, Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: target, Content: []byte(obj.Spec.Content), Mode: 0o644}),
	}
	if obj.Spec.Secret != "" {
		key, err := req.Secrets.Get(ctx, secrets.Ref(obj.Spec.Secret))
		if err != nil {
			return reconcile.Plan{}, err
		}
		steps = append(steps, agent.WriteFile.Step(agent.WriteFileInput{Path: target + ".key", Content: key, Mode: 0o600}))
	}
	return reconcile.Plan{
		Steps:   steps,
		Effects: []system.Effect{{Type: system.EffectService, Action: "estimate", Target: "pages", MemoryBytes: 1 << 20}},
	}, nil
}

func (pageReconciler) Apply(ctx context.Context, obj *store.Object[pageSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

// publishChecks are the checks the publish action's plan carries, for the
// tests of the verify stage.
var publishChecks []reconcile.HealthCheck

func (pageReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	if req.Action == "touch" {
		return reconcile.ActionPlan{}, nil
	}
	var in publishInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return reconcile.ActionPlan{}, err
	}
	return reconcile.ActionPlan{Plan: reconcile.Plan{Steps: []agent.Step{
		agent.WriteFile.Step(agent.WriteFileInput{Path: pagesDir + "/.published", Content: []byte(in.Message), Mode: 0o644}),
	}, Checks: publishChecks}}, nil
}

// A menu lists pages; planning it reads them, so it can only be planned
// once its pages exist, at least in the same change set.
type menuSpec struct {
	Pages []store.ID `json:"pages"`
}

var menuKind = store.Define[menuSpec, pageStatus](store.KindInfo{
	Name: "test.menu", Prefix: "menu", Scope: store.ScopeAccount, Title: "Menu", Plural: "menus",
	Description: "A list of pages, for pipeline tests.",
})

type menuReconciler struct{}

func (menuReconciler) Plan(ctx context.Context, obj *store.Object[menuSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	target := pagesDir + "/menu-" + string(obj.ID)
	if obj.Deleted() {
		return reconcile.Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: target})}}, nil
	}
	var lines []string
	for _, id := range obj.Spec.Pages {
		page, err := pageKind.Get(ctx, req.Read, id)
		if err != nil {
			return reconcile.Plan{}, fmt.Errorf("menu page %s: %w", id, err)
		}
		lines = append(lines, page.Spec.Path)
	}
	return reconcile.Plan{Steps: []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: pagesDir, Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: target, Content: []byte(strings.Join(lines, "\n")), Mode: 0o644}),
	}}, nil
}

func (menuReconciler) Apply(ctx context.Context, obj *store.Object[menuSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

type fakeHooks struct {
	mu       sync.Mutex
	deny     bool
	validate []string
	modify   map[string][]int
	applied  map[string][]int
	// rewrite, when set, is what a modify hook does instead of adding an
	// effect: anything, as a misbehaving Hooks implementation might.
	rewrite func(cs *ChangeSet, changes []int)
}

func (f *fakeHooks) Validate(ctx context.Context, point string, cs *ChangeSet, change *ResourceChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validate = append(f.validate, point)
	if f.deny {
		return i18n.ErrForbidden.New("action", "publish pages on Fridays")
	}
	return nil
}

func (f *fakeHooks) Modify(ctx context.Context, point string, cs *ChangeSet, changes []int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modify == nil {
		f.modify = map[string][]int{}
	}
	f.modify[point] = changes
	if f.rewrite != nil {
		f.rewrite(cs, changes)
		return nil
	}
	cs.Effects = append(cs.Effects, system.Effect{Type: system.EffectFile, Action: "write", Target: "/etc/racksail/generated/cdn.conf", Contributor: "addon:cdn"})
	cs.Contributions = append(cs.Contributions, Contribution{Source: Source{Type: "addon", Name: "cdn", Hook: point}, Summary: "Purge the CDN", Effects: []int{len(cs.Effects) - 1}})
	return nil
}

func (f *fakeHooks) Applied(ctx context.Context, point string, cs *ChangeSet, changes []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applied == nil {
		f.applied = map[string][]int{}
	}
	f.applied[point] = changes
}

type env struct {
	engine  *Engine
	store   store.Store
	sim     *sim.Machine
	bus     *events.Log
	jobs    *jobs.Manager
	hooks   *fakeHooks
	secrets *secrets.Memory
	owner   *auth.Actor
	acct    store.ID
}

func newEnv(t *testing.T, retention Retention, now func() time.Time) *env {
	return buildEnv(t, envConfig{retention: retention, now: now})
}

// envConfig says how to build a test environment. With db set, everything
// the panel keeps is kept in that database, as in the panel.
type envConfig struct {
	retention Retention
	now       func() time.Time
	db        *store.DB
	machine   *sim.Machine
	vault     *secrets.Memory
	ctx       context.Context
	// audit, when set, wraps the audit log the engine writes to.
	audit func(events.AuditLog) events.AuditLog
}

func buildEnv(t *testing.T, cfg envConfig) *env {
	t.Helper()
	ctx := cfg.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{pageKind, menuKind, ticketKind, labelKind, gateKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	var st store.Store = store.NewMemory(kinds)
	bus := events.NewMemory(100)
	q := jobs.NewMemory(ctx, jobs.MemoryOptions{Workers: 2})
	var storage Storage
	if cfg.db != nil {
		var err error
		if st, err = store.NewSQLite(ctx, cfg.db, kinds); err != nil {
			t.Fatal(err)
		}
		if bus, err = events.NewSQL(ctx, cfg.db, events.Options{}); err != nil {
			t.Fatal(err)
		}
		if q, err = jobs.NewSQL(ctx, cfg.db, jobs.ManagerOptions{Workers: 2}); err != nil {
			t.Fatal(err)
		}
		if storage, err = NewSQLStorage(ctx, cfg.db); err != nil {
			t.Fatal(err)
		}
	}
	m := cfg.machine
	if m == nil {
		m = sim.New(sim.Options{})
	}
	recs := reconcile.NewRegistry()
	for _, r := range []reconcile.Reconciler{reconcile.Typed(pageKind, pageReconciler{}), reconcile.Typed(menuKind, menuReconciler{}), reconcile.Typed(ticketKind, ticketReconciler{}), reconcile.Typed(gateKind, gateReconciler{})} {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	vault := cfg.vault
	if vault == nil {
		key, _ := secrets.NewKey()
		box, _ := secrets.NewBox(key)
		vault = secrets.NewMemory(box)
	}
	hooks := &fakeHooks{}
	var audit events.AuditLog = bus
	if cfg.audit != nil {
		audit = cfg.audit(bus)
	}
	engine, err := NewEngine(Options{
		Store: st, Kinds: kinds, Jobs: q, Events: bus, Audit: audit, Hooks: hooks, Secrets: vault, Storage: storage,
		Runner:     &reconcile.Runner{Store: st, Agent: agent.NewLocal(m.Backend(), nil), Registry: recs, Secrets: vault},
		Authorizer: auth.NewRoleAuthorizer(nil), Retention: cfg.retention, Now: cfg.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	// The queue stops before the database it may be writing to closes.
	t.Cleanup(func() { q.Stop(context.Background()) })
	acct := store.NewID("acct")
	owner := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(acct)}}}
	return &env{engine: engine, store: st, sim: m, bus: bus, jobs: q, hooks: hooks, secrets: vault, owner: owner, acct: acct}
}

func (e *env) submit(t *testing.T, dryRun bool, changes ...Change) *ChangeSet {
	t.Helper()
	cs, err := e.engine.Submit(context.Background(), Request{Actor: e.owner, Changes: changes, DryRun: dryRun})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun {
		return cs
	}
	return e.wait(t, cs)
}

func (e *env) createPage(spec pageSpec) Change {
	raw, _ := json.Marshal(spec)
	return Change{Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: raw}
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

func (e *env) file(path string) string {
	data, err := e.sim.Backend().Files.ReadFile(context.Background(), path, 0)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestDryRunPreview(t *testing.T) {
	e := newEnv(t, Retention{}, nil)
	ch := e.createPage(pageSpec{Path: "about.html", Content: "About us"})
	ch.Secrets = map[string]string{"secret": "hunter2"}
	cs := e.submit(t, true, ch)
	if !cs.DryRun || cs.State != StatePlanned || cs.Job != "" || cs.Title != "Create page" {
		t.Errorf("dry run = %+v", cs)
	}
	if len(cs.Changes) != 1 || cs.Changes[0].Op != OpCreate || cs.Changes[0].ID.Prefix() != "page" {
		t.Fatalf("changes = %+v", cs.Changes)
	}
	raw, _ := json.Marshal(cs)
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("the secret leaked into the change set: %s", raw)
	}
	var secretDiff bool
	for _, d := range cs.Changes[0].Diff {
		if d.Path == "secret" {
			secretDiff = d.Sensitive && d.After == nil
		}
	}
	if !secretDiff {
		t.Errorf("the secret field shows as a redacted change: %+v", cs.Changes[0].Diff)
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
	// The directory, the page and its key, planned with the secret's value
	// before it is stored anywhere.
	if files < 3 || contributed != 1 || len(cs.Contributions) != 1 || cs.Estimate.MemoryBytes != 1<<20 {
		t.Errorf("effects = %+v, contributions = %+v, estimate = %+v", cs.Effects, cs.Contributions, cs.Estimate)
	}
	ctx := context.Background()
	if page, _ := e.store.List(ctx, store.Query{Kind: "test.page"}); len(page.Items) != 0 {
		t.Error("a dry run must not store anything")
	}
	if refs, _ := e.secrets.List(ctx, cs.Changes[0].ID); len(refs) != 0 {
		t.Errorf("a dry run must not store secrets: %v", refs)
	}
	for _, c := range e.sim.Calls() {
		if c.Op == "write" || c.Op == "mkdir" {
			t.Errorf("a dry run changed the machine: %v", c)
		}
	}
	if kept, err := e.engine.Get(ctx, cs.ID); err != nil || !kept.DryRun {
		t.Errorf("previews can be fetched again: %v", err)
	}
}

func TestApplyUpdateUndo(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	sub := e.bus.Subscribe(events.Filter{Types: []string{"test.page.*"}}, 16)
	defer sub.Close()

	cs := e.submit(t, false, e.createPage(pageSpec{Path: "index.html", Content: "v1"}))
	if cs.State != StateApplied {
		t.Fatalf("change set = %+v", cs)
	}
	id := cs.Changes[0].ID
	if got := e.file(pagesDir + "/index.html"); got != "v1" {
		t.Errorf("page = %q", got)
	}
	if ev := <-sub.C; ev.Type != "test.page.created" || ev.Subject != id || ev.ChangeSet != cs.ID {
		t.Errorf("event = %+v", ev)
	}
	got, _ := pageKind.Get(ctx, e.store, id)
	if got.Status.Phase != store.PhaseReady {
		t.Errorf("status = %+v", got.Status)
	}

	raw, _ := json.Marshal(pageSpec{Path: "index.html", Content: "v2"})
	update := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw, Revision: got.Revision, Labels: map[string]string{"env": "prod"}})
	if update.State != StateApplied {
		t.Fatalf("update = %+v", update)
	}
	if len(update.Changes[0].Diff) != 1 || update.Changes[0].Diff[0].Path != "content" {
		t.Errorf("update diff = %+v", update.Changes[0].Diff)
	}
	if got := e.file(pagesDir + "/index.html"); got != "v2" {
		t.Errorf("page after update = %q", got)
	}

	stale, _ := json.Marshal(pageSpec{Path: "index.html", Content: "v3"})
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpUpdate, Kind: "test.page", ID: id, Spec: stale, Revision: got.Revision}}}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a stale revision should conflict: %v", err)
	}

	preview, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: update.ID, DryRun: true})
	if err != nil || !preview.DryRun || preview.UndoOf != update.ID || len(preview.Changes) != 1 {
		t.Fatalf("undo preview = %+v, %v", preview, err)
	}
	if orig, _ := e.engine.Get(ctx, update.ID); orig.State != StateApplied {
		t.Error("previewing an undo changes nothing")
	}
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: update.ID})
	if err != nil {
		t.Fatal(err)
	}
	if undo = e.wait(t, undo); undo.State != StateApplied || undo.UndoOf != update.ID || undo.Title != "Undo: Update page" {
		t.Fatalf("undo = %+v", undo)
	}
	if got := e.file(pagesDir + "/index.html"); got != "v1" {
		t.Errorf("page after undo = %q", got)
	}
	if reverted, _ := e.store.Get(ctx, id); len(reverted.Labels) != 0 {
		t.Errorf("undo puts the labels back too: %v", reverted.Labels)
	}
	if orig, _ := e.engine.Get(ctx, update.ID); orig.State != StateUndone {
		t.Errorf("the undone change set should say so: %s", orig.State)
	}
	if _, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: update.ID}); !errors.Is(err, ErrNotApplied) {
		t.Errorf("undoing twice: %v", err)
	}

	del := e.submit(t, false, Change{Op: OpDelete, Kind: "test.page", ID: id})
	if _, err := e.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the page should be purged after its files are gone: %v", err)
	}
	if _, err := e.sim.Backend().Files.Stat(ctx, pagesDir+"/index.html"); !errors.Is(err, system.ErrNotFound) {
		t.Error("the page file should be removed")
	}
	records, _, _ := e.bus.QueryAudit(ctx, events.Query{Subject: id})
	if len(records) < 3 {
		t.Errorf("audit records = %d", len(records))
	}
	back, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: del.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, back)
	if got := e.file(pagesDir + "/index.html"); got != "v1" {
		t.Errorf("undoing the delete brings the page back: %q", got)
	}
}

func TestActionsAndValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	cs := e.submit(t, false, e.createPage(pageSpec{Path: "news.html", Content: "news"}))
	id := cs.Changes[0].ID
	action := func(name, input string) (*ChangeSet, error) {
		return e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpAction, Kind: "test.page", ID: id, Action: name, Input: json.RawMessage(input)}}})
	}
	if _, err := action("publish", `{}`); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("missing action input: %v", err)
	}
	if _, err := action("explode", ``); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("unknown action: %v", err)
	}
	pub, err := action("publish", `{"message":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Title != "Publish page" {
		t.Errorf("title = %q", pub.Title)
	}
	e.wait(t, pub)
	if got := e.file(pagesDir + "/.published"); got != "hello" {
		t.Errorf("the action's step didn't run: %q", got)
	}
	published, _, _ := e.bus.QueryEvents(ctx, events.Query{Types: []string{"test.page.published"}})
	if len(published) != 1 {
		t.Errorf("published events = %d", len(published))
	}
	if _, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: pub.ID}); !errors.Is(err, ErrNothingToUndo) {
		t.Errorf("an action that changed no settings can't be undone: %v", err)
	}

	// Changes that don't go through are in the audit log too: refused ones
	// as denied, allowed ones that couldn't be made as failed.
	outcome := func(actor *auth.Actor, action string) events.Outcome {
		t.Helper()
		records, _, _ := e.bus.QueryAudit(ctx, events.Query{Actor: actor.ID, Types: []string{action}, Limit: 1})
		if len(records) == 0 {
			return ""
		}
		return records[0].Outcome
	}
	invalid := Change{Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: json.RawMessage(`{"content":"x"}`)}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{invalid}, DryRun: true}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("kind validation of a preview: %v", err)
	}
	if got := outcome(e.owner, "test.page.create"); got == events.OutcomeFailed {
		t.Error("a preview that fails isn't recorded")
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{invalid}}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("kind validation: %v", err)
	}
	if got := outcome(e.owner, "test.page.create"); got != events.OutcomeFailed {
		t.Errorf("a change with settings that aren't valid is recorded as failed, not %q", got)
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner}); !errors.Is(err, ErrEmpty) {
		t.Errorf("a request without changes: %v", err)
	}
	// Someone who can't see the page is told it doesn't exist, as reads are;
	// someone who can see it but not delete it is told they may not.
	stranger := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(store.NewID("acct"))}}}
	if _, err := e.engine.Submit(ctx, Request{Actor: stranger, Changes: []Change{{Op: OpDelete, Kind: "test.page", ID: id}}}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another account's owner: %v", err)
	}
	if got := outcome(stranger, "test.page.delete"); got != events.OutcomeDenied {
		t.Errorf("the stranger's attempt is recorded as denied, not %q", got)
	}
	reader := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleReadOnly, Scope: store.AccountScope(e.acct)}}}
	if _, err := e.engine.Submit(ctx, Request{Actor: reader, Changes: []Change{{Op: OpDelete, Kind: "test.page", ID: id}}}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("a reader of the account: %v", err)
	}
	e.hooks.deny = true
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpDelete, Kind: "test.page", ID: id}}, DryRun: true}); !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("a validate hook should deny: %v", err)
	}
	if got := outcome(e.owner, "test.page.delete"); got != events.OutcomeDenied {
		t.Errorf("a change a validate hook refused is recorded as denied, even in a preview, not %q", got)
	}
	if e.hooks.validate[len(e.hooks.validate)-1] != "test.page.delete.validate" {
		t.Errorf("hook points = %v", e.hooks.validate)
	}
}

func TestIdempotency(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	req := Request{Actor: e.owner, Changes: []Change{e.createPage(pageSpec{Path: "a.html", Content: "a"})}, IdempotencyKey: "retry-1"}
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
	other := *e.owner
	other.ID = store.NewID("user")
	if third, err := e.engine.Submit(ctx, Request{Actor: &other, Changes: []Change{e.createPage(pageSpec{Path: "b.html", Content: "b"})}, IdempotencyKey: "retry-1"}); err != nil || third.ID == first.ID {
		t.Errorf("keys belong to their actor: %v, %v", third, err)
	}
}

func TestSeveralChangesAreOneChangeSet(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	pageID := store.NewID("page")
	page := e.createPage(pageSpec{Path: "home.html", Content: "home"})
	page.ID = pageID
	raw, _ := json.Marshal(menuSpec{Pages: []store.ID{pageID}})
	menu := Change{Op: OpCreate, Kind: "test.menu", Scope: store.AccountScope(e.acct), Spec: raw}

	// The menu's reconciler reads the page, which only exists in the plan.
	preview := e.submit(t, true, page, menu)
	if len(preview.Changes) != 2 || preview.Title != "Create page and create menu" {
		t.Fatalf("preview = %+v", preview)
	}
	var menuFile bool
	for _, eff := range preview.Effects {
		menuFile = menuFile || strings.HasPrefix(eff.Target, pagesDir+"/menu-")
	}
	if !menuFile {
		t.Errorf("the menu was planned against the planned page: %+v", preview.Effects)
	}
	if got := e.hooks.modify; len(got["test.page.create.plan"]) != 1 || got["test.menu.create.plan"][0] != 1 {
		t.Errorf("modify hooks run once per point with their changes: %v", got)
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{menu}, DryRun: true}); err == nil {
		t.Error("without the page in the same request, the menu can't be planned")
	}

	cs := e.submit(t, false, page, menu)
	if cs.State != StateApplied || cs.Job == "" {
		t.Fatalf("change set = %+v", cs)
	}
	menuID := cs.Changes[1].ID
	for _, id := range []store.ID{pageID, menuID} {
		h, err := e.store.History(ctx, id)
		if err != nil || len(h) != 1 || h[0].ChangeSet != cs.ID {
			t.Errorf("%s was written by the change set: %+v, %v", id, h, err)
		}
	}
	if got := e.file(pagesDir + "/menu-" + string(menuID)); got != "home.html" {
		t.Errorf("menu = %q", got)
	}
	if got := e.hooks.applied; len(got["test.page.create.applied"]) != 1 || len(got["test.menu.create.applied"]) != 1 {
		t.Errorf("applied hooks run once per point: %v", got)
	}

	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: cs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(undo.Changes) != 2 || undo.Changes[0].ID != menuID || undo.Changes[1].Op != OpDelete {
		t.Errorf("undo deletes the menu, then the page: %+v", undo.Changes)
	}
	e.wait(t, undo)
	for _, id := range []store.ID{pageID, menuID} {
		if _, err := e.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s should be gone after one undo: %v", id, err)
		}
	}
}

func TestChangeSetsAreAtomic(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	first := e.createPage(pageSpec{Path: "same.html", Content: "one"})
	second := e.createPage(pageSpec{Path: "same.html", Content: "two"})
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{first, second}, DryRun: true}); !errors.Is(err, store.ErrExists) {
		t.Errorf("a preview reports what applying would run into: %v", err)
	}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{first, second}}); !errors.Is(err, store.ErrExists) {
		t.Errorf("applying: %v", err)
	}
	if page, _ := e.store.List(ctx, store.Query{Kind: "test.page"}); len(page.Items) != 0 {
		t.Error("nothing is written when one change fails")
	}
	bad := e.createPage(pageSpec{Content: "no path"})
	_, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{first, bad}})
	var coded *i18n.Error
	if !errors.As(err, &coded) || len(coded.Fields) != 1 || coded.Fields[0].Path != "changes[1].spec.path" {
		t.Errorf("errors point at the change they belong to: %v", err)
	}
}

func TestPlacementIsKept(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	server := store.NewID("srv")
	ch := e.createPage(pageSpec{Path: "far.html", Content: "far away"})
	ch.Server = server
	cs := e.submit(t, false, ch)
	id := cs.Changes[0].ID
	if cs.Changes[0].Server != server {
		t.Errorf("the change records the placement: %+v", cs.Changes[0])
	}
	if r, _ := e.store.Get(ctx, id); r.Server != server {
		t.Errorf("stored server = %q", r.Server)
	}
	del := e.submit(t, false, Change{Op: OpDelete, Kind: "test.page", ID: id})
	back, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: del.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, back)
	if r, err := e.store.Get(ctx, id); err != nil || r.Server != server {
		t.Errorf("undoing a delete puts the resource back on its server: %+v, %v", r, err)
	}
}

func TestSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	plain, _ := json.Marshal(pageSpec{Path: "p.html", Content: "x", Secret: "hunter2"})
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: plain}}}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("a secret value in a spec is refused: %v", err)
	}
	foreign, _ := json.Marshal(pageSpec{Path: "p.html", Content: "x", Secret: string(secrets.NewRef(store.NewID("page"), "key"))})
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), Spec: foreign}}}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("another resource's secret is refused: %v", err)
	}
	notSecret := e.createPage(pageSpec{Path: "p.html", Content: "x"})
	notSecret.Secrets = map[string]string{"content": "x"}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{notSecret}}); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("secrets only go to secret fields: %v", err)
	}

	ch := e.createPage(pageSpec{Path: "vault.html", Content: "locked"})
	ch.Secrets = map[string]string{"secret": "first key"}
	cs := e.submit(t, false, ch)
	id := cs.Changes[0].ID
	if got := e.file(pagesDir + "/vault.html.key"); got != "first key" {
		t.Errorf("the reconciler resolves the reference: %q", got)
	}
	created, _ := pageKind.Get(ctx, e.store, id)
	firstRef := secrets.Ref(created.Spec.Secret)
	if owner, _, err := firstRef.Parse(); err != nil || owner != id {
		t.Errorf("the spec holds a reference: %q", firstRef)
	}

	keep, _ := json.Marshal(created.Spec)
	update := e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: keep, Secrets: map[string]string{"secret": "second key"}})
	if got := e.file(pagesDir + "/vault.html.key"); got != "second key" {
		t.Errorf("after the update: %q", got)
	}
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: update.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, undo)
	if got := e.file(pagesDir + "/vault.html.key"); got != "first key" {
		t.Errorf("undo brings the previous secret back: %q", got)
	}
	if v, err := e.secrets.Get(ctx, firstRef); err != nil || string(v) != "first key" {
		t.Errorf("the first value = %q, %v", v, err)
	}
}

func TestRetention(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(d)
	}
	e := newEnv(t, Retention{Previews: 10 * time.Minute, MaxPreviews: 2, Finished: time.Hour, MaxFinished: 2}, now)

	var previews []*ChangeSet
	for i := range 3 {
		previews = append(previews, e.submit(t, true, e.createPage(pageSpec{Path: fmt.Sprintf("p%d.html", i), Content: "x"})))
	}
	if _, err := e.engine.Get(ctx, previews[0].ID); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("the oldest preview over the cap is dropped: %v", err)
	}
	advance(11 * time.Minute)
	e.submit(t, true, e.createPage(pageSpec{Path: "late.html", Content: "x"}))
	if _, err := e.engine.Get(ctx, previews[2].ID); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("old previews are dropped: %v", err)
	}

	ch := e.createPage(pageSpec{Path: "vault.html", Content: "x"})
	ch.Secrets = map[string]string{"secret": "one"}
	created := e.submit(t, false, ch)
	id := created.Changes[0].ID
	spec, _ := pageKind.Get(ctx, e.store, id)
	keep, _ := json.Marshal(spec.Spec)
	update := func(key string) *ChangeSet {
		return e.submit(t, false, Change{Op: OpUpdate, Kind: "test.page", ID: id, Spec: keep, Secrets: map[string]string{"secret": key}})
	}
	second := update("two")
	update("three")
	update("four")
	kept := func() string {
		refs, _ := e.secrets.List(ctx, id)
		var values []string
		for _, ref := range refs {
			v, _ := e.secrets.Get(ctx, ref)
			values = append(values, string(v))
		}
		slices.Sort(values)
		return strings.Join(values, ",")
	}

	// Keeping the last update dropped the create; keeping anything now drops
	// the first update, and with it the first value, which nothing kept uses.
	e.submit(t, true, e.createPage(pageSpec{Path: "trigger.html", Content: "x"}))
	if _, err := e.engine.Get(ctx, created.ID); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("finished change sets over the cap are dropped: %v", err)
	}
	if _, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: second.ID}); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("dropped change sets can't be undone: %v", err)
	}
	if got := kept(); got != "four,three,two" {
		t.Errorf("secret versions kept = %s", got)
	}

	// Once every change set that used them is gone, only the current value
	// is left.
	advance(2 * time.Hour)
	e.submit(t, true, e.createPage(pageSpec{Path: "later.html", Content: "x"}))
	if got := kept(); got != "four" {
		t.Errorf("secret versions kept = %s", got)
	}
}

func TestHooksCantChangeWhatWasAsked(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	page := e.submit(t, false, e.createPage(pageSpec{Path: "keep.html", Content: "keep"}))
	id := page.Changes[0].ID
	raw, _ := json.Marshal(pageSpec{Path: "keep.html", Content: "new"})
	for what, rewrite := range map[string]func(cs *ChangeSet, changes []int){
		"operation": func(cs *ChangeSet, _ []int) { cs.Changes[0].Op = OpDelete },
		"resource":  func(cs *ChangeSet, _ []int) { cs.Changes[0].ID = store.NewID("page") },
		"scope":     func(cs *ChangeSet, _ []int) { cs.Changes[0].Scope = store.AccountScope(store.NewID("acct")) },
		"revision":  func(cs *ChangeSet, _ []int) { cs.Changes[0].Revision = 99 },
		"changes":   func(cs *ChangeSet, _ []int) { cs.Changes = cs.Changes[:0] },
		"effects":   func(cs *ChangeSet, _ []int) { cs.Effects = cs.Effects[:0] },
	} {
		e.hooks.rewrite = rewrite
		_, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpUpdate, Kind: "test.page", ID: id, Spec: raw}}})
		if !errors.Is(err, ErrHookTampered) {
			t.Errorf("a hook changing the %s: %v", what, err)
		}
	}
	if got, err := e.store.Get(ctx, id); err != nil || got.Deleted() || strings.Contains(string(got.Spec), "new") {
		t.Errorf("nothing was written: %+v, %v", got, err)
	}
}

func TestHookContributionsAreChecked(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	theirs := e.createPage(pageSpec{Path: "theirs.html", Content: "theirs"})
	theirs.Scope = store.AccountScope(store.NewID("acct"))
	admin := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}
	other, err := e.engine.Submit(ctx, Request{Actor: admin, Changes: []Change{theirs}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, other)
	theirID := other.Changes[0].ID
	mirror := Source{Type: "addon", Name: "mirror", Hook: "test.page.create.plan"}
	add := func(c ResourceChange) func(cs *ChangeSet, _ []int) {
		return func(cs *ChangeSet, _ []int) {
			c.Source = mirror
			cs.Changes = append(cs.Changes, c)
		}
	}
	request := func() Request {
		return Request{Actor: e.owner, Changes: []Change{e.createPage(pageSpec{Path: "home.html", Content: "home"})}}
	}

	e.hooks.rewrite = add(ResourceChange{Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), After: json.RawMessage(`{"path":"home.html.bak","content":"home"}`)})
	req := request()
	req.DryRun = true
	preview, err := e.engine.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Changes) != 2 || preview.Changes[1].Source != mirror || preview.Changes[1].ID.Prefix() != "page" || len(preview.Changes[1].Diff) == 0 {
		t.Fatalf("the added change is in the preview, labelled and diffed: %+v", preview.Changes)
	}
	var planned bool
	for _, eff := range preview.Effects {
		planned = planned || (eff.Contributor == "addon:mirror" && eff.Target == pagesDir+"/home.html.bak")
	}
	if !planned {
		t.Errorf("what the added change does to the machine is in the preview: %+v", preview.Effects)
	}
	applied := e.wait(t, mustSubmit(t, e.engine, request()))
	if applied.State != StateApplied || e.file(pagesDir+"/home.html.bak") != "home" {
		t.Errorf("the added change is applied with the rest: %+v", applied)
	}

	e.hooks.rewrite = add(ResourceChange{Op: OpDelete, Kind: "test.page", ID: theirID})
	if _, err := e.engine.Submit(ctx, request()); !errors.Is(err, ErrContributionDenied) || !errors.Is(err, i18n.ErrForbidden) {
		t.Errorf("a hook can't make a change the actor may not: %v", err)
	}
	records, _, _ := e.bus.QueryAudit(ctx, events.Query{Subject: theirID})
	if len(records) == 0 || records[0].Outcome != events.OutcomeDenied {
		t.Errorf("the attempt is audited: %+v", records)
	}
	if got, err := e.store.Get(ctx, theirID); err != nil || got.Deleted() {
		t.Errorf("the other account's page is still there: %v", err)
	}

	e.hooks.rewrite = add(ResourceChange{Op: OpAction, Action: "publish", Kind: "test.page", ID: theirID, Input: json.RawMessage(`{"message":"x"}`)})
	if _, err := e.engine.Submit(ctx, request()); !errors.Is(err, ErrBadContribution) {
		t.Errorf("hooks can't add actions: %v", err)
	}
	e.hooks.rewrite = add(ResourceChange{Op: OpCreate, Kind: "test.page", Scope: store.AccountScope(e.acct), After: json.RawMessage(`{"content":"no path"}`)})
	if _, err := e.engine.Submit(ctx, request()); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("added changes are validated: %v", err)
	}

	// A rewritten spec goes through validation and the validate hooks again.
	e.hooks.rewrite = func(cs *ChangeSet, changes []int) {
		cs.Changes[changes[0]].After = json.RawMessage(`{"content":"lost its path"}`)
	}
	if _, err := e.engine.Submit(ctx, request()); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("rewritten specs are validated: %v", err)
	}
	e.hooks.validate = nil
	e.hooks.rewrite = func(cs *ChangeSet, changes []int) {
		cs.Changes[changes[0]].After = json.RawMessage(`{"path":"draft.html","content":"rewritten"}`)
	}
	rewritten, err := e.engine.Submit(ctx, Request{Actor: e.owner, DryRun: true, Changes: []Change{e.createPage(pageSpec{Path: "draft.html", Content: "draft"})}})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.hooks.validate) != 2 || !strings.Contains(string(rewritten.Changes[0].After), "rewritten") || rewritten.Changes[0].Diff[0].After == nil {
		t.Errorf("validate hooks ran %d times; change = %+v", len(e.hooks.validate), rewritten.Changes[0])
	}
}

func mustSubmit(t *testing.T, engine *Engine, req Request) *ChangeSet {
	t.Helper()
	cs, err := engine.Submit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// failingSnapshots makes the agent's SnapshotPaths fail when it is really
// asked to take snapshots, as a full disk would.
type failingSnapshots struct{ agent.Client }

func (f failingSnapshots) Call(ctx context.Context, op string, input json.RawMessage, opts agent.CallOptions) (json.RawMessage, error) {
	if op == agent.SnapshotPaths.Name() && !opts.Check {
		return nil, agent.ErrFailed.New("op", op, "reason", "no space left on device")
	}
	return f.Client.Call(ctx, op, input, opts)
}

func TestSnapshotsBeforeRemovingData(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{MaxFinished: 2}, nil)
	page := e.submit(t, false, e.createPage(pageSpec{Path: "old.html", Content: "old news"}))
	id := page.Changes[0].ID
	if page.Snapshot || len(page.Snapshots) != 0 {
		t.Errorf("creating a page removes nothing: %+v", page)
	}

	preview := e.submit(t, true, Change{Op: OpDelete, Kind: "test.page", ID: id})
	var listed bool
	for _, eff := range preview.Effects {
		listed = listed || (eff.Action == "snapshot" && eff.Detail == "/srv/racksail")
	}
	if !preview.Snapshot || !listed || len(preview.Snapshots) != 0 {
		t.Errorf("the preview says a snapshot of /srv/racksail comes first: %+v", preview.Effects)
	}

	runner := e.engine.opts.Runner
	local := runner.Agent
	runner.Agent = failingSnapshots{local}
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpDelete, Kind: "test.page", ID: id}}}); !errors.Is(err, ErrSnapshotFailed) {
		t.Errorf("without its snapshot, a risky change doesn't go ahead: %v", err)
	}
	if got, err := e.store.Get(ctx, id); err != nil || got.Deleted() {
		t.Errorf("nothing was written: %v", err)
	}
	runner.Agent = local

	del := e.submit(t, false, Change{Op: OpDelete, Kind: "test.page", ID: id})
	if del.State != StateApplied || len(del.Snapshots) != 1 || del.Snapshots[0].Source != "/srv/racksail" {
		t.Fatalf("delete = %+v", del)
	}
	snap := del.Snapshots[0].Path
	if got := e.file(snap + "/pages/old.html"); got != "old news" {
		t.Errorf("the snapshot keeps what the delete removed: %q", got)
	}
	if e.file(pagesDir+"/old.html") != "" {
		t.Error("the page is gone from the site")
	}

	// Once the change set is no longer kept, neither is its snapshot.
	for i := range 3 {
		e.submit(t, false, e.createPage(pageSpec{Path: fmt.Sprintf("p%d.html", i), Content: "x"}))
	}
	if _, err := e.engine.Get(ctx, del.ID); !errors.Is(err, i18n.ErrNotFound) {
		t.Fatalf("the delete should be dropped by now: %v", err)
	}
	if _, err := e.sim.Backend().Files.Stat(ctx, snap); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("its snapshot is deleted: %v", err)
	}
	if _, err := e.sim.Backend().Files.Stat(ctx, path.Dir(snap)); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("and so is its directory: %v", err)
	}
}

func TestActionChecks(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	id := e.submit(t, false, e.createPage(pageSpec{Path: "news.html", Content: "news"})).Changes[0].ID
	defer func() { publishChecks = nil }()
	publish := func() *ChangeSet {
		cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpAction, Kind: "test.page", ID: id, Action: "publish", Input: json.RawMessage(`{"message":"hello"}`)}}})
		if err != nil {
			t.Fatal(err)
		}
		return e.wait(t, cs)
	}
	publishChecks = []reconcile.HealthCheck{{Name: "ssh", Type: reconcile.CheckUnit, Target: "ssh.service"}}
	if cs := publish(); cs.State != StateApplied || len(cs.Changes[0].Checks) != 1 {
		t.Errorf("a passing check = %+v", cs)
	}
	publishChecks = []reconcile.HealthCheck{{Name: "stopped ssh", Type: reconcile.CheckUnit, Target: "ssh.service", Expect: "inactive", Timeout: 50 * time.Millisecond}}
	failed := publish()
	if failed.State != StateFailed || !strings.Contains(failed.Error, "stopped ssh") {
		t.Errorf("a failing check fails the change, which can then be undone: %+v", failed)
	}
}
