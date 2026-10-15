package changeset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Error codes.
var (
	ErrConflicts     = i18n.Define("changeset.conflicts", http.StatusConflict, "The change has {count} conflicting contributions from hooks. Resolve them before applying it.")
	ErrNotApplied    = i18n.Define("changeset.not_applied", http.StatusConflict, "Only applied changes can be undone.")
	ErrUnknownAction = i18n.Define("changeset.unknown_action", http.StatusNotFound, "{kind} has no action called {action}.")
	ErrStillRemoving = i18n.Define("changeset.still_removing", http.StatusConflict, "{what} is still being removed. Try again when the removal has finished.")
)

// ApplyJob is the job type that carries out applied change sets.
const ApplyJob = "changeset.apply"

// Options are the dependencies of an Engine. Hooks, Authorizer, Runner,
// Events and Audit may be nil; the corresponding stages are then skipped.
type Options struct {
	Store      store.Store
	Kinds      *store.KindRegistry
	Runner     *reconcile.Runner
	Jobs       jobs.Queue
	Events     events.Bus
	Audit      events.AuditLog
	Hooks      Hooks
	Authorizer auth.Authorizer
	Log        *slog.Logger
	Now        func() time.Time
}

// Engine is the default Pipeline.
type Engine struct {
	opts Options

	mu   sync.Mutex
	sets map[store.ID]*ChangeSet
	idem map[string]store.ID
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
	e := &Engine{opts: opts, sets: map[store.ID]*ChangeSet{}, idem: map[string]store.ID{}}
	if err := opts.Jobs.Handle(jobs.Type{Name: ApplyJob, Run: e.runApply}); err != nil {
		return nil, err
	}
	return e, nil
}

// Submit implements Pipeline.
func (e *Engine) Submit(ctx context.Context, req Request) (*ChangeSet, error) {
	if req.IdempotencyKey != "" {
		e.mu.Lock()
		id, seen := e.idem[req.Actor.String()+"\x00"+req.IdempotencyKey]
		e.mu.Unlock()
		if seen {
			return e.Get(ctx, id)
		}
	}
	cs, err := e.plan(ctx, req)
	if err != nil {
		return nil, err
	}
	cs.DryRun = req.DryRun
	if req.DryRun {
		e.save(cs)
		return cs, nil
	}
	if len(cs.Conflicts) > 0 {
		return cs, ErrConflicts.New("count", len(cs.Conflicts))
	}
	if err := e.apply(ctx, cs); err != nil {
		return nil, err
	}
	if req.IdempotencyKey != "" {
		e.mu.Lock()
		e.idem[req.Actor.String()+"\x00"+req.IdempotencyKey] = cs.ID
		e.mu.Unlock()
	}
	return cs, nil
}

// Get implements Pipeline.
func (e *Engine) Get(ctx context.Context, id store.ID) (*ChangeSet, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, ok := e.sets[id]
	if !ok {
		return nil, i18n.ErrNotFound.New("what", "Change "+string(id))
	}
	c := *cs
	return &c, nil
}

func (e *Engine) save(cs *ChangeSet) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := *cs
	e.sets[cs.ID] = &c
}

func (e *Engine) operation(req Request) string {
	if req.Op == OpAction {
		return req.Action
	}
	return string(req.Op)
}

// plan runs the validate and plan stages for one request.
func (e *Engine) plan(ctx context.Context, req Request) (*ChangeSet, error) {
	kind, ok := e.opts.Kinds.Get(req.Kind)
	if !ok {
		return nil, i18n.ErrUnknownName.New("what", "kind", "name", req.Kind)
	}
	info := kind.Info()
	now := e.opts.Now().UTC()
	var old, next *store.Resource
	var action store.Action
	risk := info.Risks.Update
	switch req.Op {
	case OpCreate:
		risk = info.Risks.Create
		next = &store.Resource{Meta: store.Meta{ID: req.ID, Kind: info.Name, Scope: req.Scope, Server: req.Server, Labels: req.Labels}, Spec: req.Spec}
		if next.ID == "" {
			next.ID = store.NewID(info.Prefix)
		}
		if len(next.Spec) == 0 {
			next.Spec = json.RawMessage("{}")
		}
		if err := kind.Default(ctx, next); err != nil {
			return nil, err
		}
	case OpUpdate, OpDelete, OpAction:
		r, err := e.opts.Store.Get(ctx, req.ID)
		if err != nil {
			return nil, err
		}
		if r.Kind != info.Name || r.Deleted() {
			return nil, store.ErrNotFound.New("what", string(req.ID))
		}
		old, next = r, r.Clone()
		switch req.Op {
		case OpUpdate:
			if req.Spec != nil {
				next.Spec = req.Spec
			}
			if req.Labels != nil {
				next.Labels = req.Labels
			}
		case OpDelete:
			risk = info.Risks.Delete
			next.DeletedAt = &now
		case OpAction:
			a, ok := info.Action(req.Action)
			if !ok {
				return nil, ErrUnknownAction.New("kind", info.Title, "action", req.Action)
			}
			if err := checkInput(a, req.Input); err != nil {
				return nil, err
			}
			action, risk = a, a.Risk
		}
	default:
		return nil, i18n.ErrBadRequest.New("reason", fmt.Sprintf("unknown operation %q", req.Op))
	}
	if !risk.Valid() {
		risk = store.RiskMedium
	}

	operation := e.operation(req)
	target := auth.Target{Scope: next.Scope, Object: next.ID}
	if req.Op == OpCreate {
		target.Object = ""
	}
	perm := auth.KindPermission(info.Name, operation)
	if e.opts.Authorizer != nil {
		if err := e.opts.Authorizer.Authorize(ctx, req.Actor, perm, target); err != nil {
			e.audit(ctx, req.Actor, string(perm), next, "", events.OutcomeDenied, err.Error(), "")
			return nil, err
		}
	}
	if req.Op == OpCreate || req.Op == OpUpdate {
		if err := kind.Validate(ctx, next, old); err != nil {
			return nil, err
		}
	}

	change := ResourceChange{
		Op: req.Op, Action: req.Action, Kind: info.Name, ID: next.ID, Scope: next.Scope, Revision: req.Revision,
		Labels: next.Labels, After: next.Spec, Input: req.Input, Source: RequestSource,
	}
	if old != nil {
		change.Before = old.Spec
	}
	var effects []system.Effect
	rec, hasReconciler := e.reconciler(info.Name)
	if req.Op == OpAction {
		if planner, ok := rec.(reconcile.ActionPlanner); hasReconciler && ok {
			facts := e.facts(ctx, old.Server)
			actionPlan, err := planner.PlanAction(ctx, reconcile.ActionRequest{Request: reconcile.Request{Resource: old, Read: e.opts.Store, Facts: facts}, Action: req.Action, Input: req.Input})
			if err != nil {
				return nil, err
			}
			if actionPlan.Spec != nil {
				next.Spec = actionPlan.Spec
				change.After = actionPlan.Spec
				if err := kind.Validate(ctx, next, old); err != nil {
					return nil, err
				}
			}
			change.Steps = actionPlan.Plan.Steps
			effects = append(effects, actionPlan.Plan.Effects...)
		}
	}
	if req.Op != OpDelete {
		diff, err := Diff(change.Before, change.After)
		if err != nil {
			return nil, err
		}
		change.Diff = Redact(diff, sensitiveFields(info))
	}

	title := req.Title
	if title == "" {
		verb := map[Op]string{OpCreate: "Create", OpUpdate: "Update", OpDelete: "Delete"}[req.Op]
		if req.Op == OpAction {
			verb = action.Title
			if verb == "" {
				verb = strings.ToUpper(action.Name[:1]) + strings.ReplaceAll(action.Name[1:], "_", " ")
			}
		}
		title = verb + " " + strings.ToLower(info.Title)
	}
	cs := &ChangeSet{
		ID: store.NewID(store.PrefixChangeSet), Title: title, Actor: req.Actor, Changes: []ResourceChange{change},
		Risk: risk, State: StatePlanned, CreatedAt: now,
	}
	if e.opts.Hooks != nil {
		if err := e.opts.Hooks.Validate(ctx, HookPoint(info.Name, operation, StageValidate), cs, &cs.Changes[0]); err != nil {
			return nil, err
		}
	}
	if hasReconciler && (req.Op != OpAction || !bytes.Equal(change.Before, change.After)) {
		facts := e.facts(ctx, next.Server)
		plan, err := rec.Plan(ctx, reconcile.Request{Resource: next, Read: e.opts.Store, Facts: facts})
		if err != nil {
			return nil, err
		}
		effects = append(effects, plan.Effects...)
		effects = append(effects, e.stepEffects(ctx, plan.Steps, next.ID)...)
	}
	effects = append(effects, e.stepEffects(ctx, change.Steps, next.ID)...)
	cs.Effects = effects
	if cs.Effects == nil {
		cs.Effects = []system.Effect{}
	}
	if e.opts.Hooks != nil {
		if err := e.opts.Hooks.Modify(ctx, HookPoint(info.Name, operation, StagePlan), cs); err != nil {
			return nil, err
		}
	}
	for _, eff := range cs.Effects {
		if eff.Destructive {
			cs.Snapshot = true
		}
	}
	cs.Summarize()
	return cs, nil
}

func (e *Engine) reconciler(kind string) (reconcile.Reconciler, bool) {
	if e.opts.Runner == nil || e.opts.Runner.Registry == nil {
		return nil, false
	}
	return e.opts.Runner.Registry.For(kind)
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

func sensitiveFields(info *store.KindInfo) []string {
	var out []string
	for path, hint := range info.UI.Fields {
		if hint.Sensitive {
			out = append(out, path)
		}
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

// apply writes the store changes in one transaction and queues the job that
// carries out the system changes.
func (e *Engine) apply(ctx context.Context, cs *ChangeSet) error {
	meta := store.WriteMeta{ChangeSet: cs.ID, Actor: cs.Actor.String()}
	err := e.opts.Store.Write(ctx, meta, func(tx store.Tx) error {
		for _, c := range cs.Changes {
			if err := writeChange(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	cs.State = StateApplying
	job, err := e.opts.Jobs.Enqueue(ctx, ApplyJob, cs.ID, jobs.Options{Title: cs.Title, Actor: cs.Actor, ChangeSet: cs.ID})
	if err != nil {
		return err
	}
	cs.Job = job.ID
	e.save(cs)
	for _, c := range cs.Changes {
		perm := string(auth.KindPermission(c.Kind, string(c.Op)))
		if c.Op == OpAction {
			perm = string(auth.KindPermission(c.Kind, c.Action))
		}
		e.audit(ctx, cs.Actor, perm, &store.Resource{Meta: store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope}}, cs.ID, events.OutcomeSuccess, "", cs.Title)
	}
	return nil
}

func writeChange(ctx context.Context, tx store.Tx, c ResourceChange) error {
	switch c.Op {
	case OpCreate:
		return tx.Create(ctx, &store.Resource{Meta: store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope, Labels: c.Labels}, Spec: c.After})
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
	e.mu.Lock()
	stored := e.sets[cs.ID]
	if failure != nil {
		stored.State, stored.Error = StateFailed, i18n.From(failure).Message
	} else {
		stored.State, stored.AppliedAt = StateApplied, &now
	}
	final := *stored
	e.mu.Unlock()
	if failure == nil && e.opts.Hooks != nil {
		for _, c := range cs.Changes {
			op := string(c.Op)
			if c.Op == OpAction {
				op = c.Action
			}
			e.opts.Hooks.Applied(ctx, HookPoint(c.Kind, op, StageApplied), &final)
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
			return e.opts.Store.Write(ctx, store.WriteMeta{ChangeSet: "", Actor: "system:changeset"}, func(tx store.Tx) error { return tx.Purge(ctx, c.ID) })
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

// Undo implements Pipeline. It reverts the store changes of an applied
// change set with a new change set, which the reconcilers then carry out.
func (e *Engine) Undo(ctx context.Context, actor *auth.Actor, id store.ID) (*ChangeSet, error) {
	orig, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if orig.State != StateApplied {
		return nil, ErrNotApplied.New()
	}
	undo := &ChangeSet{
		ID: store.NewID(store.PrefixChangeSet), Title: "Undo: " + orig.Title, Actor: actor, UndoOf: orig.ID,
		Risk: orig.Risk, State: StatePlanned, CreatedAt: e.opts.Now().UTC(), Effects: []system.Effect{},
	}
	for i := len(orig.Changes) - 1; i >= 0; i-- {
		c := orig.Changes[i]
		cur, getErr := e.opts.Store.Get(ctx, c.ID)
		switch c.Op {
		case OpCreate:
			if getErr != nil || cur.Deleted() {
				continue
			}
			undo.Changes = append(undo.Changes, ResourceChange{Op: OpDelete, Kind: c.Kind, ID: c.ID, Scope: c.Scope, Before: cur.Spec, Source: RequestSource})
		case OpUpdate, OpAction:
			if bytes.Equal(c.Before, c.After) {
				continue
			}
			if getErr != nil {
				return nil, getErr
			}
			diff, _ := Diff(cur.Spec, c.Before)
			undo.Changes = append(undo.Changes, ResourceChange{Op: OpUpdate, Kind: c.Kind, ID: c.ID, Scope: c.Scope, Labels: cur.Labels, Before: cur.Spec, After: c.Before, Diff: diff, Source: RequestSource})
		case OpDelete:
			if getErr == nil {
				return nil, ErrStillRemoving.New("what", string(c.ID))
			}
			undo.Changes = append(undo.Changes, ResourceChange{Op: OpCreate, Kind: c.Kind, ID: c.ID, Scope: c.Scope, Labels: c.Labels, After: c.Before, Source: RequestSource})
		}
	}
	if e.opts.Authorizer != nil {
		for _, c := range undo.Changes {
			if err := e.opts.Authorizer.Authorize(ctx, actor, auth.KindPermission(c.Kind, string(c.Op)), auth.Target{Scope: c.Scope, Object: c.ID}); err != nil {
				return nil, err
			}
		}
	}
	if err := e.apply(ctx, undo); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.sets[orig.ID].State = StateUndone
	e.mu.Unlock()
	return undo, nil
}
