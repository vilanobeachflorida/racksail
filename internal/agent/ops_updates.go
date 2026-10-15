package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// UpgradeInput asks for package upgrades.
type UpgradeInput struct {
	// Full also takes upgrades that add or remove packages, such as a new
	// kernel.
	Full bool `json:"full,omitempty" doc:"Also take upgrades that add or remove packages, such as a new kernel"`
	// Snapshot labels read-only snapshots of the system's subvolumes taken
	// before anything is upgraded, so a bad upgrade can be rolled back.
	Snapshot string `json:"snapshot,omitempty" doc:"Take read-only snapshots of the system first, under this label"`
}

// Validate implements Input.
func (in UpgradeInput) Validate() error {
	if in.Snapshot != "" && !snapshotLabelPattern.MatchString(in.Snapshot) {
		return fmt.Errorf("label %q must be lowercase letters, digits and underscores, starting with a letter", in.Snapshot)
	}
	return nil
}

// UpgradeResult is what an upgrade did.
type UpgradeResult struct {
	Result
	Snapshots []Snapshot `json:"snapshots,omitempty" doc:"Snapshots taken first"`
	// Unprotected system paths aren't on Btrfs, so no snapshot covers them.
	Unprotected []string `json:"unprotected,omitempty"`
}

// systemPaths are what an upgrade changes: the subvolumes holding them are
// snapshotted first.
var systemPaths = []string{"/", "/etc", "/usr", "/var/lib"}

// UpgradePackages installs available updates, leaving held packages alone.
var UpgradePackages = Register(Def[UpgradeInput, UpgradeResult]{
	Name:    "UpgradePackages",
	Summary: "Upgrade packages",
	Description: "Installs every available update that isn't held, after read-only snapshots of the system when a label is given. " +
		"In check mode it lists the updates with their versions and download sizes.",
	Effects: func(UpgradeInput) []system.Effect {
		return []system.Effect{{Type: system.EffectPackage, Action: "upgrade", Target: "apt"}}
	},
	Run: func(ctx context.Context, env *Env, in UpgradeInput) (UpgradeResult, error) {
		pk := env.System.Packages
		ups, err := pk.Upgradable(ctx)
		if err != nil {
			return UpgradeResult{}, err
		}
		held, err := pk.Held(ctx)
		if err != nil {
			return UpgradeResult{}, err
		}
		var effects []system.Effect
		for _, p := range ups {
			if slices.Contains(held, p.Name) {
				continue
			}
			effects = append(effects, system.Effect{
				Type: system.EffectPackage, Action: "upgrade", Target: p.Name, Detail: p.Version,
				Source: p.Origin, DownloadBytes: p.DownloadBytes,
			})
		}
		if len(effects) == 0 {
			return UpgradeResult{Result: Unchanged()}, nil
		}
		var out UpgradeResult
		if in.Snapshot != "" {
			snaps, err := SnapshotPaths.def.Run(ctx, env, SnapshotPathsInput{Paths: systemPaths, Label: in.Snapshot})
			if err != nil {
				return out, fmt.Errorf("snapshotting the system before upgrading: %w", err)
			}
			out.Snapshots, out.Unprotected = snaps.Snapshots, snaps.Unprotected
			effects = append(snaps.Effects, effects...)
		}
		out.Result = Changed(effects...)
		if env.Check {
			return out, nil
		}
		return out, pk.Upgrade(ctx, in.Full)
	},
})

// HoldInput holds packages at their versions, or releases them.
type HoldInput struct {
	Names []string `json:"names" doc:"Package names"`
	Hold  bool     `json:"hold" doc:"Hold the packages; false releases them"`
}

// Validate implements Input.
func (in HoldInput) Validate() error { return checkPackageNames(in.Names) }

// HoldPackages keeps packages at their installed versions, so neither
// automatic nor scheduled upgrades touch them, such as database majors
// waiting for a tested upgrade.
var HoldPackages = Register(Def[HoldInput, Result]{
	Name:    "HoldPackages",
	Summary: "Hold packages at their versions",
	Effects: func(in HoldInput) []system.Effect {
		return holdEffects(in.Names, in.Hold)
	},
	Run: func(ctx context.Context, env *Env, in HoldInput) (Result, error) {
		held, err := env.System.Packages.Held(ctx)
		if err != nil {
			return Result{}, err
		}
		var todo []string
		for _, n := range in.Names {
			if slices.Contains(held, n) != in.Hold {
				todo = append(todo, n)
			}
		}
		if len(todo) == 0 {
			return Unchanged(), nil
		}
		effects := holdEffects(todo, in.Hold)
		if env.Check {
			return Changed(effects...), nil
		}
		return Changed(effects...), env.System.Packages.SetHold(ctx, in.Hold, todo...)
	},
})

func holdEffects(names []string, hold bool) []system.Effect {
	action := "unhold"
	if hold {
		action = "hold"
	}
	var out []system.Effect
	for _, n := range names {
		out = append(out, system.Effect{Type: system.EffectPackage, Action: action, Target: n})
	}
	return out
}

// fileChange is a file an operation keeps in a fixed place.
type fileChange struct {
	path    string
	content []byte
}

// syncFiles writes the files that differ, creating their directories, and
// returns what changed. In check mode it only reports.
func syncFiles(ctx context.Context, env *Env, files []fileChange) ([]system.Effect, error) {
	f := env.System.Files
	var effects []system.Effect
	for _, fc := range files {
		current, err := f.ReadFile(ctx, fc.path, 1<<20)
		if err == nil && bytes.Equal(current, fc.content) {
			continue
		}
		if err != nil && !errors.Is(err, system.ErrNotFound) {
			return nil, err
		}
		effects = append(effects, system.Effect{Type: system.EffectFile, Action: "write", Target: fc.path})
		if env.Check {
			continue
		}
		if err := mkdirParents(ctx, f, fc.path); err != nil {
			return nil, err
		}
		if err := f.WriteFile(ctx, fc.path, fc.content, 0o644, system.Owner{}); err != nil {
			return nil, err
		}
	}
	return effects, nil
}

// mkdirParents creates the missing directories above p, owned by root.
func mkdirParents(ctx context.Context, f system.Files, p string) error {
	var missing []string
	for d := path.Dir(p); d != "/"; d = path.Dir(d) {
		if _, err := f.Stat(ctx, d); err == nil {
			break
		} else if !errors.Is(err, system.ErrNotFound) {
			return err
		}
		missing = append([]string{d}, missing...)
	}
	for _, d := range missing {
		if err := f.Mkdir(ctx, d, 0o755, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
			return err
		}
	}
	return nil
}

// ensurePackage installs a package when it's missing.
func ensurePackage(ctx context.Context, env *Env, name string) ([]system.Effect, error) {
	installed, err := env.System.Packages.Installed(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(installed) > 0 {
		return nil, nil
	}
	candidates, err := env.System.Packages.Candidates(ctx, name)
	if err != nil {
		return nil, err
	}
	effects := packageEffects(candidates)
	if env.Check {
		return effects, nil
	}
	return effects, env.System.Packages.Install(ctx, system.PackageRequest{Name: name})
}

// ConfigureUpdatesInput configures automatic updates.
type ConfigureUpdatesInput struct {
	Updates system.UnattendedUpgrades `json:"updates"`
}

// Validate implements Input.
func (in ConfigureUpdatesInput) Validate() error {
	_, err := in.Updates.Render()
	return err
}

// ConfigureUpdates sets up unattended upgrades: Debian's security updates
// always, point releases and vendor origins when asked, never held
// packages, and reboots only when allowed.
var ConfigureUpdates = Register(Def[ConfigureUpdatesInput, Result]{
	Name:        "ConfigureUpdates",
	Summary:     "Configure automatic security updates",
	Description: "Installs unattended-upgrades when it's needed and writes its settings: which origins are upgraded automatically, which packages never are, and whether and when the server may reboot for an update.",
	Effects: func(in ConfigureUpdatesInput) []system.Effect {
		return []system.Effect{{Type: system.EffectFile, Action: "write", Target: system.UnattendedConfigFile}}
	},
	Run: func(ctx context.Context, env *Env, in ConfigureUpdatesInput) (Result, error) {
		rendered, err := in.Updates.Render()
		if err != nil {
			return Result{}, err
		}
		var effects []system.Effect
		if in.Updates.Enabled {
			pkg, err := ensurePackage(ctx, env, system.UnattendedPackage)
			if err != nil {
				return Result{}, err
			}
			effects = append(effects, pkg...)
		}
		var files []fileChange
		for _, p := range slices.Sorted(maps.Keys(rendered)) {
			files = append(files, fileChange{p, rendered[p]})
		}
		written, err := syncFiles(ctx, env, files)
		if err != nil {
			return Result{}, err
		}
		effects = append(effects, written...)
		if len(effects) == 0 {
			return Unchanged(), nil
		}
		return Changed(effects...), nil
	},
})

// KernelDefaultsInput applies Racksail's kernel and network defaults.
type KernelDefaultsInput struct {
	// Zram turns on compressed swap in memory.
	Zram       bool              `json:"zram" doc:"Use compressed swap in memory"`
	ZramConfig system.ZramConfig `json:"zramConfig,omitzero"`
	// Overrides replace or add parameters, within what Racksail tunes.
	Overrides map[string]string `json:"overrides,omitempty" doc:"Kernel parameters to set differently"`
}

// Validate implements Input.
func (in KernelDefaultsInput) Validate() error {
	for k, v := range in.Overrides {
		if err := checkTunable(k, v); err != nil {
			return err
		}
	}
	if in.Zram {
		if _, err := in.ZramConfig.Render(); err != nil {
			return err
		}
	}
	return nil
}

func checkTunable(k, v string) error {
	if !system.ValidSysctlKey(k) || !slices.ContainsFunc(sysctlPrefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
		return fmt.Errorf("parameter %s isn't one Racksail tunes", k)
	}
	if strings.ContainsAny(v, "\n\x00") || len(v) > 256 {
		return fmt.Errorf("value of %s", k)
	}
	return nil
}

// zramUnit is the service zram-generator makes for the first device.
const zramUnit = "systemd-zram-setup@zram0.service"

// ApplyKernelDefaults applies BBR with fq, connection and buffer limits
// sized to the memory, and zram swap, now and at every boot.
var ApplyKernelDefaults = Register(Def[KernelDefaultsInput, Result]{
	Name:    "ApplyKernelDefaults",
	Summary: "Apply kernel and network defaults",
	Description: "Sets BBR congestion control with the fq scheduler, larger accept queues and buffers, a connection tracking table sized to the memory, " +
		"hardened routing defaults and more inotify watches, now and at boot, and sets up compressed swap in memory with zram when asked.",
	Effects: func(in KernelDefaultsInput) []system.Effect {
		return []system.Effect{{Type: system.EffectKernel, Action: "tune", Target: system.KernelSysctlFile}}
	},
	Run: func(ctx context.Context, env *Env, in KernelDefaultsInput) (Result, error) {
		facts, err := env.System.Facts.Facts(ctx)
		if err != nil {
			return Result{}, err
		}
		values := system.KernelDefaults(facts.MemoryBytes, in.Zram)
		maps.Copy(values, in.Overrides)
		sysctlFile, err := system.RenderSysctl(values)
		if err != nil {
			return Result{}, err
		}
		modules, err := system.RenderModulesLoad(system.KernelModules)
		if err != nil {
			return Result{}, err
		}
		var effects []system.Effect
		files := []fileChange{{system.KernelSysctlFile, sysctlFile}, {system.ModulesLoadFile, modules}}
		if in.Zram {
			pkg, err := ensurePackage(ctx, env, system.ZramPackage)
			if err != nil {
				return Result{}, err
			}
			effects = append(effects, pkg...)
			zram, err := in.ZramConfig.Render()
			if err != nil {
				return Result{}, err
			}
			files = append(files, fileChange{system.ZramGeneratorFile, zram})
		}
		written, err := syncFiles(ctx, env, files)
		if err != nil {
			return Result{}, err
		}
		effects = append(effects, written...)
		zramChanged := slices.ContainsFunc(written, func(e system.Effect) bool { return e.Target == system.ZramGeneratorFile })
		if !in.Zram {
			if _, err := env.System.Files.Stat(ctx, system.ZramGeneratorFile); err == nil {
				effects = append(effects, system.Effect{Type: system.EffectFile, Action: "delete", Target: system.ZramGeneratorFile})
				zramChanged = true
				if !env.Check {
					if err := env.System.Files.Remove(ctx, system.ZramGeneratorFile); err != nil {
						return Result{}, err
					}
				}
			}
		}
		for _, k := range slices.Sorted(maps.Keys(values)) {
			cur, err := env.System.Sysctl.Get(ctx, k)
			if err == nil && cur == values[k] {
				continue
			}
			if err != nil && !errors.Is(err, system.ErrNotFound) {
				return Result{}, err
			}
			effects = append(effects, system.Effect{Type: system.EffectKernel, Action: "set", Target: k, Detail: values[k]})
			if env.Check {
				continue
			}
			// A parameter of a module that isn't loaded yet, such as
			// connection tracking before the firewall is up, takes effect
			// at the next boot from the file.
			if err := env.System.Sysctl.Set(ctx, k, values[k]); err != nil && !errors.Is(err, system.ErrNotFound) {
				return Result{}, err
			}
		}
		if zramChanged && !env.Check {
			units := env.System.Units
			if err := units.DaemonReload(ctx); err != nil {
				return Result{}, err
			}
			if in.Zram {
				if err := units.Restart(ctx, zramUnit); err != nil {
					return Result{}, err
				}
				effects = append(effects, system.Effect{Type: system.EffectService, Action: "start", Target: zramUnit})
			} else if st, err := units.Status(ctx, zramUnit); err == nil && st.Active() {
				if err := units.Stop(ctx, zramUnit); err != nil {
					return Result{}, err
				}
				effects = append(effects, system.Effect{Type: system.EffectService, Action: "stop", Target: zramUnit})
			}
		}
		if len(effects) == 0 {
			return Unchanged(), nil
		}
		return Changed(effects...), nil
	},
})

// TimeSyncInput configures the clock.
type TimeSyncInput struct {
	Sync system.TimeSync `json:"sync,omitzero"`
}

// Validate implements Input.
func (in TimeSyncInput) Validate() error {
	_, err := in.Sync.Render()
	return err
}

const (
	progTimedatectl = "/usr/bin/timedatectl"
	timesyncdUnit   = "systemd-timesyncd.service"
)

// ConfigureTimeSync keeps the clock synchronized with NTP and on UTC, as
// every Racksail server runs.
var ConfigureTimeSync = Register(Def[TimeSyncInput, Result]{
	Name:        "ConfigureTimeSync",
	Summary:     "Synchronize the clock on UTC",
	Description: "Sets the time zone to UTC, configures systemd-timesyncd with the given NTP servers or Debian's pool, and keeps it running.",
	Effects: func(TimeSyncInput) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "configure", Target: timesyncdUnit}}
	},
	Run: func(ctx context.Context, env *Env, in TimeSyncInput) (Result, error) {
		var effects []system.Effect
		pkg, err := ensurePackage(ctx, env, "systemd-timesyncd")
		if err != nil {
			return Result{}, err
		}
		effects = append(effects, pkg...)
		conf, err := in.Sync.Render()
		if err != nil {
			return Result{}, err
		}
		written, err := syncFiles(ctx, env, []fileChange{{system.TimesyncdDropIn, conf}})
		if err != nil {
			return Result{}, err
		}
		effects = append(effects, written...)
		facts, err := env.System.Facts.Facts(ctx)
		if err != nil {
			return Result{}, err
		}
		if facts.Timezone != "UTC" && facts.Timezone != "Etc/UTC" {
			effects = append(effects, system.Effect{Type: system.EffectKernel, Action: "timezone", Target: "UTC", Detail: "was " + facts.Timezone})
			if !env.Check {
				if _, err := env.System.Exec.Run(ctx, system.Command{Path: progTimedatectl, Args: []string{"set-timezone", "UTC"}}); err != nil {
					return Result{}, err
				}
			}
		}
		st, err := env.System.Units.Status(ctx, timesyncdUnit)
		if err != nil {
			return Result{}, err
		}
		if len(written) > 0 || !st.Active() || st.UnitFileState != "enabled" {
			effects = append(effects, system.Effect{Type: system.EffectService, Action: "restart", Target: timesyncdUnit})
			if !env.Check {
				if err := env.System.Units.Enable(ctx, timesyncdUnit); err != nil {
					return Result{}, err
				}
				if err := env.System.Units.Restart(ctx, timesyncdUnit); err != nil {
					return Result{}, err
				}
			}
		}
		if len(effects) == 0 {
			return Unchanged(), nil
		}
		return Changed(effects...), nil
	},
})

// Reboot units: a timer that starts a service that reboots.
const (
	rebootTimer   = "racksail-reboot.timer"
	rebootService = "racksail-reboot.service"
	rebootFlag    = "/run/reboot-required"
)

// RebootInfo says whether the server should reboot and whether it will.
type RebootInfo struct {
	Required bool     `json:"required"`
	Reasons  []string `json:"reasons,omitempty"`
	// Running is the kernel running now and Newest the newest installed.
	Running   string     `json:"running"`
	Newest    string     `json:"newest,omitempty"`
	Scheduled *time.Time `json:"scheduled,omitempty" doc:"When the scheduled reboot happens"`
}

// RebootStatus reports whether a reboot is needed, for example for a new
// kernel, and when one is scheduled.
var RebootStatus = Register(Def[Empty, RebootInfo]{
	Name:     "RebootStatus",
	Summary:  "Report whether the server needs a reboot",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (RebootInfo, error) {
		facts, err := env.System.Facts.Facts(ctx)
		if err != nil {
			return RebootInfo{}, err
		}
		info := RebootInfo{Running: facts.Kernel}
		entries, err := env.System.Files.ReadDir(ctx, "/boot")
		if err != nil && !errors.Is(err, system.ErrNotFound) {
			return info, err
		}
		for _, e := range entries {
			release, ok := strings.CutPrefix(path.Base(e.Path), "vmlinuz-")
			if ok && compareReleases(release, info.Newest) > 0 {
				info.Newest = release
			}
		}
		if info.Newest != "" && info.Newest != info.Running {
			info.Required = true
			info.Reasons = append(info.Reasons, "kernel "+info.Newest+" is installed")
		}
		if _, err := env.System.Files.Stat(ctx, rebootFlag); err == nil {
			info.Required = true
			info.Reasons = append(info.Reasons, "an update asked for a reboot")
		}
		if st, err := env.System.Units.Status(ctx, rebootTimer); err == nil && st.Active() {
			if content, err := env.System.Units.ReadUnit(ctx, rebootTimer); err == nil {
				if at, ok := scheduledTime(content); ok {
					info.Scheduled = &at
				}
			}
		}
		return info, nil
	},
})

// compareReleases orders kernel releases such as 6.12.48+deb13-amd64 by
// their numbers.
func compareReleases(a, b string) int {
	digits := func(s string) []int {
		var out []int
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' }) {
			n, _ := strconv.Atoi(part)
			out = append(out, n)
		}
		return out
	}
	return slices.Compare(digits(a), digits(b))
}

const calendarLayout = "2006-01-02 15:04:05"

func scheduledTime(timer []byte) (time.Time, bool) {
	for _, line := range strings.Split(string(timer), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "OnCalendar="); ok {
			t, err := time.Parse(calendarLayout, strings.TrimSuffix(v, " UTC"))
			return t.UTC(), err == nil
		}
	}
	return time.Time{}, false
}

// ScheduleRebootInput schedules a reboot.
type ScheduleRebootInput struct {
	At time.Time `json:"at" doc:"When to reboot"`
}

// Validate implements Input.
func (in ScheduleRebootInput) Validate() error {
	// A timer for a time that has passed would never fire.
	now := time.Now()
	if in.At.Before(now) || in.At.After(now.Add(31*24*time.Hour)) {
		return fmt.Errorf("a reboot can be scheduled from now up to 31 days ahead")
	}
	return nil
}

// ScheduleReboot reboots the server at a time, such as the maintenance
// window after a kernel update. Scheduling again moves the reboot.
var ScheduleReboot = Register(Def[ScheduleRebootInput, Result]{
	Name:    "ScheduleReboot",
	Summary: "Schedule a reboot",
	Effects: func(in ScheduleRebootInput) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "schedule-reboot", Target: rebootTimer, Detail: in.At.UTC().Format(time.RFC3339)}}
	},
	Run: func(ctx context.Context, env *Env, in ScheduleRebootInput) (Result, error) {
		units := env.System.Units
		timer, err := system.TimerSpec{
			Description: "Racksail scheduled reboot",
			OnCalendar:  []string{in.At.UTC().Format(calendarLayout) + " UTC"},
			Accuracy:    time.Second,
			Unit:        rebootService,
		}.Render()
		if err != nil {
			return Result{}, err
		}
		var svc system.UnitFile
		svc.Section("Unit").Add("Description", "Racksail scheduled reboot")
		svc.Section("Service").Add("Type", "oneshot").Add("ExecStart", "/usr/bin/systemctl reboot")
		service, err := svc.Render()
		if err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectService, Action: "schedule-reboot", Target: rebootTimer, Detail: in.At.UTC().Format(time.RFC3339)}
		current, err := units.ReadUnit(ctx, rebootTimer)
		if err == nil && bytes.Equal(current, timer) {
			if st, err := units.Status(ctx, rebootTimer); err == nil && st.Active() {
				return Unchanged(), nil
			}
		}
		if env.Check {
			return Changed(effect), nil
		}
		if err := units.WriteUnit(ctx, rebootService, service); err != nil {
			return Result{}, err
		}
		if err := units.WriteUnit(ctx, rebootTimer, timer); err != nil {
			return Result{}, err
		}
		if err := units.DaemonReload(ctx); err != nil {
			return Result{}, err
		}
		return Changed(effect), units.Restart(ctx, rebootTimer)
	},
})

// CancelReboot cancels a scheduled reboot.
var CancelReboot = Register(Def[Empty, Result]{
	Name:    "CancelReboot",
	Summary: "Cancel a scheduled reboot",
	Effects: func(Empty) []system.Effect {
		return []system.Effect{{Type: system.EffectService, Action: "cancel-reboot", Target: rebootTimer}}
	},
	Run: func(ctx context.Context, env *Env, _ Empty) (Result, error) {
		units := env.System.Units
		if _, err := units.ReadUnit(ctx, rebootTimer); errors.Is(err, system.ErrNotFound) {
			return Unchanged(), nil
		} else if err != nil {
			return Result{}, err
		}
		effect := system.Effect{Type: system.EffectService, Action: "cancel-reboot", Target: rebootTimer}
		if env.Check {
			return Changed(effect), nil
		}
		if st, err := units.Status(ctx, rebootTimer); err == nil && st.Active() {
			if err := units.Stop(ctx, rebootTimer); err != nil {
				return Result{}, err
			}
		}
		for _, u := range []string{rebootTimer, rebootService} {
			if err := units.RemoveUnit(ctx, u); err != nil && !errors.Is(err, system.ErrNotFound) {
				return Result{}, err
			}
		}
		return Changed(effect), units.DaemonReload(ctx)
	},
})
