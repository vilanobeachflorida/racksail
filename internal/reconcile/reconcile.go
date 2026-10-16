package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Request is everything a plan may depend on.
type Request struct {
	// Resource is the desired state. When it is marked deleted, the plan
	// removes what earlier plans created.
	Resource *store.Resource
	// Read gives read-only access to other resources, such as a site's
	// account or the runtime it uses.
	Read store.Reader
	// Facts describes the machine the resource is placed on.
	Facts system.MachineFacts
	// Secrets resolves the secret references in specs. It may be nil when
	// nothing needs secrets, such as in tests of kinds without any.
	Secrets secrets.Reader
}

// HealthCheck verifies a result after it is applied.
type HealthCheck struct {
	Name string `json:"name" doc:"What the check confirms, such as the site answering"`
	// Type is http, tcp, unit, dns or tls.
	Type string `json:"type" enum:"http,tcp,unit,dns,tls" doc:"How the check works"`
	// Target is a URL, host:port, unit name or DNS name.
	Target string `json:"target" doc:"A URL, host:port, unit name or DNS name"`
	// Expect is the expected status code, unit state, address or, for tls
	// checks, the name the certificate must be valid for. When empty, any
	// status below 400, an active unit, any address and the target's host
	// name pass.
	Expect string `json:"expect,omitempty" doc:"The expected status code, unit state, address or certificate name"`
	// Timeout is how long the check keeps trying; zero means
	// DefaultCheckTimeout.
	Timeout time.Duration `json:"timeout,omitempty" doc:"How long the check keeps trying, in nanoseconds"`
}

// Plan is what a reconciler wants done.
type Plan struct {
	Steps []agent.Step `json:"steps"`
	// Effects lists what the steps don't describe themselves, such as the
	// memory a new service will use or the ports it opens.
	Effects []system.Effect `json:"effects,omitempty"`
	// Checks confirm the result once the steps have run. A check that
	// doesn't pass fails the reconcile, so the change that caused it fails
	// and can be undone.
	Checks []HealthCheck `json:"checks,omitempty"`
}

// Executor runs plan steps for one resource, reporting progress to its job.
type Executor interface {
	Run(ctx context.Context, steps []agent.Step) ([]agent.StepResult, error)
}

// Reconciler keeps resources of one kind in line with their desired state.
type Reconciler interface {
	Kind() string
	// Plan computes the steps for a resource. It must not do I/O beyond
	// reading from the request.
	Plan(ctx context.Context, req Request) (Plan, error)
	// Apply runs the plan and returns the resource's new status as a value
	// whose JSON embeds store.Status.
	Apply(ctx context.Context, req Request, plan Plan, ex Executor) (any, error)
}

// ActionRequest asks a reconciler to plan an action.
type ActionRequest struct {
	Request
	Action string
	Input  json.RawMessage
}

// ActionPlan is what an action does: optionally a new desired spec, which
// goes through the store like any update, and steps to run once.
type ActionPlan struct {
	Spec json.RawMessage
	Plan Plan
}

// ActionPlanner is implemented by reconcilers of kinds with actions.
type ActionPlanner interface {
	PlanAction(ctx context.Context, req ActionRequest) (ActionPlan, error)
}

// ActionApplier is implemented by reconcilers whose actions do work in the
// panel itself rather than on the machine, such as sending a webhook a test
// delivery. ApplyAction runs once the change set is applied, after the
// action's steps, and what it returns is the action's output. Like a plan's
// steps, it must not do anything a preview could set off.
type ActionApplier interface {
	ApplyAction(ctx context.Context, req ActionRequest) (any, error)
}

// Limiter is implemented by reconcilers that need a concurrency limit other
// than the runner's default for their kind, such as one that must reconcile
// a single resource at a time because its program can't run twice at once.
type Limiter interface {
	// Concurrency is how many resources of the kind may be reconciled at
	// once; zero or less means the runner's default.
	Concurrency() int
}

// TypedReconciler is a reconciler written against a kind's own types.
type TypedReconciler[S, T any] interface {
	Plan(ctx context.Context, obj *store.Object[S, T], req Request) (Plan, error)
	Apply(ctx context.Context, obj *store.Object[S, T], plan Plan, ex Executor) (T, error)
}

// Typed adapts a typed reconciler to the Reconciler interface. If r also
// implements ActionPlanner, ActionApplier or Limiter, so does the result.
func Typed[S, T any](kind *store.KindOf[S, T], r TypedReconciler[S, T]) Reconciler {
	t := &typed[S, T]{kind: kind, r: r}
	ap, planner := r.(ActionPlanner)
	aa, applier := r.(ActionApplier)
	switch {
	case planner && applier:
		return &typedFullActions[S, T]{typed: t, ap: ap, aa: aa}
	case planner:
		return &typedActions[S, T]{typed: t, ap: ap}
	case applier:
		return &typedApplier[S, T]{typed: t, aa: aa}
	}
	return t
}

type typed[S, T any] struct {
	kind *store.KindOf[S, T]
	r    TypedReconciler[S, T]
}

func (t *typed[S, T]) Kind() string { return t.kind.Name() }

func (t *typed[S, T]) Plan(ctx context.Context, req Request) (Plan, error) {
	obj, err := t.kind.Decode(req.Resource)
	if err != nil {
		return Plan{}, err
	}
	return t.r.Plan(ctx, obj, req)
}

func (t *typed[S, T]) Apply(ctx context.Context, req Request, plan Plan, ex Executor) (any, error) {
	obj, err := t.kind.Decode(req.Resource)
	if err != nil {
		return nil, err
	}
	return t.r.Apply(ctx, obj, plan, ex)
}

// Concurrency passes on the typed reconciler's limit, if it has one.
func (t *typed[S, T]) Concurrency() int {
	if l, ok := t.r.(Limiter); ok {
		return l.Concurrency()
	}
	return 0
}

type typedActions[S, T any] struct {
	*typed[S, T]
	ap ActionPlanner
}

func (t *typedActions[S, T]) PlanAction(ctx context.Context, req ActionRequest) (ActionPlan, error) {
	return t.ap.PlanAction(ctx, req)
}

type typedApplier[S, T any] struct {
	*typed[S, T]
	aa ActionApplier
}

func (t *typedApplier[S, T]) ApplyAction(ctx context.Context, req ActionRequest) (any, error) {
	return t.aa.ApplyAction(ctx, req)
}

type typedFullActions[S, T any] struct {
	*typed[S, T]
	ap ActionPlanner
	aa ActionApplier
}

func (t *typedFullActions[S, T]) PlanAction(ctx context.Context, req ActionRequest) (ActionPlan, error) {
	return t.ap.PlanAction(ctx, req)
}

func (t *typedFullActions[S, T]) ApplyAction(ctx context.Context, req ActionRequest) (any, error) {
	return t.aa.ApplyAction(ctx, req)
}

// Conditions every kind's status can carry.
const (
	// ConditionReady is true when the last reconcile applied the latest
	// generation and its checks passed.
	ConditionReady = "Ready"
	// ConditionDrifted is true while the machine is known to differ from
	// the resource, and false, with the reason Corrected, once Racksail has
	// put it back; its transition time says when.
	ConditionDrifted = "Drifted"
)

// ApplySteps is the standard Apply: it runs the steps and returns status
// with the phase and a Ready condition set from the outcome. Kinds with more
// status fields call it and fill in the rest.
func ApplySteps(ctx context.Context, generation int64, plan Plan, ex Executor, status *store.Status, now time.Time) error {
	_, err := ex.Run(ctx, plan.Steps)
	status.ObservedGeneration = generation
	if err != nil {
		e := i18n.From(err)
		status.Phase = store.PhaseFailed
		status.Message, status.Code = e.Message, e.Code
		status.SetCondition(store.Condition{Type: ConditionReady, Status: store.ConditionFalse, Reason: "ApplyFailed", Message: e.Message, Code: e.Code, ObservedGeneration: generation}, now)
		return err
	}
	status.Phase = store.PhaseReady
	status.Message, status.Code = "", ""
	status.SetCondition(store.Condition{Type: ConditionReady, Status: store.ConditionTrue, Reason: "Applied", ObservedGeneration: generation}, now)
	return nil
}

// Registry maps kinds to their reconcilers.
type Registry struct {
	mu   sync.RWMutex
	recs map[string]Reconciler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{recs: map[string]Reconciler{}} }

// Register adds a reconciler. A kind has at most one.
func (r *Registry) Register(rec Reconciler) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.recs[rec.Kind()]; ok {
		return fmt.Errorf("reconcile: kind %s has two reconcilers", rec.Kind())
	}
	r.recs[rec.Kind()] = rec
	return nil
}

// For returns the reconciler of a kind.
func (r *Registry) For(kind string) (Reconciler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.recs[kind]
	return rec, ok
}

// Kinds returns the kinds with reconcilers, sorted.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.recs))
	for k := range r.recs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
