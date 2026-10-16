package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func newOpenAPICommand() *cobra.Command {
	var agentProtocol, addonProtocol bool
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Print the OpenAPI document of the API",
		Long: "Prints the OpenAPI 3.1 document of the panel API, generated from the code, as committed in " +
			"spec/openapi.json. With --agent it prints the agent protocol instead (spec/agent-protocol.json), and " +
			"with --addon the add-on protocol (spec/addon-protocol.json).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkRegistries(); err != nil {
				return err
			}
			var doc []byte
			var err error
			switch {
			case agentProtocol && addonProtocol:
				return errors.New("--agent and --addon print different documents; choose one")
			case agentProtocol:
				doc, err = agent.NewServer(nil, updates.Build().Version, nil).OpenAPI()
			case addonProtocol:
				doc, err = hooks.ProtocolDocument()
			default:
				// Modules must not use their services while registering
				// operations, so empty services are enough here.
				doc, err = api.New(api.Options{Modules: modules.Default(), Services: &modules.Services{}}).OpenAPI()
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", doc)
			return err
		},
	}
	cmd.Flags().BoolVar(&agentProtocol, "agent", false, "print the agent protocol")
	cmd.Flags().BoolVar(&addonProtocol, "addon", false, "print the add-on protocol")
	return cmd
}

func newVersionCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the version of this binary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b := updates.Build()
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(b)
			}
			fmt.Fprintf(out, "racksail %s (%s channel)\n", b.Version, b.Channel)
			if b.Commit != "" {
				fmt.Fprintf(out, "commit %s", b.Commit)
				if b.CommitTime != "" {
					fmt.Fprintf(out, ", committed %s", b.CommitTime)
				}
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "%s %s/%s\n", b.GoVersion, b.OS, b.Arch)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
