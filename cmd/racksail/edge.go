package main

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

func newEdgeCommand() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "edge",
		Short: "Run the edge, the web server in front of every site and app",
		Long: "Runs the edge as the racksail-edge user. The panel sends it the routing table over " +
			"/run/racksail/edge.sock; it starts listening on ports 80 and 443 when the first route arrives.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			log := newLogger(os.Stderr, cfg.Log)
			if err := os.MkdirAll(filepath.Dir(cfg.Edge.Socket), 0o755); err != nil {
				return err
			}
			if err := os.Remove(cfg.Edge.Socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			admin, err := net.Listen("unix", cfg.Edge.Socket)
			if err != nil {
				return err
			}
			if err := os.Chmod(cfg.Edge.Socket, 0o660); err != nil {
				return err
			}
			log.Info("edge waiting for routes", "socket", cfg.Edge.Socket)
			srv := edge.NewServer(edge.NewTable(), edge.FileCerts{Dir: system.CertsDir}, log)
			return srv.Run(cmd.Context(), admin, cfg.Edge.HTTP, cfg.Edge.HTTPS)
		},
	}
	addConfigFlag(cmd, &configPath)
	return cmd
}
