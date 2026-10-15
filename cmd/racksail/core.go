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
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
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
}

// newCore wires the panel's services around an agent client and an edge
// router.
func newCore(ctx context.Context, cfg config.Config, log *slog.Logger, agentClient agent.Client, router edge.Router) (*core, error) {
	if err := checkRegistries(); err != nil {
		return nil, err
	}
	kinds := store.Kinds()
	st := store.NewMemory(kinds)
	bus := events.NewMemory(10000)
	queue := jobs.NewMemory(ctx, 4, log)
	reg := modules.Default()

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
	runner := &reconcile.Runner{
		Store: st, Agent: agentClient, Registry: recs, Secrets: vault, Log: log,
		Facts: func(ctx context.Context, server store.ID) (system.MachineFacts, error) { return facts(ctx) },
	}
	hookRunner := &hooks.Runner{Registry: hookReg, Events: bus, Log: log}
	authorizer := auth.NewRoleAuthorizer(nil)
	pipeline, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: queue, Events: bus, Audit: bus,
		Hooks: hookRunner, Authorizer: authorizer, Secrets: vault, Log: log,
	})
	if err != nil {
		return nil, err
	}
	svc := &modules.Services{
		Config: cfg, Store: st, Kinds: kinds, Pipeline: pipeline, Jobs: queue, Events: bus, Audit: bus,
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
	manager := modules.NewManager(reg, modules.ManagerOptions{Facts: facts, Services: svc, Log: log})
	c := &core{log: log, agent: agentClient, modules: reg, manager: manager, services: svc}
	c.health = []api.HealthCheck{
		{Name: "agent", Check: func(ctx context.Context) error {
			_, err := facts(ctx)
			return err
		}},
		{Name: "modules", Check: func(ctx context.Context) error {
			failed := manager.Failed()
			ids := slices.Sorted(maps.Keys(failed))
			if len(ids) == 0 {
				return nil
			}
			return failed[ids[0]]
		}},
	}
	// A module that doesn't start is reported by the health check rather
	// than keeping the panel down, so it can still be turned off.
	if err := manager.Boot(ctx); err != nil {
		log.Warn("some modules didn't start; the health check lists them")
	}
	hookRunner.Notify(ctx, hooks.CoreBoot, auth.System("core"), nil)
	return c, nil
}

// stop stops the modules that are running.
func (c *core) stop(ctx context.Context) {
	c.manager.Shutdown(ctx)
}

// apiServer builds the panel API with the embedded UI.
func (c *core) apiServer(authn auth.Authenticator) *api.Server {
	assets, built := webui.Assets()
	if !built {
		c.log.Info("this binary has no web UI; the panel serves a page explaining how to build it")
	}
	return api.New(api.Options{
		Auth: authn, Modules: c.modules, Services: c.services, Health: c.health,
		UI: webui.Handler(assets), Log: c.log,
	})
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
