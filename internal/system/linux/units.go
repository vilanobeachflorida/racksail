package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Systemd is the part of systemd's D-Bus API the units adapter needs.
type Systemd interface {
	DaemonReload(ctx context.Context) error
	// Start, Stop, Restart and Reload queue a job and wait for its result.
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
	Reload(ctx context.Context, name string) error
	Enable(ctx context.Context, names []string) error
	Disable(ctx context.Context, names []string) error
	// Properties returns the unit's properties, merged with the type-specific
	// ones (for example Service) when the unit is loaded.
	Properties(ctx context.Context, name string) (map[string]any, error)
	// List returns the names of loaded units and installed unit files that
	// match any of the patterns.
	List(ctx context.Context, patterns []string) ([]string, error)
	Kill(ctx context.Context, name string, signal int32) error
	ResetFailed(ctx context.Context, name string) error
}

// Linux signal numbers, which differ from other systems for USR1 and USR2.
var signalNumbers = map[system.Signal]int32{
	system.SIGHUP: 1, system.SIGINT: 2, system.SIGKILL: 9, system.SIGUSR1: 10, system.SIGUSR2: 12, system.SIGTERM: 15,
}

type units struct {
	h       *host
	systemd Systemd
}

func checkUnit(name string) error {
	if !system.ValidUnitName(name) {
		return fmt.Errorf("unit name %q: %w", name, system.ErrInvalid)
	}
	return nil
}

func (u *units) fs() *files { return &files{u.h} }

func (u *units) WriteUnit(ctx context.Context, name string, content []byte) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	return u.fs().WriteFile(ctx, path.Join(system.UnitDir, name), content, 0o644, system.Owner{})
}

func (u *units) ReadUnit(ctx context.Context, name string) ([]byte, error) {
	if err := checkUnit(name); err != nil {
		return nil, err
	}
	return u.fs().ReadFile(ctx, path.Join(system.UnitDir, name), 1<<20)
}

// RemoveUnit removes the unit file with its drop-ins, the ones set-property
// wrote included, so a unit of the same name made later doesn't inherit
// the limits of this one.
func (u *units) RemoveUnit(ctx context.Context, name string) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	if err := u.fs().Remove(ctx, path.Join(system.UnitDir, name)); err != nil {
		return err
	}
	for _, dir := range []string{system.UnitDir, system.ControlDir, system.RuntimeControlDir} {
		err := u.fs().RemoveAll(ctx, path.Join(dir, name+".d"))
		if err != nil && !errors.Is(err, system.ErrNotFound) {
			return err
		}
	}
	return nil
}

var dropInPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (u *units) WriteDropIn(ctx context.Context, unit, name string, content []byte) error {
	if err := checkUnit(unit); err != nil {
		return err
	}
	if !dropInPattern.MatchString(name) {
		return fmt.Errorf("drop-in name %q: %w", name, system.ErrInvalid)
	}
	dir := path.Join(system.UnitDir, unit+".d")
	if err := u.fs().Mkdir(ctx, dir, 0o755, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
		return err
	}
	return u.fs().WriteFile(ctx, path.Join(dir, name+".conf"), content, 0o644, system.Owner{})
}

func (u *units) RemoveDropIn(ctx context.Context, unit, name string) error {
	if err := checkUnit(unit); err != nil {
		return err
	}
	if !dropInPattern.MatchString(name) {
		return fmt.Errorf("drop-in name %q: %w", name, system.ErrInvalid)
	}
	return u.fs().Remove(ctx, path.Join(system.UnitDir, unit+".d", name+".conf"))
}

func (u *units) DaemonReload(ctx context.Context) error { return u.systemd.DaemonReload(ctx) }

func (u *units) Start(ctx context.Context, name string) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	return u.systemd.Start(ctx, name)
}

func (u *units) Stop(ctx context.Context, name string) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	return u.systemd.Stop(ctx, name)
}

func (u *units) Restart(ctx context.Context, name string) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	return u.systemd.Restart(ctx, name)
}

func (u *units) Reload(ctx context.Context, name string) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	return u.systemd.Reload(ctx, name)
}

func (u *units) Enable(ctx context.Context, names ...string) error {
	for _, n := range names {
		if err := checkUnit(n); err != nil {
			return err
		}
	}
	return u.systemd.Enable(ctx, names)
}

func (u *units) Disable(ctx context.Context, names ...string) error {
	for _, n := range names {
		if err := checkUnit(n); err != nil {
			return err
		}
	}
	return u.systemd.Disable(ctx, names)
}

func (u *units) Status(ctx context.Context, name string) (system.UnitState, error) {
	if err := checkUnit(name); err != nil {
		return system.UnitState{}, err
	}
	props, err := u.systemd.Properties(ctx, name)
	if err != nil {
		return system.UnitState{}, fmt.Errorf("status of %s: %w", name, err)
	}
	return unitStateFromProperties(name, props), nil
}

func (u *units) List(ctx context.Context, patterns ...string) ([]system.UnitState, error) {
	names, err := u.systemd.List(ctx, patterns)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]system.UnitState, 0, len(names))
	for _, n := range names {
		st, err := u.Status(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

var (
	propertyNamePattern  = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}$`)
	propertyValuePattern = regexp.MustCompile(`^[A-Za-z0-9.%:/_+-]{0,128}$`)
)

func (u *units) SetProperties(ctx context.Context, name string, runtime bool, props ...system.UnitProperty) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	args := []string{"set-property"}
	if runtime {
		args = append(args, "--runtime")
	}
	args = append(args, "--", name)
	for _, p := range props {
		if !propertyNamePattern.MatchString(p.Name) || !propertyValuePattern.MatchString(p.Value) {
			return fmt.Errorf("unit property %s=%q: %w", p.Name, p.Value, system.ErrInvalid)
		}
		args = append(args, p.Name+"="+p.Value)
	}
	// systemctl knows how to parse every property's text form, which the
	// D-Bus API would need typed one by one.
	if _, err := u.h.exec(ctx, progSystemctl, args...); err != nil {
		return fmt.Errorf("setting properties of %s: %w", name, err)
	}
	return nil
}

// d-Bus property names of the resource-control limits, in the order of
// system.ResourceControlProperties. CPU quota is kept as microseconds of CPU
// per second, and AllowedCPUs as a bitmask that CanonicalLimit won't reduce,
// so it isn't read here.
var resourceControlProps = map[string]string{
	"MemoryMin": "MemoryMin", "MemoryLow": "MemoryLow", "MemoryHigh": "MemoryHigh",
	"MemoryMax": "MemoryMax", "MemorySwapMax": "MemorySwapMax", "CPUQuota": "CPUQuotaPerSecUSec",
	"CPUWeight": "CPUWeight", "IOWeight": "IOWeight", "TasksMax": "TasksMax",
}

func (u *units) ResourceControl(ctx context.Context, name string, runtime bool) (map[string]string, error) {
	if err := checkUnit(name); err != nil {
		return nil, err
	}
	if !runtime {
		return u.persistentLimits(ctx, name)
	}
	props, err := u.systemd.Properties(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("resource limits of %s: %w", name, err)
	}
	out := map[string]string{}
	for limit, prop := range resourceControlProps {
		v, ok := props[prop]
		if !ok {
			continue
		}
		raw := num(v)
		// systemd reports an unset limit as the maximum of a uint64, and a
		// set one in the base unit CanonicalLimit produces: bytes for memory
		// and tasks, microseconds of CPU per second for the quota, a plain
		// number for the weights.
		if raw == ^uint64(0) {
			out[limit] = "infinity"
		} else {
			out[limit] = strconv.FormatUint(raw, 10)
		}
	}
	return out, nil
}

// persistentLimits reads the resource-control limits a unit keeps across a
// reboot: its configuration as systemd loads it from the directories a
// reboot leaves in place, so not the /run overrides a runtime set-property
// writes. Only the unit file of the highest priority counts, and its
// drop-ins, the set-property ones under system.control included, apply in
// the order of their names (system.LoadUnitConfig).
func (u *units) persistentLimits(ctx context.Context, name string) (map[string]string, error) {
	files, err := system.LoadUnitConfig(name, system.PersistentUnitSearchPath(), unitTree{ctx: ctx, h: u.h})
	if err != nil {
		return nil, err
	}
	return system.UnitResourceLimits(name, files)
}

// unitTree reads unit files and drop-ins under the machine's root for
// system.LoadUnitConfig.
type unitTree struct {
	ctx context.Context
	h   *host
}

func (t unitTree) Lstat(p string) (system.FileInfo, error) {
	if err := checkPath(p); err != nil {
		return system.FileInfo{}, err
	}
	fi, err := os.Lstat(t.h.path(p))
	if err != nil {
		return system.FileInfo{}, mapErr("stat", p, err)
	}
	return t.info(p, fi), nil
}

func (t unitTree) ReadDir(p string) ([]system.FileInfo, error) {
	if err := checkPath(p); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(t.h.path(p))
	if err != nil {
		return nil, mapErr("readdir", p, err)
	}
	out := make([]system.FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			// Removed while the directory was read.
			continue
		}
		out = append(out, t.info(path.Join(p, e.Name()), fi))
	}
	return out, nil
}

func (t unitTree) ReadFile(p string) ([]byte, error) {
	return (&files{t.h}).ReadFile(t.ctx, p, 1<<20)
}

// info describes a unit file by what loading it needs: its type and, for
// a link, its target. Owners don't matter here, so the account files
// aren't read for every entry of a large unit directory.
func (t unitTree) info(p string, fi fs.FileInfo) system.FileInfo {
	info := system.FileInfo{Path: p, Mode: fi.Mode(), Size: fi.Size(), ModTime: fi.ModTime().UTC()}
	if fi.Mode()&fs.ModeSymlink != 0 {
		info.Target, _ = os.Readlink(t.h.path(p))
	}
	return info
}

func (u *units) Kill(ctx context.Context, name string, signal system.Signal) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	n, ok := signalNumbers[signal]
	if !ok {
		return fmt.Errorf("signal %q: %w", signal, system.ErrInvalid)
	}
	return u.systemd.Kill(ctx, name, n)
}

func (u *units) ResetFailed(ctx context.Context, name string) error {
	if err := checkUnit(name); err != nil {
		return err
	}
	return u.systemd.ResetFailed(ctx, name)
}

// unitStateFromProperties converts D-Bus properties to a UnitState.
func unitStateFromProperties(name string, p map[string]any) system.UnitState {
	st := system.UnitState{
		Name:          name,
		Description:   str(p["Description"]),
		LoadState:     str(p["LoadState"]),
		ActiveState:   str(p["ActiveState"]),
		SubState:      str(p["SubState"]),
		UnitFileState: str(p["UnitFileState"]),
		MainPID:       int(num(p["MainPID"])),
		Restarts:      int(num(p["NRestarts"])),
	}
	if mem := num(p["MemoryCurrent"]); mem > 0 && mem < 1<<62 {
		st.MemoryBytes = int64(mem)
	}
	if cpu := num(p["CPUUsageNSec"]); cpu > 0 && cpu < 1<<62 {
		st.CPUTime = time.Duration(cpu)
	}
	if tasks := num(p["TasksCurrent"]); tasks > 0 && tasks < 1<<62 {
		st.Tasks = int(tasks)
	}
	if usec := num(p["ActiveEnterTimestamp"]); usec > 0 && st.ActiveState == "active" {
		st.ActiveSince = time.UnixMicro(int64(usec)).UTC()
	}
	return st
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// num reads the unsigned integer types D-Bus uses. Unset values are the
// maximum of their type, which callers filter out.
func num(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case int64:
		if n > 0 {
			return uint64(n)
		}
	case int32:
		if n > 0 {
			return uint64(n)
		}
	case int:
		if n > 0 {
			return uint64(n)
		}
	}
	return 0
}
