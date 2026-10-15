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
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func newDevCommand() *cobra.Command {
	var addr, dataDir string
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Run the panel, agent and edge in one process against a simulated server",
		Long: "Runs everything in one process with the simulated backend and a data directory inside the " +
			"repository, so the whole product works on macOS or Linux without root. Everyone who reaches it " +
			"is signed in as the admin, so it only listens on a loopback address. `make dev` also starts the " +
			"UI dev server with hot reload.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return fmt.Errorf("--addr %q: %w", addr, err)
			}
			if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return fmt.Errorf("--addr %s isn't a loopback address; racksail dev signs everyone in as the admin, so it only listens on 127.0.0.1, ::1 or localhost", addr)
			}
			cfg, err := config.Dev(dataDir, addr)
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

			srv := newHTTPServer(cfg.Panel.Listen, c.apiServer(auth.DevAuthenticator{}).Handler(), log)
			log.Info("racksail dev is running", "panel", "http://"+cfg.Panel.Listen, "data", dataDir, "edge", "http://"+cfg.Edge.HTTP)
			return serveUntilDone(ctx, srv, false)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7070", "address the panel listens on")
	cmd.Flags().StringVar(&dataDir, "data", "./.dev", "directory for the development state")
	return cmd
}
