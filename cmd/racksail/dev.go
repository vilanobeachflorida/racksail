package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func newDevCommand() *cobra.Command {
	var addrs config.DevAddresses
	var dataDir string
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Run the panel, agent and edge in one process against a simulated server",
		Long: "Runs everything in one process with the simulated backend and a data directory inside the " +
			"repository, so the whole product works on macOS or Linux without root. Everyone who reaches it " +
			"is signed in as the admin, so it only listens on loopback addresses. Give each instance its own " +
			"addresses and data directory to run several side by side. `make dev` also starts the UI dev server " +
			"with hot reload.",
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
			for _, dir := range []string{cfg.Paths.Config, cfg.Paths.State, cfg.Paths.Run} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			machine := sim.New(sim.Options{Hostname: "racksail-dev"})
			backend := machine.Backend()
			table := edge.NewTable()
			c, err := newCore(ctx, cfg, log, agent.NewLocal(backend, log), table)
			if err != nil {
				return err
			}
			defer c.stop(context.WithoutCancel(ctx))

			// The simulated agent and the edge also answer on their sockets, so
			// tools can talk to them as the panel talks to real ones. The panel
			// itself uses both in-process, so a socket that can't be created
			// (paths over about 100 bytes) only costs that convenience.
			if agentLn, err := agent.ListenUnix(cfg.Agent.Socket, 0o600, -1); err != nil {
				log.Warn("the simulated agent has no socket", "path", cfg.Agent.Socket, "error", err)
			} else {
				go func() {
					if err := agent.NewServer(backend, updates.Build().Version, log).Serve(ctx, agentLn); err != nil {
						log.Error("dev agent stopped", "error", err)
					}
				}()
			}
			if err := os.Remove(cfg.Edge.Socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			admin, err := net.Listen("unix", cfg.Edge.Socket)
			if err != nil {
				log.Warn("the edge has no admin socket", "path", cfg.Edge.Socket, "error", err)
			}
			go func() {
				srv := edge.NewServer(table, edge.FileCerts{Dir: cfg.Paths.Config + "/certs"}, log)
				if err := srv.Run(ctx, admin, cfg.Edge.HTTP, cfg.Edge.HTTPS); err != nil {
					log.Error("dev edge stopped", "error", err)
				}
			}()

			apiServer, err := c.apiServer(nil, true)
			if err != nil {
				return err
			}
			srv := newHTTPServer(cfg.Panel.Listen, apiServer.Handler(), log)
			log.Info("racksail dev is running", "panel", "http://"+cfg.Panel.Listen, "data", dataDir, "edge", "http://"+cfg.Edge.HTTP)
			return serveUntilDone(ctx, srv, false)
		},
	}
	cmd.Flags().StringVar(&addrs.Panel, "addr", config.DevPanel, "address the panel listens on")
	cmd.Flags().StringVar(&addrs.EdgeHTTP, "edge-http", config.DevEdgeHTTP, "address the edge serves HTTP on once a module adds a route; port 0 picks a free one")
	cmd.Flags().StringVar(&addrs.EdgeHTTPS, "edge-https", config.DevEdgeHTTPS, "address the edge serves HTTPS on once a module adds a route; port 0 picks a free one")
	cmd.Flags().StringVar(&dataDir, "data", "./.dev", "directory for the development state")
	return cmd
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
