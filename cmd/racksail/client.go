package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/cli"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// ClientOptions are the settings shared by commands that call a server's
// API: the profile, the panel's address and how to trust it, the output
// format and --dry-run. The token comes from a profile or RACKSAIL_TOKEN,
// so it never ends up in shell history.
type ClientOptions = cli.Options

// clientCommands builds the hand-written commands that call the API; each
// gets the shared client flags. The commands for the API's operations are
// built from its document by addAPICommands.
var clientCommands = []func(opts *ClientOptions) *cobra.Command{newStatusCommand}

// mutating marks a client command as one that changes something, which
// gives it --dry-run.
func mutating(cmd *cobra.Command) *cobra.Command { return cli.Mutating(cmd) }

func addClientFlags(cmd *cobra.Command, opts *ClientOptions) { cli.AddFlags(cmd, opts) }

// addAPICommands adds a command for every operation of the panel's API
// that API tokens may use, built from the binary's own API document, and
// the commands that sign in and keep profiles.
func addAPICommands(root *cobra.Command, opts *ClientOptions) error {
	return cli.AddCommands(root, cli.Config{
		Document: func() ([]byte, error) {
			// Modules must not use their services while registering
			// operations, so empty services are enough here.
			return api.New(api.Options{Modules: modules.Default(), Services: &modules.Services{}}).OpenAPI()
		},
		Kind: func(name string) (*store.KindInfo, bool) {
			k, ok := store.Kinds().Get(name)
			if !ok {
				return nil, false
			}
			return k.Info(), true
		},
		Module: func(id string) (string, bool) {
			m, ok := modules.Default().Get(id)
			if !ok {
				return "", false
			}
			return m.Manifest().Name, true
		},
		Options: opts,
		Group:   groupClient,
	})
}

func newStatusCommand(opts *ClientOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether a server is healthy and which version it runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := opts.Connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var health api.HealthReport
			if _, err := c.Get(ctx, api.Prefix+"/health", &health); err != nil {
				return err
			}
			var version updates.BuildInfo
			if _, err := c.Get(ctx, api.Prefix+"/version", &version); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if opts.Output != "table" {
				raw, err := json.Marshal(map[string]any{"server": c.Server, "health": health, "version": version})
				if err != nil {
					return err
				}
				if err := cli.Write(out, raw, opts.Output); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(out, "%s: racksail %s (%s), %s\n", c.Server, version.Version, version.Channel, health.Status)
				for _, check := range health.Checks {
					line := fmt.Sprintf("  %-12s %s", check.Name, check.Status)
					if check.Message != "" {
						line += ": " + check.Message
					}
					fmt.Fprintln(out, line)
				}
			}
			if health.Status != "ok" {
				return exitError{code: 2}
			}
			return nil
		},
	}
}
