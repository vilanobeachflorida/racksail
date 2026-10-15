package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Racksail's own units run as root or as the platform's users by design,
// so the panel never writes them: the agent renders them itself from the
// path of the racksail executable, which has to be one only root can
// change.

// PlatformUnitsInput names the executable Racksail's services run.
type PlatformUnitsInput struct {
	Binary string `json:"binary" doc:"Path of the racksail executable, such as /usr/bin/racksail"`
}

// Validate implements Input.
func (in PlatformUnitsInput) Validate() error { return checkPlatformBinary(in.Binary) }

// checkPlatformBinary refuses executables the panel could have put in place:
// the file must be called racksail and lie outside every location the agent
// writes or reads for the panel.
func checkPlatformBinary(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	if path.Base(p) != "racksail" || strings.ContainsAny(p, " \t%$\"'\\") {
		return fmt.Errorf("the services run an executable called racksail, not %s", p)
	}
	policy.RLock()
	defer policy.RUnlock()
	if under(p, policy.writeRoots) || under(p, policy.readRoots) {
		return fmt.Errorf("%s is in a location the panel can change through the agent", p)
	}
	return nil
}

// checkRootOnly makes sure only root can change the executable: it and
// every directory above it belong to root and nobody else may write them.
func checkRootOnly(ctx context.Context, files system.Files, p string) error {
	info, err := files.Stat(ctx, p)
	if err != nil {
		return err
	}
	if !info.Mode.IsRegular() || info.Perm()&0o111 == 0 {
		return fmt.Errorf("%s isn't an executable file: %w", p, system.ErrInvalid)
	}
	for q := p; ; q = path.Dir(q) {
		if q != p {
			if info, err = files.Stat(ctx, q); err != nil {
				return err
			}
		}
		if info.UID != 0 || info.Perm()&0o022 != 0 {
			return fmt.Errorf("%s can be changed by users other than root: %w", q, system.ErrInvalid)
		}
		if q == "/" {
			return nil
		}
	}
}

// InstallPlatformUnits writes Racksail's own slices and services, rendered
// by the agent, and reloads systemd when one changed. It doesn't enable or
// start them.
var InstallPlatformUnits = Register(Def[PlatformUnitsInput, Result]{
	Name:    "InstallPlatformUnits",
	Summary: "Install Racksail's own services",
	Description: "Renders racksail.slice, racksail-accounts.slice and the agent, panel and edge units for the given racksail executable, " +
		"which only root may be able to change, and writes the ones that differ.",
	Effects: func(in PlatformUnitsInput) []system.Effect {
		var out []system.Effect
		for _, name := range system.PlatformUnitNames() {
			out = append(out, system.Effect{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + name})
		}
		return out
	},
	Run: func(ctx context.Context, env *Env, in PlatformUnitsInput) (Result, error) {
		if err := checkRootOnly(ctx, env.System.Files, in.Binary); err != nil {
			return Result{}, err
		}
		rendered, err := system.PlatformUnits(in.Binary)
		if err != nil {
			return Result{}, err
		}
		units := env.System.Units
		var effects []system.Effect
		var todo []string
		for _, name := range system.PlatformUnitNames() {
			current, err := units.ReadUnit(ctx, name)
			if err == nil && bytes.Equal(current, rendered[name]) {
				env.remember(ctx, ledgerUnit, name, current)
				continue
			}
			if err != nil && !errors.Is(err, system.ErrNotFound) {
				return Result{}, err
			}
			todo = append(todo, name)
			effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: system.UnitDir + "/" + name, DiskBytes: int64(len(rendered[name]))})
		}
		if len(todo) == 0 {
			return Unchanged(), nil
		}
		if env.Check {
			return Changed(effects...), nil
		}
		for _, name := range todo {
			if err := units.WriteUnit(ctx, name, rendered[name]); err != nil {
				return Result{}, err
			}
			env.remember(ctx, ledgerUnit, name, rendered[name])
		}
		return Changed(effects...), units.DaemonReload(ctx)
	},
})
