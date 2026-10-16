package main

import (
	"context"
	"log/slog"
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

// The agent finds a file changed by hand and the panel puts it back at
// once, recording what happened in the timeline.
func TestDriftIsPutBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(pageKind); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(kinds)
	recs := reconcile.NewRegistry()
	if err := recs.Register(reconcile.Typed(pageKind, pageReconciler{})); err != nil {
		t.Fatal(err)
	}
	m := sim.New(sim.Options{Hostname: "drift"})
	rt := agent.NewRuntime(m.Backend(), agent.RuntimeOptions{DriftInterval: 20 * time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	client := agent.NewLocalRuntime(rt)
	runner := &reconcile.Runner{Store: st, Agent: client, Registry: recs, Log: slog.New(slog.DiscardHandler)}
	bus := events.NewMemory(100)

	page, _ := pageKind.Encode(pageKind.New(store.ServerScope(), pageSpec{Text: "<h1>hello</h1>"}))
	if err := st.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, page) }); err != nil {
		t.Fatal(err)
	}
	if err := runner.Reconcile(ctx, page.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go followAgent(ctx, client, st, runner, bus, slog.New(slog.DiscardHandler))

	b := m.Backend()
	if err := b.Files.WriteFile(ctx, pagePath(page.ID), []byte("defaced"), 0o644, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, _ := b.Files.ReadFile(ctx, pagePath(page.ID), 0)
		if string(data) == "<h1>hello</h1>" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page was never put back: %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	found, _, err := bus.QueryEvents(ctx, events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	drifted := 0
	for _, e := range found {
		if e.Type == eventDriftFound && e.Subject == page.ID && e.Kind == "test.page" {
			drifted++
		}
	}
	if drifted != 1 {
		t.Errorf("drift events = %+v", found)
	}
}
