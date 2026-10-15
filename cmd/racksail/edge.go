package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

func newEdgeCommand() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "edge",
		Short: "Run the edge, the web server in front of every site and app",
		Long: "Runs the edge as the racksail-edge user. The panel sends it the routing table over " +
			"/run/racksail/edge.sock, which systemd holds for it through racksail-edge.socket and starts it on; " +
			"it starts listening on ports 80 and 443 when the first route arrives. Started by hand, it creates " +
			"edge.socket from racksail.toml itself, reachable only by the user running it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			log := newLogger(os.Stderr, cfg.Log)
			admin, err := adminListener(cfg.Edge.Socket, log)
			if err != nil {
				return err
			}
			log.Info("edge waiting for routes", "socket", admin.Addr().String())
			srv := edge.NewServer(edge.NewTable(), edge.FileCerts{Dir: system.CertsDir}, log)
			return srv.Run(cmd.Context(), admin, cfg.Edge.HTTP, cfg.Edge.HTTPS)
		},
	}
	addConfigFlag(cmd, &configPath)
	return cmd
}

// adminListener returns the socket the panel sends routes to. Under systemd
// it is the one racksail-edge.socket passes in: systemd creates it in
// /run/racksail, which the edge can't write to, and gives it to the panel's
// group, which the edge isn't in. Started by hand, the edge creates the
// socket at path itself, for its own user only.
func adminListener(path string, log *slog.Logger) (net.Listener, error) {
	listeners, err := activation.Listeners()
	if err != nil {
		return nil, err
	}
	var passed []net.Listener
	for _, ln := range listeners {
		if ln != nil {
			passed = append(passed, ln)
		}
	}
	switch len(passed) {
	case 0:
	case 1:
		return passed[0], nil
	default:
		for _, ln := range passed {
			ln.Close()
		}
		return nil, fmt.Errorf("systemd passed %d sockets; the edge takes one, its admin socket", len(passed))
	}
	log.Info("no socket from systemd; the edge creates its own, which only its user can reach", "socket", path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return agent.ListenUnix(path, 0o600, -1)
}
