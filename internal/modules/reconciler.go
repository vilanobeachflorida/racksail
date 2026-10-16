package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// TransitionPlanner is implemented by modules whose transitions change the
// server beyond what the manager does on its own. Turning a module off
// while resources use it needs one: it stops their services and closes
// their ports, keeping their data, and turning the module on again starts
// them. Removing a module uninstalls what its manifest names exactly; a
// planner also uninstalls what the manifest names with patterns, such as
// the PHP versions the web module installed.
type TransitionPlanner interface {
	PlanTransition(ctx context.Context, req TransitionRequest) (reconcile.Plan, error)
}

// TransitionRequest is what a TransitionPlanner plans for.
type TransitionRequest struct {
	Module *Manifest
	// From is the state the module is in, and To the one it goes to: on or
	// off. Remove says the module's software is being uninstalled.
	From, To State
	Remove   bool
	// KeepData says the module's resources stay when it is removed.
	KeepData bool
	// Usage counts the resources that use the module.
	Usage Usage
	// Read gives the planned state of other resources, as for reconcilers.
	Read  store.Reader
	Facts system.MachineFacts
}

// Reconciler returns the reconciler of core.module: it checks the rules
// for turning modules on and off as part of the plan, so the preview says
// why a change can't be made, and applies a change by running the module's
// transition steps and starting or stopping it.
func (m *Manager) Reconciler() reconcile.Reconciler {
	return reconcile.Typed(Kind, moduleReconciler{m: m})
}

type moduleReconciler struct{ m *Manager }

func (r moduleReconciler) Plan(ctx context.Context, obj *store.Object[ModuleSpec, ModuleStatus], req reconcile.Request) (reconcile.Plan, error) {
	m := r.m
	if obj.Deleted() {
		return reconcile.Plan{}, nil
	}
	mod, ok := m.reg.Get(obj.Spec.Module)
	if !ok {
		return reconcile.Plan{}, ErrUnknownModule.New("module", obj.Spec.Module)
	}
	man := mod.Manifest()
	if err := man.ValidateSettings(obj.Spec.Settings); err != nil {
		return reconcile.Plan{}, err
	}
	for _, id := range obj.Spec.OptIn {
		if !slices.ContainsFunc(man.Upstream, func(u Upstream) bool { return u.ID == id && u.OptIn }) {
			return reconcile.Plan{}, ErrNotUpstream.New("module", man.Name, "software", id)
		}
	}
	current := obj.Status.State
	if current == "" {
		current = StateOff
	}
	if obj.Spec.Enabled {
		if problems := HardwareProblems(man, req.Facts); len(problems) > 0 {
			return reconcile.Plan{}, ErrHardware.New("module", man.Name, "reason", problems[0])
		}
		for _, dep := range man.Dependencies.Requires {
			on, err := m.plannedOn(ctx, req.Read, dep)
			if err != nil {
				return reconcile.Plan{}, err
			}
			if !on {
				return reconcile.Plan{}, ErrDependency.New("module", man.Name, "needs", m.name(dep))
			}
		}
		if anyOf := man.Dependencies.RequiresAny; len(anyOf) > 0 {
			met := false
			var names []string
			for _, dep := range anyOf {
				on, err := m.plannedOn(ctx, req.Read, dep)
				if err != nil {
					return reconcile.Plan{}, err
				}
				met = met || on
				names = append(names, m.name(dep))
			}
			if !met {
				return reconcile.Plan{}, ErrDependency.New("module", man.Name, "needs", "one of "+strings.Join(names, ", "))
			}
		}
		if current.IsOn() {
			return reconcile.Plan{}, nil
		}
		plan := reconcile.Plan{Effects: m.turnOnEffects(ctx, man)}
		usage, err := m.usage(ctx, req.Read, man.ID)
		if err != nil {
			return reconcile.Plan{}, err
		}
		if tp, ok := mod.(TransitionPlanner); ok && usage.Resources > 0 {
			extra, err := tp.PlanTransition(ctx, TransitionRequest{Module: man, From: current, To: StateOn, Usage: usage, Read: req.Read, Facts: req.Facts})
			if err != nil {
				return reconcile.Plan{}, err
			}
			plan = merge(plan, extra)
		}
		return plan, nil
	}
	if man.Required {
		return reconcile.Plan{}, ErrRequired.New("module", man.Name)
	}
	dependents, err := m.dependents(ctx, req.Read, man.ID)
	if err != nil {
		return reconcile.Plan{}, err
	}
	if len(dependents) > 0 {
		return reconcile.Plan{}, ErrInUse.New("module", man.Name, "dependents", strings.Join(dependents, ", "))
	}
	removing := obj.Spec.Removed && !obj.Status.Uninstalled
	if !current.IsOn() && !removing {
		return reconcile.Plan{}, nil
	}
	usage, err := m.usage(ctx, req.Read, man.ID)
	if err != nil {
		return reconcile.Plan{}, err
	}
	var plan reconcile.Plan
	tp, planner := mod.(TransitionPlanner)
	if current.IsOn() {
		plan.Effects = m.turnOffEffects(man, current)
		if usage.Resources > 0 {
			if !planner {
				return reconcile.Plan{}, ErrHasResources.New("module", man.Name, "count", usage.Resources)
			}
			extra, err := tp.PlanTransition(ctx, TransitionRequest{Module: man, From: current, To: StateOff, Usage: usage, Read: req.Read, Facts: req.Facts})
			if err != nil {
				return reconcile.Plan{}, err
			}
			plan = merge(plan, extra)
		}
	}
	if removing {
		plan = merge(plan, UninstallPlan(man))
		if planner {
			extra, err := tp.PlanTransition(ctx, TransitionRequest{
				Module: man, From: current, To: StateOff, Remove: true, KeepData: usage.Resources > 0, Usage: usage, Read: req.Read, Facts: req.Facts,
			})
			if err != nil {
				return reconcile.Plan{}, err
			}
			plan = merge(plan, extra)
		}
	}
	return plan, nil
}

// merge appends what another plan does to a plan.
func merge(plan, extra reconcile.Plan) reconcile.Plan {
	plan.Steps = append(plan.Steps, extra.Steps...)
	plan.Effects = append(plan.Effects, extra.Effects...)
	plan.Checks = append(plan.Checks, extra.Checks...)
	return plan
}

// name returns a module's name, or its id when it isn't registered.
func (m *Manager) name(id string) string {
	if man, err := m.manifest(id); err == nil {
		return man.Name
	}
	return id
}

func (r moduleReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	m := r.m
	obj, err := Kind.Decode(req.Resource)
	if err != nil {
		return reconcile.ActionPlan{}, err
	}
	mod, ok := m.reg.Get(obj.Spec.Module)
	if !ok {
		return reconcile.ActionPlan{}, ErrUnknownModule.New("module", obj.Spec.Module)
	}
	man := mod.Manifest()
	spec := obj.Spec
	switch req.Action {
	case ActionTurnOn:
		if spec.Enabled {
			return reconcile.ActionPlan{}, ErrAlreadyOn.New("module", man.Name)
		}
		spec.Enabled, spec.Removed = true, false
	case ActionTurnOff:
		if !spec.Enabled {
			return reconcile.ActionPlan{}, ErrAlreadyOff.New("module", man.Name)
		}
		spec.Enabled = false
	case ActionRemove:
		var in RemoveInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return reconcile.ActionPlan{}, i18n.ErrBadRequest.Wrap(err, "reason", err.Error())
		}
		if !confirms(man, in.Confirm) {
			return reconcile.ActionPlan{}, ErrConfirm.New("name", man.Name)
		}
		if man.Required {
			return reconcile.ActionPlan{}, ErrRequired.New("module", man.Name)
		}
		usage, err := m.usage(ctx, req.Read, man.ID)
		if err != nil {
			return reconcile.ActionPlan{}, err
		}
		if !in.KeepData && usage.Resources > 0 {
			return reconcile.ActionPlan{}, ErrRemoveResources.New("module", man.Name, "count", usage.Resources)
		}
		// The module's reconciler uninstalls the software once the spec
		// says it was removed, so the module shows as being removed while
		// it does.
		spec.Enabled, spec.Removed = false, true
	default:
		return reconcile.ActionPlan{}, fmt.Errorf("modules: core.module has no action %q", req.Action)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return reconcile.ActionPlan{}, err
	}
	return reconcile.ActionPlan{Spec: raw}, nil
}

// Apply runs the transition steps, starts or stops the module and reports
// its state, its usage, the software found installed and whether the
// machine can run it.
func (r moduleReconciler) Apply(ctx context.Context, obj *store.Object[ModuleSpec, ModuleStatus], plan reconcile.Plan, ex reconcile.Executor) (ModuleStatus, error) {
	m := r.m
	st := obj.Status
	id := obj.Spec.Module
	now := m.opts.Now().UTC()
	from := st.State
	if from == "" {
		from = StateOff
	}
	mod, ok := m.reg.Get(id)
	if !ok {
		err := ErrUnknownModule.New("module", id)
		st.Phase, st.Message, st.Code = store.PhaseFailed, err.Message, err.Code
		return st, err
	}
	man := mod.Manifest()
	if obj.Deleted() {
		_ = m.stop(ctx, id)
		return st, nil
	}
	// The way to the new state is shown while it takes time.
	removing := obj.Spec.Removed && !st.Uninstalled
	switch {
	case !obj.Spec.Enabled && removing && len(plan.Steps) > 0:
		m.writeState(ctx, obj, StateRemoving)
	case !obj.Spec.Enabled && from.IsOn() && len(plan.Steps) > 0:
		m.writeState(ctx, obj, StateStopping)
	}
	err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, now)
	if err != nil {
		// The status recorded keeps the state it was in.
		m.remember(&store.Object[ModuleSpec, ModuleStatus]{Meta: obj.Meta, Spec: obj.Spec, Status: obj.Status})
		return st, err
	}
	st.Uninstalled = obj.Spec.Removed
	usage, uerr := m.usage(ctx, m.opts.Services.Store, id)
	if uerr == nil {
		st.Usage = usage
	}
	to := StateOff
	if obj.Spec.Enabled {
		if err := m.start(ctx, id); err != nil {
			e := i18n.From(err)
			st.Phase, st.Message, st.Code = store.PhaseFailed, e.Message, e.Code
			st.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionFalse, Reason: "StartFailed", Message: e.Message, Code: e.Code, ObservedGeneration: obj.Generation}, now)
			st.State, st.Started = StateOff, false
			m.recordStatus(obj, st)
			return st, err
		}
		to = StateOn
		if st.Usage.Resources > 0 {
			to = StateActive
		}
	} else if err := m.stop(ctx, id); err != nil {
		e := i18n.From(err)
		st.Phase, st.Message, st.Code = store.PhaseFailed, e.Message, e.Code
		m.recordStatus(obj, st)
		return st, err
	}
	st.Started = m.Started(id)
	if m.opts.Facts != nil {
		if facts, err := m.opts.Facts(ctx); err == nil {
			st.HardwareProblems = HardwareProblems(man, facts)
		}
	}
	if installed, err := m.installed(ctx, ex, man); err == nil {
		st.Installed = installed
	}
	if to != from || st.Since == nil {
		st.Since = &now
	}
	st.State = to
	m.recordStatus(obj, st)
	switch {
	case to.IsOn() && !from.IsOn():
		m.notify(ctx, hooks.ModuleOn(id), Transition{Module: id, From: from, To: to})
		m.enqueueResources(ctx, id)
	case !to.IsOn() && from.IsOn():
		m.notify(ctx, hooks.ModuleOff(id), Transition{Module: id, From: from, To: to})
	}
	return st, nil
}

// recordStatus updates the cache from a status Apply returns.
func (m *Manager) recordStatus(obj *store.Object[ModuleSpec, ModuleStatus], st ModuleStatus) {
	m.remember(&store.Object[ModuleSpec, ModuleStatus]{Meta: obj.Meta, Spec: obj.Spec, Status: st})
}

// writeState records a passing state, such as stopping, while a transition
// runs.
func (m *Manager) writeState(ctx context.Context, obj *store.Object[ModuleSpec, ModuleStatus], s State) {
	st := obj.Status
	st.State = s
	m.setState(obj.Spec.Module, s)
	if m.opts.Services.Store == nil {
		return
	}
	if err := store.SetStatus(context.WithoutCancel(ctx), m.opts.Services.Store, obj.ID, st); err != nil {
		m.opts.Log.Warn("recording a module's state", "module", obj.Spec.Module, "error", err)
	}
}

// enqueueResources has the module's resources reconciled again once it is
// on, so what a module kept while it was off comes back.
func (m *Manager) enqueueResources(ctx context.Context, id string) {
	if m.opts.Enqueue == nil {
		return
	}
	for _, kind := range m.KindsOf(id) {
		_ = m.each(ctx, m.opts.Services.Store, kind, func(r *store.Resource) { m.opts.Enqueue(r.ID, "module turned on") })
	}
}

// installed finds which of the module's upstream software is on the
// server: Debian packages it names exactly, and the runtime directories of
// archives.
func (m *Manager) installed(ctx context.Context, ex reconcile.Executor, man *Manifest) ([]string, error) {
	var steps []agent.Step
	var owners []string
	for _, u := range man.Upstream {
		switch u.Source.Type {
		case "debian", "apt-repository":
			names := exactPackages(u)
			if len(names) == 0 {
				continue
			}
			steps = append(steps, agent.QueryPackages.Step(agent.QueryPackagesInput{Names: names}))
			owners = append(owners, u.ID)
		case "vendor-archive", "racksail-build":
			steps = append(steps, agent.StatPath.Step(agent.PathInput{Path: RuntimeDir(u.ID, "")}))
			owners = append(owners, u.ID)
		}
	}
	if len(steps) == 0 {
		return nil, nil
	}
	var found []string
	for i, s := range steps {
		results, err := ex.Run(ctx, []agent.Step{s})
		if err != nil {
			// A runtime directory that doesn't exist isn't installed.
			if s.Op == agent.StatPath.Name() {
				continue
			}
			return nil, err
		}
		if len(results) == 0 {
			continue
		}
		switch s.Op {
		case agent.QueryPackages.Name():
			var q agent.PackageQuery
			if json.Unmarshal(results[0].Output, &q) == nil && len(q.Installed) > 0 {
				found = append(found, owners[i])
			}
		default:
			var info agent.PathInfo
			if json.Unmarshal(results[0].Output, &info) == nil && info.Exists {
				found = append(found, owners[i])
			}
		}
	}
	return found, nil
}

// Gate wraps the reconciler of a module's kind so that, while the module is
// off, reconciling one of its resources leaves the server alone: the
// periodic resync doesn't start what turning the module off stopped.
// Deleting a resource still removes what it set up.
func (m *Manager) Gate(rec reconcile.Reconciler) reconcile.Reconciler {
	return &gated{m: m, rec: rec}
}

type gated struct {
	m   *Manager
	rec reconcile.Reconciler
}

func (g *gated) Kind() string { return g.rec.Kind() }

// off reports whether a resource's module is off.
func (g *gated) off(res *store.Resource) bool {
	if res == nil || res.Deleted() {
		return false
	}
	id, ok := g.m.ModuleOf(res.Kind)
	return ok && !g.m.State(id).IsOn() && !g.m.Enabled(id)
}

func (g *gated) Plan(ctx context.Context, req reconcile.Request) (reconcile.Plan, error) {
	if g.off(req.Resource) {
		return reconcile.Plan{}, nil
	}
	return g.rec.Plan(ctx, req)
}

func (g *gated) Apply(ctx context.Context, req reconcile.Request, plan reconcile.Plan, ex reconcile.Executor) (any, error) {
	if g.off(req.Resource) && len(plan.Steps) == 0 {
		status := req.Resource.Status
		if len(status) == 0 {
			status = json.RawMessage("{}")
		}
		return status, nil
	}
	return g.rec.Apply(ctx, req, plan, ex)
}

func (g *gated) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	if p, ok := g.rec.(reconcile.ActionPlanner); ok {
		return p.PlanAction(ctx, req)
	}
	return reconcile.ActionPlan{}, nil
}

func (g *gated) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if a, ok := g.rec.(reconcile.ActionApplier); ok {
		return a.ApplyAction(ctx, req)
	}
	return nil, nil
}

func (g *gated) Concurrency() int {
	if l, ok := g.rec.(reconcile.Limiter); ok {
		return l.Concurrency()
	}
	return 0
}

// Hooks are the manager's own hooks: only the panel adds modules, nobody
// deletes them, and nothing of a module that is off can be created or
// changed until it is turned on.
func (m *Manager) Hooks() []hooks.Registration {
	const owner = hooks.OwnerCore + ":modules"
	return []hooks.Registration{
		{
			Point: hooks.KindPoint(Kind.Name(), "create", "validate"), Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
			OnValidate: func(ctx context.Context, c *hooks.Context) error {
				if c.Actor == nil || c.Actor.Type != auth.ActorSystem {
					return ErrCreatedBySystem.New()
				}
				return m.knownModule(c.Change)
			},
		},
		{
			Point: hooks.KindPoint(Kind.Name(), "update", "validate"), Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
			OnValidate: func(ctx context.Context, c *hooks.Context) error { return m.knownModule(c.Change) },
		},
		{
			Point: hooks.KindPoint(Kind.Name(), "delete", "validate"), Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
			OnValidate: func(ctx context.Context, c *hooks.Context) error {
				var spec ModuleSpec
				_ = json.Unmarshal(c.Change.Before, &spec)
				return ErrNotDeletable.New("module", m.name(spec.Module))
			},
		},
		{
			Point: "*.*.*.validate", Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
			OnValidate: func(ctx context.Context, c *hooks.Context) error { return m.refuseWhileOff(c) },
		},
	}
}

// knownModule refuses a core.module change for a module this binary
// doesn't have.
func (m *Manager) knownModule(ch *changeset.ResourceChange) error {
	if ch == nil {
		return nil
	}
	var spec ModuleSpec
	if err := json.Unmarshal(ch.After, &spec); err != nil {
		return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", "the spec isn't a module's"))
	}
	if _, ok := m.reg.Get(spec.Module); !ok {
		return ErrUnknownModule.New("module", spec.Module)
	}
	return nil
}

// refuseWhileOff refuses creating, changing or acting on a resource of a
// module that is off, unless the same change set turns it on first. The
// change set holds the changes planned before this one.
func (m *Manager) refuseWhileOff(c *hooks.Context) error {
	ch := c.Change
	if ch == nil || ch.Op == changeset.OpDelete {
		return nil
	}
	id, ok := m.ModuleOf(ch.Kind)
	if !ok {
		return nil
	}
	on := m.Enabled(id)
	if rid, ok := m.Resource(id); ok && c.ChangeSet != nil {
		for i := range c.ChangeSet.Changes {
			other := &c.ChangeSet.Changes[i]
			if other == ch || other.ID != rid {
				continue
			}
			var spec ModuleSpec
			if json.Unmarshal(other.After, &spec) == nil {
				on = spec.Enabled
			}
		}
	}
	if !on {
		return ErrOff.New("module", m.name(id))
	}
	return nil
}

// RuntimeDir is where a version of upstream software unpacked from an
// archive lives; without a version, the directory of all its versions.
func RuntimeDir(upstream, version string) string {
	dir := system.RuntimesDir + "/" + upstream
	if version != "" {
		dir += "/" + version
	}
	return dir
}

// exactPackages lists the packages an upstream entry names without
// patterns.
func exactPackages(u Upstream) []string {
	var out []string
	for _, p := range u.Source.Packages {
		if !strings.ContainsAny(p, "*?[") {
			out = append(out, p)
		}
	}
	return out
}

// UninstallPlan is what removing a module uninstalls on its own: the
// Debian packages its manifest names exactly, the vendor repositories it
// added and the runtime directories of the archives it unpacked. Every step
// does nothing for software that isn't there.
func UninstallPlan(man *Manifest) reconcile.Plan {
	var plan reconcile.Plan
	for _, u := range man.Upstream {
		switch u.Source.Type {
		case "debian":
			if names := exactPackages(u); len(names) > 0 {
				plan.Steps = append(plan.Steps, agent.RemovePackages.Titled("Uninstall "+u.Name, agent.RemovePackagesInput{Names: names, Purge: true}))
			}
		case "apt-repository":
			if names := exactPackages(u); len(names) > 0 {
				plan.Steps = append(plan.Steps, agent.RemovePackages.Titled("Uninstall "+u.Name, agent.RemovePackagesInput{Names: names, Purge: true}))
			}
			plan.Steps = append(plan.Steps, agent.RemoveAptSource.Titled("Remove the "+u.Name+" package repository", agent.SourceInput{Name: u.ID}))
		case "vendor-archive", "racksail-build":
			plan.Steps = append(plan.Steps, agent.RemovePath.Titled("Remove "+u.Name, agent.RemovePathInput{Path: RuntimeDir(u.ID, ""), Recursive: true}))
		}
	}
	return plan
}
