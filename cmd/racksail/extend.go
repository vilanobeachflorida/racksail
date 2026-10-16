package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/recipes"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/updates"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore"
)

// newAddonRuntime builds what runs installed add-ons: their tokens, kept in
// the panel database, and safe mode, read from the kernel's command line
// and the flags in the panel's state.
func newAddonRuntime(ctx context.Context, cfg config.Config, db *store.DB, st store.Store, reg *hooks.Registry, agentClient agent.Client,
	timeline *events.Log, runner *reconcile.Runner, log *slog.Logger) (*addons.Runtime, error) {
	tokens, err := addons.NewSQLTokens(ctx, db)
	if err != nil {
		return nil, err
	}
	return addons.NewRuntime(addons.Options{
		Registry: reg, Store: st, Agent: agentClient, Events: timeline, Audit: timeline, Tokens: tokens,
		SafeMode: addons.OpenSafeMode(cfg.Paths.State, addons.ReadCmdline()),
		TokenDir: filepath.Join(cfg.Paths.State, "addons"), SocketDir: filepath.Join(cfg.Paths.Run, "addons"),
		Enqueue: runner.Enqueue, Log: log,
	}), nil
}

// newRecipes builds the catalog client and the recipe engine. Catalogs are
// fetched only while the marketplace module is on; what holds data, which
// uninstalls may keep and upgrades snapshot, is what modules back up.
func newRecipes(cfg config.Config, st store.Store, kinds *store.KindRegistry, reg *modules.Registry, manager *modules.Manager,
	agentClient agent.Client, facts func(ctx context.Context) (system.MachineFacts, error), log *slog.Logger) (*recipes.Catalogs, *recipes.Engine, error) {
	catalogs := recipes.NewCatalogs(recipes.CatalogOptions{
		Store: st, Cache: filepath.Join(cfg.Paths.State, "catalogs"), Logger: log,
		Active: func() bool { return manager.State("marketplace").IsOn() },
	})
	downloads := filepath.Join(cfg.Paths.State, "downloads")
	if err := os.MkdirAll(downloads, 0o750); err != nil {
		return nil, nil, err
	}
	backups := map[string]modules.Backup{}
	for _, m := range reg.All() {
		if bp, ok := m.(modules.BackupProvider); ok {
			for _, b := range bp.Backups() {
				backups[b.Kind] = b
			}
		}
	}
	engine := recipes.NewEngine(recipes.EngineOptions{
		Store: st, Kinds: kinds, Catalogs: catalogs, Checker: reconcile.AgentChecker{Agent: agentClient}, Facts: facts, Logger: log,
		Verifier: &recipes.Verifier{Dir: downloads, TrustedRoot: func(ctx context.Context) (*sigstore.TrustedRoot, error) {
			root, _, err := updates.NewClient(nil).TrustedRoot(ctx, filepath.Join(cfg.Paths.State, "updates"))
			return root, err
		}},
		ModuleRecipes: func(id string) (fs.FS, bool) {
			m, ok := reg.Get(id)
			if !ok {
				return nil, false
			}
			rp, ok := m.(modules.RecipeProvider)
			if !ok {
				return nil, false
			}
			return rp.Recipes(), true
		},
		ModuleOn:  func(id string) bool { return manager.State(id).IsOn() },
		DataKinds: func(kind string) bool { _, ok := backups[kind]; return ok },
		DataPaths: func(ctx context.Context, r *store.Resource) ([]string, error) {
			if b, ok := backups[r.Kind]; ok && b.Paths != nil {
				return b.Paths(ctx, r)
			}
			return nil, nil
		},
	})
	return catalogs, engine, nil
}

// panelLink returns what notifications link back to: the address people
// reach the panel at, from panel.hostname and the port it listens on.
// Without a host name a panel listening on every address can't tell, so
// messages go without a link.
func panelLink(cfg config.Config) func(events.Event) string {
	host, port, err := net.SplitHostPort(cfg.Panel.Listen)
	if err != nil {
		return nil
	}
	if cfg.Panel.Hostname != "" {
		host = cfg.Panel.Hostname
	} else if ip, err := netip.ParseAddr(host); err != nil || ip.IsUnspecified() {
		return nil
	}
	scheme := "https"
	if cfg.Panel.Insecure {
		scheme = "http"
	}
	base := scheme + "://" + net.JoinHostPort(host, port)
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		base = scheme + "://" + host
	}
	return func(events.Event) string { return base + "/" }
}

// serveAddonGateway serves add-ons the panel's API on the gateway socket
// until ctx ends, as the add-on whose token each request carries. The API
// it serves is built the first time an add-on calls it.
func (c *core) serveAddonGateway(ctx context.Context) {
	cfg := c.services.Config
	path := filepath.Join(cfg.Paths.Run, "addons.sock")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Warn("add-ons can't reach the panel", "path", path, "error", err)
		return
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		// Paths over about 100 bytes can't hold a socket, as in some
		// development directories.
		c.log.Warn("add-ons can't reach the panel", "path", path, "error", err)
		return
	}
	// Any process may connect; only a running add-on's token gets an
	// answer.
	if err := os.Chmod(path, 0o666); err != nil {
		c.log.Warn("add-ons can't reach the panel", "path", path, "error", err)
		ln.Close()
		return
	}
	apiHandler := sync.OnceValue(func() http.Handler {
		return api.New(api.Options{
			Auth: addons.GatewayAuthenticator{}, Modules: c.modules, Services: c.services, Timeline: c.timeline, Webhooks: c.webhooks,
			RateLimit: api.RateLimit{PerActor: cfg.API.RateLimit, PerAddress: cfg.API.AnonymousRateLimit}, Log: c.log,
		}).Handler()
	})
	gateway := &addons.Gateway{
		Runtime: c.services.Addons, Secrets: c.services.Secrets, Store: c.services.Store,
		API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiHandler().ServeHTTP(w, r) }),
	}
	srv := &http.Server{Handler: gateway, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		ErrorLog: slog.NewLogLogger(c.log.Handler(), slog.LevelWarn)}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		c.log.Error("the add-on gateway stopped", "error", err)
	}
	os.Remove(path)
}
