package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/system/linux"
	"github.com/vilanobeachflorida/racksail/internal/updates"
)

// newSafeModeCommand turns safe mode on and off from the server's console,
// where it helps most: when an add-on keeps the panel from working.
func newSafeModeCommand() *cobra.Command {
	var configPath string
	stateDir := func() (string, error) {
		cfg, err := loadConfig(configPath)
		if err != nil {
			return "", err
		}
		return cfg.Paths.State, nil
	}
	cmd := &cobra.Command{
		Use:   "safe-mode",
		Short: "Stop every add-on, or let them run again",
		Long: "Safe mode stops every add-on and keeps them out of every change, so a misbehaving add-on can't keep the panel from " +
			"working. The panel notices within a few seconds. Safe mode also comes on from the racksail.safe_mode boot option, and on " +
			"its own when an add-on keeps crashing as it starts.",
		Args: cobra.NoArgs,
	}
	addConfigFlag(cmd, &configPath)
	var reason string
	on := &cobra.Command{
		Use: "on", Short: "Turn safe mode on", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := stateDir()
			if err != nil {
				return err
			}
			path := addons.ConsoleFlag(dir)
			if err := addons.WriteFlag(path, reason, time.Now()); err != nil {
				return err
			}
			// The panel runs as racksail and reads the flag for its reason.
			if u, err := user.Lookup("racksail"); err == nil && os.Geteuid() == 0 {
				uid, _ := strconv.Atoi(u.Uid)
				gid, _ := strconv.Atoi(u.Gid)
				os.Chown(path, uid, gid)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Safe mode is on. Add-ons stop within a few seconds; racksail safe-mode off lets them run again.")
			return nil
		},
	}
	on.Flags().StringVar(&reason, "reason", "Turned on from the console.", "why, for the panel and the audit log")
	off := &cobra.Command{
		Use: "off", Short: "Turn safe mode off", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := stateDir()
			if err != nil {
				return err
			}
			sm := addons.OpenSafeMode(dir, addons.ReadCmdline())
			if err := sm.Leave(); err != nil {
				return err
			}
			if sm.Active() {
				fmt.Fprintf(cmd.OutOrStdout(), "Safe mode stays on until the server boots without the %s boot option.\n", addons.BootOption)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Safe mode is off. Add-ons start again within a few seconds.")
			return nil
		},
	}
	status := &cobra.Command{
		Use: "status", Short: "Say whether safe mode is on, and why", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := stateDir()
			if err != nil {
				return err
			}
			st := addons.OpenSafeMode(dir, addons.ReadCmdline()).Status()
			out := cmd.OutOrStdout()
			if !st.Active {
				fmt.Fprintln(out, "Safe mode is off.")
				return nil
			}
			fmt.Fprintln(out, "Safe mode is on:")
			for _, s := range st.Sources {
				line := "  " + s.Kind
				if s.Reason != "" {
					line += ": " + s.Reason
				}
				if !s.Since.IsZero() {
					line += " (since " + s.Since.Local().Format(time.DateTime) + ")"
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	cmd.AddCommand(on, off, status)
	return cmd
}

// newUpdateCommand holds what the agent runs around a self-update.
func newUpdateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Finish or undo a self-update",
		Long:  "The panel installs updates itself. These commands are what the agent runs around an update; they're here for when the panel can't.",
		Args:  cobra.NoArgs,
	}
	var now bool
	guard := &cobra.Command{
		Use:   "guard",
		Short: "Roll back an update the new release didn't confirm",
		Long: "The agent runs this from the binary an update replaced, fifteen minutes after the update, as a transient systemd unit. " +
			"If the new release hasn't confirmed it works by then, it stops the panel, puts back the panel's database and the binary " +
			"from before, and starts Racksail again. --now does so at once, as a rollback from the panel does.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot("the update guard"); err != nil {
				return err
			}
			backend, err := linux.New(cmd.Context())
			if err != nil {
				return err
			}
			rolled, err := updates.Guard(cmd.Context(), backend, now)
			if err != nil {
				return err
			}
			if rolled {
				fmt.Fprintf(cmd.OutOrStdout(), "Rolled back the last update; its record is in %s.\n", updates.StateFile)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to roll back.")
			}
			return nil
		},
	}
	guard.Flags().BoolVar(&now, "now", false, "roll back at once")
	cmd.AddCommand(guard)
	return cmd
}
