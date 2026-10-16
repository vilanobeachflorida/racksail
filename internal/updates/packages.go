package updates

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Debian's security updates install by themselves with unattended-upgrades,
// configured from the settings. Every other update, Debian's and those of
// vendors' repositories alike, installs once a day in the window when
// scheduled updates are on, after snapshots of the system so a bad one can
// be rolled back. Held packages, such as database majors waiting for a
// tested upgrade, stay as they are. An update that needs a reboot gets one
// at the reboot time when the settings allow it.

// ErrPackagesFailed is an upgrade of packages that didn't finish.
var ErrPackagesFailed = i18n.Define("updates.packages_failed", http.StatusBadGateway, "The packages couldn't be upgraded: {reason}.")

// packagesEvery is how long after an upgrade of the packages the next
// scheduled one may run. It is under a day so that each day's run starts
// as the window opens rather than drifting later.
const packagesEvery = 20 * time.Hour

// unattended is the unattended-upgrades configuration the settings ask
// for.
func unattended(p PackageUpdates) agent.ConfigureUpdatesInput {
	return agent.ConfigureUpdatesInput{Updates: system.UnattendedUpgrades{Enabled: p.Security, AutoReboot: p.Reboot, RebootTime: p.RebootTime}}
}

// planPackages is what upgrading the packages does, for the preview: the
// agent lists the updates only when it runs, after refreshing the lists.
func planPackages(now time.Time) []system.Effect {
	label := packagesLabel(now)
	effects := staticEffects(agent.UpdatePackageIndex.Name(), agent.Empty{})
	for _, p := range []string{"/", "/etc", "/usr", "/var/lib"} {
		effects = append(effects, system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: p, Detail: label})
	}
	return append(effects, system.Effect{Type: system.EffectPackage, Action: "upgrade", Target: "apt", Detail: "every update that isn't held"})
}

func packagesLabel(now time.Time) string {
	return snapshotLabelFor("packages", now.UTC().Format("20060102_1504"))
}

// UpgradePackages refreshes the package lists and installs every update
// that isn't held, after snapshots of the system, then schedules a reboot
// when an update needs one and the settings allow it.
func (u *Updater) UpgradePackages(ctx context.Context) (PackagesStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.busy(); err != nil {
		return PackagesStatus{}, err
	}
	s, err := u.Settings(ctx)
	if err != nil || s == nil {
		return PackagesStatus{}, fmt.Errorf("updates: reading the settings: %w", err)
	}
	now := u.opts.Now().UTC()
	out := PackagesStatus{At: now}
	opts := agent.CallOptions{Owner: s.ID}
	fail := func(cause error) (PackagesStatus, error) {
		out.Error = reasonOf(cause)
		u.setStatus(ctx, func(st *SettingsStatus) { st.Packages = &out })
		u.publish(ctx, s.ID, EventPackagesFailed, "Upgrading the server's packages failed: "+out.Error, out)
		return out, ErrPackagesFailed.Wrap(cause, "reason", out.Error)
	}
	if _, err := agent.UpdatePackageIndex.Call(ctx, u.opts.Agent, agent.Empty{}, opts); err != nil {
		return fail(err)
	}
	res, err := agent.UpgradePackages.Call(ctx, u.opts.Agent, agent.UpgradeInput{Full: true, Snapshot: packagesLabel(now)}, opts)
	for _, snap := range res.Snapshots {
		out.Snapshots = append(out.Snapshots, snap.Path)
	}
	if err != nil {
		return fail(err)
	}
	for _, e := range res.Effects {
		if e.Type == system.EffectPackage && e.Action == "upgrade" {
			out.Upgraded++
		}
	}
	os := u.afterUpgrade(ctx, s, now)
	u.setStatus(ctx, func(st *SettingsStatus) {
		st.Packages = &out
		if os != nil {
			st.OS = os
		}
	})
	if out.Upgraded > 0 {
		u.publish(ctx, s.ID, EventPackagesUpgraded, fmt.Sprintf("%d of the server's packages were upgraded.", out.Upgraded), out)
	}
	return out, nil
}

// afterUpgrade schedules the reboot an upgrade needs, when the settings
// allow one and none is scheduled yet, and returns the server's state for
// the status; nil when the agent couldn't say.
func (u *Updater) afterUpgrade(ctx context.Context, s *store.Object[SettingsSpec, SettingsStatus], now time.Time) *OSStatus {
	reboot, err := agent.RebootStatus.Call(ctx, u.opts.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		u.opts.Logger.Warn("asking whether the server needs a reboot", "error", err)
		return nil
	}
	var os OSStatus
	if s.Status.OS != nil {
		os = *s.Status.OS
	}
	os.RebootRequired, os.RebootReasons, os.RebootAt = reboot.Required, reboot.Reasons, reboot.Scheduled
	p := s.Spec.packages()
	if !reboot.Required || !p.Reboot || reboot.Scheduled != nil {
		return &os
	}
	at := nextAt(now, p.RebootTime)
	if _, err := agent.ScheduleReboot.Call(ctx, u.opts.Agent, agent.ScheduleRebootInput{At: at}, agent.CallOptions{Owner: s.ID}); err != nil {
		u.opts.Logger.Warn("scheduling the reboot an update needs", "error", err)
		return &os
	}
	os.RebootAt = &at
	u.publish(ctx, s.ID, EventRebootScheduled, "The server reboots at "+at.Format("15:04 UTC on 2 January")+" to finish installing updates.", os)
	return &os
}

// scheduled upgrades the packages once a day, in the window, when the
// settings say so. It only runs from Run's goroutine.
func (u *Updater) scheduled(ctx context.Context) {
	s, err := u.Settings(ctx)
	if err != nil || s == nil || u.opts.Submit == nil || !s.Spec.packages().Scheduled {
		return
	}
	now := u.opts.Now()
	if !s.Spec.Window.Open(now) {
		return
	}
	last := u.queued
	if p := s.Status.Packages; p != nil && p.At.After(last) {
		last = p.At
	}
	if now.Sub(last) < packagesEvery {
		return
	}
	u.queued = now
	_, err = u.opts.Submit(ctx, changeset.Request{
		Actor: systemActor, Title: "Upgrade the server's packages",
		Changes: []changeset.Change{{Op: changeset.OpAction, Kind: Kind.Name(), ID: s.ID, Action: "upgrade_packages"}},
	})
	if err != nil {
		u.opts.Logger.Warn("upgrading packages on schedule", "error", err)
	}
}
