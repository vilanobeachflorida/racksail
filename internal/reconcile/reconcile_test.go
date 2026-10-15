package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
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

type noteReconciler struct{}

func notePath(id store.ID) string { return "/srv/racksail/notes/" + string(id) + ".txt" }

func (noteReconciler) Plan(ctx context.Context, obj *store.Object[noteSpec, noteStatus], req Request) (Plan, error) {
	if obj.Deleted() {
		return Plan{Steps: []agent.Step{agent.RemovePath.Step(agent.RemovePathInput{Path: notePath(obj.ID)})}}, nil
	}
	return Plan{Steps: []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: "/srv/racksail/notes", Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: notePath(obj.ID), Content: []byte(obj.Spec.Text), Mode: 0o644}),
	}}, nil
}

func (noteReconciler) Apply(ctx context.Context, obj *store.Object[noteSpec, noteStatus], plan Plan, ex Executor) (noteStatus, error) {
	st := obj.Status
	err := ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	if err == nil {
		st.Path = notePath(obj.ID)
	}
	return st, err
}

func setup(t *testing.T) (*Runner, *sim.Machine, *store.Memory) {
	t.Helper()
	reg := store.NewKindRegistry()
	if err := reg.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory(reg)
	m := sim.New(sim.Options{})
	recs := NewRegistry()
	if err := recs.Register(Typed(noteKind, noteReconciler{})); err != nil {
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
