package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/updates"
	"github.com/vilanobeachflorida/racksail/internal/webui"
)

// core is what the panel runs: the store, the change pipeline and
// everything modules work with.
type core struct {
	log      *slog.Logger
	agent    agent.Client
	modules  *modules.Registry
	manager  *modules.Manager
	services *modules.Services
	health   []api.HealthCheck
	db       *store.DB
	timeline *events.Log
	webhooks *webhook.Dispatcher
}

// newCore wires the panel's services around an agent client and an edge
// router. Resources, change sets, jobs, the timeline, the audit log and
// webhook deliveries are kept in the panel database.
func newCore(ctx context.Context, cfg config.Config, log *slog.Logger, agentClient agent.Client, router edge.Router) (c *core, err error) {
	if err := checkRegistries(); err != nil {
		return nil, err
	}
	db, err := store.OpenDB(ctx, cfg.Paths.Database())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	kinds := store.Kinds()
	st, err := store.NewSQLite(ctx, db, kinds)
	if err != nil {
		return nil, err
	}
	reg := modules.Default()
	// Modules' tables are brought up to date before anything reads them.
	for _, m := range reg.All() {
		if mp, ok := m.(modules.MigrationProvider); ok {
			if err := db.Migrate(ctx, m.Manifest().ID, mp.Migrations()); err != nil {
				return nil, fmt.Errorf("module %s: %w", m.Manifest().ID, err)
			}
		}
	}
	keep := cfg.Retention
	timeline, err := events.NewSQL(ctx, db, events.Options{
		Retention: events.Retention{Events: time.Duration(keep.Events), Audit: time.Duration(keep.Audit)}, Log: log,
	})
	if err != nil {
		return nil, err
	}
	queue, err := jobs.NewSQL(ctx, db, jobs.ManagerOptions{Workers: 4, Retention: jobs.Retention{Finished: time.Duration(keep.Jobs)}, Log: log})
	if err != nil {
		return nil, err
	}
	changeSets, err := changeset.NewSQLStorage(ctx, db)
	if err != nil {
		return nil, err
	}

	hookReg := hooks.NewRegistry()
	for _, k := range kinds.All() {
		hookReg.DeclareKind(k)
	}
	recs := reconcile.NewRegistry()
	for _, m := range reg.All() {
		man := m.Manifest()
		for _, p := range hooks.ModulePoints(man.ID) {
			hookReg.Declare(p, man.Name+" lifecycle.")
		}
		for _, h := range man.Hooks {
			hookReg.Declare(hooks.Point(h.Point), h.Description)
		}
		if rp, ok := m.(modules.ReconcilerProvider); ok {
			for _, r := range rp.Reconcilers() {
				if err := recs.Register(r); err != nil {
					return nil, err
				}
			}
		}
	}

	facts := func(ctx context.Context) (system.MachineFacts, error) {
		return agent.GetFacts.Call(ctx, agentClient, agent.Empty{}, agent.CallOptions{})
	}
	key, err := secrets.NewKey()
	if err != nil {
		return nil, err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return nil, err
	}
	vault := secrets.NewMemory(box)
	dispatcher, err := webhook.NewSQL(ctx, db, webhook.Options{
		Store: st, Log: timeline, Secrets: vault, Version: updates.Build().Version,
		Retention: time.Duration(keep.Deliveries), Logger: log,
	})
	if err != nil {
		return nil, err
	}
	if err := recs.Register(webhook.Reconciler(dispatcher)); err != nil {
		return nil, err
	}
	runner := &reconcile.Runner{
		Store: st, Agent: agentClient, Registry: recs, Secrets: vault, Events: timeline, Log: log,
		Facts: func(ctx context.Context, server store.ID) (system.MachineFacts, error) { return facts(ctx) },
	}
	hookRunner := &hooks.Runner{Registry: hookReg, Events: timeline, Log: log}
	authorizer := auth.NewRoleAuthorizer(nil)
	pipeline, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: queue, Events: timeline, Audit: timeline,
		Hooks: hookRunner, Authorizer: authorizer, Secrets: vault, Storage: changeSets,
		Retention: changeset.Retention{Finished: time.Duration(keep.ChangeSets)}, Log: log,
	})
	if err != nil {
		return nil, err
	}
	svc := &modules.Services{
		Config: cfg, Store: st, Kinds: kinds, Pipeline: pipeline, Jobs: queue, Events: timeline, Audit: timeline,
		Agent: agentClient, Edge: router, Secrets: vault, Hooks: hookRunner, Authorizer: authorizer, Log: log,
	}
	for _, m := range reg.All() {
		if hp, ok := m.(modules.HookProvider); ok {
			for _, h := range hp.Hooks(svc) {
				if err := hookReg.Register(h); err != nil {
					return nil, fmt.Errorf("module %s: %w", m.Manifest().ID, err)
				}
			}
		}
	}
	// Reseller grants cover their customers' accounts once the module that
	// keeps resellers says which accounts those are.
	owners, err := reg.Ownership(svc)
	if err != nil {
		return nil, err
	}
	if owners != nil {
		authorizer.SetOwnership(owners)
	}
	manager := modules.NewManager(reg, modules.ManagerOptions{Facts: facts, Services: svc, Log: log})
	c = &core{log: log, agent: agentClient, modules: reg, manager: manager, services: svc, db: db, timeline: timeline, webhooks: dispatcher}
	c.health = append([]api.HealthCheck{
		{Name: "agent", Check: func(ctx context.Context) error {
			_, err := facts(ctx)
			return err
		}},
		{Name: "database", Check: func(ctx context.Context) error {
			return db.Reader().PingContext(ctx)
		}},
		{Name: "modules", Check: func(ctx context.Context) error {
			failed := manager.Failed()
			ids := slices.Sorted(maps.Keys(failed))
			if len(ids) == 0 {
				return nil
			}
			return failed[ids[0]]
		}},
	}, moduleHealthChecks(reg, manager, svc)...)
	// A module that doesn't start is reported by the health check rather
	// than keeping the panel down, so it can still be turned off.
	if err := manager.Boot(ctx); err != nil {
		log.Warn("some modules didn't start; the health check lists them")
	}
	hookRunner.Notify(ctx, hooks.CoreBoot, auth.System("core"), nil)
	// Jobs start once every part has registered its job types; the ones a
	// previous run left behind are picked up again or marked interrupted.
	queue.Start()
	// The resync puts back what drifts on the machine and carries out
	// changes whose apply job never ran.
	go runner.Run(ctx, reconcile.ResyncInterval)
	go dispatcher.Run(ctx)
	go timeline.Run(ctx, time.Hour)
	if err := timeline.Publish(ctx, events.Event{
		Type: events.PanelStarted.Name, Severity: events.PanelStarted.Severity, Scope: store.ServerScope(),
		Actor: auth.System("core"), Message: "Racksail " + updates.Build().Version + " started.",
	}); err != nil {
		log.Warn("recording that the panel started", "error", err)
	}
	return c, nil
}

// moduleHealthChecks returns the health checks modules declare, named
// "<module id>.<check>". Each runs only while its module is on or active.
func moduleHealthChecks(reg *modules.Registry, manager *modules.Manager, svc *modules.Services) []api.HealthCheck {
	var out []api.HealthCheck
	for _, m := range reg.All() {
		hp, ok := m.(modules.HealthProvider)
		if !ok {
			continue
		}
		id := m.Manifest().ID
		for _, hc := range hp.HealthChecks() {
			out = append(out, api.HealthCheck{
				Name: id + "." + hc.Name,
				Active: func() bool {
					s := manager.State(id)
					return s == modules.StateOn || s == modules.StateActive
				},
				Check: func(ctx context.Context) error { return hc.Check(ctx, svc) },
			})
		}
	}
	return out
}

// stop stops the modules that are running and closes the database.
func (c *core) stop(ctx context.Context) {
	c.manager.Shutdown(ctx)
	if err := c.db.Close(); err != nil {
		c.log.Warn("closing the panel database", "error", err)
	}
}

// apiServer builds the panel API with the embedded UI, reachable only from
// panel.allow_from when it is set. Without an authenticator it refuses what
// needs a signed-in user, unless dev signs every request in as the
// developer.
func (c *core) apiServer(authn auth.Authenticator, dev bool) (*api.Server, error) {
	cfg := c.services.Config
	allow, err := cfg.Panel.AllowedNetworks()
	if err != nil {
		return nil, err
	}
	assets, built := webui.Assets()
	if !built {
		c.log.Info("this binary has no web UI; the panel serves a page explaining how to build it")
	}
	return api.New(api.Options{
		Auth: authn, Dev: dev, Modules: c.modules, Services: c.services, Timeline: c.timeline, Webhooks: c.webhooks,
		Health: c.health, AllowFrom: allow, RateLimit: api.RateLimit{PerActor: cfg.API.RateLimit, PerAddress: cfg.API.AnonymousRateLimit},
		UI: webui.Handler(assets), Log: c.log,
	}), nil
}

// newHTTPServer returns a server with sensible timeouts for the panel.
func newHTTPServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}
