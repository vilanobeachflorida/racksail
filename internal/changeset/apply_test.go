package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

// A gate's reconcile waits until the test opens it, or its job stops, so
// tests can stop an apply job half way.
type gateSpec struct {
	Name string `json:"name"`
}

var gateKind = store.Define[gateSpec, pageStatus](store.KindInfo{
	Name: "test.gate", Prefix: "gate", Scope: store.ScopeAccount, Title: "Gate", Plural: "gates",
	Description: "Something whose apply takes as long as a test wants, for stopping jobs.",
})

var gates sync.Map

// gate returns the function that opens the gate called name.
func gate(t *testing.T, name string) func() {
	ch := make(chan struct{})
	gates.Store(name, ch)
	var once sync.Once
	open := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(open)
	return open
}

type gateReconciler struct{}

func (gateReconciler) Plan(ctx context.Context, obj *store.Object[gateSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (gateReconciler) Apply(ctx context.Context, obj *store.Object[gateSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	if ch, ok := gates.Load(obj.Spec.Name); ok && !obj.Deleted() {
		select {
		case <-ch.(chan struct{}):
		case <-ctx.Done():
			st.Phase = store.PhaseFailed
			return st, ctx.Err()
		}
	}
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

// A probe looks for files on the server, each in a plan of its own so a
// file that isn't there doesn't stop the others, as the module manager looks
// for the software a module installed.
type probeSpec struct {
	Paths []string `json:"paths"`
}

type probeStatus struct {
	store.Status
	Found []string `json:"found,omitempty"`
}

var probeKind = store.Define[probeSpec, probeStatus](store.KindInfo{
	Name: "test.probe", Prefix: "probe", Scope: store.ScopeAccount, Title: "Probe", Plural: "probes",
	Description: "Files to look for on the server one at a time, for the steps of a job's log.",
})

type probeReconciler struct{}

func (probeReconciler) Plan(ctx context.Context, obj *store.Object[probeSpec, probeStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (probeReconciler) Apply(ctx context.Context, obj *store.Object[probeSpec, probeStatus], plan reconcile.Plan, ex reconcile.Executor) (probeStatus, error) {
	st := obj.Status
	st.Found = nil
	for _, p := range obj.Spec.Paths {
		if _, err := ex.Run(ctx, []agent.Step{agent.StatPath.Step(agent.PathInput{Path: p})}); err == nil {
			st.Found = append(st.Found, p)
		}
	}
	return st, reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

// steps lists the steps a job's log names, as title and number.
func steps(t *testing.T, q jobs.Queue, id store.ID) []string {
	t.Helper()
	logs, err := q.Logs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range logs {
		if n, ok := l.Attrs["step"]; ok {
			out = append(out, fmt.Sprintf("%s %v", l.Message, n))
		}
	}
	return out
}

// A job's log numbers the steps of a change in the order they run, however
// many plans run them, so two steps that do the same to different things
// never read the same.
func TestJobLogNumbersEveryStep(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	raw, _ := json.Marshal(probeSpec{Paths: []string{pagesDir, pagesDir + "/missing.html"}})
	probe := e.submit(t, false, Change{Op: OpCreate, Kind: "test.probe", Scope: store.AccountScope(e.acct), Spec: raw})
	if probe.State != StateApplied {
		t.Fatalf("probe = %+v", probe)
	}
	if got, want := steps(t, e.jobs, probe.Job), []string{"Describe a file or directory 1", "Describe a file or directory 2"}; !slices.Equal(got, want) {
		t.Errorf("steps of two one-step plans = %q, want %q", got, want)
	}

	// An action's own steps come first, and its reconcile's carry on from them.
	page := e.submit(t, false, e.createPage(pageSpec{Path: "steps.html", Content: "steps"}))
	pub, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpAction, Kind: "test.page", ID: page.Changes[0].ID, Action: "publish", Input: json.RawMessage(`{"message":"hello"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, pub)
	var numbers []string
	for _, s := range steps(t, e.jobs, pub.Job) {
		numbers = append(numbers, s[len(s)-1:])
	}
	if want := []string{"1", "2", "3"}; !slices.Equal(numbers, want) {
		t.Errorf("steps of an action and its reconcile = %q", steps(t, e.jobs, pub.Job))
	}
}

// running waits until a job runs.
func running(t *testing.T, q jobs.Queue, id store.ID) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if j, err := q.Get(context.Background(), id); err == nil && j.State == jobs.StateRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never started", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// types lists the types of the events a subscription has received so far.
func types(sub *events.Subscription) []string {
	var out []string
	for {
		select {
		case e := <-sub.C:
			out = append(out, e.Type)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}

// Canceling a change set's job while it runs ends the change set as failed,
// saying why, and it can be undone like any failed change.
func TestCanceledApplyEndsFailed(t *testing.T) {
	ctx := context.Background()
	e := buildEnv(t, envConfig{db: openDB(t)})
	gate(t, "slow")
	sub := e.bus.Subscribe(events.Filter{Types: []string{"core.changeset.*", "test.gate.*"}}, 16)
	defer sub.Close()
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{e.create("test.gate", gateSpec{Name: "slow"})}})
	if err != nil {
		t.Fatal(err)
	}
	running(t, e.jobs, cs.Job)
	if _, err := e.jobs.Cancel(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final := e.wait(t, cs)
	if final.State != StateFailed || final.Error != i18n.ErrCanceled.New().Message || final.Changes[0].State != ChangeStarted {
		t.Errorf("the canceled change set = %+v", final)
	}
	if job, _ := e.jobs.Get(ctx, cs.Job); job.State != jobs.StateCanceled {
		t.Errorf("job = %+v", job)
	}
	if got := types(sub); len(got) != 2 || got[0] != "test.gate.failed" || got[1] != "core.changeset.failed" {
		t.Errorf("events = %v", got)
	}
	gate, _ := gateKind.Get(ctx, e.store, cs.Changes[0].ID)
	if gate.Status.Phase == store.PhaseApplying {
		t.Errorf("the resource doesn't stay busy: %+v", gate.Status)
	}
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: cs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if done := e.wait(t, undo); done.State != StateApplied {
		t.Errorf("undoing the canceled change = %+v", done)
	}
	if orig, _ := e.engine.Get(ctx, cs.ID); orig.State != StateUndone {
		t.Errorf("the original = %s", orig.State)
	}
}

// A job canceled before it starts never runs its handler; the change set
// learns how it ended all the same, and a retry carries it out.
func TestCanceledBeforeItStarts(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	hold := make(chan struct{})
	defer close(hold)
	if err := e.jobs.Handle(jobs.Type{Name: "test.hold", Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	// Both workers are busy, so the apply job waits.
	for range 2 {
		j, _ := e.jobs.Enqueue(ctx, "test.hold", nil, jobs.Options{})
		running(t, e.jobs, j.ID)
	}
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{e.createPage(pageSpec{Path: "later.html", Content: "later"})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.jobs.Cancel(ctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final := e.wait(t, cs)
	if final.State != StateFailed || final.Error != i18n.ErrCanceled.New().Message || final.Changes[0].State != "" {
		t.Errorf("canceled before it started = %+v", final)
	}
	retry, err := e.jobs.Retry(ctx, cs.Job, jobs.RetryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hold <- struct{}{}
	if done, err := e.jobs.Wait(ctx, retry.ID); err != nil || done.State != jobs.StateSucceeded {
		t.Fatalf("the retry = %+v, %v", done, err)
	}
	if got, _ := e.engine.Get(ctx, cs.ID); got.State != StateApplied || got.Job != retry.ID || got.Changes[0].State != ChangeDone || got.Error != "" {
		t.Errorf("after the retry = %+v", got)
	}
	if got := e.file(pagesDir + "/later.html"); got != "later" {
		t.Errorf("page = %q", got)
	}
}

// A delete whose resource the resync removed and purged before the delete's
// job got to it is done, not failed: the resource is gone, as it asked.
func TestDeletesTheResyncGotToFirst(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	page := e.submit(t, false, e.createPage(pageSpec{Path: "racing.html", Content: "x"})).Changes[0].ID
	label := e.submit(t, false, e.create("test.label", labelSpec{Name: "racing"})).Changes[0].ID
	hold := make(chan struct{})
	defer close(hold)
	if err := e.jobs.Handle(jobs.Type{Name: "test.hold", Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	// Both workers are busy, so the delete's job waits.
	for range 2 {
		j, _ := e.jobs.Enqueue(ctx, "test.hold", nil, jobs.Options{})
		running(t, e.jobs, j.ID)
	}
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{
		{Op: OpDelete, Kind: "test.page", ID: page}, {Op: OpDelete, Kind: "test.label", ID: label},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The resync, or anything else that reconciles them, gets there first.
	runner := e.engine.opts.Runner
	if err := runner.Reconcile(ctx, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := runner.Reconcile(ctx, label, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []store.ID{page, label} {
		if _, err := e.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s should be purged by now: %v", id, err)
		}
	}
	hold <- struct{}{}
	hold <- struct{}{}
	if final := e.wait(t, cs); final.State != StateApplied || final.Changes[0].State != ChangeDone || final.Changes[1].State != ChangeDone {
		t.Errorf("the delete = %+v", final)
	}
	if e.file(pagesDir+"/racing.html") != "" {
		t.Error("the page is gone from the machine")
	}
}

// A retry carries on from the change that failed: what is done stays done,
// what never started runs, actions included, and an action that may have
// run already isn't run again.
func TestRetryingAnApplyCarriesOn(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	existing := e.submit(t, false, e.createPage(pageSpec{Path: "home.html", Content: "home"})).Changes[0].ID
	e.sim.FailNext("files.write", pagesDir+"/broken.html", errors.New("disk full"))
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{
		e.createPage(pageSpec{Path: "first.html", Content: "first"}),
		e.createPage(pageSpec{Path: "broken.html", Content: "broken"}),
		{Op: OpAction, Kind: "test.page", ID: existing, Action: "publish", Input: json.RawMessage(`{"message":"after"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	failed := e.wait(t, cs)
	if failed.State != StateFailed || failed.Changes[0].State != ChangeDone || failed.Changes[1].State != ChangeStarted || failed.Changes[2].State != "" {
		t.Fatalf("failed = %+v", failed)
	}
	if got := e.file(pagesDir + "/.published"); got != "" {
		t.Fatalf("the action after the failure ran: %q", got)
	}
	e.sim.ResetCalls()
	retry, err := e.jobs.Retry(ctx, cs.Job, jobs.RetryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := e.jobs.Wait(ctx, retry.ID); done.State != jobs.StateSucceeded {
		t.Fatalf("the retry = %+v", done)
	}
	final, _ := e.engine.Get(ctx, cs.ID)
	if final.State != StateApplied || final.Job != retry.ID {
		t.Errorf("after the retry = %+v", final)
	}
	if e.file(pagesDir+"/broken.html") != "broken" || e.file(pagesDir+"/.published") != "after" {
		t.Error("the retry carried out what was left")
	}
	for _, c := range e.sim.Calls() {
		if c.Target == pagesDir+"/first.html" {
			t.Errorf("the retry ran a change that was done: %v", c)
		}
	}
	if _, err := e.jobs.Retry(ctx, cs.Job, jobs.RetryOptions{}); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("retrying a change set that was applied: %v", err)
	}

	// The action's step fails; it may have done something, so a retry
	// won't run it again.
	e.sim.FailNext("files.write", pagesDir+"/.published", errors.New("disk full"))
	pub, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{{Op: OpAction, Kind: "test.page", ID: existing, Action: "publish", Input: json.RawMessage(`{"message":"again"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.wait(t, pub); got.State != StateFailed {
		t.Fatalf("publish = %+v", got)
	}
	if _, err := e.jobs.Retry(ctx, pub.Job, jobs.RetryOptions{}); !errors.Is(err, ErrActionNotRepeated) {
		t.Errorf("retrying an action that may have run: %v", err)
	}

	// A failed change set that was undone is done with.
	e.sim.FailNext("files.write", pagesDir+"/gone.html", errors.New("disk full"))
	doomed, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{e.createPage(pageSpec{Path: "gone.html", Content: "x"})}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, doomed)
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: doomed.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, undo)
	if _, err := e.jobs.Retry(ctx, doomed.Job, jobs.RetryOptions{}); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("retrying a change set that was undone: %v", err)
	}
	if got, _ := e.engine.Get(ctx, doomed.ID); got.State != StateUndone {
		t.Errorf("the undone change set = %+v", got)
	}
}

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenDB(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A job the panel stopped under isn't run again on its own: the next start
// marks its change set failed, and a retry finishes it.
func TestApplyCutShortByARestart(t *testing.T) {
	db := openDB(t)
	machine := sim.New(sim.Options{})
	open := gate(t, "restart")
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	first := buildEnv(t, envConfig{db: db, machine: machine, ctx: ctx1})
	cs, err := first.engine.Submit(context.Background(), Request{Actor: first.owner, Changes: []Change{first.create("test.gate", gateSpec{Name: "restart"})}})
	if err != nil {
		t.Fatal(err)
	}
	running(t, first.jobs, cs.Job)
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := first.jobs.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if got, _ := first.engine.Get(context.Background(), cs.ID); got.State != StateApplying {
		t.Errorf("stopping leaves the change set to the next start: %+v", got)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	second := buildEnv(t, envConfig{db: db, machine: machine, ctx: ctx2, vault: first.secrets})
	got, _ := second.engine.Get(ctx2, cs.ID)
	if got.State != StateFailed || got.Error != jobs.ErrInterrupted.New().Message {
		t.Fatalf("after the restart = %+v", got)
	}
	failedEvents, _, _ := second.bus.QueryEvents(ctx2, events.Query{Types: []string{"core.changeset.failed"}, ChangeSet: cs.ID})
	if len(failedEvents) != 1 {
		t.Errorf("failed events = %d", len(failedEvents))
	}
	open()
	retry, err := second.jobs.Retry(ctx2, cs.Job, jobs.RetryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := second.jobs.Wait(ctx2, retry.ID); done.State != jobs.StateSucceeded {
		t.Fatalf("the retry = %+v", done)
	}
	if got, _ := second.engine.Get(ctx2, cs.ID); got.State != StateApplied {
		t.Errorf("after the retry = %+v", got)
	}
}

// cancelingAudit cancels a request's context while the pipeline records it,
// as a client that goes away right after its change was written would.
type cancelingAudit struct {
	events.AuditLog
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (c *cancelingAudit) Record(ctx context.Context, r events.AuditRecord) error {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil && r.Outcome == events.OutcomeSuccess {
		cancel()
	}
	return c.AuditLog.Record(context.WithoutCancel(ctx), r)
}

// An undo marks the original as undone once it is written, even when the
// client that asked has gone away by then, so it can't be undone twice.
func TestUndoIsRecordedWhenTheClientLeaves(t *testing.T) {
	audit := &cancelingAudit{}
	e := buildEnv(t, envConfig{db: openDB(t), audit: func(a events.AuditLog) events.AuditLog {
		audit.AuditLog = a
		return audit
	}})
	cs := e.submit(t, false, e.createPage(pageSpec{Path: "x.html", Content: "x"}))
	ctx, cancel := context.WithCancel(context.Background())
	audit.mu.Lock()
	audit.cancel = cancel
	audit.mu.Unlock()
	undo, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: cs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("the request's context should have ended")
	}
	if orig, _ := e.engine.Get(context.Background(), cs.ID); orig.State != StateUndone {
		t.Errorf("the original = %s", orig.State)
	}
	if _, err := e.engine.Undo(context.Background(), UndoRequest{Actor: e.owner, ChangeSet: cs.ID}); !errors.Is(err, ErrNotApplied) {
		t.Errorf("undoing it again: %v", err)
	}
	e.wait(t, undo)
}

// Applying the same preview several times at once, without idempotency
// keys, applies it once.
func TestApplyingAPreviewOnce(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	ticket := e.submit(t, false, e.create("test.ticket", ticketSpec{Subject: "Printer"})).Changes[0].ID
	commentsMu.Lock()
	before := len(comments)
	commentsMu.Unlock()
	preview := e.submit(t, true, Change{Op: OpAction, Kind: "test.ticket", ID: ticket, Action: "comment", Input: json.RawMessage(`{"text":"once"}`)})
	var applied atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var job store.ID
	var mu sync.Mutex
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cs, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID})
			switch {
			case err == nil:
				applied.Add(1)
				mu.Lock()
				job = cs.Job
				mu.Unlock()
			case !errors.Is(err, ErrApplying) && !errors.Is(err, ErrNotPreview):
				t.Errorf("a second apply: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if applied.Load() != 1 {
		t.Fatalf("applied %d times", applied.Load())
	}
	if _, err := e.jobs.Wait(ctx, job); err != nil {
		t.Fatal(err)
	}
	commentsMu.Lock()
	defer commentsMu.Unlock()
	if len(comments)-before != 1 {
		t.Errorf("the action ran %d times", len(comments)-before)
	}
}

// A purged resource's ID isn't given to a new resource, of this account or
// another, so its history and its secrets stay its own. Undoing its delete
// still brings it back.
func TestPurgedIDsAreNotReused(t *testing.T) {
	ctx := context.Background()
	e := buildEnv(t, envConfig{db: openDB(t)})
	ch := e.createPage(pageSpec{Path: "old.html", Content: "old"})
	ch.Secrets = map[string]string{"secret": "first key"}
	created := e.submit(t, false, ch)
	id := created.Changes[0].ID
	ref := func() string {
		page, _ := pageKind.Get(ctx, e.store, id)
		return page.Spec.Secret
	}()
	deleted := e.submit(t, false, Change{Op: OpDelete, Kind: "test.page", ID: id})
	if _, err := e.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the page should be purged: %v", err)
	}
	other := e.owner.Clone()
	otherAcct := e.account(t)
	other.Grants[0].Scope = store.AccountScope(otherAcct)
	for name, req := range map[string]Request{
		"the same account": {Actor: e.owner, Changes: []Change{{Op: OpCreate, Kind: "test.page", ID: id, Scope: store.AccountScope(e.acct),
			Spec: json.RawMessage(`{"path":"new.html","content":"new","secret":"` + ref + `"}`)}}},
		"another account": {Actor: other, Changes: []Change{{Op: OpCreate, Kind: "test.page", ID: id, Scope: store.AccountScope(otherAcct),
			Spec: json.RawMessage(`{"path":"new.html","content":"new","secret":"` + ref + `"}`)}}},
	} {
		for _, dry := range []bool{true, false} {
			req.DryRun = dry
			if _, err := e.engine.Submit(ctx, req); !errors.Is(err, store.ErrIDUsed) {
				t.Errorf("%s (dry run %v): %v", name, dry, err)
			}
		}
	}
	back, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: deleted.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, back)
	if got := e.file(pagesDir + "/old.html.key"); got != "first key" {
		t.Errorf("the page comes back with its own secret: %q", got)
	}
	history, _ := e.store.History(ctx, id)
	if len(history) != 3 || history[2].ChangeSet != back.ID {
		t.Errorf("history = %+v", history)
	}
}

// failingAudit refuses the records of changes that went through while fail
// is set, as a full disk would.
type failingAudit struct {
	events.AuditLog
	fail atomic.Bool
}

var errAuditDown = errors.New("the audit log can't be written")

func (f *failingAudit) Record(ctx context.Context, r events.AuditRecord) error {
	if f.fail.Load() && r.Outcome == events.OutcomeSuccess {
		return errAuditDown
	}
	return f.AuditLog.Record(ctx, r)
}

// In the panel database, applying a change set writes the settings, the
// change set, its job and its audit records together: when one can't be
// written, none of them is, and a retry with the same key applies it.
func TestApplyIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	audit := &failingAudit{}
	e := buildEnv(t, envConfig{db: openDB(t), audit: func(a events.AuditLog) events.AuditLog {
		audit.AuditLog = a
		return audit
	}})
	watcher := e.store.Watch(store.WatchFilter{Kinds: []string{"test.page"}}, 8)
	defer watcher.Close()
	audit.fail.Store(true)
	req := Request{Actor: e.owner, Changes: []Change{e.createPage(pageSpec{Path: "all.html", Content: "all"})}, IdempotencyKey: "all-or-nothing"}
	if _, err := e.engine.Submit(ctx, req); !errors.Is(err, errAuditDown) {
		t.Fatalf("submitting while the audit log is down: %v", err)
	}
	if page, _ := e.store.List(ctx, store.Query{Kind: "test.page"}); len(page.Items) != 0 {
		t.Errorf("settings were written: %d pages", len(page.Items))
	}
	if sets, _, _ := e.engine.List(ctx, ListQuery{}); len(sets) != 0 {
		t.Errorf("change sets kept: %+v", sets)
	}
	if queued, _, _ := e.jobs.List(ctx, jobs.Filter{Type: ApplyJob}); len(queued) != 0 {
		t.Errorf("jobs queued: %+v", queued)
	}
	select {
	case n := <-watcher.C:
		t.Errorf("watchers heard of %s, which was never written", n.ID)
	default:
	}
	records, _, _ := e.bus.QueryAudit(ctx, events.Query{Actor: e.owner.ID})
	for _, r := range records {
		if r.Outcome == events.OutcomeSuccess {
			t.Errorf("a success was recorded: %+v", r)
		}
	}
	if len(records) != 1 || records[0].Outcome != events.OutcomeFailed || records[0].Action != "test.page.create" {
		t.Errorf("the failed attempt is recorded once: %+v", records)
	}

	audit.fail.Store(false)
	cs, err := e.engine.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if done := e.wait(t, cs); done.State != StateApplied {
		t.Errorf("the retry = %+v", done)
	}
	if again, err := e.engine.Submit(ctx, req); err != nil || again.ID != cs.ID {
		t.Errorf("the key now names the change set: %+v, %v", again, err)
	}
	if got := e.file(pagesDir + "/all.html"); got != "all" {
		t.Errorf("page = %q", got)
	}
}
