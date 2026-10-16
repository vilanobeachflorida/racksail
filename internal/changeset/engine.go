package changeset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
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
)

// Error codes.
var (
	ErrConflicts         = i18n.Define("changeset.conflicts", http.StatusConflict, "The change has {count} conflicting contributions from hooks. Resolve them before applying it.")
	ErrNotApplied        = i18n.Define("changeset.not_applied", http.StatusConflict, "Only applied changes can be undone.")
	ErrUnknownAction     = i18n.Define("changeset.unknown_action", http.StatusNotFound, "{kind} has no action called {action}.")
	ErrStillRemoving     = i18n.Define("changeset.still_removing", http.StatusConflict, "{what} is still being removed. Try again when the removal has finished.")
	ErrEmpty             = i18n.Define("changeset.empty", http.StatusUnprocessableEntity, "The request doesn't ask for any changes.")
	ErrNothingToUndo     = i18n.Define("changeset.nothing_to_undo", http.StatusConflict, "{title} can't be undone because it didn't change any settings.")
	ErrUndoing           = i18n.Define("changeset.undoing", http.StatusConflict, "{title} is already being undone.")
	ErrDuplicateRequest  = i18n.Define("changeset.duplicate_request", http.StatusConflict, "A request with the same Idempotency-Key is still being processed. Try again in a moment.")
	ErrNotPreview        = i18n.Define("changeset.not_preview", http.StatusConflict, "{title} isn't a preview, so it can't be applied again.")
	ErrPreviewHasSecrets = i18n.Define("changeset.preview_has_secrets", http.StatusConflict, "{title} carried secret values, which previews don't keep. Send the change again to apply it.")
	ErrPreviewStale      = i18n.Define("changeset.preview_stale", http.StatusConflict, "{title} would now do something other than its preview showed. Look at the new preview, {preview}, and apply that instead.")
	ErrApplying          = i18n.Define("changeset.applying", http.StatusConflict, "{title} is already being applied.")
	ErrNotRetryable      = i18n.Define("changeset.not_retryable", http.StatusConflict, "Only a change that failed can be carried out again, and {title} is {state}.")
	ErrActionNotRepeated = i18n.Define("changeset.action_not_repeated", http.StatusConflict, "{title} stopped while running {action}, which a retry doesn't run a second time. Run {action} again instead.")
)

// ApplyJob is the job type that carries out applied change sets.
const ApplyJob = "changeset.apply"

// Options are the dependencies of an Engine. Hooks, Authorizer, Runner,
// Events, Audit and Secrets may be nil; the corresponding stages are then
// skipped, and changes carrying secret values are refused.
type Options struct {
	Store      store.Store
	Kinds      *store.KindRegistry
	Runner     *reconcile.Runner
	Jobs       jobs.Queue
	Events     events.Bus
	Audit      events.AuditLog
	Hooks      Hooks
	Authorizer auth.Authorizer
	// Secrets keeps the values of sensitive fields.
	Secrets secrets.Store
	// Storage keeps change sets; nil keeps them in memory.
	Storage Storage
	// DB is the panel database, when the store, the change set storage, the
	// job queue and the audit log all keep their records in it. Applying a
	// change set then writes the settings, the change set, its job and its
	// audit records in one transaction, so they are all there or none is.
	// NewEngine takes it from the store when the store and the change set
	// storage live in the same database.
	DB        *store.DB
	Retention Retention
	Log       *slog.Logger
	Now       func() time.Time
}

// Engine is the default Pipeline.
type Engine struct {
	opts      Options
	storage   Storage
	retention Retention

	mu sync.Mutex
	// claims are the change sets an undo or the apply of a preview is
	// working on, which no other may start on and retention keeps.
	claims map[store.ID]bool
}

var _ Pipeline = (*Engine)(nil)

// NewEngine returns an engine and registers its apply job on the queue.
//
// An apply job that the panel stopped under isn't run again when the panel
// starts: its change set is marked failed, the resync carries out the
// settings it stored, and a retry of the job finishes it. A retry carries on
// from the change that didn't finish, and never runs what an action does
// once a second time, since the retry doesn't go through authorization and
// the validate hooks again.
func NewEngine(opts Options) (*Engine, error) {
	if opts.Store == nil || opts.Kinds == nil || opts.Jobs == nil {
		return nil, errors.New("changeset: an engine needs a store, kinds and a job queue")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Storage == nil {
		opts.Storage = NewMemoryStorage()
	}
	if opts.DB == nil {
		if st, ok := opts.Store.(*store.SQLite); ok {
			if kept, ok := opts.Storage.(*SQLStorage); ok && kept.db == st.DB() {
				opts.DB = st.DB()
			}
		}
	}
	e := &Engine{opts: opts, storage: opts.Storage, retention: opts.Retention.withDefaults(), claims: map[store.ID]bool{}}
	if err := opts.Jobs.Handle(jobs.Type{Name: ApplyJob, Run: e.runApply, Ended: e.applyEnded, Retryable: e.retryable}); err != nil {
		return nil, err
	}
	return e, nil
}

// Submit implements Pipeline.
func (e *Engine) Submit(ctx context.Context, req Request) (*ChangeSet, error) {
	var key string
	if !req.DryRun {
		key = idempotencyKey("submit", req.Actor, req.IdempotencyKey)
		if earlier, err := e.reserve(ctx, key); earlier != nil || err != nil {
			return earlier, err
		}
	}
	p, err := e.plan(ctx, req, "", "")
	if err != nil {
		e.settle(ctx, key, "")
		return nil, err
	}
	return e.finish(ctx, p, req, key)
}

// Get implements Pipeline.
func (e *Engine) Get(ctx context.Context, id store.ID) (*ChangeSet, error) {
	return e.storage.Get(ctx, id)
}

// List implements Pipeline.
func (e *Engine) List(ctx context.Context, q ListQuery) ([]*ChangeSet, string, error) {
	return e.storage.List(ctx, q)
}

// draft is a change set being planned: what it will change, the planned
// state of every resource it touches, and the secret values it carries.
type draft struct {
	cs      *ChangeSet
	view    *store.Overlay
	secrets map[secrets.Ref][]byte
	titles  []string
	// undoOf is the change set the draft undoes, whose deleted resources it
	// may bring back under their IDs.
	undoOf store.ID
	dryRun bool
	// recorded says the audit log already has why the request didn't go
	// through.
	recorded bool
}

func (p *draft) reader(base secrets.Store) secrets.Reader {
	if base == nil {
		return pending{values: p.secrets}
	}
	return pending{values: p.secrets, base: base}
}

// plan runs the validate and plan stages for every change of a request. id
// is the change set's ID, or empty for a new one; undoOf is the change set
// the request undoes, if it is an undo.
func (e *Engine) plan(ctx context.Context, req Request, id, undoOf store.ID) (*draft, error) {
	if len(req.Changes) == 0 {
		return nil, ErrEmpty.New()
	}
	if id == "" {
		id = store.NewID(store.PrefixChangeSet)
	}
	now := e.opts.Now().UTC()
	p := &draft{
		cs: &ChangeSet{
			ID: id, Actor: req.Actor, Risk: store.RiskRead,
			State: StatePlanned, CreatedAt: now, Effects: []system.Effect{},
		},
		view:    store.NewOverlay(e.opts.Store, e.opts.Kinds),
		secrets: map[secrets.Ref][]byte{},
		undoOf:  undoOf,
		dryRun:  req.DryRun,
	}
	p.cs.UndoOf = undoOf
	for i, ch := range req.Changes {
		if err := e.planChange(ctx, p, req.Actor, ch, now); err != nil {
			return nil, atChange(err, i, len(req.Changes))
		}
	}
	cs := p.cs
	if e.opts.Hooks != nil {
		requested := make([]ResourceChange, len(cs.Changes))
		for i, c := range cs.Changes {
			requested[i] = c.Clone()
		}
		effects := slices.Clone(cs.Effects)
		for _, pc := range pointsOf(requested, StagePlan) {
			if err := e.opts.Hooks.Modify(ctx, pc.point, cs, pc.changes); err != nil {
				e.failed(ctx, p, requested, err)
				return nil, err
			}
		}
		if err := e.recheck(ctx, p, req.Actor, requested, effects); err != nil {
			e.failed(ctx, p, requested, err)
			return nil, err
		}
	}
	for _, eff := range cs.Effects {
		if eff.Destructive {
			cs.Snapshot = true
		}
	}
	if cs.Snapshot {
		e.previewSnapshots(ctx, cs)
	}
	cs.Summarize()
	cs.Title = req.Title
	if cs.Title == "" {
		cs.Title = p.titles[0]
		switch n := len(p.titles); {
		case n == 2:
			cs.Title += " and " + strings.ToLower(p.titles[1][:1]) + p.titles[1][1:]
		case n > 2:
			cs.Title += fmt.Sprintf(" and %d more changes", n-1)
		}
	}
	return p, nil
}

// atChange points the field errors of a change in a request with several
// changes at that change.
func atChange(err error, i, n int) error {
	var e *i18n.Error
	if n == 1 || !errors.As(err, &e) || len(e.Fields) == 0 {
		return err
	}
	c := *e
	c.Fields = make([]i18n.FieldError, len(e.Fields))
	for j, f := range e.Fields {
		f.Path = fmt.Sprintf("changes[%d].%s", i, f.Path)
		c.Fields[j] = f
	}
	return &c
}

// planChange validates one change, adds it to the plan with its effects and
// records the resource's planned state, so later changes see it. A change
// the actor isn't allowed, or that stops after being allowed, is recorded in
// the audit log.
func (e *Engine) planChange(ctx context.Context, p *draft, actor *auth.Actor, ch Change, now time.Time) (err error) {
	// A kind's validation can ask what the actor may do, such as whether
	// they may create an account for the reseller it names.
	ctx = auth.WithAuthorizer(auth.WithActor(ctx, actor), e.opts.Authorizer)
	kind, ok := e.opts.Kinds.Get(ch.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", ch.Kind)
	}
	info := kind.Info()
	var old, next *store.Resource
	var action store.Action
	risk := info.Risks.Update
	switch ch.Op {
	case OpCreate:
		risk = info.Risks.Create
		next = &store.Resource{
			Meta: store.Meta{ID: ch.ID, Kind: info.Name, Scope: ch.Scope, Server: ch.Server, Labels: maps.Clone(ch.Labels)},
			Spec: bytes.Clone(ch.Spec),
		}
		if next.ID == "" {
			next.ID = store.NewID(info.Prefix)
		} else if err := e.checkReuse(ctx, p, next); err != nil {
			return err
		}
		if p.view.Planned(next.ID) {
			return store.ErrExists.New("what", string(next.ID))
		}
		if len(next.Spec) == 0 {
			next.Spec = json.RawMessage("{}")
		}
		if err := kind.Default(ctx, next); err != nil {
			return err
		}
	case OpUpdate, OpDelete, OpAction:
		r, err := p.view.Get(ctx, ch.ID)
		if err != nil {
			return err
		}
		if r.Kind != info.Name || r.Deleted() {
			return store.ErrNotFound.New("what", string(ch.ID))
		}
		old, next = r, r.Clone()
		switch ch.Op {
		case OpUpdate:
			if ch.Spec != nil {
				next.Spec = bytes.Clone(ch.Spec)
			}
			if ch.Labels != nil {
				next.Labels = maps.Clone(ch.Labels)
			}
		case OpDelete:
			risk = info.Risks.Delete
			next.DeletedAt = &now
		case OpAction:
			a, ok := info.Action(ch.Action)
			if !ok {
				return ErrUnknownAction.New("kind", info.Title, "action", ch.Action)
			}
			if err := store.ValidateActionInput(a, ch.Input); err != nil {
				return err
			}
			action, risk = a, a.Risk
		}
	default:
		return i18n.ErrBadRequest.New("reason", fmt.Sprintf("unknown operation %q", ch.Op))
	}
	if !risk.Valid() {
		risk = store.RiskMedium
	}
	title := changeTitle(info, ch.Op, action)

	operation := string(ch.Op)
	if ch.Op == OpAction {
		operation = ch.Action
	}
	target := auth.Target{Scope: next.Scope, Object: next.ID}
	if ch.Op == OpCreate {
		target.Object = ""
	}
	perm := auth.KindPermission(info.Name, operation)
	if e.opts.Authorizer != nil {
		if err := e.opts.Authorizer.Authorize(ctx, actor, perm, target); err != nil {
			p.recorded = true
			e.audit(ctx, actor, string(perm), next, "", events.OutcomeDenied, err, title)
			if old != nil && i18n.ErrForbidden.Is(err) && e.opts.Authorizer.Authorize(ctx, actor, auth.KindPermission(info.Name, "read"), target) != nil {
				// As with reads, a resource the actor may not see answers
				// as if it didn't exist.
				return store.ErrNotFound.New("what", string(ch.ID))
			}
			return err
		}
	}
	// From here on the change is allowed, so whatever stops it is recorded.
	defer func() {
		if err != nil {
			e.refusal(ctx, p, actor, perm, next, err, title)
		}
	}()
	if len(ch.Secrets) > 0 {
		if ch.Op != OpCreate && ch.Op != OpUpdate {
			return i18n.Invalid(i18n.Field("secrets", i18n.ErrBadValue, "reason", "only creating or updating a resource sets secrets"))
		}
		if err := e.takeSecrets(p, info, next, ch.Secrets); err != nil {
			return err
		}
	}
	if ch.Op == OpCreate || ch.Op == OpUpdate {
		if err := validate(ctx, kind, next, old); err != nil {
			return err
		}
	}

	change := ResourceChange{
		Op: ch.Op, Action: ch.Action, Kind: info.Name, ID: next.ID, Scope: next.Scope, Server: next.Server,
		Revision: ch.Revision, Labels: next.Labels, After: next.Spec, Input: ch.Input, Source: RequestSource,
	}
	if old != nil {
		change.Before = old.Spec
		if ch.Op != OpDelete {
			change.LabelsBefore = old.Labels
		}
	}
	var effects []system.Effect
	rec, hasReconciler := e.reconciler(info.Name)
	if planner, ok := rec.(reconcile.ActionPlanner); ok && hasReconciler && ch.Op == OpAction {
		actionPlan, err := planner.PlanAction(ctx, reconcile.ActionRequest{Request: e.request(ctx, p, old), Action: ch.Action, Input: ch.Input})
		if err != nil {
			return err
		}
		if actionPlan.Spec != nil {
			next.Spec = actionPlan.Spec
			change.After = actionPlan.Spec
			if err := validate(ctx, kind, next, old); err != nil {
				return err
			}
		}
		change.Steps = actionPlan.Plan.Steps
		change.Checks = actionPlan.Plan.Checks
		effects = append(effects, actionPlan.Plan.Effects...)
	}
	if ch.Op != OpDelete {
		diff, err := Diff(change.Before, change.After)
		if err != nil {
			return err
		}
		change.Diff = Redact(diff, store.SensitiveFields(info))
	}

	cs := p.cs
	cs.Changes = append(cs.Changes, change)
	if e.opts.Hooks != nil {
		if err := e.opts.Hooks.Validate(ctx, HookPoint(info.Name, operation, StageValidate), cs, &cs.Changes[len(cs.Changes)-1]); err != nil {
			return err
		}
	}
	p.view.Put(next)
	if hasReconciler && (ch.Op != OpAction || !bytes.Equal(change.Before, change.After)) {
		plan, err := rec.Plan(ctx, e.request(ctx, p, next))
		if err != nil {
			return err
		}
		effects = append(effects, plan.Effects...)
		effects = append(effects, e.stepEffects(ctx, plan.Steps, next.ID)...)
	}
	effects = append(effects, e.stepEffects(ctx, change.Steps, next.ID)...)
	cs.Effects = append(cs.Effects, effects...)
	cs.Risk = store.MaxRisk(cs.Risk, risk)
	p.titles = append(p.titles, title)
	return nil
}

// changeTitle describes a change for people, such as "Update site" or
// "Deploy site".
func changeTitle(info *store.KindInfo, op Op, action store.Action) string {
	verb := map[Op]string{OpCreate: "Create", OpUpdate: "Update", OpDelete: "Delete"}[op]
	if op == OpAction {
		verb = action.Title
		if verb == "" {
			verb = strings.ToUpper(action.Name[:1]) + strings.ReplaceAll(action.Name[1:], "_", " ")
		}
	}
	return verb + " " + strings.ToLower(info.Title)
}

// checkReuse refuses a chosen ID that a purged resource had, unless the
// draft undoes the change set that deleted it; the store refuses it too, but
// this says so before anything is planned.
func (e *Engine) checkReuse(ctx context.Context, p *draft, r *store.Resource) error {
	if r.Scope.Level == "" {
		// The scope is checked later; the kind's own fills it in.
		return nil
	}
	history, err := e.opts.Store.History(ctx, r.ID)
	switch {
	case store.ErrNotFound.Is(err):
		return nil
	case err != nil:
		return err
	}
	if last := history[len(history)-1]; !last.Deleted {
		return store.ErrExists.New("what", string(r.ID))
	}
	return store.CheckReuse(history, r, p.undoOf)
}

// validate runs a kind's validation and checks its secret fields.
func validate(ctx context.Context, kind store.Kind, next, old *store.Resource) error {
	if err := kind.Validate(ctx, next, old); err != nil {
		return err
	}
	return checkSecretRefs(kind.Info(), next)
}

// pointChanges is a hook point and the changes it is about.
type pointChanges struct {
	point   string
	changes []int
}

// pointsOf groups changes by the hook point of a stage, in the order the
// points first appear.
func pointsOf(changes []ResourceChange, stage Stage) []pointChanges {
	var out []pointChanges
	index := map[string]int{}
	for i, c := range changes {
		op := string(c.Op)
		if c.Op == OpAction {
			op = c.Action
		}
		point := HookPoint(c.Kind, op, stage)
		j, ok := index[point]
		if !ok {
			j = len(out)
			index[point] = j
			out = append(out, pointChanges{point: point})
		}
		out[j].changes = append(out[j].changes, i)
	}
	return out
}

func (e *Engine) reconciler(kind string) (reconcile.Reconciler, bool) {
	if e.opts.Runner == nil || e.opts.Runner.Registry == nil {
		return nil, false
	}
	return e.opts.Runner.Registry.For(kind)
}

// request is what a reconciler plans a preview against: the planned state of
// the change set, the facts of the machine and the secrets it carries.
func (e *Engine) request(ctx context.Context, p *draft, r *store.Resource) reconcile.Request {
	return reconcile.Request{Resource: r, Read: p.view, Facts: e.facts(ctx, r.Server), Secrets: p.reader(e.opts.Secrets)}
}

func (e *Engine) facts(ctx context.Context, server store.ID) system.MachineFacts {
	if e.opts.Runner == nil || e.opts.Runner.Facts == nil {
		return system.MachineFacts{}
	}
	f, err := e.opts.Runner.Facts(ctx, server)
	if err != nil {
		e.opts.Log.Warn("reading machine facts for a preview", "error", err)
	}
	return f
}

// stepEffects asks the agent what each step would change. Steps that can't
// be checked yet, because an earlier step must run first, fall back to the
// operation's own description.
func (e *Engine) stepEffects(ctx context.Context, steps []agent.Step, owner store.ID) []system.Effect {
	var out []system.Effect
	for _, s := range steps {
		if e.opts.Runner != nil && e.opts.Runner.Agent != nil {
			results, err := agent.Execute(ctx, e.opts.Runner.Agent, []agent.Step{s}, agent.CallOptions{Check: true, Owner: owner}, nil)
			if err == nil {
				if len(results) == 1 && results[0].Result != nil {
					out = append(out, results[0].Result.Effects...)
				}
				continue
			}
		}
		static, err := agent.PlannedEffects([]agent.Step{s})
		if err != nil {
			e.opts.Log.Warn("describing a step", "op", s.Op, "error", err)
			continue
		}
		out = append(out, static...)
	}
	return out
}

// storedRequest keeps what applying a preview by its ID needs: the changes
// as asked for, with the IDs the plan chose for new resources and without
// secret values.
func storedRequest(req Request, cs *ChangeSet) *StoredRequest {
	out := &StoredRequest{Title: req.Title, Changes: make([]Change, len(req.Changes))}
	for i, ch := range req.Changes {
		c := ch.clone()
		if len(c.Secrets) > 0 {
			out.HadSecrets = true
		}
		c.Secrets = nil
		if c.Op == OpCreate && c.ID == "" && i < len(cs.Changes) {
			c.ID = cs.Changes[i].ID
		}
		out.Changes[i] = c
	}
	return out
}

// finish returns a planned change set as a preview, or applies it.
func (e *Engine) finish(ctx context.Context, p *draft, req Request, key string) (*ChangeSet, error) {
	cs := p.cs
	cs.DryRun = req.DryRun
	if req.DryRun {
		if err := e.checkWrites(ctx, cs); err != nil {
			return nil, err
		}
		if err := e.save(ctx, cs, storedRequest(req, cs)); err != nil {
			return nil, err
		}
		return cs.Clone(), nil
	}
	if len(cs.Conflicts) > 0 {
		err := ErrConflicts.New("count", len(cs.Conflicts))
		e.failed(ctx, p, cs.Changes, err)
		e.settle(ctx, key, "")
		return cs.Clone(), err
	}
	written, err := e.apply(ctx, cs, p.secrets, key)
	if !written {
		e.failed(ctx, p, cs.Changes, err)
		e.settle(ctx, key, "")
		return nil, err
	}
	// The answer is the change set as kept, even when the client that asked
	// went away meanwhile.
	if kept, getErr := e.Get(context.WithoutCancel(ctx), cs.ID); getErr == nil {
		cs = kept
	}
	return cs, err
}

// errRollback ends the transaction of a dry run without writing anything.
var errRollback = errors.New("changeset: dry run")

// checkWrites makes the store writes of a change set in a transaction that
// is thrown away, so a preview reports what applying it would run into,
// such as a name another resource already uses.
func (e *Engine) checkWrites(ctx context.Context, cs *ChangeSet) error {
	err := e.opts.Store.Write(ctx, writeMeta(cs), func(tx store.Tx) error {
		if err := writeChanges(ctx, tx, cs.Changes); err != nil {
			return err
		}
		return errRollback
	})
	if errors.Is(err, errRollback) {
		return nil
	}
	return err
}

// apply takes the snapshots a risky change set needs and stores the secret
// values, then writes every change, keeps the change set, queues the job
// that carries out the system changes, settles the idempotency key and
// records each change in the audit log. In the panel database (Options.DB)
// that is one transaction: either all of it is there or none of it is, and
// the job starts once it commits. Otherwise each write commits on its own,
// and when one fails after the settings were written, the change set is
// kept as failed and the reconcile queue (reconcile.Runner.Run) carries the
// changes out. It reports whether the settings were written.
func (e *Engine) apply(ctx context.Context, cs *ChangeSet, values map[secrets.Ref][]byte, key string) (bool, error) {
	// What a change that didn't happen left behind goes, whether or not the
	// client is still there.
	cleanup := context.WithoutCancel(ctx)
	// Snapshots come first, so nothing changes when they can't be taken.
	if cs.Snapshot {
		if err := e.takeSnapshots(ctx, cs); err != nil {
			return false, err
		}
	}
	var stored []secrets.Ref
	if len(values) > 0 {
		var err error
		if stored, err = e.putSecrets(ctx, values); err != nil {
			e.dropSnapshots(cleanup, cs.Snapshots)
			return false, err
		}
	}
	// Once the writes start they finish, even when the client that asked
	// goes away meanwhile; before that, nothing is written for a client that
	// is gone.
	if err := ctx.Err(); err != nil {
		e.dropSecrets(cleanup, stored)
		e.dropSnapshots(cleanup, cs.Snapshots)
		return false, err
	}
	var wrote bool
	err := e.atomically(context.WithoutCancel(ctx), func(ctx context.Context) error {
		err := e.opts.Store.Write(ctx, writeMeta(cs), func(tx store.Tx) error {
			return writeChanges(ctx, tx, cs.Changes)
		})
		if err != nil {
			return err
		}
		wrote = true
		return e.record(ctx, cs, key)
	})
	switch {
	case err == nil:
		return true, nil
	case wrote && e.opts.DB == nil:
		// The settings are written and stay; what couldn't be recorded is
		// logged, and the change set is kept as failed.
		e.opts.Log.Error("recording an applied change set", "changeSet", cs.ID, "error", err)
		cs.State, cs.Error = StateFailed, i18n.From(err).Message
		if saveErr := e.save(cleanup, cs, nil); saveErr != nil {
			e.opts.Log.Error("keeping an applied change set", "changeSet", cs.ID, "error", saveErr)
		}
		e.settle(cleanup, key, cs.ID)
		if cs.UndoOf != "" {
			e.markUndone(cleanup, cs.UndoOf, cs.ID)
		}
		return true, err
	}
	cs.State, cs.Job = StatePlanned, ""
	e.dropSecrets(cleanup, stored)
	e.dropSnapshots(cleanup, cs.Snapshots)
	return false, err
}

// record keeps a change set whose settings were just written, queues its
// job, settles the request's idempotency key, marks the change set it
// undoes and records each change in the audit log.
func (e *Engine) record(ctx context.Context, cs *ChangeSet, key string) error {
	// The change set is kept before its job is queued: a queue outside the
	// panel database may start the job right away, and the job reads it.
	cs.State = StateApplying
	if err := e.save(ctx, cs, nil); err != nil {
		return err
	}
	job, err := e.opts.Jobs.Enqueue(ctx, ApplyJob, cs.ID, jobs.Options{Title: cs.Title, Actor: cs.Actor, ChangeSet: cs.ID, Scope: cs.Owner()})
	if err != nil {
		return err
	}
	cs.Job = job.ID
	// The job may already have started and claimed the change set.
	if _, err := e.storage.Update(ctx, cs.ID, func(s *ChangeSet) error {
		if s.Job == "" {
			s.Job = job.ID
		}
		return nil
	}); err != nil {
		return err
	}
	if key != "" {
		if err := e.storage.SettleKey(ctx, key, cs.ID, e.opts.Now()); err != nil {
			return err
		}
	}
	if cs.UndoOf != "" {
		e.markUndone(ctx, cs.UndoOf, cs.ID)
	}
	for _, c := range cs.Changes {
		r := &store.Resource{Meta: store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope}}
		if err := e.auditRecord(ctx, cs.Actor, string(changePermission(c)), r, cs.ID, events.OutcomeSuccess, nil, cs.Title); err != nil {
			return err
		}
	}
	return nil
}

// atomically runs fn so that the writes it makes commit together, when the
// engine has the panel database; otherwise each write commits on its own.
func (e *Engine) atomically(ctx context.Context, fn func(ctx context.Context) error) error {
	if e.opts.DB == nil {
		return fn(ctx)
	}
	return e.opts.DB.Atomic(ctx, fn)
}

// afterCommit runs fn once what was written with ctx is committed, which is
// at once outside a transaction.
func (e *Engine) afterCommit(ctx context.Context, fn func()) {
	if e.opts.DB == nil {
		fn()
		return
	}
	e.opts.DB.AfterCommit(ctx, fn)
}

// writeMeta is what the store records with the writes of a change set.
func writeMeta(cs *ChangeSet) store.WriteMeta {
	return store.WriteMeta{ChangeSet: cs.ID, Actor: cs.Actor.String(), UndoOf: cs.UndoOf}
}

func writeChanges(ctx context.Context, tx store.Tx, changes []ResourceChange) error {
	for _, c := range changes {
		if err := writeChange(ctx, tx, c); err != nil {
			return err
		}
	}
	return nil
}

func writeChange(ctx context.Context, tx store.Tx, c ResourceChange) error {
	switch c.Op {
	case OpCreate:
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope, Server: c.Server, Labels: c.Labels}, Spec: c.After})
	case OpDelete:
		return tx.Delete(ctx, c.ID, c.Revision)
	case OpUpdate, OpAction:
		if c.Op == OpAction && bytes.Equal(c.Before, c.After) {
			return nil
		}
		cur, err := tx.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		cur.Spec, cur.Labels, cur.Revision = c.After, c.Labels, c.Revision
		return tx.Update(ctx, cur)
	}
	return fmt.Errorf("unknown operation %q", c.Op)
}

// runApply is the job that carries out a change set: each change that isn't
// done yet, in order, until one fails. A change is marked as started before
// it runs and as done once it is in place, so a retry carries on from the
// change that didn't finish.
func (e *Engine) runApply(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
	var id store.ID
	if err := json.Unmarshal(args, &id); err != nil {
		return nil, err
	}
	cs, err := e.claim(ctx, id, job)
	if err != nil {
		return nil, err
	}
	// Records outlive the job's context, so a canceled job still says how
	// its change set ended.
	record := context.WithoutCancel(ctx)
	total := int64(len(cs.Changes))
	var failure error
	for i, c := range cs.Changes {
		if c.State == ChangeDone {
			continue
		}
		r.Progress(int64(i), total, "Applying "+cs.Title)
		if err := e.markChange(record, cs.ID, job.ID, i, ChangeStarted, nil); err != nil {
			failure = err
			break
		}
		output, err := e.applyChange(ctx, c, r)
		if stopped := jobs.Stopped(ctx); stopped != nil {
			if jobs.ErrInterrupted.Is(stopped) {
				// The panel is stopping: the job is reported as interrupted
				// when it starts again, which marks the change set failed.
				return nil, stopped
			}
			// Canceled or out of time; whatever the change got to is
			// unknown, so it doesn't count as done.
			err = stopped
		}
		eventType, severity := e.eventType(c), events.SeverityInfo
		msg := cs.Title
		if err != nil {
			eventType, severity, msg = c.Kind+".failed", events.SeverityError, i18n.From(err).Message
			r.Log(slog.LevelError, "applying failed", "resource", c.ID, "error", msg)
		}
		e.publish(record, events.Event{
			Type: eventType, Severity: severity, Subject: c.ID, Kind: c.Kind, Scope: c.Scope,
			Actor: cs.Actor, ChangeSet: cs.ID, Job: job.ID, Message: msg, Data: output,
		})
		if err != nil {
			failure = err
			if len(output) > 0 {
				e.update(record, cs.ID, func(s *ChangeSet) { s.Changes[i].Output = output })
			}
			break
		}
		if err := e.markChange(record, cs.ID, job.ID, i, ChangeDone, output); err != nil {
			// The change is in place; only a retry would run it again.
			e.opts.Log.Warn("recording a change as done", "changeSet", cs.ID, "change", i, "error", err)
		}
	}
	now := e.opts.Now().UTC()
	final, err := e.storage.Update(record, cs.ID, func(s *ChangeSet) error {
		if s.State != StateApplying || s.Job != job.ID {
			return errNotClaimed
		}
		if failure != nil {
			s.State, s.Error = StateFailed, i18n.From(failure).Message
		} else {
			s.State, s.Error, s.AppliedAt = StateApplied, "", &now
		}
		return nil
	})
	if err != nil {
		e.opts.Log.Error("recording how a change set ended", "changeSet", cs.ID, "error", err)
		return nil, err
	}
	setEvent := events.ChangeSetApplied
	if failure != nil {
		setEvent = events.ChangeSetFailed
	}
	e.publishSet(record, setEvent, final, "")
	if failure == nil && e.opts.Hooks != nil {
		for _, pc := range pointsOf(final.Changes, StageApplied) {
			e.opts.Hooks.Applied(record, pc.point, final.Clone(), pc.changes)
		}
	}
	r.Progress(total, total, "Done")
	result := map[string]any{"changeSet": cs.ID, "state": final.State}
	outputs := map[int]json.RawMessage{}
	for i, c := range final.Changes {
		if len(c.Output) > 0 {
			outputs[i] = c.Output
		}
	}
	if len(outputs) > 0 {
		result["outputs"] = outputs
	}
	return result, failure
}

// errNotClaimed is the update of a change set a job no longer carries out.
var errNotClaimed = errors.New("changeset: the change set isn't this job's to carry out")

// claim makes a change set the job's to carry out: the one it was queued
// for, or, when the job retries an earlier one, a change set that failed and
// can be carried on with. Only one job carries out a change set at a time,
// and one that was undone or applied never runs again.
func (e *Engine) claim(ctx context.Context, id store.ID, job *jobs.Job) (*ChangeSet, error) {
	return e.storage.Update(context.WithoutCancel(ctx), id, func(s *ChangeSet) error {
		switch {
		case s.State == StateApplying && (s.Job == "" || s.Job == job.ID):
			// The job it was queued with, which may start before the change
			// set records it.
		case s.State == StateFailed && job.RetryOf != "":
			if err := e.canCarryOn(s); err != nil {
				return err
			}
			s.Error = ""
		default:
			return ErrNotRetryable.New("title", s.Title, "state", string(s.State))
		}
		s.State, s.Job = StateApplying, job.ID
		return nil
	})
}

// markChange records how far carrying out one change got, and its output.
func (e *Engine) markChange(ctx context.Context, id, job store.ID, i int, state ChangeState, output json.RawMessage) error {
	_, err := e.storage.Update(ctx, id, func(s *ChangeSet) error {
		if s.State != StateApplying || s.Job != job || i >= len(s.Changes) {
			return errNotClaimed
		}
		s.Changes[i].State = state
		if len(output) > 0 {
			s.Changes[i].Output = output
		}
		return nil
	})
	return err
}

// canCarryOn says whether a retry may carry on with a change set that
// failed. Changes before the one that failed are done and later ones never
// started, so only the one that failed may have done something already; a
// retry runs it again unless it is an action that may already have done what
// it does once.
func (e *Engine) canCarryOn(cs *ChangeSet) error {
	for _, c := range cs.Changes {
		switch {
		case c.State == ChangeDone:
			continue
		case c.State == ChangeStarted && c.Op == OpAction && e.runsOnce(c):
			title := c.Action
			if k, ok := e.opts.Kinds.Get(c.Kind); ok {
				if a, ok := k.Info().Action(c.Action); ok && a.Title != "" {
					title = a.Title
				}
			}
			return ErrActionNotRepeated.New("title", cs.Title, "action", title)
		}
		return nil
	}
	return nil
}

// runsOnce reports whether an action's change does something besides
// bringing the resource in line with its settings: steps of its own, or work
// in the panel.
func (e *Engine) runsOnce(c ResourceChange) bool {
	if len(c.Steps) > 0 {
		return true
	}
	rec, ok := e.reconciler(c.Kind)
	if !ok {
		return false
	}
	_, applies := rec.(reconcile.ActionApplier)
	return applies
}

// retryable is the apply job's check before a retry: its change set must
// have failed in a way a retry can carry on from.
func (e *Engine) retryable(ctx context.Context, job *jobs.Job) error {
	var id store.ID
	if err := json.Unmarshal(job.Args, &id); err != nil {
		return err
	}
	cs, err := e.Get(ctx, id)
	if err != nil {
		return err
	}
	if cs.State != StateFailed {
		return ErrNotRetryable.New("title", cs.Title, "state", string(cs.State))
	}
	return e.canCarryOn(cs)
}

// applyEnded keeps a change set in line with its job when the job couldn't
// say how it ended itself: canceled before it started, or cut short by the
// panel stopping, which the next start reports.
func (e *Engine) applyEnded(ctx context.Context, job *jobs.Job) {
	var id store.ID
	if err := json.Unmarshal(job.Args, &id); err != nil {
		return
	}
	now := e.opts.Now().UTC()
	cs, err := e.storage.Update(ctx, id, func(s *ChangeSet) error {
		if s.State != StateApplying || (s.Job != "" && s.Job != job.ID) {
			return errNotClaimed
		}
		s.Job = job.ID
		switch {
		case job.State == jobs.StateSucceeded:
			s.State, s.AppliedAt = StateApplied, &now
		case job.Error != nil:
			s.State, s.Error = StateFailed, job.Error.Message
		default:
			s.State, s.Error = StateFailed, i18n.ErrInternal.New().Message
		}
		return nil
	})
	switch {
	case errors.Is(err, errNotClaimed) || store.ErrNotFound.Is(err):
		return
	case err != nil:
		e.opts.Log.Error("recording how a change set's job ended", "changeSet", id, "job", job.ID, "error", err)
		return
	}
	t := events.ChangeSetFailed
	if cs.State == StateApplied {
		t = events.ChangeSetApplied
	}
	e.publishSet(ctx, t, cs, "")
}

// applyChange carries out one change: an action's own steps, then the
// resource's reconcile, which verifies its result with the plan's checks,
// then the panel's own part of an action, then the checks of the action.
// It returns the action's output.
func (e *Engine) applyChange(ctx context.Context, c ResourceChange, r jobs.Reporter) (json.RawMessage, error) {
	runner := e.opts.Runner
	progress := func(id store.ID, i int, s agent.Step) {
		title := s.Title
		if title == "" {
			title = s.Op
		}
		r.Log(slog.LevelInfo, title, "resource", id, "step", i+1)
	}
	if len(c.Steps) > 0 {
		if runner == nil || runner.Agent == nil {
			return nil, errors.New("changeset: there is no agent to run the action's steps")
		}
		if _, err := agent.Execute(ctx, runner.Agent, c.Steps, agent.CallOptions{Owner: c.ID}, func(i int, s agent.Step) { progress(c.ID, i, s) }); err != nil {
			return nil, err
		}
	}
	rec, hasReconciler := e.reconciler(c.Kind)
	switch {
	case hasReconciler:
		if err := runner.Reconcile(ctx, c.ID, progress); err != nil {
			return nil, err
		}
	case c.Op == OpDelete:
		if err := e.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:changeset"}, func(tx store.Tx) error { return tx.Purge(ctx, c.ID) }); err != nil {
			return nil, err
		}
	default:
		if err := e.markStored(ctx, c.ID); err != nil {
			return nil, err
		}
	}
	var output json.RawMessage
	if applier, ok := rec.(reconcile.ActionApplier); ok && c.Op == OpAction {
		res, err := e.opts.Store.Get(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		req := reconcile.Request{Resource: res, Read: e.opts.Store, Secrets: e.opts.Secrets}
		if runner != nil {
			req.Facts = e.facts(ctx, res.Server)
		}
		out, err := applier.ApplyAction(ctx, reconcile.ActionRequest{Request: req, Action: c.Action, Input: c.Input})
		if err != nil {
			return nil, err
		}
		if out != nil {
			if output, err = json.Marshal(out); err != nil {
				return nil, err
			}
		}
	}
	if len(c.Checks) > 0 && runner != nil {
		r.Log(slog.LevelInfo, "Checking the result", "resource", c.ID, "checks", len(c.Checks))
		if err := runner.Verify(ctx, c.Server, c.Checks); err != nil {
			return output, err
		}
	}
	return output, nil
}

// markStored records that a resource of a kind without a reconciler is in
// place: such kinds only keep settings, so storing them is all applying
// them does.
func (e *Engine) markStored(ctx context.Context, id store.ID) error {
	return e.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:changeset"}, func(tx store.Tx) error {
		res, err := tx.Get(ctx, id)
		if err != nil {
			return err
		}
		raw, err := reconcile.WithCommonStatus(res.Status, func(st *store.Status) {
			st.Phase, st.Message, st.Code, st.ObservedGeneration = store.PhaseReady, "", "", res.Generation
			st.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionTrue, Reason: "Stored", ObservedGeneration: res.Generation}, e.opts.Now())
		})
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, id, raw)
	})
}

// eventType names the event emitted after a change succeeds.
func (e *Engine) eventType(c ResourceChange) string {
	switch c.Op {
	case OpCreate:
		return c.Kind + ".created"
	case OpUpdate:
		return c.Kind + ".updated"
	case OpDelete:
		return c.Kind + ".deleted"
	}
	if k, ok := e.opts.Kinds.Get(c.Kind); ok {
		if a, ok := k.Info().Action(c.Action); ok {
			return a.EventName(c.Kind)
		}
	}
	return store.Action{Name: c.Action}.EventName(c.Kind)
}

func (e *Engine) publish(ctx context.Context, ev events.Event) {
	if e.opts.Events == nil {
		return
	}
	if err := e.opts.Events.Publish(ctx, ev); err != nil {
		e.opts.Log.Warn("publishing an event", "type", ev.Type, "error", err)
	}
}

// publishSet publishes one of the events about a change set as a whole.
func (e *Engine) publishSet(ctx context.Context, t events.Type, cs *ChangeSet, undoneBy store.ID) {
	data := events.ChangeSetData{Title: cs.Title, UndoneBy: undoneBy, Resources: []store.ID{}}
	for _, c := range cs.Changes {
		if !slices.Contains(data.Resources, c.ID) {
			data.Resources = append(data.Resources, c.ID)
		}
	}
	raw, _ := json.Marshal(data)
	message := cs.Title
	if t.Name == events.ChangeSetFailed.Name && cs.Error != "" {
		message = cs.Error
	}
	e.publish(ctx, events.Event{Type: t.Name, Severity: t.Severity, Scope: cs.Owner(), Actor: cs.Actor, ChangeSet: cs.ID, Job: cs.Job, Message: message, Data: raw})
}

// auditRecord records in the audit log what an actor did, or tried to: a
// change to the resource r, with why it didn't go through when cause isn't
// nil.
func (e *Engine) auditRecord(ctx context.Context, actor *auth.Actor, action string, r *store.Resource, cs store.ID, outcome events.Outcome, cause error, summary string) error {
	if e.opts.Audit == nil {
		return nil
	}
	rec := events.AuditRecord{Action: action, Kind: r.Kind, Subject: r.ID, Scope: r.Scope, ChangeSet: cs, Outcome: outcome, Summary: summary}
	if cause != nil {
		rec.Reason = i18n.From(cause).Message
	}
	if actor != nil {
		rec.Actor = *actor
	}
	return e.opts.Audit.Record(ctx, rec)
}

// audit is auditRecord for records whose loss shouldn't stop anything else,
// such as those of attempts that didn't go through.
func (e *Engine) audit(ctx context.Context, actor *auth.Actor, action string, r *store.Resource, cs store.ID, outcome events.Outcome, cause error, summary string) {
	if err := e.auditRecord(ctx, actor, action, r, cs, outcome, cause, summary); err != nil {
		e.opts.Log.Error("writing an audit record", "action", action, "error", err)
	}
}

// refusal records a change that was allowed but didn't go through: denied
// when a policy refused it, as a validate hook does, and failed otherwise,
// as for settings that aren't valid. A preview that fails changes nothing
// and answers at once, so of previews only refusals are recorded.
func (e *Engine) refusal(ctx context.Context, p *draft, actor *auth.Actor, perm auth.Permission, r *store.Resource, cause error, summary string) {
	outcome := events.OutcomeFailed
	if i18n.From(cause).Status == http.StatusForbidden {
		outcome = events.OutcomeDenied
	} else if p.dryRun {
		return
	}
	p.recorded = true
	e.audit(ctx, actor, string(perm), r, "", outcome, cause, summary)
}

// failed records the changes of a request that would have been applied as
// failed, with the reason, unless the audit log already has why the
// request stopped.
func (e *Engine) failed(ctx context.Context, p *draft, changes []ResourceChange, cause error) {
	if p.dryRun || p.recorded {
		return
	}
	p.recorded = true
	for i, c := range changes {
		summary := p.cs.Title
		if summary == "" && i < len(p.titles) {
			summary = p.titles[i]
		}
		e.audit(ctx, p.cs.Actor, string(changePermission(c)), &store.Resource{Meta: store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope}}, "",
			events.OutcomeFailed, cause, summary)
	}
}

// changePermission is the permission a change needs, such as web.site.update
// or web.site.deploy.
func changePermission(c ResourceChange) auth.Permission {
	op := string(c.Op)
	if c.Op == OpAction {
		op = c.Action
	}
	return auth.KindPermission(c.Kind, op)
}

// Undo implements Pipeline.
func (e *Engine) Undo(ctx context.Context, req UndoRequest) (*ChangeSet, error) {
	orig, err := e.Get(ctx, req.ChangeSet)
	if err != nil {
		return nil, err
	}
	if orig.DryRun || (orig.State != StateApplied && orig.State != StateFailed) {
		return nil, ErrNotApplied.New()
	}
	var key string
	if !req.DryRun {
		key = idempotencyKey("undo", req.Actor, req.IdempotencyKey)
		if earlier, err := e.reserve(ctx, key); earlier != nil || err != nil {
			return earlier, err
		}
		if !e.claimSet(orig.ID) {
			e.settle(ctx, key, "")
			return nil, ErrUndoing.New("title", orig.Title)
		}
		defer e.release(orig.ID)
		// Another undo may have finished between reading the change set and
		// claiming it.
		if orig, err = e.Get(ctx, req.ChangeSet); err != nil || (orig.State != StateApplied && orig.State != StateFailed) {
			e.settle(ctx, key, "")
			if err == nil {
				err = ErrNotApplied.New()
			}
			return nil, err
		}
	}
	changes, err := e.undoChanges(ctx, orig)
	if err == nil && len(changes) == 0 {
		err = ErrNothingToUndo.New("title", orig.Title)
	}
	if err != nil {
		e.settle(ctx, key, "")
		return nil, err
	}
	request := Request{Actor: req.Actor, Title: "Undo: " + orig.Title, Changes: changes, DryRun: req.DryRun}
	p, err := e.plan(ctx, request, "", orig.ID)
	if err != nil {
		e.settle(ctx, key, "")
		return nil, err
	}
	return e.finish(ctx, p, request, key)
}

// markUndone records that a change set was undone by another. Only one that
// was applied or failed becomes undone; a change set is undone once. The
// event goes out once the record is committed.
func (e *Engine) markUndone(ctx context.Context, id, by store.ID) {
	ctx = context.WithoutCancel(ctx)
	var marked bool
	undone, err := e.storage.Update(ctx, id, func(s *ChangeSet) error {
		switch s.State {
		case StateApplied, StateFailed:
			s.State, marked = StateUndone, true
		case StateUndone:
			// Marked already, by this undo.
		default:
			return ErrNotApplied.New()
		}
		return nil
	})
	if err != nil {
		e.opts.Log.Warn("marking a change set as undone", "changeSet", id, "undoneBy", by, "error", err)
		return
	}
	if marked {
		e.afterCommit(ctx, func() { e.publishSet(ctx, events.ChangeSetUndone, undone, by) })
	}
}

// claimSet claims a change set for an undo or for applying a preview, and
// reports whether nothing else had it.
func (e *Engine) claimSet(id store.ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.claims[id] {
		return false
	}
	e.claims[id] = true
	return true
}

func (e *Engine) release(id store.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.claims, id)
}

// claimed reports whether an undo or an apply is working on a change set,
// which retention then keeps.
func (e *Engine) claimed(id store.ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.claims[id]
}

// undoChanges builds the changes that revert a change set, last change
// first: creates become deletes, updates and actions put the previous spec
// and labels back, and deletes become creates with the same ID, placement,
// labels and spec.
func (e *Engine) undoChanges(ctx context.Context, orig *ChangeSet) ([]Change, error) {
	created := map[store.ID]bool{}
	for _, c := range orig.Changes {
		if c.Op == OpCreate {
			created[c.ID] = true
		}
	}
	guarded := map[store.ID]bool{}
	var out []Change
	for i := len(orig.Changes) - 1; i >= 0; i-- {
		c := orig.Changes[i]
		cur, err := e.opts.Store.Get(ctx, c.ID)
		if err != nil && !store.ErrNotFound.Is(err) {
			return nil, err
		}
		exists := err == nil
		switch c.Op {
		case OpCreate:
			if exists && !cur.Deleted() {
				out = append(out, Change{Op: OpDelete, Kind: c.Kind, ID: c.ID})
			}
		case OpUpdate, OpAction:
			// Reverting an update of a resource whose creation is undone too
			// would only be thrown away.
			if created[c.ID] || (bytes.Equal(c.Before, c.After) && maps.Equal(c.Labels, c.LabelsBefore)) {
				continue
			}
			if !exists || cur.Deleted() {
				return nil, store.ErrNotFound.New("what", string(c.ID))
			}
			labels := maps.Clone(c.LabelsBefore)
			if labels == nil {
				labels = map[string]string{}
			}
			ch := Change{Op: OpUpdate, Kind: c.Kind, ID: c.ID, Spec: c.Before, Labels: labels}
			// Only the first revert of a resource can check its revision;
			// later ones see the revision the first one writes.
			if !guarded[c.ID] {
				ch.Revision, guarded[c.ID] = cur.Revision, true
			}
			out = append(out, ch)
		case OpDelete:
			if exists {
				return nil, ErrStillRemoving.New("what", string(c.ID))
			}
			out = append(out, Change{Op: OpCreate, Kind: c.Kind, ID: c.ID, Scope: c.Scope, Server: c.Server, Spec: c.Before, Labels: c.Labels})
		}
	}
	return out, nil
}

// Apply implements Pipeline. Applies of one preview are taken one at a
// time, so two requests that arrive together, as from a double click, apply
// it once; the other is told it is already being applied, or that it is no
// longer a preview.
func (e *Engine) Apply(ctx context.Context, req ApplyRequest) (*ChangeSet, error) {
	preview, err := e.Get(ctx, req.ChangeSet)
	if err != nil {
		return nil, err
	}
	// A retry with the key answers with what the first request applied,
	// although the preview has become that change set.
	key := idempotencyKey("apply", req.Actor, req.IdempotencyKey)
	if earlier, err := e.reserve(ctx, key); earlier != nil || err != nil {
		return earlier, err
	}
	refuse := func(err error) (*ChangeSet, error) {
		e.settle(ctx, key, "")
		return nil, err
	}
	if !preview.DryRun {
		return refuse(ErrNotPreview.New("title", preview.Title))
	}
	if !e.claimSet(preview.ID) {
		return refuse(ErrApplying.New("title", preview.Title))
	}
	defer e.release(preview.ID)
	// Another apply may have finished between reading the preview and
	// claiming it.
	if preview, err = e.Get(ctx, req.ChangeSet); err != nil {
		return refuse(err)
	}
	if !preview.DryRun {
		return refuse(ErrNotPreview.New("title", preview.Title))
	}
	stored, err := e.storage.Request(ctx, preview.ID)
	if err != nil {
		return refuse(err)
	}
	if stored.HadSecrets {
		return refuse(ErrPreviewHasSecrets.New("title", preview.Title))
	}
	if preview.UndoOf != "" {
		orig, err := e.Get(ctx, preview.UndoOf)
		if err != nil {
			return refuse(err)
		}
		if !e.claimSet(orig.ID) {
			return refuse(ErrUndoing.New("title", orig.Title))
		}
		defer e.release(orig.ID)
		if orig, err = e.Get(ctx, preview.UndoOf); err != nil {
			return refuse(err)
		}
		if orig.State != StateApplied && orig.State != StateFailed {
			return refuse(ErrNotApplied.New())
		}
	}
	request := Request{Actor: req.Actor, Title: stored.Title, Changes: stored.Changes}
	p, err := e.plan(ctx, request, preview.ID, preview.UndoOf)
	if err != nil {
		return refuse(err)
	}
	if !samePlan(preview, p.cs) {
		e.settle(ctx, key, "")
		// The new plan becomes a preview of its own, for the person to look
		// at before applying it.
		fresh := p.cs
		fresh.ID, fresh.DryRun = store.NewID(store.PrefixChangeSet), true
		if err := e.checkWrites(ctx, fresh); err != nil {
			return nil, err
		}
		if err := e.save(ctx, fresh, storedRequest(Request{Title: stored.Title, Changes: stored.Changes}, fresh)); err != nil {
			return nil, err
		}
		return nil, ErrPreviewStale.New("title", preview.Title, "preview", string(fresh.ID))
	}
	return e.finish(ctx, p, request, key)
}

// samePlan reports whether a new plan does what a preview showed: the same
// changes to the same resources, the same specs, and the same effects on the
// machine. Changes hooks added are compared without the IDs they chose for
// new resources, which differ every time.
func samePlan(preview, now *ChangeSet) bool {
	return bytes.Equal(fingerprint(preview), fingerprint(now))
}

func fingerprint(cs *ChangeSet) []byte {
	type change struct {
		Op, Action, Kind string
		ID               store.ID
		Scope            store.Scope
		Server           store.ID
		Labels           map[string]string
		After, Input     any
		Source           Source
		Steps            any
	}
	type effect struct {
		Type                       system.EffectType
		Action, Target, Detail     string
		Contributor                string
		Destructive                bool
		Download, Disk, MemoryUsed int64
	}
	var changes []change
	for _, c := range cs.Changes {
		id := c.ID
		if c.Source != RequestSource && c.Op == OpCreate {
			id = ""
		}
		after, _ := decodeAny(c.After)
		input, _ := decodeAny(c.Input)
		steps, _ := decodeAny(c.Steps)
		changes = append(changes, change{
			Op: string(c.Op), Action: c.Action, Kind: c.Kind, ID: id, Scope: c.Scope, Server: c.Server,
			Labels: c.Labels, After: after, Input: input, Source: c.Source, Steps: steps,
		})
	}
	effects := make([]effect, 0, len(cs.Effects))
	for _, eff := range cs.Effects {
		effects = append(effects, effect{
			Type: eff.Type, Action: eff.Action, Target: eff.Target, Detail: eff.Detail, Contributor: eff.Contributor,
			Destructive: eff.Destructive, Download: eff.DownloadBytes, Disk: eff.DiskBytes, MemoryUsed: eff.MemoryBytes,
		})
	}
	raw, _ := json.Marshal(struct {
		Changes   []change
		Effects   []effect
		Conflicts int
	}{changes, effects, len(cs.Conflicts)})
	return raw
}
