package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/config"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/notify"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"

	_ "github.com/vilanobeachflorida/racksail/modules/all"
)

// Command groups in the help output.
const (
	groupServer = "server"
	groupClient = "client"
	groupTools  = "tools"
)

// newRootCommand builds every command.
func newRootCommand() *cobra.Command { return buildRootCommand(true) }

// newRootCommandFor builds the commands the arguments may run. The roles
// that run Racksail have no use for the commands built from the API
// document, so they skip building them.
func newRootCommandFor(args []string) *cobra.Command {
	return buildRootCommand(len(args) == 0 || !slices.Contains(roles, args[0]))
}

// roles are the commands that run Racksail rather than call it.
var roles = []string{"panel", "agent", "edge", "wake-proxy", "dev", "install"}

func buildRootCommand(withAPICommands bool) *cobra.Command {
	root := &cobra.Command{
		Use:   "racksail",
		Short: "Your server, your rules.",
		Long: "Racksail turns a Debian 13 server into a platform for websites, email, DNS, apps, file storage, " +
			"containers and virtual machines, managed from one web panel, this command and an API.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddGroup(
		&cobra.Group{ID: groupServer, Title: "Run Racksail:"},
		&cobra.Group{ID: groupClient, Title: "Manage a server:"},
		&cobra.Group{ID: groupTools, Title: "Tools:"},
	)
	for _, cmd := range []*cobra.Command{newPanelCommand(), newAgentCommand(), newEdgeCommand(), newWakeProxyCommand(), newDevCommand(), newInstallCommand(), newSafeModeCommand(), newUpdateCommand()} {
		cmd.GroupID = groupServer
		root.AddCommand(cmd)
	}
	opts := &ClientOptions{}
	for _, build := range clientCommands {
		cmd := build(opts)
		cmd.GroupID = groupClient
		addClientFlags(cmd, opts)
		root.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{newOpenAPICommand(), newVersionCommand(), newThemeCommand()} {
		cmd.GroupID = groupTools
		root.AddCommand(cmd)
	}
	// A binary whose registries have problems refuses to run, which the
	// commands that need them say; building from them could fail first.
	if withAPICommands && checkRegistries() == nil {
		if err := addAPICommands(root, opts); err != nil {
			root.PersistentPreRunE = func(*cobra.Command, []string) error {
				return fmt.Errorf("building the commands from the API: %w", err)
			}
		}
	}
	root.SetHelpCommandGroupID(groupTools)
	root.SetCompletionCommandGroupID(groupTools)
	return root
}

// checkRegistries reports problems found while packages registered error
// codes, operations, kinds, event and job types, notification channels and
// modules. A binary with such problems refuses to run.
func checkRegistries() error {
	return errors.Join(i18n.Err(), agent.Err(), store.Err(), events.Err(), jobs.Err(), notify.Err(), auth.Err(), api.ExtensionErr(),
		modules.Default().Err())
}

// newLogger builds the logger for a role from its configuration.
func newLogger(w io.Writer, c config.Log) *slog.Logger {
	var level slog.Level
	switch c.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// defaultLog is how roles that read no configuration log.
func defaultLog() config.Log { return config.Default().Log }

// loadConfig reads the configuration file named by the --config flag.
func loadConfig(path string) (config.Config, error) {
	c, err := config.Load(path)
	if err != nil {
		return c, fmt.Errorf("configuration %s: %w", path, err)
	}
	return c, nil
}

func addConfigFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "config", system.ConfigFile, "path of racksail.toml")
}

// requireRoot refuses to continue unless running as root, for roles that
// change the machine.
func requireRoot(what string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s needs to run as root; use sudo, or --sim to try it against a simulated server", what)
	}
	return nil
}
