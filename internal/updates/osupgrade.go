package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// A Debian major upgrade is orchestrated around hooks: modules can refuse
// it at core.os_upgrade.check, for example while a database major version
// the next release doesn't ship is in use, prepare at
// core.os_upgrade.before and check their services at core.os_upgrade.after.

// OSUpgradeData is what the os upgrade hooks get.
type OSUpgradeData struct {
	From        string `json:"from" doc:"The code name of the release the server runs"`
	To          string `json:"to" doc:"The code name of the release it moves to"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	// Outcome is upgraded or failed, for after hooks.
	Outcome string `json:"outcome,omitempty" enum:"upgraded,failed"`
	Error   string `json:"error,omitempty"`
}

// minFreeBytes is the space an upgrade needs on the system disk.
const minFreeBytes = 4 << 30

func actorOf(ctx context.Context) *auth.Actor {
	if a, ok := auth.ActorFrom(ctx); ok && a != nil {
		return a
	}
	return systemActor
}

// osUpgradeTarget checks that a server running from may move to to.
func osUpgradeTarget(os system.OSRelease, to string) (OSUpgradeData, error) {
	from := os.VersionCodename
	data := OSUpgradeData{From: from, To: to, FromVersion: debianVersions[from], ToVersion: debianVersions[to]}
	if os.ID != "debian" {
		return data, ErrOSUpgrade.New("from", os.PrettyName, "to", to, "reason", "the server doesn't run Debian")
	}
	next, ok := NextDebian(from)
	switch {
	case !ok:
		return data, ErrOSUpgrade.New("from", from, "to", to, "reason", "Racksail doesn't know the release after "+from+" yet")
	case next != to:
		return data, ErrOSUpgrade.New("from", from, "to", to, "reason", "Debian is upgraded one release at a time, to "+next)
	}
	return data, nil
}

func staticEffects(op string, in any) []system.Effect {
	e, ok := agent.Lookup(op)
	if !ok {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	effects, _ := e.Effects(raw)
	return effects
}

func (u *Updater) planOSUpgrade(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	var in UpgradeOSInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return reconcile.ActionPlan{}, err
	}
	data, err := osUpgradeTarget(req.Facts.OS, in.To)
	if err != nil {
		return reconcile.ActionPlan{}, err
	}
	if u.opts.Hooks != nil {
		if err := u.opts.Hooks.Check(ctx, hooks.OSUpgradeCheck, actorOf(ctx), data); err != nil {
			return reconcile.ActionPlan{}, err
		}
	}
	label := snapshotLabelFor("os_upgrade", data.To)
	effects := staticEffects(SetDebianRelease.Name(), SetDebianReleaseInput{From: data.From, To: data.To})
	effects = append(effects, staticEffects(agent.UpdatePackageIndex.Name(), agent.Empty{})...)
	for _, p := range []string{"/", "/etc", "/usr", "/var/lib"} {
		effects = append(effects, system.Effect{Type: system.EffectStorage, Action: "snapshot", Target: p, Detail: label})
	}
	effects = append(effects, system.Effect{Type: system.EffectPackage, Action: "upgrade", Target: "apt", Detail: "every package, to Debian " + data.ToVersion + " (" + data.To + ")"})
	return reconcile.ActionPlan{Plan: reconcile.Plan{Effects: effects}}, nil
}

// UpgradeOS upgrades Debian to the next release: modules are asked again
// and told, the system is snapshotted, apt moves to the next release and
// every package is upgraded. When the upgrade fails, apt is pointed back
// at the release before, and the snapshots are there to restore.
func (u *Updater) UpgradeOS(ctx context.Context, settings store.ID, to string) (UpgradeOSOutput, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.busy(); err != nil {
		return UpgradeOSOutput{}, err
	}
	facts, err := agent.GetFacts.Call(ctx, u.opts.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		return UpgradeOSOutput{}, err
	}
	data, err := osUpgradeTarget(facts.OS, to)
	if err != nil {
		return UpgradeOSOutput{}, err
	}
	actor := actorOf(ctx)
	if usage, err := agent.FilesystemUsage.Call(ctx, u.opts.Agent, agent.MountPointInput{Mount: "/"}, agent.CallOptions{}); err == nil && usage.FreeBytes < minFreeBytes {
		return UpgradeOSOutput{}, ErrOSUpgrade.New("from", data.From, "to", data.To, "reason",
			fmt.Sprintf("the system disk has %d MiB free, and an upgrade needs %d", usage.FreeBytes>>20, minFreeBytes>>20))
	}
	if u.opts.Hooks != nil {
		if err := u.opts.Hooks.Check(ctx, hooks.OSUpgradeCheck, actor, data); err != nil {
			return UpgradeOSOutput{}, err
		}
		u.opts.Hooks.Notify(ctx, hooks.OSUpgradeBefore, actor, data)
	}
	out := UpgradeOSOutput{From: data.From, To: data.To}
	opts := agent.CallOptions{Owner: settings}
	fail := func(cause error) (UpgradeOSOutput, error) {
		// apt goes back to the release the server still mostly runs.
		if _, err := SetDebianRelease.Call(context.WithoutCancel(ctx), u.opts.Agent, SetDebianReleaseInput{From: data.To, To: data.From}, opts); err == nil {
			agent.UpdatePackageIndex.Call(context.WithoutCancel(ctx), u.opts.Agent, agent.Empty{}, opts)
		}
		data.Outcome, data.Error = "failed", reasonOf(cause)
		if u.opts.Hooks != nil {
			u.opts.Hooks.Notify(ctx, hooks.OSUpgradeAfter, actor, data)
		}
		u.publish(ctx, settings, EventOSFailed, "Upgrading Debian from "+data.From+" to "+data.To+" failed: "+data.Error, data)
		return out, ErrOSUpgrade.Wrap(cause, "from", data.From, "to", data.To, "reason", data.Error)
	}
	if _, err := SetDebianRelease.Call(ctx, u.opts.Agent, SetDebianReleaseInput{From: data.From, To: data.To}, opts); err != nil {
		return fail(err)
	}
	if _, err := agent.UpdatePackageIndex.Call(ctx, u.opts.Agent, agent.Empty{}, opts); err != nil {
		return fail(err)
	}
	res, err := agent.UpgradePackages.Call(ctx, u.opts.Agent, agent.UpgradeInput{Full: true, Snapshot: snapshotLabelFor("os_upgrade", data.To)}, opts)
	if err != nil {
		return fail(err)
	}
	for _, s := range res.Snapshots {
		out.Snapshots = append(out.Snapshots, s.Path)
	}
	// A new release always wants its kernel and init booted. The agent says
	// what else wants a reboot, such as the kernel the release brought, and
	// whether one is scheduled already.
	next, _ := NextDebian(data.To)
	upgraded := &OSStatus{Codename: data.To, Version: data.ToVersion, Next: next, RebootRequired: true, RebootReasons: []string{"Debian " + data.To + " is installed"}}
	if reboot, err := agent.RebootStatus.Call(ctx, u.opts.Agent, agent.Empty{}, agent.CallOptions{}); err == nil {
		upgraded.RebootReasons = append(upgraded.RebootReasons, reboot.Reasons...)
		upgraded.RebootAt = reboot.Scheduled
	}
	out.RebootRequired, out.RebootReasons = true, upgraded.RebootReasons
	data.Outcome = "upgraded"
	if u.opts.Hooks != nil {
		u.opts.Hooks.Notify(ctx, hooks.OSUpgradeAfter, actor, data)
	}
	u.publish(ctx, settings, EventOSUpgraded, "Debian was upgraded from "+data.From+" to "+data.To+". Reboot the server to finish.", out)
	u.setStatus(ctx, func(st *SettingsStatus) { st.OS = upgraded })
	return out, nil
}

func (u *Updater) publish(ctx context.Context, subject store.ID, t events.Type, message string, data any) {
	if u.opts.Events == nil {
		return
	}
	raw, _ := json.Marshal(data)
	u.opts.Events.Publish(context.WithoutCancel(ctx), events.Event{
		Type: t.Name, Severity: t.Severity, Scope: store.ServerScope(), Kind: Kind.Name(), Subject: subject,
		Actor: systemActor, Message: message, Data: raw,
	})
}

// osStatus asks the agent for the server's Debian release, whether it
// needs a reboot and when one is scheduled.
func (u *Updater) osStatus(ctx context.Context) (*OSStatus, error) {
	facts, err := agent.GetFacts.Call(ctx, u.opts.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		return nil, err
	}
	os := &OSStatus{Codename: facts.OS.VersionCodename, Version: facts.OS.VersionID}
	os.Next, _ = NextDebian(os.Codename)
	if reboot, err := agent.RebootStatus.Call(ctx, u.opts.Agent, agent.Empty{}, agent.CallOptions{}); err == nil {
		os.RebootRequired, os.RebootReasons, os.RebootAt = reboot.Required, reboot.Reasons, reboot.Scheduled
	}
	return os, nil
}

// RefreshOS records the server's Debian release, whether it needs a
// reboot and when one is scheduled.
func (u *Updater) RefreshOS(ctx context.Context) error {
	os, err := u.osStatus(ctx)
	if err != nil {
		return err
	}
	// The status is only written when something changed, since this runs
	// every hour.
	if s, err := u.Settings(ctx); err != nil || s == nil || reflect.DeepEqual(s.Status.OS, os) {
		return err
	}
	return u.setStatus(ctx, func(st *SettingsStatus) { st.OS = os })
}
