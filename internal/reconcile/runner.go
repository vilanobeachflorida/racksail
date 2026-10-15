package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Runner reconciles resources: it plans, applies through the agent, runs the
// plan's health checks, writes status and purges deleted resources once their
// system state is gone. It reconciles one resource at a time, so an apply job
// and the queue never work on the same resource at once, and it keeps to
// limits on how many resources, in all and of each kind, it works on at
// once.
//
// While Run is going, failed reconciles are retried with a growing wait,
// resources reported as drifted are put back, and every resource is
// reconciled again at each resync.
type Runner struct {
	Store    store.Store
	Agent    agent.Client
	Registry *Registry
	// Facts returns the facts of the machine a resource is placed on.
	Facts func(ctx context.Context, server store.ID) (system.MachineFacts, error)
	// Secrets resolves secret references for reconcilers.
	Secrets secrets.Reader
	// Checker runs plans' health checks; nil means an AgentChecker on Agent.
	Checker Checker
	// Events receives what reconciles find outside change sets: resources
	// that drifted, and resources that failed when the queue retried or
	// resynced them. It may be nil.
	Events events.Bus
	// Workers bounds the reconciles running at once, of every kind; the
	// default is 8.
	Workers int
	// PerKind bounds the reconciles of one kind running at once, unless
	// its reconciler sets its own limit (Limiter); the default is 2.
	PerKind int
	// Backoff is the wait before the first retry of a failed reconcile. It
	// doubles with each failure in a row, up to MaxBackoff. The defaults are
	// five seconds and ten minutes.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// MaxRetries is how many failures in a row the queue retries on its
	// own; after that the resource waits for the next change, drift report
	// or resync. The default is 8.
	MaxRetries int
	Log        *slog.Logger
	Now        func() time.Time

	locks  keyLocks
	limits limits
	queue  workQueue
	stats  counters
}

// counters count what the runner did since the panel started.
type counters struct {
	running                     atomic.Int64
	reconciles, failures, drift atomic.Int64
}

// Stats describe the reconcile queue for the panel's metrics.
type Stats struct {
	// Queued resources wait to be reconciled, now or after a wait;
	// Retrying is how many resources failed their last reconciles.
	Queued, Retrying int
	// Running is how many reconciles run now, for the queue and for change
	// sets' jobs.
	Running int
	// Reconciles, Failures and Drift count, since the panel started, the
	// reconciles that ran, those that failed, and the times the machine was
	// found to differ from a resource.
	Reconciles, Failures, Drift int64
}

// Stats describes the queue for the panel's metrics.
func (r *Runner) Stats() Stats {
	queued, retrying := r.queue.counts()
	return Stats{
		Queued: queued, Retrying: retrying, Running: int(r.stats.running.Load()),
		Reconciles: r.stats.reconciles.Load(), Failures: r.stats.failures.Load(), Drift: r.stats.drift.Load(),
	}
}

// Check is the reconcile queue's health check: it fails while the queue
// isn't running, as when nothing started it or it stopped.
func (r *Runner) Check(ctx context.Context) error {
	if !r.queue.isRunning() {
		return i18n.ErrUnavailable.New("what", "The reconcile queue")
	}
	return nil
}

// Progress receives the steps of a reconcile as they run.
type Progress func(resource store.ID, step int, s agent.Step)

// executor runs the steps of one reconcile and remembers what they changed.
type executor struct {
	client   agent.Client
	owner    store.ID
	progress Progress
	changed  []string
}

func (e *executor) Run(ctx context.Context, steps []agent.Step) ([]agent.StepResult, error) {
	results, err := agent.Execute(ctx, e.client, steps, agent.CallOptions{Owner: e.owner}, func(i int, s agent.Step) {
		if e.progress != nil {
			e.progress(e.owner, i, s)
		}
	})
	if err != nil && ctx.Err() != nil {
		// The status says why the steps stopped, not how the call broke off.
		err = stopped(ctx)
	}
	for _, res := range results {
		if res.Result == nil || !res.Result.Changed {
			continue
		}
		if len(res.Result.Effects) == 0 {
			e.changed = append(e.changed, res.Step.Op)
		}
		for _, eff := range res.Result.Effects {
			e.changed = append(e.changed, strings.TrimSpace(eff.Action+" "+eff.Target))
		}
	}
	return results, err
}

// ErrNoReconciler is returned for kinds without a reconciler.
var ErrNoReconciler = errors.New("reconcile: the kind has no reconciler")

// stopped describes why ctx ended, for a status: what ended it when that
// was said, as the job queue says why it stops a job, or that it was
// canceled or ran out of time.
func stopped(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case i18n.CodeOf(cause) != "":
		return cause
	case errors.Is(cause, context.DeadlineExceeded):
		return i18n.ErrTimeout.Wrap(cause, "what", "The reconcile")
	}
	return i18n.ErrCanceled.Wrap(cause)
}

// Reconcile brings one resource in line with its desired state and verifies
// the result with the plan's health checks.
func (r *Runner) Reconcile(ctx context.Context, id store.ID, progress Progress) error {
	return r.reconcile(ctx, id, progress, false)
}

// reconcile does the work of Reconcile. background says the queue started
// it, rather than a change set's apply job, which reports failures itself.
func (r *Runner) reconcile(ctx context.Context, id store.ID, progress Progress, background bool) error {
	unlock := r.locks.lock(id)
	defer unlock()
	res, err := r.Store.Get(ctx, id)
	if err != nil {
		if store.ErrNotFound.Is(err) {
			r.queue.forget(id)
		}
		return err
	}
	rec, ok := r.Registry.For(res.Kind)
	if !ok {
		if res.Deleted() {
			return r.purge(ctx, id)
		}
		return fmt.Errorf("%s: %w", res.Kind, ErrNoReconciler)
	}
	release, err := r.limits.acquire(ctx, res.Kind, r.workers(), r.perKind(rec))
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.stats.running.Add(1)
	err = r.reconcileResource(ctx, res, rec, progress)
	r.stats.running.Add(-1)
	r.stats.reconciles.Add(1)
	if err != nil {
		r.stats.failures.Add(1)
	}
	r.settle(ctx, res, err, background)
	return err
}

func (r *Runner) reconcileResource(ctx context.Context, res *store.Resource, rec Reconciler, progress Progress) error {
	st, err := res.CommonStatus()
	if err != nil {
		st = store.Status{}
	}
	pending := res.Deleted() || st.ObservedGeneration < res.Generation
	// A resource whose generation was applied before and is still ready
	// should need nothing; whatever its plan changes now had drifted.
	inSync := !pending && st.Phase == store.PhaseReady
	wasDrifted := st.IsTrue(ConditionDrifted)
	// What a reconcile found is recorded even when its context ends, as
	// when its job is canceled, so the status never claims work that
	// stopped is still going on.
	record := context.WithoutCancel(ctx)
	if pending {
		r.markPending(record, res, st)
	}
	facts, err := r.facts(ctx, res.Server)
	if err != nil {
		return r.fail(ctx, res, fmt.Errorf("reading the facts of the machine: %w", err), "ReconcileFailed")
	}
	req := Request{Resource: res, Read: r.Store, Facts: facts, Secrets: r.Secrets}
	plan, err := rec.Plan(ctx, req)
	if err != nil {
		return r.fail(ctx, res, fmt.Errorf("planning %s: %w", res.ID, err), "ReconcileFailed")
	}
	ex := &executor{client: r.Agent, owner: res.ID, progress: progress}
	status, applyErr := rec.Apply(ctx, req, plan, ex)
	drifted := inSync && applyErr == nil && len(ex.changed) > 0
	if status != nil {
		raw, err := json.Marshal(status)
		if err != nil {
			return err
		}
		if applyErr == nil && (drifted || wasDrifted) {
			raw, err = WithCommonStatus(raw, func(s *store.Status) {
				s.Conditions = removeCondition(s.Conditions, ConditionDrifted)
				s.SetCondition(store.Condition{
					Type: ConditionDrifted, Status: store.ConditionFalse, Reason: "Corrected",
					Message: describeChanges(ex.changed), ObservedGeneration: res.Generation,
				}, r.now())
			})
			if err != nil {
				return err
			}
		}
		if err := r.writeStatus(record, res.ID, raw); err != nil {
			return err
		}
	}
	if applyErr != nil {
		if status == nil {
			return r.fail(ctx, res, applyErr, "ReconcileFailed")
		}
		return applyErr
	}
	if drifted {
		r.stats.drift.Add(1)
		r.log().Warn("the machine had drifted from a resource; its settings were put back", "resource", res.ID, "kind", res.Kind, "changes", ex.changed)
		r.publish(ctx, res, events.Event{
			Type: res.Kind + ".drifted", Severity: events.SeverityWarning,
			Message: "The server no longer matched these settings; Racksail put them back.",
		}, events.Drift{Source: "resync", Corrected: true, Changes: ex.changed})
	}
	if res.Deleted() {
		return r.purge(ctx, res.ID)
	}
	if err := r.Verify(ctx, res.Server, plan.Checks); err != nil {
		// The status Apply wrote says Ready; the failed check overrides it.
		current, getErr := r.Store.Get(ctx, res.ID)
		if getErr != nil {
			return errors.Join(err, getErr)
		}
		return r.fail(ctx, current, err, "VerifyFailed")
	}
	return nil
}

// describeChanges summarizes what a reconcile changed, for a condition's
// message.
func describeChanges(changes []string) string {
	switch n := len(changes); {
	case n == 0:
		return ""
	case n > 5:
		return "Put back: " + strings.Join(changes[:5], ", ") + fmt.Sprintf(" and %d more.", n-5)
	}
	return "Put back: " + strings.Join(changes, ", ") + "."
}

func removeCondition(conditions []store.Condition, typ string) []store.Condition {
	out := conditions[:0:0]
	for _, c := range conditions {
		if c.Type != typ {
			out = append(out, c)
		}
	}
	return out
}

// markPending says in the status that the resource's latest generation, or
// its removal, is being carried out, so lists show it while a job works.
func (r *Runner) markPending(ctx context.Context, res *store.Resource, st store.Status) {
	phase := store.PhaseApplying
	if res.Deleted() {
		phase = store.PhaseDeleting
	}
	if st.Phase == phase {
		return
	}
	raw, err := WithCommonStatus(res.Status, func(s *store.Status) { s.Phase = phase })
	if err == nil {
		err = r.writeStatus(ctx, res.ID, raw)
	}
	if err != nil {
		r.log().Warn("marking a resource as being applied", "resource", res.ID, "error", err)
	}
}

// WithCommonStatus changes the fields every kind's status shares and keeps
// the kind's own fields as they are.
func WithCommonStatus(raw json.RawMessage, fn func(s *store.Status)) (json.RawMessage, error) {
	merged := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &merged); err != nil || merged == nil {
			merged = map[string]any{}
		}
	}
	var st store.Status
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			st = store.Status{}
		}
	}
	fn(&st)
	shared, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	var sharedMap map[string]any
	if err := json.Unmarshal(shared, &sharedMap); err != nil {
		return nil, err
	}
	for _, key := range []string{"phase", "message", "code", "observedGeneration", "conditions"} {
		if v, ok := sharedMap[key]; ok {
			merged[key] = v
		} else {
			delete(merged, key)
		}
	}
	return json.Marshal(merged)
}

func (r *Runner) writeStatus(ctx context.Context, id store.ID, raw json.RawMessage) error {
	return r.Store.Write(ctx, store.WriteMeta{Actor: "system:reconciler"}, func(tx store.Tx) error {
		return tx.UpdateStatus(ctx, id, raw)
	})
}

// Verify runs health checks in order and returns the first that doesn't
// pass. It is the verify stage of the change pipeline: Reconcile runs a
// plan's checks once the plan is applied, and the pipeline runs an action's
// checks after its steps.
func (r *Runner) Verify(ctx context.Context, server store.ID, checks []HealthCheck) error {
	checker := r.Checker
	if checker == nil {
		checker = AgentChecker{Agent: r.Agent}
	}
	for _, c := range checks {
		if err := checker.Check(ctx, server, c); err != nil {
			return err
		}
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

func (r *Runner) workers() int {
	if r.Workers > 0 {
		return r.Workers
	}
	return 8
}

func (r *Runner) perKind(rec Reconciler) int {
	if l, ok := rec.(Limiter); ok && l.Concurrency() > 0 {
		return l.Concurrency()
	}
	if r.PerKind > 0 {
		return r.PerKind
	}
	return 2
}

// fail records a failure the reconciler couldn't express in its own status,
// with reason as the Ready condition's reason. A reconcile whose context
// ended failed because of that, which is what the status says.
func (r *Runner) fail(ctx context.Context, res *store.Resource, err error, reason string) error {
	if ctx.Err() != nil {
		err, reason = stopped(ctx), "Stopped"
	}
	e := i18n.From(err)
	raw, mergeErr := WithCommonStatus(res.Status, func(st *store.Status) {
		st.Phase = store.PhaseFailed
		st.Message, st.Code = e.Message, e.Code
		st.SetCondition(store.Condition{Type: ConditionReady, Status: store.ConditionFalse, Reason: reason, Message: e.Message, Code: e.Code, ObservedGeneration: res.Generation}, r.now())
	})
	if mergeErr != nil {
		return errors.Join(err, mergeErr)
	}
	if setErr := r.writeStatus(context.WithoutCancel(ctx), res.ID, raw); setErr != nil {
		return errors.Join(err, setErr)
	}
	level := slog.LevelWarn
	if ctx.Err() != nil {
		level = slog.LevelInfo
	}
	r.log().Log(ctx, level, "reconcile failed", "resource", res.ID, "kind", res.Kind, "reason", reason, "error", err)
	return err
}

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

func (r *Runner) purge(ctx context.Context, id store.ID) error {
	err := r.Store.Write(ctx, store.WriteMeta{Actor: "system:reconciler"}, func(tx store.Tx) error {
		return tx.Purge(ctx, id)
	})
	if err == nil {
		r.queue.forget(id)
	}
	return err
}

// publish sends an event about a resource, from the reconciler.
func (r *Runner) publish(ctx context.Context, res *store.Resource, e events.Event, data any) {
	if r.Events == nil {
		return
	}
	e.Subject, e.Kind, e.Scope, e.Actor = res.ID, res.Kind, res.Scope, auth.System("reconciler")
	if data != nil {
		raw, err := json.Marshal(data)
		if err == nil {
			e.Data = raw
		}
	}
	if err := r.Events.Publish(ctx, e); err != nil {
		r.log().Warn("publishing an event", "type", e.Type, "error", err)
	}
}

// ReconcileAll reconciles every live and deleted resource of the kinds that
// have reconcilers, one after the other. It keeps going after failures and
// returns them all.
func (r *Runner) ReconcileAll(ctx context.Context) error {
	var errs []error
	err := r.eachResource(ctx, store.Query{IncludeDeleted: true}, func(id store.ID) {
		if err := r.Reconcile(ctx, id, nil); err != nil {
			errs = append(errs, err)
		}
	})
	if err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// eachResource calls fn for every resource of the kinds with reconcilers
// that q selects.
func (r *Runner) eachResource(ctx context.Context, q store.Query, fn func(id store.ID)) error {
	var errs []error
	for _, kind := range r.Registry.Kinds() {
		q.Kind, q.Cursor, q.Limit = kind, "", store.MaxLimit
		for {
			page, err := r.Store.List(ctx, q)
			if err != nil {
				errs = append(errs, err)
				break
			}
			for _, res := range page.Items {
				fn(res.ID)
			}
			if page.Next == "" {
				break
			}
			q.Cursor = page.Next
		}
	}
	return errors.Join(errs...)
}

// keyLocks serializes work on one resource, such as an apply job and the
// queue reconciling it at the same moment.
type keyLocks struct {
	mu    sync.Mutex
	locks map[store.ID]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyLocks) lock(id store.ID) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[store.ID]*keyLock{}
	}
	l := k.locks[id]
	if l == nil {
		l = &keyLock{}
		k.locks[id] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		defer k.mu.Unlock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, id)
		}
	}
}

// limits are the semaphores that bound reconciles running at once.
type limits struct {
	mu     sync.Mutex
	global chan struct{}
	kinds  map[string]chan struct{}
}

// acquire waits for room to reconcile a resource of kind, and returns what
// gives the room back.
func (l *limits) acquire(ctx context.Context, kind string, global, perKind int) (func(), error) {
	l.mu.Lock()
	if l.global == nil {
		l.global = make(chan struct{}, global)
		l.kinds = map[string]chan struct{}{}
	}
	k := l.kinds[kind]
	if k == nil {
		k = make(chan struct{}, perKind)
		l.kinds[kind] = k
	}
	g := l.global
	l.mu.Unlock()
	select {
	case g <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case k <- struct{}{}:
	case <-ctx.Done():
		<-g
		return nil, ctx.Err()
	}
	return func() { <-k; <-g }, nil
}
