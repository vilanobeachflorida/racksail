package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/linux"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func newAgentCommand() *cobra.Command {
	var configPath string
	var simulated bool
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the agent, which changes the machine on the panel's behalf",
		Long: "Runs the agent as root. It accepts typed operations from a fixed allowlist on its Unix socket, " +
			"/run/racksail/agent.sock, which only the racksail group can reach. With --sim it acts on a " +
			"simulated server instead, which needs no root and works on any operating system.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}
			log := newLogger(os.Stderr, cfg.Log)
			if err := checkRegistries(); err != nil {
				return err
			}
			var backend *system.Backend
			if simulated {
				host, _ := os.Hostname()
				backend = sim.New(sim.Options{Hostname: host}).Backend()
			} else {
				if err := requireRoot("the agent"); err != nil {
					return err
				}
				if backend, err = linux.New(cmd.Context()); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(cfg.Agent.Socket), 0o755); err != nil {
				return err
			}
			gid := -1
			if g, err := user.LookupGroup("racksail"); err == nil {
				if gid, err = strconv.Atoi(g.Gid); err != nil {
					return fmt.Errorf("group racksail has gid %q", g.Gid)
				}
			} else if !simulated {
				log.Warn("the racksail group doesn't exist; only root can reach the agent socket")
			}
			ln, err := agent.ListenUnix(cfg.Agent.Socket, 0o660, gid)
			if err != nil {
				return err
			}
			log.Info("agent listening", "socket", cfg.Agent.Socket, "backend", backend.Name, "operations", len(agent.Operations()))
			return agent.NewServer(backend, updates.Build().Version, log).Serve(cmd.Context(), ln)
		},
	}
	addConfigFlag(cmd, &configPath)
	cmd.Flags().BoolVar(&simulated, "sim", false, "act on a simulated server")
	return cmd
}
