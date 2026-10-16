package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/installer"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/linux"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func newInstallCommand() *cobra.Command {
	var answersPath, binary string
	var dryRun, simulated, convert bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Turn this Debian 13 server into a Racksail server, from an answers file",
		Long: "Reads an answers file (see spec/schemas/answers.schema.json) and carries it out. The convert mode " +
			"sets up the running Debian 13 system: Racksail's users, directories and services; --convert does that " +
			"without an answers file. The install and restore modes erase disks, so they run from the Racksail " +
			"installer image. --dry-run shows the plan without changing anything.",
		Example: "  racksail install --convert --dry-run\n  sudo racksail install --convert\n" +
			"  sudo racksail install --answers answers.yaml",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			switch {
			case answersPath == "" && convert:
				data = []byte("format: 1\nmode: convert\n")
			case answersPath == "":
				return errors.New("give an answers file with --answers, or use --convert to set up this server as it is")
			case answersPath == "-":
				data, err = io.ReadAll(cmd.InOrStdin())
			default:
				data, err = os.ReadFile(answersPath)
			}
			if err != nil {
				return err
			}
			a, err := installer.ParseAnswers(data)
			if err != nil {
				return err
			}
			if convert && a.Mode != installer.ModeConvert {
				return fmt.Errorf("--convert sets up this server as it is, but the answers file is for the %s mode", a.Mode)
			}
			if a.Mode != installer.ModeConvert {
				return fmt.Errorf("the %s mode erases disks, so it runs from the Racksail installer image, not on a running system; to set up this server as it is, use mode: convert", a.Mode)
			}
			if binary == "" && simulated {
				// The simulated server has Racksail where its package puts it.
				binary = "/usr/bin/racksail"
			}
			if binary == "" {
				if binary, err = os.Executable(); err != nil {
					return err
				}
				if binary, err = filepath.EvalSymlinks(binary); err != nil {
					return err
				}
			}
			steps, err := installer.ConvertSteps(a, binary)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintln(out, "Converting this server would:")
				for i, s := range steps {
					fmt.Fprintf(out, "%d. %s\n", i+1, s.Title)
					effects, err := agent.PlannedEffects(s.Ops)
					if err != nil {
						return err
					}
					for _, e := range effects {
						fmt.Fprintf(out, "   %s %s %s\n", e.Type, e.Action, e.Target)
					}
				}
				return nil
			}
			var backend *system.Backend
			if simulated {
				backend = sim.New(sim.Options{}).Backend()
			} else {
				if err := requireRoot("converting a server"); err != nil {
					return err
				}
				if backend, err = linux.New(cmd.Context()); err != nil {
					return err
				}
			}
			client := agent.NewLocal(backend, nil)
			return installer.Convert(cmd.Context(), client, steps, func(i int, s installer.Step) {
				fmt.Fprintf(out, "[%d/%d] %s\n", i+1, len(steps), s.Title)
			})
		},
	}
	cmd.Flags().StringVar(&answersPath, "answers", "", "answers file, or - to read it from standard input")
	cmd.Flags().BoolVar(&convert, "convert", false, "set up this running Debian 13 system as it is; an answers file is then optional")
	cmd.Flags().StringVar(&binary, "binary", "", "path the services run racksail from; this executable by default")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan without changing anything")
	cmd.Flags().BoolVar(&simulated, "sim", false, "carry out the plan on a simulated server")
	return cmd
}
