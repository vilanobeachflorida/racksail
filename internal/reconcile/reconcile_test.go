package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

type noteSpec struct {
	Text string `json:"text"`
}

type noteStatus struct {
	store.Status
	Path string `json:"path,omitempty"`
}

var noteKind = store.Define[noteSpec, noteStatus](store.KindInfo{
	Name: "test.note", Prefix: "note", Scope: store.ScopeServer, Title: "Note", Plural: "notes",
	Description: "A file with some text, for testing reconcilers.",
})

// noteReconciler writes a note to a file. Its plans carry checks, when a
// test sets them.
type noteReconciler struct {
	checks []HealthCheck
}

func notePath(id store.ID) string { return "/srv/racksail/notes/" + string(id) + ".txt" }

func (n noteReconciler) Plan(ctx context.Context, obj *store.Object[noteSpec, noteStatus], req Request) (Plan, error) {
	if obj.Deleted() {
		return Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: notePath(obj.ID)})}}, nil
	}
	return Plan{Steps: []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: "/srv/racksail/notes", Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: notePath(obj.ID), Content: []byte(obj.Spec.Text), Mode: 0o644}),
	}, Checks: n.checks}, nil
}

func (noteReconciler) Apply(ctx context.Context, obj *store.Object[noteSpec, noteStatus], plan Plan, ex Executor) (noteStatus, error) {
	st := obj.Status
	err := ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	if err == nil {
		st.Path = notePath(obj.ID)
	}
	return st, err
}

func setup(t *testing.T, checks ...HealthCheck) (*Runner, *sim.Machine, *store.Memory) {
	t.Helper()
	reg := store.NewKindRegistry()
	if err := reg.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(reg)
	m := sim.New(sim.Options{})
	recs := NewRegistry()
	if err := recs.Register(Typed(noteKind, noteReconciler{checks: checks})); err != nil {
		t.Fatal(err)
	}
	if err := recs.Register(Typed(noteKind, noteReconciler{})); err == nil {
		t.Error("a second reconciler for a kind must be refused")
	}
	return &Runner{Store: s, Agent: agent.NewLocal(m.Backend(), nil), Registry: recs}, m, s
}

func TestReconcileLifecycle(t *testing.T) {
	ctx := context.Background()
	r, m, s := setup(t)
	obj, _ := noteKind.Encode(noteKind.New(store.ServerScope(), noteSpec{Text: "hello"}))
	if err := s.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, obj) }); err != nil {
		t.Fatal(err)
	}
	var steps []string
	if err := r.Reconcile(ctx, obj.ID, func(_ store.ID, _ int, s agent.Step) { steps = append(steps, s.Op) }); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[1] != "WriteFile" {
		t.Errorf("steps = %v", steps)
	}
	got, _ := noteKind.Get(ctx, s, obj.ID)
	if got.Status.Phase != store.PhaseReady || got.Status.ObservedGeneration != 1 || !got.Status.IsTrue("Ready") || got.Status.Path == "" {
		t.Errorf("status = %+v", got.Status)
	}
	data, _ := m.Backend().Files.ReadFile(ctx, notePath(obj.ID), 0)
	if string(data) != "hello" {
		t.Errorf("file = %q", data)
	}

	// Reconciling again changes nothing on the machine.
	m.ResetCalls()
	if err := r.Reconcile(ctx, obj.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Calls() {
		if c.Op == "write" {
			t.Errorf("a second reconcile wrote %s", c.Target)
		}
	}

	if err := s.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Delete(ctx, obj.ID, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, obj.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the deleted note should be purged: %v", err)
	}
	if _, err := m.Backend().Files.Stat(ctx, notePath(obj.ID)); !errors.Is(err, system.ErrNotFound) {
		t.Error("the note's file should be gone")
	}
}

func TestReconcileFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	r, m, s := setup(t)
	obj, _ := noteKind.Encode(noteKind.New(store.ServerScope(), noteSpec{Text: "x"}))
	if err := s.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, obj) }); err != nil {
		t.Fatal(err)
	}
	m.FailNext("files.write", notePath(obj.ID), errors.New("disk full"))
	if err := r.Reconcile(ctx, obj.ID, nil); err == nil {
		t.Fatal("the reconcile should fail")
	}
	got, _ := s.Get(ctx, obj.ID)
	var st noteStatus
	if err := json.Unmarshal(got.Status, &st); err != nil {
		t.Fatal(err)
	}
	if st.Phase != store.PhaseFailed || st.IsTrue("Ready") || st.Message == "" {
		t.Errorf("status after failure = %+v", st)
	}
}

type otherSpec struct{}

func TestNoReconciler(t *testing.T) {
	ctx := context.Background()
	reg := store.NewKindRegistry()
	other := store.Define[otherSpec, store.Status](store.KindInfo{Name: "test.other", Prefix: "other", Scope: store.ScopeServer, Title: "Other", Plural: "others", Description: "No reconciler."})
	if err := reg.Register(other); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(reg)
	r := &Runner{Store: s, Registry: NewRegistry()}
	obj, _ := other.Encode(other.New(store.ServerScope(), otherSpec{}))
	if err := s.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, obj) }); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, obj.ID, nil); !errors.Is(err, ErrNoReconciler) {
		t.Errorf("err = %v", err)
	}
}

func createNote(t *testing.T, s *store.Memory, text string) store.ID {
	t.Helper()
	ctx := context.Background()
	obj, _ := noteKind.Encode(noteKind.New(store.ServerScope(), noteSpec{Text: text}))
	if err := s.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, obj) }); err != nil {
		t.Fatal(err)
	}
	return obj.ID
}

func TestPlanChecksVerifyTheResult(t *testing.T) {
	ctx := context.Background()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/", http.StatusMovedPermanently)
		}
	}))
	defer web.Close()
	passing := []HealthCheck{
		{Name: "page", Type: CheckHTTP, Target: web.URL},
		{Name: "redirect", Type: CheckHTTP, Target: web.URL + "/old", Expect: "301"},
		{Name: "port", Type: CheckTCP, Target: web.Listener.Addr().String()},
		{Name: "ssh", Type: CheckUnit, Target: "ssh.service"},
	}
	r, _, s := setup(t, passing...)
	id := createNote(t, s, "checked")
	if err := r.Reconcile(ctx, id, nil); err != nil {
		t.Fatalf("checks that pass: %v", err)
	}
	if got, _ := noteKind.Get(ctx, s, id); got.Status.Phase != store.PhaseReady {
		t.Errorf("status = %+v", got.Status)
	}

	// A port nothing listens on.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	r, _, s = setup(t, HealthCheck{Name: "worker", Type: CheckTCP, Target: closed, Timeout: 50 * time.Millisecond})
	r.Checker = AgentChecker{Agent: r.Agent, Interval: 10 * time.Millisecond}
	r.Log = slog.New(slog.DiscardHandler)
	id = createNote(t, s, "unchecked")
	err := r.Reconcile(ctx, id, nil)
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("a check that never passes fails the reconcile: %v", err)
	}
	got, _ := noteKind.Get(ctx, s, id)
	if got.Status.Phase != store.PhaseFailed || got.Status.IsTrue("Ready") || got.Status.Path == "" {
		t.Errorf("the status says the check failed and keeps the kind's fields: %+v", got.Status)
	}
	if c, ok := got.Status.Condition("Ready"); !ok || c.Reason != "VerifyFailed" || c.Code != ErrCheckFailed.Code {
		t.Errorf("Ready condition = %+v", c)
	}
}

func TestCheckTypes(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{})
	checker := AgentChecker{Agent: agent.NewLocal(m.Backend(), nil), Interval: 5 * time.Millisecond}
	quick := func(c HealthCheck) HealthCheck { c.Timeout = 30 * time.Millisecond; return c }
	tls := httptest.NewUnstartedServer(http.NotFoundHandler())
	// The untrusted check makes the server log failed handshakes.
	tls.Config.ErrorLog = log.New(io.Discard, "", 0)
	tls.StartTLS()
	defer tls.Close()
	for _, tc := range []struct {
		check HealthCheck
		pass  bool
	}{
		{HealthCheck{Name: "running", Type: CheckUnit, Target: "ssh.service"}, true},
		{HealthCheck{Name: "stopped", Type: CheckUnit, Target: "ssh.service", Expect: "inactive"}, false},
		{HealthCheck{Name: "missing page", Type: CheckHTTP, Target: tls.URL}, false},
		{HealthCheck{Name: "untrusted", Type: CheckTLS, Target: tls.Listener.Addr().String()}, false},
		{HealthCheck{Name: "localhost", Type: CheckDNS, Target: "localhost", Expect: "127.0.0.1"}, true},
		{HealthCheck{Name: "nonsense", Type: "ping", Target: "example.test"}, false},
	} {
		err := checker.Check(ctx, "", quick(tc.check))
		if (err == nil) != tc.pass {
			t.Errorf("%s: %v", tc.check.Name, err)
		}
		if err != nil && !errors.Is(err, ErrCheckFailed) {
			t.Errorf("%s: failures carry a code: %v", tc.check.Name, err)
		}
	}
	var coded *i18n.Error
	err := checker.Check(ctx, "", quick(HealthCheck{Name: "stopped", Type: CheckUnit, Target: "ssh.service", Expect: "inactive"}))
	if !errors.As(err, &coded) || coded.Params["reason"] != "ssh.service is active, not inactive" {
		t.Errorf("the reason says what was seen: %v", err)
	}
}

// slowReconciler counts how many reconciles of one resource run at once.
type slowReconciler struct {
	running, most *atomic.Int32
}

func (s slowReconciler) Plan(ctx context.Context, obj *store.Object[noteSpec, noteStatus], req Request) (Plan, error) {
	return Plan{}, nil
}

func (s slowReconciler) Apply(ctx context.Context, obj *store.Object[noteSpec, noteStatus], plan Plan, ex Executor) (noteStatus, error) {
	n := s.running.Add(1)
	defer s.running.Add(-1)
	for {
		most := s.most.Load()
		if n <= most || s.most.CompareAndSwap(most, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	st := obj.Status
	return st, ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

func TestReconcilesOfOneResourceDontOverlap(t *testing.T) {
	ctx := context.Background()
	reg := store.NewKindRegistry()
	if err := reg.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(reg)
	var running, most atomic.Int32
	recs := NewRegistry()
	if err := recs.Register(Typed(noteKind, slowReconciler{running: &running, most: &most})); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Store: s, Agent: agent.NewLocal(sim.New(sim.Options{}).Backend(), nil), Registry: recs}
	id := createNote(t, s, "busy")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.Reconcile(ctx, id, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Errorf("%d reconciles of the same resource ran at once", most.Load())
	}
	if len(r.locks.locks) != 0 {
		t.Errorf("locks are released: %v", r.locks.locks)
	}
}

// run starts the runner's queue for the rest of the test.
func run(t *testing.T, r *Runner, interval time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx, interval)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestResyncPutsBackWhatDrifted(t *testing.T) {
	r, m, s := setup(t)
	bus := events.NewMemory(100)
	r.Events = bus
	drifts := bus.Subscribe(events.Filter{Types: []string{"test.note.drifted"}}, 4)
	defer drifts.Close()
	id := createNote(t, s, "drifted")
	run(t, r, 20*time.Millisecond)
	path := notePath(id)
	ctx := context.Background()
	// The first pass runs at once and writes the note nobody reconciled yet.
	waitFor(t, "wrote the note", func() bool {
		data, err := m.Backend().Files.ReadFile(ctx, path, 0)
		return err == nil && string(data) == "drifted"
	})
	waitFor(t, "marked the note ready", func() bool {
		got, err := noteKind.Get(ctx, s, id)
		return err == nil && got.Status.Phase == store.PhaseReady
	})
	select {
	case e := <-drifts.C:
		t.Fatalf("writing a note for the first time isn't drift: %+v", e)
	default:
	}
	// Later passes put back what changed on the machine, and say so.
	if err := m.Backend().Files.Remove(ctx, path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "put the note back", func() bool {
		_, err := m.Backend().Files.Stat(ctx, path)
		return err == nil
	})
	select {
	case e := <-drifts.C:
		var d events.Drift
		if err := json.Unmarshal(e.Data, &d); err != nil || !d.Corrected || d.Source != "resync" || len(d.Changes) == 0 || e.Subject != id || e.Severity != events.SeverityWarning {
			t.Errorf("drift event = %+v, %+v", e, d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no drift event")
	}
	waitFor(t, "recorded the correction", func() bool {
		got, _ := noteKind.Get(ctx, s, id)
		c, ok := got.Status.Condition(ConditionDrifted)
		return ok && c.Status == store.ConditionFalse && c.Reason == "Corrected" && strings.Contains(c.Message, path)
	})
}

func TestReportedDrift(t *testing.T) {
	r, m, s := setup(t)
	bus := events.NewMemory(100)
	r.Events = bus
	drifts := bus.Subscribe(events.Filter{Types: []string{"*.drifted"}}, 4)
	defer drifts.Close()
	ctx := context.Background()
	id := createNote(t, s, "original")
	if err := r.Reconcile(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	// Someone edits the file by hand; the agent notices before the queue
	// runs, and the report stands until the queue puts the file back.
	if err := m.Backend().Files.WriteFile(ctx, notePath(id), []byte("edited"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if err := r.ReportDrift(ctx, DriftReport{Resource: id, Changes: []string{notePath(id) + " was edited"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := noteKind.Get(ctx, s, id)
	if !got.Status.IsTrue(ConditionDrifted) || got.Status.Phase != store.PhaseReady {
		t.Errorf("status after the report = %+v", got.Status)
	}
	if e := <-drifts.C; e.Subject != id || !strings.Contains(e.Message, "was edited") {
		t.Errorf("event = %+v", e)
	}
	run(t, r, time.Hour)
	r.Enqueue(id, "drift")
	waitFor(t, "put the file back", func() bool {
		data, _ := m.Backend().Files.ReadFile(ctx, notePath(id), 0)
		return string(data) == "original"
	})
	waitFor(t, "cleared the condition", func() bool {
		got, _ := noteKind.Get(ctx, s, id)
		c, ok := got.Status.Condition(ConditionDrifted)
		return ok && c.Status == store.ConditionFalse
	})
	if err := r.ReportDrift(ctx, DriftReport{Resource: store.NewID("note")}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a report about a resource that doesn't exist: %v", err)
	}
}

// flakyReconciler fails until failures runs out.
type flakyReconciler struct {
	failures *atomic.Int32
	calls    *atomic.Int32
}

func (f flakyReconciler) Plan(ctx context.Context, obj *store.Object[noteSpec, noteStatus], req Request) (Plan, error) {
	return Plan{}, nil
}

func (f flakyReconciler) Apply(ctx context.Context, obj *store.Object[noteSpec, noteStatus], plan Plan, ex Executor) (noteStatus, error) {
	f.calls.Add(1)
	st := obj.Status
	if f.failures.Add(-1) >= 0 {
		err := i18n.ErrUnavailable.New("what", "The mirror")
		st.Phase, st.Message = store.PhaseFailed, err.Message
		return st, err
	}
	return st, ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

func TestFailedReconcilesAreRetried(t *testing.T) {
	ctx := context.Background()
	reg := store.NewKindRegistry()
	if err := reg.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(reg)
	var failures, calls atomic.Int32
	failures.Store(2)
	recs := NewRegistry()
	if err := recs.Register(Typed(noteKind, flakyReconciler{failures: &failures, calls: &calls})); err != nil {
		t.Fatal(err)
	}
	bus := events.NewMemory(100)
	failed := bus.Subscribe(events.Filter{Types: []string{"test.note.failed"}}, 4)
	defer failed.Close()
	r := &Runner{Store: s, Registry: recs, Events: bus, Backoff: 10 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}
	id := createNote(t, s, "flaky")
	run(t, r, time.Hour)
	waitFor(t, "succeeded after its retries", func() bool {
		got, _ := noteKind.Get(ctx, s, id)
		return got.Status.Phase == store.PhaseReady
	})
	if calls.Load() != 3 || r.Failures(id) != 0 {
		t.Errorf("calls = %d, failures = %d", calls.Load(), r.Failures(id))
	}
	// Only the first failure in a row is news.
	if e := <-failed.C; e.Subject != id || e.Severity != events.SeverityError {
		t.Errorf("failure event = %+v", e)
	}
	select {
	case e := <-failed.C:
		t.Errorf("a second failure event: %+v", e)
	default:
	}

	// After MaxRetries failures in a row the queue stops retrying on its
	// own.
	failures.Store(100)
	calls.Store(0)
	r2 := &Runner{Store: s, Registry: recs, Backoff: time.Millisecond, MaxRetries: 2, Log: slog.New(slog.DiscardHandler)}
	other := createNote(t, s, "hopeless")
	run(t, r2, time.Hour)
	waitFor(t, "gave up", func() bool { return r2.Failures(other) == 3 })
	time.Sleep(50 * time.Millisecond)
	if r2.Failures(other) != 3 {
		t.Errorf("failures = %d, want the first try and two retries", r2.Failures(other))
	}
}

// gateReconciler counts how many reconciles of its kind run at once.
type gateReconciler struct {
	running, most *atomic.Int32
	limit         int
}

func (g gateReconciler) Plan(ctx context.Context, obj *store.Object[noteSpec, noteStatus], req Request) (Plan, error) {
	return Plan{}, nil
}

func (g gateReconciler) Apply(ctx context.Context, obj *store.Object[noteSpec, noteStatus], plan Plan, ex Executor) (noteStatus, error) {
	n := g.running.Add(1)
	defer g.running.Add(-1)
	for {
		most := g.most.Load()
		if n <= most || g.most.CompareAndSwap(most, n) {
			break
		}
	}
	time.Sleep(15 * time.Millisecond)
	st := obj.Status
	return st, ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
}

func (g gateReconciler) Concurrency() int { return g.limit }

func TestConcurrencyLimits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit, global int
		want          int32
	}{
		{"the kind's own limit", 1, 8, 1},
		{"the runner's default per kind", 0, 8, 2},
		{"the limit across kinds", 5, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			reg := store.NewKindRegistry()
			if err := reg.Register(noteKind); err != nil {
				t.Fatal(err)
			}
			s := store.NewMemory(reg)
			var running, most atomic.Int32
			recs := NewRegistry()
			if err := recs.Register(Typed(noteKind, gateReconciler{running: &running, most: &most, limit: tc.limit})); err != nil {
				t.Fatal(err)
			}
			r := &Runner{Store: s, Registry: recs, Workers: tc.global}
			var ids []store.ID
			for i := range 8 {
				ids = append(ids, createNote(t, s, fmt.Sprint(i)))
			}
			var wg sync.WaitGroup
			for _, id := range ids {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := r.Reconcile(ctx, id, nil); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if most.Load() != tc.want {
				t.Errorf("%d ran at once, want %d", most.Load(), tc.want)
			}
		})
	}
}

func TestStatusShowsWorkInProgress(t *testing.T) {
	r, _, s := setup(t)
	ctx := context.Background()
	id := createNote(t, s, "x")
	w := s.Watch(store.WatchFilter{IDs: []store.ID{id}}, 16)
	defer w.Close()
	if err := r.Reconcile(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	var phases []store.Phase
	for len(phases) < 2 {
		select {
		case c := <-w.C:
			phases = append(phases, c.Phase)
		case <-time.After(time.Second):
			t.Fatalf("phases = %v", phases)
		}
	}
	if phases[0] != store.PhaseApplying || phases[1] != store.PhaseReady {
		t.Errorf("phases = %v", phases)
	}
	// Reconciling a resource that is ready changes nothing about it.
	if err := r.Reconcile(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-w.C:
		if c.Phase != store.PhaseReady {
			t.Errorf("a resync of a ready resource reported %v", c.Phase)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

// orderReconciler writes notes like noteReconciler and records the order it
// reconciles them in.
type orderReconciler struct {
	noteReconciler
	mu   *sync.Mutex
	seen *[]store.ID
}

func (o orderReconciler) Apply(ctx context.Context, obj *store.Object[noteSpec, noteStatus], plan Plan, ex Executor) (noteStatus, error) {
	o.mu.Lock()
	*o.seen = append(*o.seen, obj.ID)
	o.mu.Unlock()
	return o.noteReconciler.Apply(ctx, obj, plan, ex)
}

func TestRunCarriesOutUnfinishedChangesFirst(t *testing.T) {
	ctx := context.Background()
	reg := store.NewKindRegistry()
	if err := reg.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(reg)
	m := sim.New(sim.Options{})
	var mu sync.Mutex
	var seen []store.ID
	recs := NewRegistry()
	if err := recs.Register(Typed(noteKind, orderReconciler{mu: &mu, seen: &seen})); err != nil {
		t.Fatal(err)
	}
	// One worker, so the order the queue takes resources in is the order
	// they are reconciled in.
	r := &Runner{Store: s, Agent: agent.NewLocal(m.Backend(), nil), Registry: recs, Workers: 1}
	// The notes that are in place were created first, so going by creation
	// alone would leave the waiting one for last.
	var done []store.ID
	for i := range 3 {
		id := createNote(t, s, fmt.Sprint("done ", i))
		if err := r.Reconcile(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		done = append(done, id)
	}
	waiting := createNote(t, s, "waiting")
	mu.Lock()
	seen = nil
	mu.Unlock()
	run(t, r, time.Hour)
	waitFor(t, "reconciled every note", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 4
	})
	mu.Lock()
	// The resync pass that follows may take the waiting note once more.
	order := slices.Clone(seen[:4])
	mu.Unlock()
	if order[0] != waiting || !slices.Equal(order[1:], done) {
		t.Errorf("reconciled %v, want the waiting note %s first, then %v", order, waiting, done)
	}
	if data, _ := m.Backend().Files.ReadFile(ctx, notePath(waiting), 0); string(data) != "waiting" {
		t.Errorf("the waiting note = %q", data)
	}
}

// A reconcile stopped part way, as when its job is canceled, says so in the
// status rather than leaving the resource looking busy.
func TestStoppedReconcilesSaySo(t *testing.T) {
	r, m, s := setup(t)
	id := createNote(t, s, "slow")
	ctx, cancel := context.WithCancelCause(context.Background())
	m.ResetCalls()
	err := r.Reconcile(ctx, id, func(_ store.ID, step int, _ agent.Step) {
		if step == 1 {
			cancel(i18n.ErrCanceled.New())
		}
	})
	if !errors.Is(err, i18n.ErrCanceled) {
		t.Errorf("a canceled reconcile = %v", err)
	}
	got, _ := noteKind.Get(context.Background(), s, id)
	if got.Status.Phase != store.PhaseFailed || got.Status.Code != i18n.ErrCanceled.Code || got.Status.IsTrue(ConditionReady) {
		t.Errorf("status after it stopped = %+v", got.Status)
	}
	st := r.Stats()
	if st.Reconciles != 1 || st.Failures != 1 || st.Running != 0 {
		t.Errorf("stats = %+v", st)
	}
	// The next reconcile finishes the job.
	if err := r.Reconcile(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := noteKind.Get(context.Background(), s, id); got.Status.Phase != store.PhaseReady {
		t.Errorf("status after another reconcile = %+v", got.Status)
	}
}

func TestQueueHealthAndStats(t *testing.T) {
	r, _, s := setup(t)
	if err := r.Check(context.Background()); err == nil {
		t.Error("a queue that isn't running isn't healthy")
	}
	createNote(t, s, "one")
	run(t, r, time.Hour)
	waitFor(t, "started the queue", func() bool { return r.Check(context.Background()) == nil })
	// The first pass takes the note as unfinished, and the second may take
	// it again.
	waitFor(t, "reconciled the note", func() bool { st := r.Stats(); return st.Reconciles >= 1 && st.Running == 0 && st.Queued == 0 })
	if st := r.Stats(); st.Failures != 0 || st.Retrying != 0 {
		t.Errorf("stats = %+v", st)
	}
}
