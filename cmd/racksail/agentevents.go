package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// driftSettle is how long the panel waits for more drift before reporting
// it: a directory edited by hand drifts file by file, and each resource is
// reported, and put back, once for all of it.
const driftSettle = 2 * time.Second

// driftQueue bounds the drift waiting to be reported; beyond it, the resync
// puts things back.
const driftQueue = 256

// driftChanges bounds the changes one report about a resource lists; the
// rest are counted.
const driftChanges = 10

// Event types for what the agent reports that no resource puts back. Drift
// on a resource is published by the reconcile queue, as <kind>.drifted.
var (
	driftFound = events.Type{
		Name: "core.drift.found", Severity: events.SeverityWarning, Data: driftFoundData{},
		Description: "One of Racksail's own files or units was changed or removed outside Racksail, or one of its services failed, and no resource puts it back.",
	}
	networkRolledBack = events.Type{
		Name: "core.network.rolled_back", Severity: events.SeverityWarning,
		Description: "A network change wasn't confirmed in time, so the server went back to the previous configuration.",
	}
	networkConfirmed = events.Type{
		Name: "core.network.confirmed", Severity: events.SeverityInfo,
		Description: "A network change was confirmed in time and kept.",
	}
)

// driftFoundData is the data of core.drift.found: the drift as the agent
// reported it. It has a name of its own, since events.Drift, the data of
// <kind>.drifted, already takes Drift in the catalog's schemas.
type driftFoundData agent.Drift

func init() {
	for _, t := range []events.Type{driftFound, networkRolledBack, networkConfirmed} {
		events.RegisterType(t)
	}
}

// followAgent records in the timeline what the agent reports without being
// asked. Drift on a resource goes to the reconcile queue as soon as the
// agent finds it (reconcile.Runner.ReportDrift), which marks the resource,
// publishes <kind>.drifted and puts it back, rather than waiting for the
// next resync. It returns once ctx is done and nothing it started still
// runs, so the panel can close its database after it.
func followAgent(ctx context.Context, src agent.EventSource, runner *reconcile.Runner, timeline events.Bus, log *slog.Logger) {
	found := make(chan agent.Event, driftQueue)
	var following sync.WaitGroup
	defer following.Wait()
	following.Go(func() {
		agent.FollowEvents(ctx, src, func(e agent.Event) {
			if e.Type != agent.EventDrift || e.Drift == nil || e.Drift.Owner == "" {
				publishAgentEvent(ctx, timeline, e, log)
				return
			}
			select {
			case found <- e:
			default:
				log.Warn("too much drift at once; the rest waits for the resync", "resource", e.Drift.Owner)
			}
		}, log)
	})
	pending := map[store.ID][]agent.Event{}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-found:
			pending[e.Drift.Owner] = append(pending[e.Drift.Owner], e)
			if settle == nil {
				settle = time.After(driftSettle)
			}
		case <-settle:
			settle = nil
			for owner, drift := range pending {
				reportDrift(ctx, runner, timeline, owner, drift, log)
			}
			clear(pending)
		}
	}
}

// reportDrift hands what drifted on one resource to the reconcile queue.
// Drift on a resource that no longer exists is recorded as core.drift.found
// instead, since nothing puts it back.
func reportDrift(ctx context.Context, runner *reconcile.Runner, timeline events.Bus, owner store.ID, drift []agent.Event, log *slog.Logger) {
	changes := make([]string, 0, min(len(drift), driftChanges+1))
	for i, e := range drift {
		if i == driftChanges {
			changes = append(changes, fmt.Sprintf("%d more", len(drift)-i))
			break
		}
		changes = append(changes, driftChange(*e.Drift))
	}
	err := runner.ReportDrift(ctx, reconcile.DriftReport{Resource: owner, Changes: changes})
	switch {
	case err == nil || ctx.Err() != nil:
	case store.ErrNotFound.Is(err):
		for _, e := range drift {
			publishAgentEvent(ctx, timeline, e, log)
		}
	default:
		log.Warn("reporting what drifted", "resource", owner, "error", err)
	}
}

// publishAgentEvent records one of the agent's events in the timeline.
func publishAgentEvent(ctx context.Context, timeline events.Bus, e agent.Event, log *slog.Logger) {
	ev := events.Event{Time: e.Time, Scope: store.ServerScope()}
	switch e.Type {
	case agent.EventDrift:
		if e.Drift == nil {
			return
		}
		ev.Type, ev.Severity, ev.Subject = driftFound.Name, driftFound.Severity, e.Drift.Owner
		ev.Message = driftMessage(*e.Drift)
		ev.Data, _ = json.Marshal(driftFoundData(*e.Drift))
	case agent.EventNetworkRolledBack:
		ev.Type, ev.Severity, ev.Message = networkRolledBack.Name, networkRolledBack.Severity, e.Message
	case agent.EventNetworkConfirmed:
		ev.Type, ev.Severity, ev.Message = networkConfirmed.Name, networkConfirmed.Severity, "The network change was confirmed and kept."
	default:
		return
	}
	if err := timeline.Publish(ctx, ev); err != nil {
		log.Warn("recording what the agent reported", "type", ev.Type, "error", err)
	}
}

// driftChange says what drifted, for the changes a drift report lists.
func driftChange(d agent.Drift) string {
	switch d.Reason {
	case "missing":
		return d.Target + " was removed"
	case "failed":
		return d.Target + " failed"
	}
	return d.Target + " was edited"
}

func driftMessage(d agent.Drift) string {
	switch d.Reason {
	case "missing":
		return fmt.Sprintf("%s was removed outside Racksail.", d.Target)
	case "failed":
		return fmt.Sprintf("%s failed.", d.Target)
	}
	return fmt.Sprintf("%s was changed outside Racksail.", d.Target)
}
