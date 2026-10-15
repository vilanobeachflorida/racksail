package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	Name string `json:"name"`
	// Type is http, tcp, unit, dns or tls.
	Type string `json:"type" enum:"http,tcp,unit,dns,tls"`
	// Target is a URL, host:port, unit name or DNS name.
	Target string `json:"target"`
	// Expect is the expected status code, record value or unit state.
	Expect  string        `json:"expect,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Plan is what a reconciler wants done.
type Plan struct {
	Steps []agent.Step `json:"steps"`
	// Effects lists what the steps don't describe themselves, such as the
	// memory a new service will use or the ports it opens.
	Effects []system.Effect `json:"effects,omitempty"`
	Checks  []HealthCheck   `json:"checks,omitempty"`
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

// TypedReconciler is a reconciler written against a kind's own types.
type TypedReconciler[S, T any] interface {
	Plan(ctx context.Context, obj *store.Object[S, T], req Request) (Plan, error)
	Apply(ctx context.Context, obj *store.Object[S, T], plan Plan, ex Executor) (T, error)
}

// Typed adapts a typed reconciler to the Reconciler interface. If r also
// implements ActionPlanner, so does the result.
func Typed[S, T any](kind *store.KindOf[S, T], r TypedReconciler[S, T]) Reconciler {
	t := &typed[S, T]{kind: kind, r: r}
	if ap, ok := r.(ActionPlanner); ok {
		return &typedActions[S, T]{typed: t, ap: ap}
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

type typedActions[S, T any] struct {
	*typed[S, T]
	ap ActionPlanner
}

func (t *typedActions[S, T]) PlanAction(ctx context.Context, req ActionRequest) (ActionPlan, error) {
	return t.ap.PlanAction(ctx, req)
}

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
		status.SetCondition(store.Condition{Type: "Ready", Status: store.ConditionFalse, Reason: "ApplyFailed", Message: e.Message, Code: e.Code, ObservedGeneration: generation}, now)
		return err
	}
	status.Phase = store.PhaseReady
	status.Message, status.Code = "", ""
	status.SetCondition(store.Condition{Type: "Ready", Status: store.ConditionTrue, Reason: "Applied", ObservedGeneration: generation}, now)
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

// Runner reconciles resources: it plans, applies through the agent, writes
// status and purges deleted resources once their system state is gone.
type Runner struct {
	Store    store.Store
	Agent    agent.Client
	Registry *Registry
	// Facts returns the facts of the machine a resource is placed on.
	Facts func(ctx context.Context, server store.ID) (system.MachineFacts, error)
	// Secrets resolves secret references for reconcilers.
	Secrets secrets.Reader
	Log     *slog.Logger
	Now     func() time.Time
}

// Progress receives the steps of a reconcile as they run.
type Progress func(resource store.ID, step int, s agent.Step)

type executor struct {
	client   agent.Client
	owner    store.ID
	progress Progress
}

func (e executor) Run(ctx context.Context, steps []agent.Step) ([]agent.StepResult, error) {
	return agent.Execute(ctx, e.client, steps, agent.CallOptions{Owner: e.owner}, func(i int, s agent.Step) {
		if e.progress != nil {
			e.progress(e.owner, i, s)
		}
	})
}

// ErrNoReconciler is returned for kinds without a reconciler.
var ErrNoReconciler = errors.New("reconcile: the kind has no reconciler")

// Reconcile brings one resource in line with its desired state.
func (r *Runner) Reconcile(ctx context.Context, id store.ID, progress Progress) error {
	res, err := r.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	rec, ok := r.Registry.For(res.Kind)
	if !ok {
		if res.Deleted() {
			return r.purge(ctx, id)
		}
		return fmt.Errorf("%s: %w", res.Kind, ErrNoReconciler)
	}
	facts, err := r.facts(ctx, res.Server)
	if err != nil {
		return err
	}
	req := Request{Resource: res, Read: r.Store, Facts: facts, Secrets: r.Secrets}
	plan, err := rec.Plan(ctx, req)
	if err != nil {
		return r.fail(ctx, res, fmt.Errorf("planning %s: %w", id, err))
	}
	status, applyErr := rec.Apply(ctx, req, plan, executor{client: r.Agent, owner: id, progress: progress})
	if status != nil {
		if err := store.SetStatus(ctx, r.Store, id, status); err != nil {
			return err
		}
	}
	if applyErr != nil {
		if status == nil {
			return r.fail(ctx, res, applyErr)
		}
		return applyErr
	}
	if res.Deleted() {
		return r.purge(ctx, id)
	}
	return nil
}

func (r *Runner) facts(ctx context.Context, server store.ID) (system.MachineFacts, error) {
	if r.Facts == nil {
		return system.MachineFacts{}, nil
	}
	return r.Facts(ctx, server)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// fail records a failure the reconciler couldn't express in its own status.
func (r *Runner) fail(ctx context.Context, res *store.Resource, err error) error {
	st, decodeErr := res.CommonStatus()
	if decodeErr != nil {
		return errors.Join(err, decodeErr)
	}
	e := i18n.From(err)
	st.Phase = store.PhaseFailed
	st.Message, st.Code = e.Message, e.Code
	st.SetCondition(store.Condition{Type: "Ready", Status: store.ConditionFalse, Reason: "ReconcileFailed", Message: e.Message, Code: e.Code, ObservedGeneration: res.Generation}, r.now())
	// Keep the kind's own status fields and replace the shared ones.
	merged := map[string]any{}
	if len(res.Status) > 0 {
		if err := json.Unmarshal(res.Status, &merged); err != nil {
			merged = map[string]any{}
		}
	}
	shared, _ := json.Marshal(st)
	var sharedMap map[string]any
	if err := json.Unmarshal(shared, &sharedMap); err == nil {
		for k, v := range sharedMap {
			merged[k] = v
		}
	}
	if setErr := store.SetStatus(ctx, r.Store, res.ID, merged); setErr != nil {
		return errors.Join(err, setErr)
	}
	if r.Log != nil {
		r.Log.Warn("reconcile failed", "resource", res.ID, "kind", res.Kind, "error", err)
	}
	return err
}

func (r *Runner) purge(ctx context.Context, id store.ID) error {
	return r.Store.Write(ctx, store.WriteMeta{Actor: "system:reconciler"}, func(tx store.Tx) error {
		return tx.Purge(ctx, id)
	})
}

// ReconcileAll reconciles every live and deleted resource of the kinds that
// have reconcilers, as the periodic resync does. It keeps going after
// failures and returns them all.
func (r *Runner) ReconcileAll(ctx context.Context) error {
	var errs []error
	for _, kind := range r.Registry.Kinds() {
		cursor := ""
		for {
			page, err := r.Store.List(ctx, store.Query{Kind: kind, IncludeDeleted: true, Cursor: cursor, Limit: store.MaxLimit})
			if err != nil {
				errs = append(errs, err)
				break
			}
			for _, res := range page.Items {
				if err := r.Reconcile(ctx, res.ID, nil); err != nil {
					errs = append(errs, err)
				}
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	return errors.Join(errs...)
}
