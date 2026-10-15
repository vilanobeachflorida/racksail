package installer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Step is one step of an installation, with the agent operations that carry
// it out.
type Step struct {
	Title string       `json:"title"`
	Ops   []agent.Step `json:"ops"`
}

// Engine installs Racksail. The installer image provides one that also
// partitions disks, bootstraps Debian and installs the bootloader.
type Engine interface {
	Plan(ctx context.Context, a *Answers) ([]Step, error)
	Run(ctx context.Context, steps []Step, progress func(i int, s Step)) error
}

// directories are created on every Racksail server.
var directories = []agent.EnsureDirInput{
	{Path: system.ConfigDir, Mode: 0o755},
	{Path: system.GeneratedDir, Mode: 0o755},
	{Path: system.CertsDir, Mode: 0o755},
	{Path: system.StateDir, Mode: 0o750, Owner: system.Owner{User: "racksail", Group: "racksail"}},
	{Path: "/srv/racksail", Mode: 0o755},
	{Path: system.AccountsDir, Mode: 0o755},
	{Path: "/opt/racksail", Mode: 0o755},
	{Path: system.RuntimesDir, Mode: 0o755},
	{Path: system.ModulesDir, Mode: 0o755},
	{Path: "/var/log/racksail", Mode: 0o750},
	{Path: system.DataDisksDir, Mode: 0o755},
}

// ConvertSteps plans turning a running Debian 13 system into a Racksail
// server: system users, directories, Racksail's own units, and starting the
// agent, the panel and the edge's socket. The edge itself starts later, when
// the panel first sends it routes.
func ConvertSteps(a *Answers, binary string) ([]Step, error) {
	if a.Mode != ModeConvert {
		return nil, fmt.Errorf("ConvertSteps plans the convert mode, not %s", a.Mode)
	}
	units, err := Units(binary)
	if err != nil {
		return nil, err
	}
	var steps []Step
	steps = append(steps, Step{Title: "Create the Racksail system users", Ops: []agent.Step{
		agent.EnsureUser.Step(agent.EnsureUserInput{Name: "racksail", System: true, Home: system.StateDir, Comment: "Racksail panel", Locked: true}),
		agent.EnsureUser.Step(agent.EnsureUserInput{Name: "racksail-edge", System: true, Home: "/nonexistent", Comment: "Racksail edge", Locked: true}),
	}})
	var dirs []agent.Step
	for _, d := range directories {
		dirs = append(dirs, agent.EnsureDir.Step(d))
	}
	steps = append(steps, Step{Title: "Create Racksail's directories", Ops: dirs})
	if a.Hostname != "" {
		short, _, _ := strings.Cut(a.Hostname, ".")
		steps = append(steps, Step{Title: "Set the hostname", Ops: []agent.Step{agent.SetHostname.Step(agent.HostnameInput{Name: short})}})
	}
	var unitOps []agent.Step
	for _, name := range UnitNames() {
		unitOps = append(unitOps, agent.EnsureUnit.Step(agent.EnsureUnitInput{Name: name, Content: units[name]}))
	}
	steps = append(steps, Step{Title: "Install Racksail's services", Ops: unitOps})
	steps = append(steps, Step{Title: "Start the agent, the panel and the edge's socket", Ops: []agent.Step{
		agent.EnableUnits.Step(agent.UnitsInput{Names: []string{AgentUnit, PanelUnit, EdgeSocket}}),
		agent.StartUnit.Step(agent.UnitInput{Name: AgentUnit}),
		agent.StartUnit.Step(agent.UnitInput{Name: PanelUnit}),
		agent.StartUnit.Step(agent.UnitInput{Name: EdgeSocket}),
	}})
	return steps, nil
}

// ErrNotDebian13 is returned when converting anything but Debian 13.
var ErrNotDebian13 = errors.New("this machine doesn't run Debian 13 (trixie), which Racksail needs")

// Convert checks that the machine runs Debian 13 and then runs the steps
// through the agent client, which must be backed by the local machine.
func Convert(ctx context.Context, c agent.Client, steps []Step, progress func(i int, s Step)) error {
	facts, err := agent.GetFacts.Call(ctx, c, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		return err
	}
	if !facts.OS.IsDebian13() {
		return fmt.Errorf("%w (%s %s)", ErrNotDebian13, facts.OS.ID, facts.OS.VersionID)
	}
	for i, s := range steps {
		if progress != nil {
			progress(i, s)
		}
		if _, err := agent.Execute(ctx, c, s.Ops, agent.CallOptions{}, nil); err != nil {
			return fmt.Errorf("%s: %w", s.Title, err)
		}
	}
	return nil
}
