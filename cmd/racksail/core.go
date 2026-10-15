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
	queue := jobs.NewMemory(ctx, jobs.MemoryOptions{Workers: 4, Log: log})
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
	vault, err := openSecrets(ctx, cfg, log, agentClient)
	if err != nil {
		return nil, err
	}
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
	c := &core{log: log, agent: agentClient, modules: reg, manager: manager, services: svc}
	c.health = append([]api.HealthCheck{
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
	}, moduleHealthChecks(reg, manager, svc)...)
	// A module that doesn't start is reported by the health check rather
	// than keeping the panel down, so it can still be turned off.
	if err := manager.Boot(ctx); err != nil {
		log.Warn("some modules didn't start; the health check lists them")
	}
	hookRunner.Notify(ctx, hooks.CoreBoot, auth.System("core"), nil)
	// The resync puts back what drifts on the machine and carries out
	// changes whose apply job never ran; drift the agent reports is put back
	// as soon as it is found.
	go runner.Resync(ctx, reconcile.ResyncInterval)
	if src, ok := agentClient.(agent.EventSource); ok {
		go followAgent(ctx, src, st, runner, bus, log)
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

// stop stops the modules that are running.
func (c *core) stop(ctx context.Context) {
	c.manager.Shutdown(ctx)
}

// apiServer builds the panel API with the embedded UI, reachable only from
// panel.allow_from when it is set.
func (c *core) apiServer(authn auth.Authenticator) (*api.Server, error) {
	allow, err := c.services.Config.Panel.AllowedNetworks()
	if err != nil {
		return nil, err
	}
	assets, built := webui.Assets()
	if !built {
		c.log.Info("this binary has no web UI; the panel serves a page explaining how to build it")
	}
	return api.New(api.Options{
		Auth: authn, Modules: c.modules, Services: c.services, Health: c.health, AllowFrom: allow,
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
