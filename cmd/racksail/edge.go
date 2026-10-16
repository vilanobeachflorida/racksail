package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

func newEdgeCommand() *cobra.Command {
	var configPath, stateDir, socket string
	cmd := &cobra.Command{
		Use:   "edge",
		Short: "Run the edge, the web server in front of every site and app",
		Long: "Runs the edge as the racksail-edge user: an embedded Caddy serving the routing table the panel sends " +
			"over /run/racksail/edge.sock, which systemd holds for it through racksail-edge.socket and starts it on. " +
			"It listens on ports 80 and 443 from the first route, obtains certificates through ACME, and keeps its " +
			"certificates, settings and the last routing table in its state directory, so a restarted edge serves " +
			"its sites before the panel gets in touch. Started by hand, without a socket from systemd, it creates " +
			"the socket itself, at --socket, reachable only by the user running it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			log := newLogger(os.Stderr, cfg.Log)
			admin, activated, err := adminListener(socket, log)
			if err != nil {
				return err
			}
			keepCaddyData(stateDir)
			opts := edge.Options{
				HTTP: cfg.Edge.HTTP, HTTPS: cfg.Edge.HTTPS, StateDir: stateDir,
				Certs: edge.FileCerts{Dir: system.CertsDir}, Log: log,
			}
			if activated {
				// systemd holds the socket and starts the edge again on the
				// next request, so an edge with nothing to serve can go.
				opts.IdleExit = edgeIdleExit
			}
			e, err := edge.New(opts)
			if err != nil {
				return err
			}
			log.Info("edge waiting for routes", "socket", admin.Addr().String(), "state", stateDir)
			return e.Run(cmd.Context(), admin)
		},
	}
	addConfigFlag(cmd, &configPath)
	cmd.Flags().StringVar(&stateDir, "state", system.EdgeStateDir, "directory for certificates, settings and the last routing table")
	cmd.Flags().StringVar(&socket, "socket", system.EdgeSocket, "the admin socket to create when systemd passes none; the panel calls the default one")
	return cmd
}

// keepCaddyData has Caddy keep its instance ID with the edge's state
// rather than in the user's home, unless the environment says where.
func keepCaddyData(stateDir string) {
	if os.Getenv("XDG_DATA_HOME") == "" {
		os.Setenv("XDG_DATA_HOME", stateDir)
	}
}

// edgeIdleExit is how long an edge started by systemd waits with nothing
// to serve before it exits.
const edgeIdleExit = 5 * time.Minute

// adminListener returns the socket the panel sends routes to, and whether
// systemd passed it in. Under systemd it is the one racksail-edge.socket
// holds: systemd creates it in /run/racksail, which the edge can't write
// to, and gives it to the panel's group, which the edge isn't in. Started
// by hand, the edge creates the socket at path itself, for its own user
// only.
func adminListener(path string, log *slog.Logger) (net.Listener, bool, error) {
	passed, err := passedListeners()
	if err != nil {
		return nil, false, err
	}
	switch len(passed) {
	case 0:
	case 1:
		return passed[0], true, nil
	default:
		for _, ln := range passed {
			ln.Close()
		}
		return nil, false, fmt.Errorf("systemd passed %d sockets; the edge takes one, its admin socket", len(passed))
	}
	log.Info("no socket from systemd; the edge creates its own, which only its user can reach", "socket", path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	ln, err := agent.ListenUnix(path, 0o600, -1)
	return ln, false, err
}

// passedListeners returns the sockets systemd passed in.
func passedListeners() ([]net.Listener, error) {
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
	return passed, nil
}

func newWakeProxyCommand() *cobra.Command {
	var upstream string
	var idle, start time.Duration
	cmd := &cobra.Command{
		Use:   "wake-proxy",
		Short: "Forward a sleeping application's socket to it, waking it on the first connection",
		Long: "Runs under systemd socket activation for an application that sleeps when idle, as the units Racksail " +
			"renders for such applications start it. It forwards connections on the sockets systemd passes to the " +
			"application at --upstream, waiting up to --start-timeout for it to start, and exits once nothing has " +
			"used it for --idle-timeout, which lets systemd stop the application too.",
		Example: "  racksail wake-proxy --upstream /run/racksail-sites/blog/shop.sock --idle-timeout 900s --start-timeout 60s",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			listeners, err := passedListeners()
			if err != nil {
				return err
			}
			p := &edge.WakeProxy{Upstream: upstream, IdleTimeout: idle, StartTimeout: start, Log: newLogger(os.Stderr, defaultLog())}
			return p.Run(cmd.Context(), listeners)
		},
	}
	cmd.Flags().StringVar(&upstream, "upstream", "", "the application's Unix socket or host:port")
	cmd.Flags().DurationVar(&idle, "idle-timeout", 15*time.Minute, "how long without connections before the application sleeps")
	cmd.Flags().DurationVar(&start, "start-timeout", time.Minute, "how long the application may take to start")
	cmd.MarkFlagRequired("upstream")
	return cmd
}
