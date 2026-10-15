package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// driftSettle is how long the panel waits for more drift before putting
// back what drifted: a directory edited by hand drifts file by file, and
// each resource is reconciled once for all of it.
const driftSettle = 2 * time.Second

// Event types for what the agent notices on its own.
const (
	eventDriftFound        = "core.drift.found"
	eventNetworkRolledBack = "core.network.rolled_back"
	eventNetworkConfirmed  = "core.network.confirmed"
)

// driftQueue bounds the resources waiting to be put back; beyond it, the
// resync does it.
const driftQueue = 256

// followAgent records in the timeline what the agent reports without being
// asked, and puts back what drifted on the machine as soon as the agent
// finds it, rather than at the next resync, by reconciling the resource it
// was written for.
func followAgent(ctx context.Context, src agent.EventSource, st store.Store, runner *reconcile.Runner, bus events.Bus, log *slog.Logger) {
	owners := make(chan store.ID, driftQueue)
	go agent.FollowEvents(ctx, src, func(e agent.Event) {
		publishAgentEvent(ctx, st, bus, e, log)
		if e.Type == agent.EventDrift && e.Drift != nil && e.Drift.Owner != "" {
			select {
			case owners <- e.Drift.Owner:
			default:
				// The resync puts it back later.
				log.Warn("too much drift at once; the rest waits for the resync", "resource", e.Drift.Owner)
			}
		}
	}, log)
	pending := map[store.ID]bool{}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-owners:
			pending[id] = true
			if settle == nil {
				settle = time.After(driftSettle)
			}
		case <-settle:
			settle = nil
			for id := range pending {
				if err := runner.Reconcile(ctx, id, nil); err != nil && ctx.Err() == nil {
					log.Warn("putting back what drifted", "resource", id, "error", err)
				}
			}
			clear(pending)
		}
	}
}

// publishAgentEvent records one of the agent's events in the timeline.
func publishAgentEvent(ctx context.Context, st store.Store, bus events.Bus, e agent.Event, log *slog.Logger) {
	ev := events.Event{Time: e.Time, Scope: store.ServerScope()}
	switch e.Type {
	case agent.EventDrift:
		if e.Drift == nil {
			return
		}
		d := e.Drift
		ev.Type, ev.Severity, ev.Subject = eventDriftFound, events.SeverityWarning, d.Owner
		if d.Owner != "" {
			if res, err := st.Get(ctx, d.Owner); err == nil {
				ev.Kind, ev.Scope = res.Kind, res.Scope
			}
		}
		ev.Message = driftMessage(*d)
		ev.Data, _ = json.Marshal(d)
	case agent.EventNetworkRolledBack:
		ev.Type, ev.Severity, ev.Message = eventNetworkRolledBack, events.SeverityWarning, e.Message
	case agent.EventNetworkConfirmed:
		ev.Type, ev.Severity, ev.Message = eventNetworkConfirmed, events.SeverityInfo, "The network change was confirmed and kept."
	default:
		return
	}
	if err := bus.Publish(ctx, ev); err != nil {
		log.Warn("recording what the agent reported", "type", ev.Type, "error", err)
	}
}

func driftMessage(d agent.Drift) string {
	fix := ""
	if d.Owner != "" {
		fix = " Racksail puts it back."
	}
	switch d.Reason {
	case "missing":
		return fmt.Sprintf("%s was removed outside Racksail.%s", d.Target, fix)
	case "failed":
		return fmt.Sprintf("%s failed.%s", d.Target, fix)
	}
	return fmt.Sprintf("%s was changed outside Racksail.%s", d.Target, fix)
}
