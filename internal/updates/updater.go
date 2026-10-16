package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Options configure an Updater.
type Options struct {
	Store store.Store
	Agent agent.Client
	// Events receives the updater's events.
	Events events.Bus
	Hooks  *hooks.Runner
	// Submit makes changes as the system: the settings' first version and
	// automatic updates.
	Submit func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error)
	// Channel is the channel the settings start on: updates.channel in
	// racksail.toml.
	Channel Channel
	// StateDir is the panel's state, where the trusted root is cached and
	// the database is backed up before an update.
	StateDir string
	// Backup writes a consistent copy of the panel's database to a file.
	Backup func(ctx context.Context, dest string) error
	// Health reports whether the panel works, which a new release must
	// before it is confirmed.
	Health func(ctx context.Context) error
	// Binary is where the running racksail binary is; os.Executable when
	// empty.
	Binary string
	// NewClient builds the client releases are fetched with; NewClient
	// when nil.
	NewClient func(mirrors []string) *Client
	// Build is the running binary; Build() when zero.
	Build  BuildInfo
	Arch   string
	Logger *slog.Logger
	Now    func() time.Time
	// CheckEvery is how often the updater looks for releases; daily by
	// default. ConfirmWithin is how long a new release has to report
	// healthy, which must be less than GuardDelay.
	CheckEvery    time.Duration
	ConfirmWithin time.Duration
}

// Updater keeps Racksail up to date.
type Updater struct {
	opts Options
	// mu keeps one update or upgrade going at a time.
	mu sync.Mutex
}

// NewUpdater builds an updater.
func NewUpdater(opts Options) *Updater {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewClient == nil {
		opts.NewClient = NewClient
	}
	if opts.Build.Version == "" {
		opts.Build = Build()
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}
	if opts.CheckEvery <= 0 {
		opts.CheckEvery = 24 * time.Hour
	}
	if opts.ConfirmWithin <= 0 {
		opts.ConfirmWithin = 10 * time.Minute
	}
	if !opts.Channel.Valid() {
		opts.Channel = Stable
	}
	return &Updater{opts: opts}
}

func (u *Updater) dev() bool {
	_, err := ChannelOf(u.opts.Build.Version)
	return u.opts.Build.Channel == "dev" || err != nil
}

// Settings returns the update settings, or nil before they exist.
func (u *Updater) Settings(ctx context.Context) (*store.Object[SettingsSpec, SettingsStatus], error) {
	page, _, err := Kind.List(ctx, u.opts.Store, store.Query{Limit: 1})
	if err != nil || len(page) == 0 {
		return nil, err
	}
	return page[0], nil
}

var systemActor = auth.System("updates")

// Boot creates the settings the first time the panel starts, on the
// channel racksail.toml names.
func (u *Updater) Boot(ctx context.Context) error {
	s, err := u.Settings(ctx)
	if err != nil || s != nil {
		return err
	}
	if u.opts.Submit == nil {
		return errors.New("updates: there is nothing to submit the settings through")
	}
	spec, err := json.Marshal(SettingsSpec{Channel: u.opts.Channel})
	if err != nil {
		return err
	}
	_, err = u.opts.Submit(ctx, changeset.Request{
		Actor: systemActor, Title: "Set up updates",
		Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: Kind.Name(), Scope: store.ServerScope(), Spec: spec}},
	})
	return err
}

// setStatus changes the settings' status.
func (u *Updater) setStatus(ctx context.Context, change func(st *SettingsStatus)) error {
	s, err := u.Settings(ctx)
	if err != nil || s == nil {
		return err
	}
	return u.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:updates"}, func(tx store.Tx) error {
		obj, err := Kind.Get(ctx, tx, s.ID)
		if err != nil {
			return err
		}
		st := obj.Status
		change(&st)
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, obj.ID, raw)
	})
}

func (u *Updater) client(spec SettingsSpec) *Client { return u.opts.NewClient(spec.Mirrors) }

// Check looks for a newer release on the channel and records what it
// found.
func (u *Updater) Check(ctx context.Context) (CheckOutput, error) {
	out := CheckOutput{Running: u.opts.Build.Version}
	s, err := u.Settings(ctx)
	if err != nil {
		return out, err
	}
	if s == nil {
		return out, errors.New("updates: the settings don't exist yet")
	}
	avail, err := u.find(ctx, s.Spec)
	now := u.opts.Now().UTC()
	serr := u.setStatus(ctx, func(st *SettingsStatus) {
		st.Running, st.CheckedAt = u.opts.Build.Version, &now
		st.CheckError = ""
		if err != nil {
			st.CheckError = reasonOf(err)
			return
		}
		if avail != nil && (st.Available == nil || st.Available.Tag != avail.Tag) && u.opts.Events != nil {
			data, _ := json.Marshal(avail)
			u.opts.Events.Publish(ctx, events.Event{
				Type: EventAvailable.Name, Severity: EventAvailable.Severity, Scope: store.ServerScope(), Kind: Kind.Name(), Subject: s.ID,
				Actor: systemActor, Message: "Racksail " + avail.Version + " is available.", Data: data,
			})
		}
		st.Available = avail
	})
	if err != nil {
		return out, err
	}
	out.Available = avail
	return out, serr
}

// find returns the newest release on the channel newer than the running
// one, with its signed manifest checked, or nil.
func (u *Updater) find(ctx context.Context, spec SettingsSpec) (*Available, error) {
	if u.dev() {
		return nil, ErrDevBuild.New()
	}
	current, err := ParseVersion(u.opts.Build.Version)
	if err != nil {
		return nil, err
	}
	c := u.client(spec)
	releases, err := c.Releases(ctx)
	if err != nil {
		return nil, err
	}
	latest, ok := Latest(releases, spec.Channel, current)
	if !ok {
		return nil, nil
	}
	root, _, err := c.TrustedRoot(ctx, filepath.Join(u.opts.StateDir, "updates"))
	if err != nil {
		return nil, err
	}
	rel, err := c.Manifest(ctx, root, latest.Tag)
	if err != nil {
		return nil, err
	}
	asset, ok := rel.Binary(u.opts.Arch)
	if !ok {
		return nil, fmt.Errorf("%s has no binary for %s", rel.Tag, u.opts.Arch)
	}
	return &Available{Tag: rel.Tag, Version: rel.Version.String(), Channel: rel.Channel, Published: rel.Published, NotesURL: rel.NotesURL, Asset: asset}, nil
}

func (u *Updater) binary() (string, error) {
	if u.opts.Binary != "" {
		return u.opts.Binary, nil
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

var labelChars = regexp.MustCompile(`[^a-z0-9]+`)

// snapshotLabel names the snapshots taken before an update to version.
func snapshotLabelFor(prefix, version string) string {
	l := prefix + "_" + strings.Trim(labelChars.ReplaceAllString(strings.ToLower(version), "_"), "_")
	if len(l) > 64 {
		l = l[:64]
	}
	return l
}

// installInput is what the agent is asked to do for a release.
func (u *Updater) installInput(spec SettingsSpec, a *Available, binary, backup string) InstallReleaseInput {
	c := u.client(spec)
	files, bundles := c.AssetURLs(a.Tag, a.Asset)
	in := InstallReleaseInput{
		Tag: a.Tag, Asset: a.Asset.Name, Size: a.Asset.Size, SHA256: a.Asset.SHA256, URLs: files, BundleURLs: bundles,
		TrustedRootURLs: c.TrustedRootURLs(), Binary: binary, Snapshot: snapshotLabelFor("update", a.Version),
	}
	// The agent only puts back a backup in the panel's usual place.
	if strings.HasPrefix(backup, PanelBackups+"/") {
		in.PanelBackup = backup
	}
	return in
}

// UpdateHookData is what core.update.before and core.update.after hooks
// get: the versions before and after.
type UpdateHookData struct {
	From string `json:"from"`
	To   string `json:"to"`
	Tag  string `json:"tag"`
}

// Install backs up the panel's database and has the agent install the
// release the last check found.
func (u *Updater) Install(ctx context.Context, want string) (UpdateOutput, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s, err := u.Settings(ctx)
	if err != nil || s == nil {
		return UpdateOutput{}, fmt.Errorf("updates: reading the settings: %w", err)
	}
	a := s.Status.Available
	if a == nil {
		return UpdateOutput{}, ErrNothingToInstall.New()
	}
	if want != "" && want != a.Version {
		return UpdateOutput{}, ErrStaleCheck.New("found", a.Version, "version", want)
	}
	binary, err := u.binary()
	if err != nil {
		return UpdateOutput{}, err
	}
	backup := filepath.Join(u.opts.StateDir, "updates", a.Tag, "state.db")
	if u.opts.Backup != nil {
		if err := os.MkdirAll(filepath.Dir(backup), 0o750); err != nil {
			return UpdateOutput{}, err
		}
		os.Remove(backup)
		if err := u.opts.Backup(ctx, backup); err != nil {
			return UpdateOutput{}, ErrUpdateFailed.Wrap(err, "reason", "the panel's database couldn't be backed up first")
		}
	} else {
		backup = ""
	}
	in := u.installInput(s.Spec, a, binary, backup)
	if u.opts.Hooks != nil {
		u.opts.Hooks.Notify(ctx, hooks.CoreUpdateBefore, actorOf(ctx), UpdateHookData{From: u.opts.Build.Version, To: a.Version, Tag: a.Tag})
	}
	res, err := InstallRelease.Call(ctx, u.opts.Agent, in, agent.CallOptions{Owner: s.ID})
	if err != nil {
		return UpdateOutput{}, ErrUpdateFailed.Wrap(err, "reason", i18n.From(err).Message)
	}
	u.setStatus(ctx, func(st *SettingsStatus) { st.Last = res.State })
	return UpdateOutput{From: u.opts.Build.Version, To: a.Version, RestartsAt: u.opts.Now().Add(3 * time.Second).UTC()}, nil
}

// Rollback has the agent go back to the release before the last update.
func (u *Updater) Rollback(ctx context.Context, reason string) error {
	_, err := RollbackRelease.Call(ctx, u.opts.Agent, RollbackReleaseInput{Reason: reason}, agent.CallOptions{})
	return err
}

// Confirm finishes an update after Racksail restarted onto it: once the
// panel reports healthy, the agent is told the release works, which
// disarms the guard; a release that doesn't become healthy in time is
// rolled back. It also records and announces how earlier updates ended.
func (u *Updater) Confirm(ctx context.Context) error {
	out, err := ReleaseState.Call(ctx, u.opts.Agent, agent.Empty{}, agent.CallOptions{})
	if err != nil {
		return err
	}
	st := out.State
	u.setStatus(ctx, func(s *SettingsStatus) {
		s.Last, s.CanRollBack, s.Running = st, out.CanRollBack, u.opts.Build.Version
	})
	if st == nil {
		return nil
	}
	switch {
	case st.State == StateInstalled && st.To == u.opts.Build.Version:
		deadline := u.opts.Now().Add(u.opts.ConfirmWithin)
		interval := min(5*time.Second, u.opts.ConfirmWithin/4)
		var last error
		for {
			last = nil
			if u.opts.Health != nil {
				last = u.opts.Health(ctx)
			}
			if last == nil {
				break
			}
			if u.opts.Now().After(deadline) {
				reason := fmt.Sprintf("%s didn't become healthy: %s", st.To, reasonOf(last))
				u.opts.Logger.Error("rolling back an update that doesn't work", "release", st.Tag, "error", last)
				return u.Rollback(ctx, reason)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
		if _, err := ConfirmRelease.Call(ctx, u.opts.Agent, ConfirmReleaseInput{Tag: st.Tag}, agent.CallOptions{}); err != nil {
			return err
		}
		now := u.opts.Now().UTC()
		st.State, st.ConfirmedAt = StateConfirmed, &now
		if u.opts.Hooks != nil {
			u.opts.Hooks.Notify(ctx, hooks.CoreUpdateAfter, systemActor, UpdateHookData{From: st.From, To: st.To, Tag: st.Tag})
		}
		u.announce(ctx, st, EventInstalled, "Racksail was updated from "+st.From+" to "+st.To+".")
	case st.State == StateConfirmed:
		u.announce(ctx, st, EventInstalled, "Racksail was updated from "+st.From+" to "+st.To+".")
	case st.State == StateRolledBack:
		u.announce(ctx, st, EventRolledBack, "The update to Racksail "+st.To+" was rolled back: "+st.Reason)
	}
	return nil
}

// reasonOf is an error's message for people: its own when it has a code,
// its text otherwise.
func reasonOf(err error) string {
	e := i18n.From(err)
	if e.Code == i18n.ErrInternal.Code {
		return err.Error()
	}
	return e.Message
}

// announce publishes how an update ended, once.
func (u *Updater) announce(ctx context.Context, st *UpdateState, t events.Type, message string) {
	key := st.Tag + " " + st.State
	s, err := u.Settings(ctx)
	if err != nil || s == nil || s.Status.Reported == key {
		return
	}
	u.setStatus(ctx, func(s *SettingsStatus) {
		s.Last, s.Reported = st, key
		if st.State == StateConfirmed && s.Available != nil && s.Available.Tag == st.Tag {
			s.Available = nil
		}
	})
	if u.opts.Events != nil {
		data, _ := json.Marshal(st)
		u.opts.Events.Publish(ctx, events.Event{
			Type: t.Name, Severity: t.Severity, Scope: store.ServerScope(), Kind: Kind.Name(), Subject: s.ID,
			Actor: systemActor, Message: message, Data: data,
		})
	}
}

// Run finishes an update the panel restarted onto, then checks for
// releases every day and, when the settings say so, installs them in the
// window, until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	if err := u.Confirm(ctx); err != nil && ctx.Err() == nil {
		u.opts.Logger.Warn("finishing the last update", "error", err)
	}
	if u.dev() {
		return
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	var lastCheck time.Time
	for {
		now := u.opts.Now()
		if now.Sub(lastCheck) >= u.opts.CheckEvery {
			lastCheck = now
			if _, err := u.Check(ctx); err != nil && ctx.Err() == nil {
				u.opts.Logger.Warn("checking for updates", "error", err)
			}
		}
		u.automatic(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// automatic installs what the last check found, when updates install
// themselves and the window is open.
func (u *Updater) automatic(ctx context.Context) {
	s, err := u.Settings(ctx)
	if err != nil || s == nil || !s.Spec.Automatic || s.Status.Available == nil || u.opts.Submit == nil {
		return
	}
	if !s.Spec.Window.Open(u.opts.Now()) {
		return
	}
	input, _ := json.Marshal(UpdateInput{Version: s.Status.Available.Version})
	_, err = u.opts.Submit(ctx, changeset.Request{
		Actor: systemActor, Title: "Update Racksail to " + s.Status.Available.Version,
		Changes: []changeset.Change{{Op: changeset.OpAction, Kind: Kind.Name(), ID: s.ID, Action: "install", Input: input}},
	})
	if err != nil {
		u.opts.Logger.Warn("installing an update by itself", "error", err)
	}
}

// Reconciler returns the reconciler of core.update_settings, whose actions
// the updater carries out.
func (u *Updater) Reconciler() reconcile.Reconciler {
	return reconcile.Typed(Kind, settingsReconciler{u: u})
}

type settingsReconciler struct {
	u *Updater
}

func (r settingsReconciler) Plan(ctx context.Context, obj *store.Object[SettingsSpec, SettingsStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (r settingsReconciler) Apply(ctx context.Context, obj *store.Object[SettingsSpec, SettingsStatus], plan reconcile.Plan, ex reconcile.Executor) (SettingsStatus, error) {
	st := obj.Status
	st.Running = r.u.opts.Build.Version
	// A new channel or new mirrors may offer another release.
	if obj.Spec.Channel != "" && st.Available != nil {
		if ch, err := ChannelOf(st.Available.Tag); err != nil || !obj.Spec.Channel.Accepts(ch) {
			st.Available = nil
		}
	}
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, r.u.opts.Now())
	return st, err
}

func (r settingsReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	var st SettingsStatus
	if err := json.Unmarshal(req.Resource.Status, &st); err != nil && len(req.Resource.Status) > 0 {
		return reconcile.ActionPlan{}, err
	}
	var spec SettingsSpec
	if err := json.Unmarshal(req.Resource.Spec, &spec); err != nil {
		return reconcile.ActionPlan{}, err
	}
	switch req.Action {
	case "check":
		return reconcile.ActionPlan{}, nil
	case "install":
		var in UpdateInput
		if len(req.Input) > 0 {
			if err := json.Unmarshal(req.Input, &in); err != nil {
				return reconcile.ActionPlan{}, err
			}
		}
		if r.u.dev() {
			return reconcile.ActionPlan{}, ErrDevBuild.New()
		}
		a := st.Available
		if a == nil {
			return reconcile.ActionPlan{}, ErrNothingToInstall.New()
		}
		if in.Version != "" && in.Version != a.Version {
			return reconcile.ActionPlan{}, ErrStaleCheck.New("found", a.Version, "version", in.Version)
		}
		binary, err := r.u.binary()
		if err != nil {
			return reconcile.ActionPlan{}, err
		}
		effects := []system.Effect{{Type: system.EffectFile, Action: "write", Target: filepath.Join(r.u.opts.StateDir, "updates", a.Tag, "state.db"), Detail: "a copy of the panel's database"}}
		effects = append(effects, installEffects(r.u.installInput(spec, a, binary, ""))...)
		return reconcile.ActionPlan{Plan: reconcile.Plan{Effects: effects}}, nil
	case "rollback":
		return reconcile.ActionPlan{Plan: reconcile.Plan{Effects: rollbackEffects("")}}, nil
	case "upgrade_os":
		return r.u.planOSUpgrade(ctx, req)
	}
	return reconcile.ActionPlan{}, fmt.Errorf("updates: there is no action called %q", req.Action)
}

func (r settingsReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	switch req.Action {
	case "check":
		return r.u.Check(ctx)
	case "install":
		var in UpdateInput
		if len(req.Input) > 0 {
			if err := json.Unmarshal(req.Input, &in); err != nil {
				return nil, err
			}
		}
		return r.u.Install(ctx, in.Version)
	case "rollback":
		var in RollbackInput
		if len(req.Input) > 0 {
			if err := json.Unmarshal(req.Input, &in); err != nil {
				return nil, err
			}
		}
		reason := in.Reason
		if reason == "" {
			reason = "rolled back by an admin"
		}
		return nil, r.u.Rollback(ctx, reason)
	case "upgrade_os":
		var in UpgradeOSInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return nil, err
		}
		return r.u.UpgradeOS(ctx, req.Resource.ID, in.To)
	}
	return nil, fmt.Errorf("updates: there is no action called %q", req.Action)
}
