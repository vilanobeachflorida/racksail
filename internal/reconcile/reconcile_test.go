package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
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

func TestResync(t *testing.T) {
	r, m, s := setup(t)
	id := createNote(t, s, "drifted")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Resync(ctx, 10*time.Millisecond)
	}()
	path := notePath(id)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("the resync never %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// The first pass runs at once and writes the note nobody reconciled yet.
	waitFor("wrote the note", func() bool {
		data, err := m.Backend().Files.ReadFile(context.Background(), path, 0)
		return err == nil && string(data) == "drifted"
	})
	// Later passes put back what changed on the machine.
	if err := m.Backend().Files.Remove(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	waitFor("put the note back", func() bool {
		_, err := m.Backend().Files.Stat(context.Background(), path)
		return err == nil
	})
	cancel()
	<-done
}
