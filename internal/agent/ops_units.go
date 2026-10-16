package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// EnsureUnitInput is a unit file to install.
type EnsureUnitInput struct {
	Name    string `json:"name" doc:"Unit name, such as racksail-site-blog.service"`
	Content []byte `json:"content,omitempty" doc:"The unit file"`
}

// Validate implements Input.
func (in EnsureUnitInput) Validate() error {
	_, err := checkUnitFile(in.Name, in.Content)
	return err
}

// EnsureUnit installs or updates a unit file and reloads systemd when it
// changed. It doesn't start or enable the unit.
var EnsureUnit = Register(Def[EnsureUnitInput, Result]{
	Name:    "EnsureUnit",
	Summary: "Install a systemd unit",
	Description: "Writes a racksail-* unit file and reloads systemd when the file changed. The file may only use the sections and " +
		"directives Racksail renders; services run as an account, in the sandbox accounts get, or one of Racksail's service users, " +
		"never as root, and nothing else runs commands. Mount units mount data disks only, as EnsureMounted does. " +
		"Racksail's own units are installed with InstallPlatformUnits.",
	Effects: func(in EnsureUnitInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + in.Name, DiskBytes: int64(len(in.Content))}}
	},
	Run: func(ctx context.Context, env *Env, in EnsureUnitInput) (Result, error) {
		unit, err := checkUnitFile(in.Name, in.Content)
		if err != nil {
			return Result{}, err
		}
		if err := checkServiceUser(ctx, env.System.Users, unit.UnitIdentity, env.Check); err != nil {
			return Result{}, fmt.Errorf("service %s: %w: %w", in.Name, err, system.ErrInvalid)
		}
		if unit.Mount != nil {
			if _, err := checkDataDevice(ctx, env.System, *unit.Mount, env.Check); err != nil {
				return Result{}, fmt.Errorf("mount %s: %w", in.Name, err)
			}
		}
		units := env.System.Units
		current, err := units.ReadUnit(ctx, in.Name)
		if err == nil && bytes.Equal(current, in.Content) {
			env.remember(ctx, ledgerUnit, in.Name, in.Content)
			return Unchanged(), nil
		}
		if err != nil && !errors.Is(err, system.ErrNotFound) {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + in.Name, DiskBytes: int64(len(in.Content))}
		if env.Check {
			return Changed(effect), nil
		}
		if err := units.WriteUnit(ctx, in.Name, in.Content); err != nil {
			return Result{}, err
		}
		env.remember(ctx, ledgerUnit, in.Name, in.Content)
		return Changed(effect), units.DaemonReload(ctx)
	},
})

// UnitInput names one unit.
type UnitInput struct {
	Name string `json:"name" doc:"Unit name"`
}

// Validate implements Input.
func (in UnitInput) Validate() error { return checkUnit(in.Name) }

// RemoveUnit stops, disables and removes a unit file Racksail installed.
var RemoveUnit = Register(Def[UnitInput, Result]{
	Name:    "RemoveUnit",
	Summary: "Remove a systemd unit",
	Effects: func(in UnitInput) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "remove", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in UnitInput) (Result, error) {
		if err := checkOwnedUnit(in.Name); err != nil {
			return Result{}, err
		}
		if system.IsPlatformUnit(in.Name) {
			return Result{}, ErrNotAllowed.New("op", "RemoveUnit", "target", in.Name)
		}
		units := env.System.Units
		if _, err := units.ReadUnit(ctx, in.Name); errors.Is(err, system.ErrNotFound) {
			env.forget(ctx, in.Name)
			return Unchanged(), nil
		} else if err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectService, Action: "remove", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		st, err := units.Status(ctx, in.Name)
		if err != nil {
			return Result{}, err
		}
		if st.Active() {
			if err := units.Stop(ctx, in.Name); err != nil {
				return Result{}, err
			}
		}
		if st.UnitFileState == "enabled" {
			if err := units.Disable(ctx, in.Name); err != nil {
				return Result{}, err
			}
		}
		if err := units.RemoveUnit(ctx, in.Name); err != nil {
			return Result{}, err
		}
		env.forget(ctx, in.Name)
		return Changed(effect), units.DaemonReload(ctx)
	},
})

var dropInNamePattern = regexp.MustCompile(`^racksail[A-Za-z0-9_.-]{0,56}$`)

// DropInInput is a drop-in for a unit Racksail doesn't own.
type DropInInput struct {
	Unit    string `json:"unit" doc:"Unit the drop-in adjusts"`
	Name    string `json:"name" doc:"Drop-in name, starting with racksail"`
	Content []byte `json:"content,omitempty" doc:"The drop-in file; ignored when removing"`
}

// Validate implements Input. Drop-ins tune resources, restarts and
// dependencies; they never change what a unit runs, as whom or with which
// environment. Racksail's own units and the root system services it renders
// take none.
func (in DropInInput) Validate() error {
	if err := checkAdjustableUnit(in.Unit); err != nil {
		return err
	}
	if !dropInNamePattern.MatchString(in.Name) {
		return fmt.Errorf("drop-in name %q must start with racksail", in.Name)
	}
	if len(in.Content) > 64<<10 {
		return fmt.Errorf("drop-in is larger than 64 KiB")
	}
	return system.CheckDropIn(in.Unit, in.Content, unitRules())
}

// EnsureDropIn installs a drop-in that adjusts a unit, such as resource
// limits for a distribution service.
var EnsureDropIn = Register(Def[DropInInput, Result]{
	Name:    "EnsureDropIn",
	Summary: "Adjust a systemd unit with a drop-in",
	Effects: func(in DropInInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + in.Unit + ".d/" + in.Name + ".conf"}}
	},
	Run: func(ctx context.Context, env *Env, in DropInInput) (Result, error) {
		units := env.System.Units
		target := system.UnitDir + "/" + in.Unit + ".d/" + in.Name + ".conf"
		if current, err := env.System.Files.ReadFile(ctx, target, 64<<10); err == nil && bytes.Equal(current, in.Content) {
			env.remember(ctx, ledgerDropIn, target, in.Content)
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectFile, Action: "write", Target: target}
		if env.Check {
			return Changed(effect), nil
		}
		if err := units.WriteDropIn(ctx, in.Unit, in.Name, in.Content); err != nil {
			return Result{}, err
		}
		env.remember(ctx, ledgerDropIn, target, in.Content)
		return Changed(effect), units.DaemonReload(ctx)
	},
})

// RemoveDropIn removes a drop-in Racksail installed.
var RemoveDropIn = Register(Def[DropInInput, Result]{
	Name:    "RemoveDropIn",
	Summary: "Remove a drop-in",
	Effects: func(in DropInInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "delete", Target: system.UnitDir + "/" + in.Unit + ".d/" + in.Name + ".conf"}}
	},
	Run: func(ctx context.Context, env *Env, in DropInInput) (Result, error) {
		units := env.System.Units
		target := system.UnitDir + "/" + in.Unit + ".d/" + in.Name + ".conf"
		if _, err := env.System.Files.Stat(ctx, target); errors.Is(err, system.ErrNotFound) {
			env.forget(ctx, target)
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectFile, Action: "delete", Target: target}
		if env.Check {
			return Changed(effect), nil
		}
		if err := units.RemoveDropIn(ctx, in.Unit, in.Name); err != nil {
			return Result{}, err
		}
		env.forget(ctx, target)
		return Changed(effect), units.DaemonReload(ctx)
	},
})

// unitControl builds the start, stop, restart and reload operations.
func unitControl(name, summary, action string, needed func(system.UnitState) bool, do func(system.Units, context.Context, string) error) *Op[UnitInput, Result] {
	return Register(Def[UnitInput, Result]{
		Name:    name,
		Summary: summary,
		Effects: func(in UnitInput) []system.Effect {
			return []system.Effect{{Type: system.EffectService, Action: action, Target: in.Name}}
		},
		Run: func(ctx context.Context, env *Env, in UnitInput) (Result, error) {
			units := env.System.Units
			st, err := units.Status(ctx, in.Name)
			if err != nil {
				return Result{}, err
			}
			if st.LoadState == "not-found" {
				return Result{}, fmt.Errorf("unit %s: %w", in.Name, system.ErrNotFound)
			}
			if !needed(st) {
				return Unchanged(), nil
			}
			effect := system.Effect{Type: system.EffectService, Action: action, Target: in.Name, MemoryBytes: st.MemoryBytes}
			if env.Check {
				return Changed(effect), nil
			}
			return Changed(effect), do(units, ctx, in.Name)
		},
	})
}

// Unit control operations. Start and stop do nothing when the unit is
// already in that state; restart and reload always act.
var (
	StartUnit = unitControl("StartUnit", "Start a unit", "start",
		func(s system.UnitState) bool { return !s.Active() }, system.Units.Start)
	StopUnit = unitControl("StopUnit", "Stop a unit", "stop",
		func(s system.UnitState) bool { return s.Active() || s.ActiveState == "activating" }, system.Units.Stop)
	RestartUnit = unitControl("RestartUnit", "Restart a unit", "restart",
		func(system.UnitState) bool { return true }, system.Units.Restart)
	ReloadUnit = unitControl("ReloadUnit", "Reload a unit's configuration", "reload",
		func(system.UnitState) bool { return true }, system.Units.Reload)
)

// UnitsInput names several units.
type UnitsInput struct {
	Names []string `json:"names" doc:"Unit names"`
}

// Validate implements Input.
func (in UnitsInput) Validate() error {
	if len(in.Names) == 0 {
		return fmt.Errorf("no units given")
	}
	for _, n := range in.Names {
		if err := checkUnit(n); err != nil {
			return err
		}
	}
	return nil
}

func unitEnablement(name, summary, action string, enable bool) *Op[UnitsInput, Result] {
	return Register(Def[UnitsInput, Result]{
		Name:    name,
		Summary: summary,
		Effects: func(in UnitsInput) []system.Effect {
			var out []system.Effect
			for _, n := range in.Names {
				out = append(out, system.Effect{Type: system.EffectService, Action: action, Target: n})
			}
			return out
		},
		Run: func(ctx context.Context, env *Env, in UnitsInput) (Result, error) {
			units := env.System.Units
			var todo []string
			var effects []system.Effect
			for _, n := range in.Names {
				if !enable && n == system.AgentUnit {
					// Every server runs an agent and nothing else starts it again,
					// so a panel never keeps it from starting at the next boot.
					return Result{}, ErrNotAllowed.New("op", name, "target", n)
				}
				st, err := units.Status(ctx, n)
				if err != nil {
					return Result{}, err
				}
				if (st.UnitFileState == "enabled") != enable {
					todo = append(todo, n)
					effects = append(effects, system.Effect{Type: system.EffectService, Action: action, Target: n})
				}
			}
			if len(todo) == 0 {
				return Unchanged(), nil
			}
			if env.Check {
				return Changed(effects...), nil
			}
			var err error
			if enable {
				err = units.Enable(ctx, todo...)
			} else {
				err = units.Disable(ctx, todo...)
			}
			return Changed(effects...), err
		},
	})
}

// Unit enablement operations.
var (
	EnableUnits  = unitEnablement("EnableUnits", "Start units at boot", "enable", true)
	DisableUnits = unitEnablement("DisableUnits", "Stop starting units at boot", "disable", false)
)

// UnitStatusInput names any unit, including ones Racksail doesn't manage.
type UnitStatusInput struct {
	Name string `json:"name" doc:"Unit name"`
}

// Validate implements Input.
func (in UnitStatusInput) Validate() error {
	if !system.ValidUnitName(in.Name) {
		return fmt.Errorf("%q is not a unit name", in.Name)
	}
	return nil
}

// UnitStatus reports the state of a unit.
var UnitStatus = Register(Def[UnitStatusInput, system.UnitState]{
	Name:     "UnitStatus",
	Summary:  "Report the state of a unit",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in UnitStatusInput) (system.UnitState, error) {
		return env.System.Units.Status(ctx, in.Name)
	},
})

// ListUnitsInput selects units by glob patterns.
type ListUnitsInput struct {
	Patterns []string `json:"patterns" doc:"Glob patterns such as racksail-*"`
}

var unitPatternPattern = regexp.MustCompile(`^[A-Za-z0-9:_.@*?\[\]-]{1,128}$`)

// Validate implements Input.
func (in ListUnitsInput) Validate() error {
	if len(in.Patterns) == 0 {
		return fmt.Errorf("give at least one pattern")
	}
	for _, p := range in.Patterns {
		if !unitPatternPattern.MatchString(p) {
			return fmt.Errorf("unit pattern %q", p)
		}
	}
	return nil
}

// UnitList lists units.
type UnitList struct {
	Units []system.UnitState `json:"units"`
}

// ListUnits lists units matching patterns.
var ListUnits = Register(Def[ListUnitsInput, UnitList]{
	Name:     "ListUnits",
	Summary:  "List units",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in ListUnitsInput) (UnitList, error) {
		units, err := env.System.Units.List(ctx, in.Patterns...)
		if units == nil {
			units = []system.UnitState{}
		}
		return UnitList{Units: units}, err
	},
})

// SetUnitPropertiesInput changes resource limits of a running unit.
type SetUnitPropertiesInput struct {
	Name       string                `json:"name" doc:"Unit name"`
	Runtime    bool                  `json:"runtime,omitempty" doc:"Don't keep the change across reboots"`
	Properties []system.UnitProperty `json:"properties" doc:"Properties to set"`
}

// Validate implements Input. A persistent change is kept as a drop-in, so
// the units it may reach are those a drop-in may adjust: never Racksail's
// own units, which a tiny limit on would leave the server unmanageable
// until someone fixed it at the console, nor the root system services.
func (in SetUnitPropertiesInput) Validate() error {
	if err := checkAdjustableUnit(in.Name); err != nil {
		return err
	}
	if len(in.Properties) == 0 {
		return fmt.Errorf("no properties given")
	}
	for _, p := range in.Properties {
		if !slices.Contains(system.ResourceControlProperties, p.Name) {
			return fmt.Errorf("property %s isn't a resource limit", p.Name)
		}
	}
	return nil
}

// SetUnitProperties applies resource limits, such as plan limits on an
// account slice, without restarting anything. It reports no change only
// when the limits are in effect with the asked values and, for a change
// kept across reboots, are also what the unit's configuration keeps, so
// running a plan twice does nothing and a limit is never left to the next
// reboot.
var SetUnitProperties = Register(Def[SetUnitPropertiesInput, Result]{
	Name:    "SetUnitProperties",
	Summary: "Change the resource limits of a unit",
	Description: "Sets resource-control limits of a unit Racksail manages, as systemctl set-property does: at once, and unless runtime " +
		"is set, also in a drop-in that outlives a reboot. Racksail's own units and the root system services can't be adjusted.",
	Effects: func(in SetUnitPropertiesInput) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "limit", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in SetUnitPropertiesInput) (Result, error) {
		// A read error leaves a view empty, so every property counts as a
		// change and set-property runs, reporting any real problem itself.
		// The live values must match for either kind of change, so a runtime
		// override with another value is replaced; a persistent change must
		// also match what survives a reboot, so it still writes its drop-in
		// when a runtime override is all that currently matches.
		live, _ := env.System.Units.ResourceControl(ctx, in.Name, true)
		var kept map[string]string
		if !in.Runtime {
			kept, _ = env.System.Units.ResourceControl(ctx, in.Name, false)
		}
		var todo []system.UnitProperty
		for _, p := range in.Properties {
			want, ok := system.CanonicalLimit(p.Name, p.Value)
			if !ok || live[p.Name] != want || (!in.Runtime && kept[p.Name] != want) {
				todo = append(todo, p)
			}
		}
		if len(todo) == 0 {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectService, Action: "limit", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Units.SetProperties(ctx, in.Name, in.Runtime, todo...)
	},
})

// KillUnitInput sends a signal to a unit's processes.
type KillUnitInput struct {
	Name   string        `json:"name" doc:"Unit name"`
	Signal system.Signal `json:"signal" enum:"TERM,KILL,HUP,INT,USR1,USR2" doc:"Signal to send"`
}

// Validate implements Input.
func (in KillUnitInput) Validate() error {
	if err := checkUnit(in.Name); err != nil {
		return err
	}
	if !in.Signal.Valid() {
		return fmt.Errorf("signal %q isn't allowed", in.Signal)
	}
	return nil
}

// KillUnit signals the processes of a unit, for example HUP to reopen logs.
var KillUnit = Register(Def[KillUnitInput, Result]{
	Name:    "KillUnit",
	Summary: "Send a signal to a unit",
	Effects: func(in KillUnitInput) []system.Effect {
		return []system.Effect{{Type: system.EffectProcess, Action: "signal", Target: in.Name, Detail: string(in.Signal)}}
	},
	Run: func(ctx context.Context, env *Env, in KillUnitInput) (Result, error) {
		effect := system.Effect{Type: system.EffectProcess, Action: "signal", Target: in.Name, Detail: string(in.Signal)}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Units.Kill(ctx, in.Name, in.Signal)
	},
})
