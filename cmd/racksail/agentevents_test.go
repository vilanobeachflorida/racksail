package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

type pageSpec struct {
	Text string `json:"text"`
}

type pageStatus struct {
	store.Status
}

var pageKind = store.Define[pageSpec, pageStatus](store.KindInfo{
	Name: "test.page", Prefix: "tpage", Scope: store.ScopeServer, Title: "Page", Plural: "pages",
	Description: "A file with some text, for testing how drift is put back.",
})

type pageReconciler struct{}

func pagePath(id store.ID) string { return "/srv/racksail/pages/" + string(id) + ".html" }

func (pageReconciler) Plan(ctx context.Context, obj *store.Object[pageSpec, pageStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{Steps: []agent.Step{
		agent.EnsureDir.Step(agent.EnsureDirInput{Path: "/srv/racksail/pages", Mode: 0o755}),
		agent.WriteFile.Step(agent.WriteFileInput{Path: pagePath(obj.ID), Content: []byte(obj.Spec.Text), Mode: 0o644}),
	}}, nil
}

func (pageReconciler) Apply(ctx context.Context, obj *store.Object[pageSpec, pageStatus], plan reconcile.Plan, ex reconcile.Executor) (pageStatus, error) {
	st := obj.Status
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now())
	return st, err
}

// background runs fn until the test ends, and waits for it to return.
func background(t *testing.T, fn func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("it didn't stop")
		}
	})
}

// The agent finds a file changed by hand; the panel reports it to the
// reconcile queue at once, which records it and puts the file back.
func TestDriftIsPutBack(t *testing.T) {
	ctx := context.Background()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(pageKind); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(kinds)
	recs := reconcile.NewRegistry()
	if err := recs.Register(reconcile.Typed(pageKind, pageReconciler{})); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	m := sim.New(sim.Options{Hostname: "drift"})
	rt := agent.NewRuntime(m.Backend(), agent.RuntimeOptions{DriftInterval: 20 * time.Millisecond, Log: log})
	client := agent.NewLocalRuntime(rt)
	timeline := events.NewMemory(100)
	runner := &reconcile.Runner{Store: st, Agent: client, Registry: recs, Events: timeline, Log: log}

	page, _ := pageKind.Encode(pageKind.New(store.ServerScope(), pageSpec{Text: "<h1>hello</h1>"}))
	if err := st.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, page) }); err != nil {
		t.Fatal(err)
	}
	if err := runner.Reconcile(ctx, page.ID, nil); err != nil {
		t.Fatal(err)
	}
	background(t, func(ctx context.Context) {
		if err := rt.Start(ctx); err != nil {
			t.Error(err)
		}
		<-ctx.Done()
	})
	background(t, func(ctx context.Context) { runner.Run(ctx, time.Hour) })
	background(t, func(ctx context.Context) { followAgent(ctx, client, runner, timeline, log) })
	// The queue reconciles everything when it starts; only the agent's
	// report can put back what changes after that, the resync being an
	// hour away.
	for deadline := time.Now().Add(10 * time.Second); ; {
		if s := runner.Stats(); s.Reconciles >= 2 && s.Queued == 0 && s.Running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the queue never settled: %+v", runner.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}

	b := m.Backend()
	if err := b.Files.WriteFile(ctx, pagePath(page.ID), []byte("defaced"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, _ := b.Files.ReadFile(ctx, pagePath(page.ID), 0)
		got, _ := pageKind.Get(ctx, st, page.ID)
		c, ok := got.Status.Condition(reconcile.ConditionDrifted)
		if string(data) == "<h1>hello</h1>" && ok && c.Status == store.ConditionFalse && c.Reason == "Corrected" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page was never put back: %q, %+v", data, got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	found, _, err := timeline.QueryEvents(ctx, events.Query{Types: []string{"test.page.drifted"}})
	if err != nil {
		t.Fatal(err)
	}
	reported := 0
	for _, e := range found {
		var d events.Drift
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Source == "agent" && e.Subject == page.ID && !d.Corrected &&
			slices.Equal(d.Changes, []string{pagePath(page.ID) + " was edited"}) {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("drift events = %+v", found)
	}
}

// agentEvents replays a fixed list of events, as an agent's stream would.
type agentEvents []agent.Event

func (a agentEvents) Events(ctx context.Context, fn func(agent.Event)) error {
	for _, e := range a {
		fn(e)
	}
	<-ctx.Done()
	return ctx.Err()
}

// What no resource puts back is recorded as core events, with types the
// catalog lists.
func TestAgentEventsWithoutAResource(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	kinds := store.NewKindRegistry()
	st := store.NewMemory(kinds)
	timeline := events.NewMemory(100)
	runner := &reconcile.Runner{Store: st, Registry: reconcile.NewRegistry(), Events: timeline, Log: log}
	gone := store.NewID("tpage")
	now := time.Now().UTC()
	src := agentEvents{
		{Type: agent.EventDrift, Time: now, Drift: &agent.Drift{Kind: "unit", Target: "racksail-edge.service", Reason: "failed"}},
		{Type: agent.EventDrift, Time: now, Drift: &agent.Drift{Owner: gone, Kind: "file", Target: "/srv/racksail/pages/old.html", Reason: "missing"}},
		{Type: agent.EventNetworkRolledBack, Time: now, Message: "The network change wasn't confirmed in time and was rolled back."},
		{Type: agent.EventNetworkConfirmed, Time: now},
	}
	background(t, func(ctx context.Context) { runner.Run(ctx, time.Hour) })
	background(t, func(ctx context.Context) { followAgent(ctx, src, runner, timeline, log) })

	var found []events.Event
	deadline := time.Now().Add(10 * time.Second)
	for len(found) < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("events = %+v", found)
		}
		time.Sleep(20 * time.Millisecond)
		var err error
		if found, _, err = timeline.QueryEvents(ctx, events.Query{Types: []string{"core.*"}}); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string][]events.Event{}
	for _, e := range found {
		got[e.Type] = append(got[e.Type], e)
	}
	drift := got[driftFound.Name]
	if len(drift) != 2 || len(got[networkRolledBack.Name]) != 1 || len(got[networkConfirmed.Name]) != 1 {
		t.Fatalf("events = %+v", got)
	}
	for _, e := range drift {
		var d agent.Drift
		if err := json.Unmarshal(e.Data, &d); err != nil || e.Severity != events.SeverityWarning || e.Subject != d.Owner {
			t.Errorf("drift event = %+v, %v", e, err)
		}
		if d.Owner == gone && !strings.Contains(e.Message, "was removed outside Racksail") {
			t.Errorf("message = %q", e.Message)
		}
	}
	if m := got[networkRolledBack.Name][0].Message; !strings.Contains(m, "rolled back") {
		t.Errorf("rollback message = %q", m)
	}

	if err := events.Err(); err != nil {
		t.Fatal(err)
	}
	catalog := events.Catalog(nil)
	for _, want := range []string{driftFound.Name, networkRolledBack.Name, networkConfirmed.Name} {
		if !slices.ContainsFunc(catalog, func(t events.Type) bool { return t.Name == want }) {
			t.Errorf("the catalog has no %s", want)
		}
	}
}
