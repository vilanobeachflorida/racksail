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
	"reflect"
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
	ErrConflicts        = i18n.Define("changeset.conflicts", http.StatusConflict, "The change has {count} conflicting contributions from hooks. Resolve them before applying it.")
	ErrNotApplied       = i18n.Define("changeset.not_applied", http.StatusConflict, "Only applied changes can be undone.")
	ErrUnknownAction    = i18n.Define("changeset.unknown_action", http.StatusNotFound, "{kind} has no action called {action}.")
	ErrStillRemoving    = i18n.Define("changeset.still_removing", http.StatusConflict, "{what} is still being removed. Try again when the removal has finished.")
	ErrEmpty            = i18n.Define("changeset.empty", http.StatusUnprocessableEntity, "The request doesn't ask for any changes.")
	ErrNothingToUndo    = i18n.Define("changeset.nothing_to_undo", http.StatusConflict, "{title} can't be undone because it didn't change any settings.")
	ErrUndoing          = i18n.Define("changeset.undoing", http.StatusConflict, "{title} is already being undone.")
	ErrDuplicateRequest = i18n.Define("changeset.duplicate_request", http.StatusConflict, "A request with the same Idempotency-Key is still being processed. Try again in a moment.")
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
	Secrets   secrets.Store
	Retention Retention
	Log       *slog.Logger
	Now       func() time.Time
}

// Engine is the default Pipeline.
type Engine struct {
	opts      Options
	retention Retention

	mu      sync.Mutex
	sets    map[store.ID]*ChangeSet
	idem    map[string]idemEntry
	undoing map[store.ID]bool
}

// NewEngine returns an engine and registers its apply job on the queue.
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
	e := &Engine{
		opts: opts, retention: opts.Retention.withDefaults(),
		sets: map[store.ID]*ChangeSet{}, idem: map[string]idemEntry{}, undoing: map[store.ID]bool{},
	}
	if err := opts.Jobs.Handle(jobs.Type{Name: ApplyJob, Run: e.runApply}); err != nil {
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
	p, err := e.plan(ctx, req)
	if err != nil {
		e.settle(key, "")
		return nil, err
	}
	return e.finish(ctx, p, req.DryRun, key)
}

// Get implements Pipeline.
func (e *Engine) Get(ctx context.Context, id store.ID) (*ChangeSet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, ok := e.sets[id]
	if !ok {
		return nil, i18n.ErrNotFound.New("what", "Change "+string(id))
	}
	return cs.Clone(), nil
}

// draft is a change set being planned: what it will change, the planned
// state of every resource it touches, and the secret values it carries.
type draft struct {
	cs      *ChangeSet
	view    *store.Overlay
	secrets map[secrets.Ref][]byte
	titles  []string
}

func (p *draft) reader(base secrets.Store) secrets.Reader {
	if base == nil {
		return pending{values: p.secrets}
	}
	return pending{values: p.secrets, base: base}
}

// plan runs the validate and plan stages for every change of a request.
func (e *Engine) plan(ctx context.Context, req Request) (*draft, error) {
	if len(req.Changes) == 0 {
		return nil, ErrEmpty.New()
	}
	now := e.opts.Now().UTC()
	p := &draft{
		cs: &ChangeSet{
			ID: store.NewID(store.PrefixChangeSet), Actor: req.Actor, Risk: store.RiskRead,
			State: StatePlanned, CreatedAt: now, Effects: []system.Effect{},
		},
		view:    store.NewOverlay(e.opts.Store, e.opts.Kinds),
		secrets: map[secrets.Ref][]byte{},
	}
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
				return nil, err
			}
		}
		if err := e.recheck(ctx, p, req.Actor, requested, effects); err != nil {
			return nil, err
		}
	}
	for _, eff := range cs.Effects {
		if eff.Destructive {
			cs.Snapshot = true
		}
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
// records the resource's planned state, so later changes see it.
func (e *Engine) planChange(ctx context.Context, p *draft, actor *auth.Actor, ch Change, now time.Time) error {
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
			if err := checkInput(a, ch.Input); err != nil {
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
			e.audit(ctx, actor, string(perm), next, "", events.OutcomeDenied, err.Error(), "")
			return err
		}
	}
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

	verb := map[Op]string{OpCreate: "Create", OpUpdate: "Update", OpDelete: "Delete"}[ch.Op]
	if ch.Op == OpAction {
		verb = action.Title
		if verb == "" {
			verb = strings.ToUpper(action.Name[:1]) + strings.ReplaceAll(action.Name[1:], "_", " ")
		}
	}
	p.titles = append(p.titles, verb+" "+strings.ToLower(info.Title))
	return nil
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

func checkInput(a store.Action, input json.RawMessage) error {
	trimmed := bytes.TrimSpace(input)
	if a.Input == nil {
		if len(trimmed) == 0 || string(trimmed) == "{}" || string(trimmed) == "null" {
			return nil
		}
		return i18n.Invalid(i18n.Field("input", i18n.ErrBadValue, "reason", "this action takes no input"))
	}
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	v := reflect.New(reflect.TypeOf(a.Input)).Interface()
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return i18n.Invalid(i18n.Field("input", i18n.ErrBadValue, "reason", err.Error()))
	}
	if validator, ok := v.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			var coded *i18n.Error
			if errors.As(err, &coded) {
				return err
			}
			return i18n.Invalid(i18n.Field("input", i18n.ErrBadValue, "reason", err.Error()))
		}
	}
	return nil
}

// finish returns a planned change set as a preview, or applies it.
func (e *Engine) finish(ctx context.Context, p *draft, dryRun bool, key string) (*ChangeSet, error) {
	cs := p.cs
	cs.DryRun = dryRun
	if dryRun {
		if err := e.checkWrites(ctx, cs); err != nil {
			return nil, err
		}
		e.save(ctx, cs)
		return cs.Clone(), nil
	}
	if len(cs.Conflicts) > 0 {
		e.settle(key, "")
		return cs.Clone(), ErrConflicts.New("count", len(cs.Conflicts))
	}
	written, err := e.apply(ctx, cs, p.secrets)
	if !written {
		e.settle(key, "")
		return nil, err
	}
	e.settle(key, cs.ID)
	if kept, getErr := e.Get(ctx, cs.ID); getErr == nil {
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
	err := e.opts.Store.Write(ctx, store.WriteMeta{ChangeSet: cs.ID, Actor: cs.Actor.String()}, func(tx store.Tx) error {
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

// apply stores the secret values, writes every change in one transaction
// and queues the job that carries out the system changes. It reports
// whether the store was written: when queueing the job fails after that,
// the change set is kept as failed and the periodic resync carries the
// changes out.
func (e *Engine) apply(ctx context.Context, cs *ChangeSet, values map[secrets.Ref][]byte) (bool, error) {
	var stored []secrets.Ref
	if len(values) > 0 {
		var err error
		if stored, err = e.putSecrets(ctx, values); err != nil {
			return false, err
		}
	}
	err := e.opts.Store.Write(ctx, store.WriteMeta{ChangeSet: cs.ID, Actor: cs.Actor.String()}, func(tx store.Tx) error {
		return writeChanges(ctx, tx, cs.Changes)
	})
	if err != nil {
		e.dropSecrets(ctx, stored)
		return false, err
	}
	// The change set is kept before its job is queued, because the job
	// reads it and may start right away.
	cs.State = StateApplying
	e.save(ctx, cs)
	job, err := e.opts.Jobs.Enqueue(ctx, ApplyJob, cs.ID, jobs.Options{Title: cs.Title, Actor: cs.Actor, ChangeSet: cs.ID})
	if err != nil {
		// The store has the changes; the periodic resync carries them out.
		message := i18n.From(err).Message
		e.update(cs.ID, func(s *ChangeSet) { s.State, s.Error = StateFailed, message })
	} else {
		e.update(cs.ID, func(s *ChangeSet) { s.Job = job.ID })
	}
	for _, c := range cs.Changes {
		op := string(c.Op)
		if c.Op == OpAction {
			op = c.Action
		}
		e.audit(ctx, cs.Actor, string(auth.KindPermission(c.Kind, op)), &store.Resource{Meta: store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope}}, cs.ID, events.OutcomeSuccess, "", cs.Title)
	}
	return true, err
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

// runApply is the job that reconciles every resource a change set touched.
func (e *Engine) runApply(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
	var id store.ID
	if err := json.Unmarshal(args, &id); err != nil {
		return nil, err
	}
	cs, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	total := int64(len(cs.Changes))
	var failure error
	for i, c := range cs.Changes {
		r.Progress(int64(i), total, "Applying "+cs.Title)
		err := e.applyChange(ctx, c, r)
		eventType, severity := e.eventType(c), events.SeverityInfo
		if err != nil {
			failure = err
			eventType, severity = c.Kind+".failed", events.SeverityError
			r.Log(slog.LevelError, "applying failed", "resource", c.ID, "error", i18n.From(err).Message)
		}
		if e.opts.Events != nil {
			msg := cs.Title
			if err != nil {
				msg = i18n.From(err).Message
			}
			if pubErr := e.opts.Events.Publish(ctx, events.Event{
				Type: eventType, Severity: severity, Subject: c.ID, Kind: c.Kind, Scope: c.Scope,
				Actor: cs.Actor, ChangeSet: cs.ID, Job: job.ID, Message: msg,
			}); pubErr != nil {
				e.opts.Log.Warn("publishing an event", "type", eventType, "error", pubErr)
			}
		}
		if err != nil {
			break
		}
	}
	now := e.opts.Now().UTC()
	var final *ChangeSet
	e.update(cs.ID, func(s *ChangeSet) {
		if failure != nil {
			s.State, s.Error = StateFailed, i18n.From(failure).Message
		} else {
			s.State, s.AppliedAt = StateApplied, &now
		}
		final = s.Clone()
	})
	if final == nil {
		final = cs
	}
	if failure == nil && e.opts.Hooks != nil {
		for _, pc := range pointsOf(final.Changes, StageApplied) {
			e.opts.Hooks.Applied(ctx, pc.point, final.Clone(), pc.changes)
		}
	}
	r.Progress(total, total, "Done")
	return map[string]string{"changeSet": string(cs.ID), "state": string(final.State)}, failure
}

func (e *Engine) applyChange(ctx context.Context, c ResourceChange, r jobs.Reporter) error {
	runner := e.opts.Runner
	if runner == nil {
		return nil
	}
	progress := func(id store.ID, i int, s agent.Step) {
		title := s.Title
		if title == "" {
			title = s.Op
		}
		r.Log(slog.LevelInfo, title, "resource", id, "step", i+1)
	}
	if len(c.Steps) > 0 && runner.Agent != nil {
		if _, err := agent.Execute(ctx, runner.Agent, c.Steps, agent.CallOptions{Owner: c.ID}, func(i int, s agent.Step) { progress(c.ID, i, s) }); err != nil {
			return err
		}
	}
	if _, ok := e.reconciler(c.Kind); !ok {
		if c.Op == OpDelete {
			return e.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:changeset"}, func(tx store.Tx) error { return tx.Purge(ctx, c.ID) })
		}
		return nil
	}
	return runner.Reconcile(ctx, c.ID, progress)
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

func (e *Engine) audit(ctx context.Context, actor *auth.Actor, action string, r *store.Resource, cs store.ID, outcome events.Outcome, reason, summary string) {
	if e.opts.Audit == nil {
		return
	}
	rec := events.AuditRecord{Action: action, Kind: r.Kind, Subject: r.ID, Scope: r.Scope, ChangeSet: cs, Outcome: outcome, Reason: reason, Summary: summary}
	if actor != nil {
		rec.Actor = *actor
	}
	if err := e.opts.Audit.Record(ctx, rec); err != nil {
		e.opts.Log.Error("writing an audit record", "action", action, "error", err)
	}
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
		if !e.startUndo(orig.ID) {
			e.settle(key, "")
			return nil, ErrUndoing.New("title", orig.Title)
		}
		defer e.endUndo(orig.ID)
	}
	changes, err := e.undoChanges(ctx, orig)
	if err == nil && len(changes) == 0 {
		err = ErrNothingToUndo.New("title", orig.Title)
	}
	if err != nil {
		e.settle(key, "")
		return nil, err
	}
	p, err := e.plan(ctx, Request{Actor: req.Actor, Title: "Undo: " + orig.Title, Changes: changes})
	if err != nil {
		e.settle(key, "")
		return nil, err
	}
	p.cs.UndoOf = orig.ID
	cs, err := e.finish(ctx, p, req.DryRun, key)
	if err != nil || req.DryRun {
		return cs, err
	}
	e.update(orig.ID, func(s *ChangeSet) { s.State = StateUndone })
	return cs, nil
}

func (e *Engine) startUndo(id store.ID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, ok := e.sets[id]
	if !ok || e.undoing[id] || (cs.State != StateApplied && cs.State != StateFailed) {
		return false
	}
	e.undoing[id] = true
	return true
}

func (e *Engine) endUndo(id store.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.undoing, id)
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
