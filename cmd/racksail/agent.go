package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/linux"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func newAgentCommand() *cobra.Command {
	var configPath, socket string
	var simulated bool
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the agent, which changes the machine on the panel's behalf",
		Long: "Runs the agent as root. It accepts typed operations from a fixed allowlist on its Unix socket, " +
			"agent.socket in racksail.toml (/run/racksail/agent.sock by default), which only the racksail group can reach. " +
			"With --sim it acts on a simulated server instead and needs no root; its socket is then in a directory of " +
			"your own under $XDG_RUNTIME_DIR or the temporary directory, unless --socket names one.",
		Example: "  sudo racksail agent\n  racksail agent --sim --socket /tmp/racksail-agent.sock",
		Args:    cobra.NoArgs,
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
			path, perm, gid := cfg.Agent.Socket, fs.FileMode(0o660), -1
			if simulated {
				host, _ := os.Hostname()
				backend = sim.New(sim.Options{Hostname: host}).Backend()
				// Only the user running it reaches a simulated agent.
				perm = 0o600
				if socket == "" {
					if path, err = simSocket(); err != nil {
						return err
					}
				}
			} else {
				if err := requireRoot("the agent"); err != nil {
					return err
				}
				if backend, err = linux.New(cmd.Context()); err != nil {
					return err
				}
				if g, err := user.LookupGroup("racksail"); err == nil {
					if gid, err = strconv.Atoi(g.Gid); err != nil {
						return fmt.Errorf("group racksail has gid %q", g.Gid)
					}
				} else {
					log.Warn("the racksail group doesn't exist; only root can reach the agent socket")
				}
			}
			if socket != "" {
				if path, err = filepath.Abs(socket); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			ln, err := agent.ListenUnix(path, perm, gid)
			if err != nil {
				return err
			}
			log.Info("agent listening", "socket", path, "backend", backend.Name, "operations", len(agent.Operations()))
			return agent.NewServer(backend, updates.Build().Version, log).Serve(cmd.Context(), ln)
		},
	}
	addConfigFlag(cmd, &configPath)
	cmd.Flags().BoolVar(&simulated, "sim", false, "act on a simulated server")
	cmd.Flags().StringVar(&socket, "socket", "", "Unix socket to listen on instead of agent.socket from racksail.toml")
	return cmd
}

// simSocket returns where a simulated agent listens when no socket is
// given: a directory only the current user can reach, in $XDG_RUNTIME_DIR
// or else the temporary directory.
func simSocket() (string, error) {
	dir := filepath.Join(os.TempDir(), "racksail-"+strconv.Itoa(os.Getuid()))
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		dir = filepath.Join(runtime, "racksail")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// The temporary directory is shared, so make sure nobody else prepared
	// this one.
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || (ok && int(st.Uid) != os.Getuid()) {
		return "", fmt.Errorf("%s isn't a directory of yours; pass --socket", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent.sock"), nil
}
