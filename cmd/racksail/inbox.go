package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/inbox"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// certificateLister is an edge router that can also list the certificates
// the edge serves: the link to the edge on a server, and the edge in the
// same process in racksail dev.
type certificateLister interface {
	Certificates(ctx context.Context) ([]edge.Certificate, error)
}

// How long the inbox keeps what its slower sources found before it checks
// again. Updates arrive overnight; certificates are renewed weeks ahead.
const (
	inboxUpdatesKeep      = 30 * time.Minute
	inboxCertificatesKeep = 10 * time.Minute
	inboxDisksKeep        = 10 * time.Minute
)

// newInbox builds the attention inbox from the core's sources and those
// modules add, with dismissals kept in the panel database.
func newInbox(ctx context.Context, db *store.DB, svc *modules.Services, timeline *events.Log, reg *modules.Registry, manager *modules.Manager, router edge.Router, log *slog.Logger) (*inbox.Service, error) {
	dismissals, err := inbox.NewSQL(ctx, db)
	if err != nil {
		return nil, err
	}
	certs := inbox.Certificates{Agent: svc.Agent, Store: svc.Store}
	if l, ok := router.(certificateLister); ok {
		certs.Edge = l.Certificates
	}
	sources := []inbox.Registration{
		{Name: "jobs", Source: inbox.FailedJobs{Queue: svc.Jobs, ChangeSets: svc.Pipeline.Get}},
		{Name: "resources", Source: inbox.Resources{Store: svc.Store, Kinds: svc.Kinds}},
		{Name: "alerts", Source: inbox.Alerts{Events: timeline}},
		{Name: "updates", Source: inbox.Updates{Agent: svc.Agent}, Keep: inboxUpdatesKeep},
		{Name: "certificates", Source: certs, Keep: inboxCertificatesKeep},
		{Name: "suggestions", Source: inbox.Suggestions{
			Modules: moduleStates(reg, manager), Store: svc.Store, Kinds: svc.Kinds,
			UnusedDisks: inbox.UnusedDisks(svc.Agent, inboxDisksKeep),
		}},
	}
	for _, m := range reg.All() {
		p, ok := m.(modules.InboxProvider)
		if !ok {
			continue
		}
		id := m.Manifest().ID
		for _, r := range p.InboxSources(svc) {
			r.Owner = id
			r.Active = func() bool {
				s := manager.State(id)
				return s == modules.StateOn || s == modules.StateActive
			}
			sources = append(sources, r)
		}
	}
	return inbox.New(ctx, inbox.Options{Sources: sources, Dismissals: dismissals, Log: log}), nil
}

// moduleStates lists every module with its state, for the inbox's
// suggestions.
func moduleStates(reg *modules.Registry, manager *modules.Manager) func() []inbox.ModuleState {
	return func() []inbox.ModuleState {
		states := manager.States()
		out := make([]inbox.ModuleState, 0, len(states))
		for _, m := range reg.All() {
			man := m.Manifest()
			out = append(out, inbox.ModuleState{ID: man.ID, Name: man.Name, State: string(states[man.ID])})
		}
		return out
	}
}
