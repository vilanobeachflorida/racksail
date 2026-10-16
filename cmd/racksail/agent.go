package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
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

// auditMaxBytes is when the agent's audit trail is rotated.
const auditMaxBytes = 64 << 20

func newAgentCommand() *cobra.Command {
	var configPath, socket, listenTLS, clusterFile string
	var simulated bool
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the agent, which changes the machine on the panel's behalf",
		Long: "Runs the agent as root. It accepts typed operations from a fixed allowlist on its Unix socket, " +
			"agent.socket in racksail.toml (/run/racksail/agent.sock by default), which only the racksail group can reach, " +
			"and records every call in /var/log/racksail-agent/audit.jsonl, which only root can read. A server that joined a cluster with " +
			"`racksail agent join` also connects out to the panel's hub and serves the panel over those connections; " +
			"--listen-tls additionally accepts the panel directly with mutual TLS. " +
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
			var audit agent.Auditor
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
				if err := os.MkdirAll(system.AgentLogDir, 0o700); err != nil {
					return err
				}
				fileAudit, err := agent.OpenFileAudit(system.AgentAuditLog, auditMaxBytes)
				if err != nil {
					return err
				}
				defer fileAudit.Close()
				audit = fileAudit
			}
			if socket != "" {
				if path, err = filepath.Abs(socket); err != nil {
					return err
				}
			}
			cluster, err := readCluster(clusterFile)
			joined := err == nil
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if listenTLS != "" && !joined {
				return fmt.Errorf("--listen-tls needs the certificate `racksail agent join` keeps in %s", clusterFile)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			ln, err := agent.ListenUnix(path, perm, gid)
			if err != nil {
				return err
			}
			rt := agent.NewRuntime(backend, agent.RuntimeOptions{Audit: audit, Log: log})
			srv := agent.NewServerWith(agent.ServerOptions{Backend: backend, Runtime: rt, Version: updates.Build().Version, Log: log})
			log.Info("agent listening", "socket", path, "backend", backend.Name, "operations", len(agent.Operations()))

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			var tasks []func() error
			tasks = append(tasks, func() error { return srv.Serve(ctx, ln) })
			if joined {
				conn := &agent.Connector{
					Addr: cluster.Hub, Credentials: cluster.Credentials, Server: srv, Log: log,
					OnRenew: func(c agent.Credentials) error {
						cluster.Credentials = c
						return writeCluster(clusterFile, cluster)
					},
				}
				tasks = append(tasks, func() error { return conn.Run(ctx) })
				log.Info("connecting to the panel's hub", "hub", cluster.Hub)
			}
			if listenTLS != "" {
				tlsConfig, err := agent.ServerTLSConfig(cluster.Credentials)
				if err != nil {
					return err
				}
				tcp, err := net.Listen("tcp", listenTLS)
				if err != nil {
					return err
				}
				tasks = append(tasks, func() error { return srv.ServeTLS(ctx, tcp, tlsConfig) })
				log.Info("agent accepting the panel with mutual TLS", "address", tcp.Addr().String())
			}
			return runAll(cancel, tasks...)
		},
	}
	addConfigFlag(cmd, &configPath)
	cmd.Flags().BoolVar(&simulated, "sim", false, "act on a simulated server")
	cmd.Flags().StringVar(&socket, "socket", "", "Unix socket to listen on instead of agent.socket from racksail.toml")
	cmd.Flags().StringVar(&listenTLS, "listen-tls", "", "also accept the panel with mutual TLS on this address, such as [::]:8445")
	cmd.Flags().StringVar(&clusterFile, "cluster-file", system.AgentClusterFile, "where `racksail agent join` keeps the hub and certificate")
	cmd.AddCommand(newAgentJoinCommand())
	return cmd
}

// runAll runs tasks until the first one ends, then stops the others and
// waits for them, returning the first error.
func runAll(cancel context.CancelFunc, tasks ...func() error) error {
	errs := make(chan error, len(tasks))
	for _, task := range tasks {
		go func() { errs <- task() }()
	}
	first := <-errs
	cancel()
	for range len(tasks) - 1 {
		if err := <-errs; first == nil {
			first = err
		}
	}
	return first
}

func newAgentJoinCommand() *cobra.Command {
	var fingerprint, clusterFile, hostname string
	cmd := &cobra.Command{
		Use:   "join HUB TOKEN",
		Short: "Join this server to a panel's cluster",
		Long: "Asks the panel's hub at HUB (host or host:port, port 8444 by default) for a certificate, with the " +
			"one-time TOKEN the panel gave you. The panel's CA is checked against --fingerprint, so nothing pretending " +
			"to be the panel can take the token. The hub and certificate are kept in /var/lib/racksail-agent/cluster.json; " +
			"from its next start the agent connects out to the hub, so the server needs no open management port.",
		Example: "  sudo racksail agent join panel.example.net 3f9c... --fingerprint 8a1d...",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clusterFile == system.AgentClusterFile {
				if err := requireRoot("joining a cluster"); err != nil {
					return err
				}
			}
			hub := args[0]
			if _, _, err := net.SplitHostPort(hub); err != nil {
				hub = net.JoinHostPort(hub, strconv.Itoa(agent.DefaultHubPort))
			}
			if hostname == "" {
				hostname, _ = os.Hostname()
			}
			creds, err := agent.Join(cmd.Context(), hub, args[1], fingerprint, hostname)
			if err != nil {
				return err
			}
			id, err := creds.Identity()
			if err != nil {
				return err
			}
			if err := writeCluster(clusterFile, clusterState{Hub: hub, Credentials: creds}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Joined the cluster as %s. Restart the agent to connect: systemctl restart racksail-agent\n", id.Server)
			return nil
		},
	}
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "SHA-256 fingerprint of the panel's CA, shown with the token")
	cmd.Flags().StringVar(&clusterFile, "cluster-file", system.AgentClusterFile, "where to keep the hub and certificate")
	cmd.Flags().StringVar(&hostname, "hostname", "", "the name this server reports to the panel; its hostname by default")
	cmd.MarkFlagRequired("fingerprint")
	return cmd
}

// clusterState is what a joined server keeps: where the panel's hub is and
// the certificate it proves itself with.
type clusterState struct {
	Hub         string            `json:"hub"`
	Credentials agent.Credentials `json:"credentials"`
}

func readCluster(p string) (clusterState, error) {
	var c clusterState
	data, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	if _, err := c.Credentials.Identity(); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

// writeCluster keeps the cluster state readable by root only, replacing
// the file atomically so a crash never leaves half a key.
func writeCluster(p string, c clusterState) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
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
