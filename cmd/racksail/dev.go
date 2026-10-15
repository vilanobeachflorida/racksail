package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func newDevCommand() *cobra.Command {
	var addrs config.DevAddresses
	var dataDir string
	var fresh bool
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Run the panel, agent and edge in one process against a simulated server",
		Long: "Runs everything in one process with the simulated backend and a data directory inside the " +
			"repository, so the whole product works on macOS or Linux without root. The simulated server is kept in " +
			"the data directory between runs, with the timings of a small real server; --fresh starts over with a newly " +
			"installed one. The edge serves what the simulated server holds, with certificates from a local CA. " +
			"Everyone who reaches it is signed in as the admin, so it only listens on loopback addresses. Give each " +
			"instance its own addresses and data directory to run several side by side. `make dev` also starts the UI " +
			"dev server with hot reload.",
		Example: "  racksail dev\n  racksail dev --addr 127.0.0.1:7170 --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data .dev-2",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range [][2]string{{"--addr", addrs.Panel}, {"--edge-http", addrs.EdgeHTTP}, {"--edge-https", addrs.EdgeHTTPS}} {
				if err := loopback(a[0], a[1]); err != nil {
					return err
				}
			}
			cfg, err := config.Dev(dataDir, addrs)
			if err != nil {
				return err
			}
			log := newLogger(os.Stderr, cfg.Log)
			ctx := cmd.Context()
			d, err := newDevServer(ctx, cfg, dataDir, fresh, log)
			if err != nil {
				return err
			}
			defer d.close(context.WithoutCancel(ctx))
			apiServer, err := d.core.apiServer(auth.DevAuthenticator{})
			if err != nil {
				return err
			}
			srv := newHTTPServer(cfg.Panel.Listen, apiServer.Handler(), log)
			log.Info("racksail dev is running", "panel", "http://"+cfg.Panel.Listen, "data", dataDir, "edge", "https://"+cfg.Edge.HTTPS)
			return serveUntilDone(ctx, srv, false)
		},
	}
	cmd.Flags().StringVar(&addrs.Panel, "addr", config.DevPanel, "address the panel listens on")
	cmd.Flags().StringVar(&addrs.EdgeHTTP, "edge-http", config.DevEdgeHTTP, "address the edge serves HTTP on once a module adds a route; port 0 picks a free one")
	cmd.Flags().StringVar(&addrs.EdgeHTTPS, "edge-https", config.DevEdgeHTTPS, "address the edge serves HTTPS on once a module adds a route; port 0 picks a free one")
	cmd.Flags().StringVar(&dataDir, "data", "./.dev", "directory for the development state")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "start over with a newly installed simulated server and no saved state")
	return cmd
}

// devServer is the panel, the simulated agent and the edge in one process.
type devServer struct {
	log     *slog.Logger
	machine *sim.Machine
	runtime *agent.Runtime
	core    *core
	edge    *edge.Edge
	cancel  context.CancelFunc
	done    chan struct{}
}

// newDevServer starts the simulated server, the agent and edge sockets and
// the edge, and builds the panel's core around them. The caller serves the
// panel's API and closes the server.
func newDevServer(ctx context.Context, cfg config.Config, dataDir string, fresh bool, log *slog.Logger) (*devServer, error) {
	machineDir := filepath.Join(dataDir, "machine")
	if fresh {
		// Only what dev itself keeps goes, never the directory as a whole,
		// which --data may point anywhere.
		for _, dir := range []string{machineDir, cfg.Paths.State} {
			if err := os.RemoveAll(dir); err != nil {
				return nil, err
			}
		}
	}
	for _, dir := range []string{cfg.Paths.Config, cfg.Paths.State, cfg.Paths.Run} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	machine, err := sim.Open(machineDir, sim.Options{Hostname: "racksail-dev", Realistic: true})
	if err != nil {
		return nil, err
	}
	d := &devServer{log: log, machine: machine, done: make(chan struct{})}
	backend := machine.Backend()
	// The panel in this process and tools on the socket share one runtime,
	// so they see the same audit trail and pending changes.
	d.runtime = agent.NewRuntime(backend, agent.RuntimeOptions{Log: log})
	table := edge.NewTable()
	// The edge reads what the agent wrote on the simulated server from its
	// copy on disk, and answers the panel's questions in this process.
	d.edge, err = edge.New(edge.Options{
		Table: table, HTTP: cfg.Edge.HTTP, HTTPS: cfg.Edge.HTTPS,
		StateDir: filepath.Join(cfg.Paths.State, "edge"), Dev: true,
		Certs: edge.FileCerts{Dir: machine.HostPath(system.CertsDir)}, MapPath: machine.HostPath,
		Responder: edge.DefaultAnswers, Log: log,
	})
	if err != nil {
		machine.Close()
		return nil, err
	}
	if d.core, err = newCore(ctx, cfg, log, agent.NewLocalRuntime(d.runtime), table); err != nil {
		machine.Close()
		return nil, err
	}
	if route, err := edge.PanelRoute(cfg.Panel.Hostname, cfg.Panel.Listen, false); err == nil {
		if err := table.SetRoutes(ctx, edge.PanelOwner, []edge.Route{route}); err != nil {
			log.Warn("serving the panel on the edge", "error", err)
		}
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d.cancel = cancel
	// The simulated agent and the edge also answer on their sockets, so
	// tools can talk to them as the panel talks to real ones. The panel
	// itself uses both in-process, so a socket that can't be created (paths
	// over about 100 bytes) only costs that convenience.
	var agentLn net.Listener
	if agentLn, err = agent.ListenUnix(cfg.Agent.Socket, 0o600, -1); err != nil {
		log.Warn("the simulated agent has no socket", "path", cfg.Agent.Socket, "error", err)
		agentLn = nil
	}
	var admin net.Listener
	if err := os.Remove(cfg.Edge.Socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("the edge has no admin socket", "path", cfg.Edge.Socket, "error", err)
	} else if admin, err = net.Listen("unix", cfg.Edge.Socket); err != nil {
		log.Warn("the edge has no admin socket", "path", cfg.Edge.Socket, "error", err)
		admin = nil
	}
	go func() {
		defer close(d.done)
		tasks := []func() error{func() error { return d.edge.Run(runCtx, admin) }}
		if agentLn != nil {
			srv := agent.NewServerWith(agent.ServerOptions{Backend: backend, Runtime: d.runtime, Version: updates.Build().Version, Log: log})
			tasks = append(tasks, func() error { return srv.Serve(runCtx, agentLn) })
		}
		if err := runAll(cancel, tasks...); err != nil {
			log.Error("racksail dev stopped serving", "error", err)
		}
	}()
	return d, nil
}

// close stops the edge and the sockets, then the panel's modules, and
// saves the simulated server.
func (d *devServer) close(ctx context.Context) {
	d.cancel()
	<-d.done
	d.core.stop(ctx)
	if err := d.machine.Close(); err != nil {
		d.log.Error("saving the simulated server", "error", err)
	}
}

// loopback refuses addresses other machines could reach, because racksail
// dev signs everyone in as the admin and serves simulated sites.
func loopback(flag, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q: %w", flag, addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%s %s isn't a loopback address; racksail dev signs everyone in as the admin, so it only listens on 127.0.0.1, ::1 or localhost", flag, addr)
	}
	return nil
}
