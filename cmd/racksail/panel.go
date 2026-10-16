package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

func newPanelCommand() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "panel",
		Short: "Run the panel: API, web UI, state and the change pipeline",
		Long: "Runs the panel as the racksail user. It serves the API and the web UI over HTTPS on port 8443, " +
			"keeps the desired state, and asks the agent to change the machine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			log := newLogger(os.Stderr, cfg.Log)
			ctx := cmd.Context()
			agentClient := agent.DialUnix(cfg.Agent.Socket)
			link := edgeLink(ctx, cfg, agentClient, log)
			defer link.Close()
			c, err := newCore(ctx, cfg, log, agentClient, link)
			if err != nil {
				return err
			}
			defer c.stop(context.WithoutCancel(ctx))
			if err := c.bootstrap(ctx, cmd.ErrOrStderr()); err != nil {
				return err
			}
			apiServer, err := c.apiServer(c.identity, false)
			if err != nil {
				return err
			}
			if len(cfg.Panel.AllowFrom) > 0 {
				log.Info("the panel only accepts requests from panel.allow_from", "networks", cfg.Panel.AllowFrom)
			}
			srv := newHTTPServer(cfg.Panel.Listen, apiServer, log)
			if cfg.Panel.Insecure {
				return serveUntilDone(ctx, srv, false)
			}
			cert, err := panelCertificate(cfg.Panel.TLSCert, cfg.Panel.TLSKey, filepath.Join(cfg.Paths.State, "tls"), cfg.Panel.Hostname)
			if err != nil {
				return err
			}
			srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
			log.Info("panel listening", "address", "https://"+cfg.Panel.Listen)
			return serveUntilDone(ctx, srv, true)
		},
	}
	addConfigFlag(cmd, &configPath)
	return cmd
}

// edgeLink is how the panel reaches the edge, which systemd starts on the
// first request to its socket: only once modules route something through
// it. The panel then answers the edge's questions, such as which names may
// have on-demand certificates, is served under its own hostname on 443, and
// has the edge start at boot.
func edgeLink(ctx context.Context, cfg config.Config, agentClient agent.Client, log *slog.Logger) *edge.Link {
	opts := edge.LinkOptions{
		Client: edge.DialAdmin(cfg.Edge.Socket), Responder: edge.DefaultAnswers, Log: log,
		Running: func(ctx context.Context) (bool, error) {
			st, err := agent.UnitStatus.Call(ctx, agentClient, agent.UnitStatusInput{Name: system.EdgeUnit}, agent.CallOptions{})
			return st.Active() || st.ActiveState == "activating", err
		},
		Boot: func(ctx context.Context, start bool) error {
			in := agent.UnitsInput{Names: []string{system.EdgeUnit}}
			op := agent.DisableUnits
			if start {
				op = agent.EnableUnits
			}
			_, err := op.Call(ctx, agentClient, in, agent.CallOptions{})
			return err
		},
	}
	if hostname := cfg.Panel.Hostname; hostname != "" && hostname != "localhost" {
		route, err := edge.PanelRoute(hostname, cfg.Panel.Listen, !cfg.Panel.Insecure)
		if err != nil {
			log.Warn("the edge won't serve the panel under its hostname", "hostname", hostname, "error", err)
		} else {
			opts.Panel = &route
		}
	}
	return edge.NewLink(ctx, opts)
}

// panelCertificate loads the configured certificate, or the panel's own
// self-signed one.
func panelCertificate(certFile, keyFile, dir, hostname string) (tls.Certificate, error) {
	if certFile != "" {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if hostname != "" {
		hosts = append(hosts, hostname)
	}
	if name, err := os.Hostname(); err == nil {
		hosts = append(hosts, name)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.IsGlobalUnicast() {
				hosts = append(hosts, ipnet.IP.String())
			}
		}
	}
	return api.PanelCertificate(dir, hosts, time.Now())
}

// serveUntilDone serves until ctx is done, then shuts down gracefully.
func serveUntilDone(ctx context.Context, srv *http.Server, secure bool) error {
	errs := make(chan error, 1)
	go func() {
		var err error
		if secure {
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		errs <- err
	}()
	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving %s: %w", srv.Addr, err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		// Whatever is still going on is cut off, so nothing is left
		// answering once the panel's database closes.
		srv.Close()
		return fmt.Errorf("shutting down %s: %w", srv.Addr, err)
	}
	return nil
}
