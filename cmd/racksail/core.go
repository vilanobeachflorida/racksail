package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/auth/account"
	"github.com/vilanobeachflorida/racksail/internal/auth/identity"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/hooks/rules"
	"github.com/vilanobeachflorida/racksail/internal/inbox"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/notify"
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
	queue    *jobs.Manager
	runner   *reconcile.Runner
	inbox    *inbox.Service
	// identity signs people in; owners says whose each account is.
	identity *identity.Service
	owners   *account.Ownership
	// cancel ends the background work, which work counts, so stop can wait
	// for it before closing the database.
	cancel context.CancelFunc
	work   sync.WaitGroup
}

// newCore wires the panel's services around an agent client and an edge
// router. Resources, change sets, jobs, the timeline, the audit log and
// webhook deliveries are kept in the panel database.
func newCore(ctx context.Context, cfg config.Config, log *slog.Logger, agentClient agent.Client, router edge.Router) (c *core, err error) {
	if err := checkRegistries(); err != nil {
		return nil, err
	}
	hooksAllowed, err := cfg.Webhooks.AllowedNetworks()
	if err != nil {
		return nil, err
	}
	db, err := store.OpenDB(ctx, cfg.Paths.Database())
	if err != nil {
		return nil, err
	}
	// The background work lives until stop ends it, whether or not the
	// command's context ends first.
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancel()
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
	people, err := identity.NewSQL(ctx, db)
	if err != nil {
		return nil, err
	}

	hookReg := hooks.NewRegistry()
	if err := modules.DeclareHookPoints(hookReg, reg, kinds); err != nil {
		return nil, err
	}
	// A hook the circuit breaker switched off stays off across restarts,
	// until an admin switches it back on.
	breakers, err := hooks.NewSQLBreakers(ctx, db)
	if err != nil {
		return nil, err
	}
	if err := hookReg.UseBreakerStore(ctx, breakers, log); err != nil {
		return nil, err
	}

	facts := func(ctx context.Context) (system.MachineFacts, error) {
		return agent.GetFacts.Call(ctx, agentClient, agent.Empty{}, agent.CallOptions{})
	}
	vault, err := openSecrets(ctx, cfg, log, agentClient)
	if err != nil {
		return nil, err
	}
	recs := reconcile.NewRegistry()
	runner := &reconcile.Runner{
		Store: st, Agent: agentClient, Registry: recs, Secrets: vault, Events: timeline, Log: log,
		Facts: func(ctx context.Context, server store.ID) (system.MachineFacts, error) { return facts(ctx) },
	}
	// The services are filled in as their parts are built; the module
	// manager holds them from the start.
	svc := &modules.Services{
		Config: cfg, Store: st, Kinds: kinds, Jobs: queue, Events: timeline, Audit: timeline,
		Agent: agentClient, Edge: router, Secrets: vault, Log: log,
	}
	manager := modules.NewManager(reg, modules.ManagerOptions{Facts: facts, Services: svc, Enqueue: runner.Enqueue, Log: log})
	svc.Modules = manager
	if err := recs.Register(manager.Reconciler()); err != nil {
		return nil, err
	}
	// The reconcilers of a module that is off leave the server alone.
	for _, m := range reg.All() {
		if rp, ok := m.(modules.ReconcilerProvider); ok {
			for _, r := range rp.Reconcilers() {
				if err := recs.Register(manager.Gate(r)); err != nil {
					return nil, err
				}
			}
		}
	}
	dispatcher, err := webhook.NewSQL(ctx, db, webhook.Options{
		Store: st, Log: timeline, Secrets: vault, Version: updates.Build().Version, AllowNetworks: hooksAllowed,
		Retention: time.Duration(keep.Deliveries), Logger: log,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range []reconcile.Reconciler{
		webhook.Reconciler(dispatcher), account.Reconciler(kinds), identity.PolicyReconciler(), identity.ProviderReconciler(),
	} {
		if err := recs.Register(r); err != nil {
			return nil, err
		}
	}
	hookRunner := &hooks.Runner{Registry: hookReg, Events: timeline, Audit: timeline, Log: log}
	authorizer := auth.NewRoleAuthorizer(nil)
	var owners auth.Ownership
	ruleEngine := rules.NewEngine(rules.Options{
		Registry: hookReg, Facts: rules.StoreFacts{Store: st, Machine: facts},
		Ownership: func() auth.Ownership { return owners },
	})
	for _, r := range ruleEngine.Reconcilers() {
		if err := recs.Register(r); err != nil {
			return nil, err
		}
	}
	// Notifications follow the timeline like webhooks, and reach the same
	// networks they may. People's own reach the users the identity service
	// keeps, once it exists.
	users := notify.Users(func() auth.Directory { return svc.Identity })
	notifier, err := notify.NewSQL(ctx, db, notify.Options{
		Store: st, Log: timeline, Secrets: vault, Registry: hookReg, Directory: users, Authorizer: authorizer, BrandName: "Racksail",
		Link: panelLink(cfg), AllowNetworks: hooksAllowed, Retention: time.Duration(keep.Deliveries), Logger: log,
	})
	if err != nil {
		return nil, err
	}
	svc.Notify = notifier
	for _, r := range notify.Reconcilers(notifier) {
		if err := recs.Register(r); err != nil {
			return nil, err
		}
	}
	catalogs, recipeEngine, err := newRecipes(cfg, st, kinds, reg, manager, agentClient, facts, log)
	if err != nil {
		return nil, err
	}
	svc.Catalogs, svc.Recipes = catalogs, recipeEngine
	for _, r := range append(recipeEngine.Reconcilers(), catalogs.Reconciler()) {
		if err := recs.Register(r); err != nil {
			return nil, err
		}
	}
	// Accounts' add-ons run where the module that keeps accounts says,
	// once it has said.
	var accounts addons.Accounts
	addonRuntime, err := newAddonRuntime(ctx, cfg, db, st, hookReg, agentClient, timeline, runner, catalogs,
		func() addons.Accounts { return accounts }, log)
	if err != nil {
		return nil, err
	}
	svc.Addons = addonRuntime
	for _, r := range addonRuntime.Reconcilers() {
		if err := recs.Register(r); err != nil {
			return nil, err
		}
	}
	// The kinds installed add-ons add are there before anything reads or
	// reconciles their resources.
	addonRuntime.LoadKinds(ctx)
	pipeline, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: queue, Events: timeline, Audit: timeline,
		Hooks: hookRunner, Authorizer: authorizer, Secrets: vault, Storage: changeSets,
		Retention: changeset.Retention{Finished: time.Duration(keep.ChangeSets)}, Log: log,
	})
	if err != nil {
		return nil, err
	}
	svc.Pipeline, svc.Hooks, svc.Authorizer, svc.Rules = pipeline, hookRunner, authorizer, ruleEngine
	// The updater confirms a new release once the panel's own parts work;
	// a module that fails to start is no reason to roll Racksail back.
	var coreHealth []api.HealthCheck
	updater := updates.NewUpdater(updates.Options{
		Store: st, Agent: agentClient, Events: timeline, Hooks: hookRunner, Channel: updates.Channel(cfg.Updates.Channel),
		StateDir: cfg.Paths.State, Logger: log,
		Submit: func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error) {
			return pipeline.Submit(ctx, req)
		},
		Backup: func(ctx context.Context, dest string) error {
			_, err := db.Exec(ctx, "VACUUM INTO ?", dest)
			return err
		},
		Health: func(ctx context.Context) error {
			for _, hc := range coreHealth {
				if err := hc.Check(ctx); err != nil {
					return fmt.Errorf("%s: %w", hc.Name, err)
				}
			}
			return nil
		},
	})
	if err := recs.Register(updater.Reconciler()); err != nil {
		return nil, err
	}
	for _, h := range slices.Concat(manager.Hooks(), addonRuntime.Hooks(), notifier.Hooks(), recipeEngine.Hooks()) {
		if err := hookReg.Register(h); err != nil {
			return nil, err
		}
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
	// Reseller grants cover their customers' accounts: the accounts say
	// whose they are, and a module that keeps resellers may say more.
	resellers, err := reg.Ownership(svc)
	if err != nil {
		return nil, err
	}
	ownership := account.NewOwnership(st, resellers)
	owners = ownership
	authorizer.SetOwnership(ownership)
	ids := identity.New(identity.Options{
		Store: people, Resources: st, Pipeline: pipeline, Refresh: runner.Enqueue, Secrets: vault, Channels: notifier.Env,
		Events: timeline, Audit: timeline, Hooks: hookRunner, Authorizer: authorizer, Hostname: cfg.Panel.Hostname,
		SetupFile: filepath.Join(cfg.Paths.State, setupCodeFile), Log: log,
	})
	// Users are reconciled by the service that signs them in, which only
	// exists once the pipeline that changes them does.
	if err := recs.Register(ids.UserReconciler()); err != nil {
		return nil, err
	}
	svc.Identity = ids.Directory()
	if accounts, err = reg.Accounts(svc); err != nil {
		return nil, err
	}
	// Rules take part in changes from the start; a rule that can't be
	// attached is reported in its status when it is next reconciled.
	if err := ruleEngine.Load(ctx, st); err != nil {
		log.Warn("some rules couldn't be attached", "error", err)
	}
	attention, err := newInbox(ctx, db, svc, timeline, reg, manager, router, log)
	if err != nil {
		return nil, err
	}
	c = &core{
		log: log, agent: agentClient, modules: reg, manager: manager, services: svc, db: db, timeline: timeline, webhooks: dispatcher,
		queue: queue, runner: runner, inbox: attention, identity: ids, owners: ownership, cancel: cancel,
	}
	// These are what a new release must have working to confirm itself.
	coreHealth = []api.HealthCheck{
		{Name: "agent", Check: func(ctx context.Context) error {
			_, err := facts(ctx)
			return err
		}},
		{Name: "database", Check: func(ctx context.Context) error {
			return db.Reader().PingContext(ctx)
		}},
		{Name: "jobs", Check: queue.Check},
		{Name: "reconcile", Check: runner.Check},
	}
	c.health = append(slices.Clone(coreHealth), []api.HealthCheck{
		{Name: "webhooks", Check: dispatcher.Check},
		{Name: "notifications", Check: notifier.Check},
		{Name: "modules", Check: func(ctx context.Context) error {
			failed := manager.Failed()
			ids := slices.Sorted(maps.Keys(failed))
			if len(ids) == 0 {
				return nil
			}
			return failed[ids[0]]
		}},
	}...)
	c.health = append(c.health, moduleHealthChecks(reg, manager, svc)...)
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
	c.work.Go(func() { runner.Run(ctx, reconcile.ResyncInterval) })
	c.work.Go(func() { dispatcher.Run(ctx) })
	c.work.Go(func() { notifier.Run(ctx) })
	c.work.Go(func() { timeline.Run(ctx, time.Hour) })
	// The sign-in policy follows its resource, and expired sessions go.
	c.work.Go(func() { ids.Run(ctx) })
	c.work.Go(func() { attention.Run(ctx, time.Minute) })
	// Modules become active as resources start to use them.
	c.work.Go(func() { manager.Run(ctx) })
	// Add-ons are watched for safe mode and crash loops, and reach the
	// panel through their gateway.
	addonRuntime.Load(ctx)
	c.work.Go(func() { addonRuntime.Run(ctx) })
	c.work.Go(func() { c.serveAddonGateway(ctx) })
	// The update settings exist from the first start; then the updater
	// finishes an update the panel restarted onto and looks for releases.
	if err := updater.Boot(ctx); err != nil {
		log.Warn("setting up updates", "error", err)
	}
	c.work.Go(func() { updater.Run(ctx) })
	// The official catalog is there from the first start; catalogs are
	// fetched daily while the marketplace is on, and what was installed
	// from them is checked every few minutes.
	if err := catalogs.Boot(ctx, pipeline.Submit); err != nil {
		log.Warn("adding the official catalog", "error", err)
	}
	c.work.Go(func() { catalogs.Run(ctx) })
	c.work.Go(func() { recipeEngine.Run(ctx) })
	if err := timeline.Publish(ctx, events.Event{
		Type: events.PanelStarted.Name, Severity: events.PanelStarted.Severity, Scope: store.ServerScope(),
		Actor: auth.System("core"), Message: "Racksail " + updates.Build().Version + " started.",
	}); err != nil {
		log.Warn("recording that the panel started", "error", err)
	}
	// Drift the agent reports is put back as soon as it is found, rather
	// than at the next resync.
	if src, ok := agentClient.(agent.EventSource); ok {
		c.work.Go(func() { followAgent(ctx, src, runner, timeline, log) })
	}
	return c, nil
}

// openSecrets opens the secrets kept in the panel's state. Their key is
// sealed with systemd-creds through the agent, which on a server systemd
// hands the panel at start; see internal/secrets.
func openSecrets(ctx context.Context, cfg config.Config, log *slog.Logger, agentClient agent.Client) (*secrets.FileStore, error) {
	vault, err := secrets.Open(ctx, secrets.Options{
		Dir:            filepath.Join(cfg.Paths.State, "secrets"),
		CredentialsDir: os.Getenv("CREDENTIALS_DIRECTORY"),
		Sealer:         agent.KeySealer{Client: agentClient, Name: secrets.CredentialName},
		KeyFile:        filepath.Join(cfg.Paths.State, "secrets.key"),
		Log:            log,
	})
	if err != nil {
		return nil, err
	}
	info := vault.Info()
	log.Info("secrets ready", "key", info.Source, "protection", info.Protection, "new", info.Created)
	return vault, nil
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

// stopTimeout bounds how long stopping waits for running jobs and the
// background work to wind down.
const stopTimeout = 30 * time.Second

// stop stops the background work and the jobs running here, then the
// modules that are running, and closes the database once nothing writes to
// it any more. Jobs it stops are picked up again when the panel starts.
func (c *core) stop(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	c.cancel()
	if err := c.queue.Stop(ctx); err != nil {
		c.log.Warn("stopping the job queue", "error", err)
	}
	done := make(chan struct{})
	go func() {
		c.work.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		c.log.Warn("the reconcile queue, webhook deliveries, the timeline's pruning or following the agent didn't stop in time")
	}
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
	states := func() map[string]modules.State { return c.manager.States() }
	return api.New(api.Options{
		Auth: authn, Dev: dev, Modules: c.modules, Services: c.services, Timeline: c.timeline, Webhooks: c.webhooks,
		Identity: c.identity, Accounts: c.owners, ModuleStates: states,
		Inbox: c.inbox, Health: c.health, AllowFrom: allow, RateLimit: api.RateLimit{PerActor: cfg.API.RateLimit, PerAddress: cfg.API.AnonymousRateLimit},
		Metrics: c.metrics, UI: webui.Handler(assets), Log: c.log,
	}), nil
}

// metrics reports how the job queue, the reconcile queue and webhook
// deliveries are doing, for GET /api/v1/metrics.
func (c *core) metrics(ctx context.Context) ([]api.Metric, error) {
	q, err := c.queue.Stats(ctx)
	if err != nil {
		return nil, err
	}
	d, err := c.webhooks.Stats(ctx)
	if err != nil {
		return nil, err
	}
	published, err := c.timeline.LastSeq(ctx)
	if err != nil {
		return nil, err
	}
	r := c.runner.Stats()
	gauge := func(name, help string, v float64) api.Metric { return api.Metric{Name: name, Help: help, Value: v} }
	counter := func(name, help string, v int64) api.Metric {
		return api.Metric{Name: name, Help: help, Counter: true, Value: float64(v)}
	}
	return []api.Metric{
		gauge("racksail_jobs_queued", "Jobs waiting for a worker.", float64(q.Queued)),
		gauge("racksail_jobs_waiting", "Jobs waiting for a later time, such as automatic retries.", float64(q.Waiting)),
		gauge("racksail_jobs_running", "Jobs running now.", float64(q.Running)),
		gauge("racksail_jobs_oldest_queued_seconds", "How long the job that has waited longest for a worker has waited.", q.OldestQueued.Seconds()),
		counter("racksail_jobs_started_total", "Jobs started.", q.Started),
		counter("racksail_jobs_succeeded_total", "Jobs that succeeded.", q.Succeeded),
		counter("racksail_jobs_failed_total", "Jobs that failed.", q.Failed),
		counter("racksail_jobs_canceled_total", "Jobs that were canceled.", q.Canceled),
		gauge("racksail_reconcile_queued", "Resources waiting to be reconciled.", float64(r.Queued)),
		gauge("racksail_reconcile_retrying", "Resources whose last reconciles failed.", float64(r.Retrying)),
		gauge("racksail_reconcile_running", "Reconciles running now.", float64(r.Running)),
		counter("racksail_reconciles_total", "Reconciles that ran.", r.Reconciles),
		counter("racksail_reconcile_failures_total", "Reconciles that failed.", r.Failures),
		counter("racksail_drift_total", "Times the server was found to differ from a resource's settings.", r.Drift),
		gauge("racksail_webhook_deliveries_pending", "Webhook deliveries waiting to be sent, or sent again.", float64(d.Pending)),
		gauge("racksail_webhook_events_behind", "Events not yet turned into webhook deliveries.", float64(d.Lag)),
		counter("racksail_webhook_attempts_total", "Attempts to send webhook deliveries.", d.Attempts),
		counter("racksail_webhook_attempts_succeeded_total", "Attempts the receiver accepted.", d.Succeeded),
		counter("racksail_webhook_attempts_failed_total", "Attempts that failed.", d.Failed),
		counter("racksail_webhook_deliveries_failed_total", "Deliveries given up on.", d.GaveUp),
		// Sequence numbers are never reused, so the last one counts every
		// event the timeline took.
		counter("racksail_events_total", "Events published.", published),
	}, nil
}

// newHTTPServer returns a server with sensible timeouts for the panel. When
// it shuts down, the API's live streams and waits end, so shutting down
// doesn't wait for clients that would stay connected.
func newHTTPServer(addr string, apiServer *api.Server, log *slog.Logger) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(apiServer.Close)
	return srv
}
