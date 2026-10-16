package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// State is where a module is in its lifecycle.
type State string

// Module states.
const (
	// StateOff: not visible, nothing installed, no data, no network calls.
	StateOff State = "off"
	// StateOn: visible and ready; nothing heavy is installed yet.
	StateOn State = "on"
	// StateActive: a resource needs it, so its software runs as needed.
	StateActive State = "active"
	// StateStopping: turning off, keeping data and settings.
	StateStopping State = "stopping"
	// StateRemoving: uninstalling software; data goes only after a typed
	// confirmation and an offered export.
	StateRemoving State = "removing"
)

// IsOn reports whether a module in this state is visible and usable.
func (s State) IsOn() bool { return s == StateOn || s == StateActive }

// Error codes.
var (
	ErrUnknownModule   = i18n.Define("modules.unknown", http.StatusNotFound, "There is no module called {module}.")
	ErrHardware        = i18n.Define("modules.hardware", http.StatusConflict, "{module} can't be turned on here: {reason}.")
	ErrDependency      = i18n.Define("modules.dependency", http.StatusConflict, "{module} needs {needs} to be on first.")
	ErrRequired        = i18n.Define("modules.required", http.StatusConflict, "{module} is required and can't be turned off.")
	ErrInUse           = i18n.Define("modules.in_use", http.StatusConflict, "{module} can't be turned off while {dependents} need it.")
	ErrBusy            = i18n.Define("modules.busy", http.StatusConflict, "{module} is already being turned on or off. Try again when that has finished.")
	ErrStartFailed     = i18n.Define("modules.start_failed", http.StatusInternalServerError, "{module} couldn't start. {reason}")
	ErrStopFailed      = i18n.Define("modules.stop_failed", http.StatusInternalServerError, "{module} couldn't stop. {reason}")
	ErrOff             = i18n.Define("modules.off", http.StatusConflict, "{module} is off. Turn it on first.")
	ErrAlreadyOn       = i18n.Define("modules.already_on", http.StatusConflict, "{module} is already on.")
	ErrAlreadyOff      = i18n.Define("modules.already_off", http.StatusConflict, "{module} is already off.")
	ErrHasResources    = i18n.Define("modules.has_resources", http.StatusConflict, "{module} still has {count} resources, and it can't keep them while it is off. Delete them first, or remove the module with its data.")
	ErrRemoveResources = i18n.Define("modules.remove_resources", http.StatusConflict, "Removing {module} deletes its {count} resources. Remove it with its data, or keep the data.")
	ErrConfirm         = i18n.Define("modules.confirm", http.StatusUnprocessableEntity, "Type {name} to confirm removing it.")
	ErrNotDeletable    = i18n.Define("modules.not_deletable", http.StatusConflict, "Modules come with Racksail and can't be deleted. Turn {module} off or remove it instead.")
	ErrNotReady        = i18n.Define("modules.not_ready", http.StatusServiceUnavailable, "The panel is still setting up {module}. Try again in a moment.")
	ErrUnknownProfile  = i18n.Define("modules.unknown_profile", http.StatusNotFound, "There is no profile called {profile}.")
	ErrNothingToTurnOn = i18n.Define("modules.nothing_to_turn_on", http.StatusConflict, "Every module of the {profile} profile that can run here is already on.")
	ErrOptIn           = i18n.Define("modules.opt_in", http.StatusConflict, "{software} is only installed when an admin allows it in {module}'s settings.")
	ErrNotUpstream     = i18n.Define("modules.not_upstream", http.StatusUnprocessableEntity, "{module} doesn't install {software}.")
	ErrCreatedBySystem = i18n.Define("modules.created_by_system", http.StatusForbidden, "Modules come with Racksail; the panel adds them itself.")
)

// Transition is what observe hooks at modules.<id>.on and modules.<id>.off
// receive as their data.
type Transition struct {
	Module string `json:"module"`
	From   State  `json:"from"`
	To     State  `json:"to"`
}

// ManagerOptions are the dependencies of a Manager.
type ManagerOptions struct {
	// Facts describes the machine, for hardware requirements. Without it,
	// they aren't checked.
	Facts func(ctx context.Context) (system.MachineFacts, error)
	// Services are what modules get when they start, and what the manager
	// keeps module states with: their Store and Pipeline. When their Hooks
	// are set, turning a module on or off notifies modules.<id>.on and
	// modules.<id>.off.
	Services *Services
	// Enqueue asks for a resource to be reconciled again, as the reconcile
	// queue's Enqueue does; a module turned on again has what it kept while
	// it was off put back. Nil skips it.
	Enqueue func(id store.ID, reason string)
	Log     *slog.Logger
	Now     func() time.Time
}

// Manager keeps the module states, which live as core.module resources:
// it creates one for every module when the panel first starts with it,
// builds the change sets that turn modules on, off and remove them,
// reconciles core.module resources by starting and stopping modules,
// counts what uses each module, and holds back the reconcilers of modules
// that are off.
type Manager struct {
	reg  *Registry
	opts ManagerOptions

	mu sync.Mutex
	// ids are the core.module resources by module id, and cache their
	// spec and status.
	ids     map[string]store.ID
	enabled map[string]bool
	states  map[string]State
	started map[string]bool
	failed  map[string]error
	// kinds maps every module kind to its module.
	kinds map[string]string
	// refresh asks the usage watcher to count a module's resources again.
	refresh chan string
}

// NewManager returns a manager. Until Boot loads the states kept in the
// store, every module counts as off except the required ones.
func NewManager(reg *Registry, opts ManagerOptions) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Services == nil {
		opts.Services = &Services{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &Manager{
		reg: reg, opts: opts, ids: map[string]store.ID{}, enabled: map[string]bool{}, states: map[string]State{},
		started: map[string]bool{}, failed: map[string]error{}, kinds: map[string]string{}, refresh: make(chan string, 64),
	}
	for _, mod := range reg.All() {
		man := mod.Manifest()
		m.states[man.ID] = StateOff
		if man.Required {
			m.enabled[man.ID] = true
		}
		if kp, ok := mod.(KindProvider); ok {
			for _, k := range kp.Kinds() {
				m.kinds[k.Info().Name] = man.ID
			}
		}
	}
	return m
}

// Registry returns the modules the manager manages.
func (m *Manager) Registry() *Registry { return m.reg }

// ModuleOf returns the module a kind belongs to; core kinds belong to none.
func (m *Manager) ModuleOf(kind string) (string, bool) {
	id, ok := m.kinds[kind]
	return id, ok
}

// KindsOf lists the kinds of a module, sorted.
func (m *Manager) KindsOf(module string) []string {
	var out []string
	for k, id := range m.kinds {
		if id == module {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// State returns the state of a module.
func (m *Manager) State(id string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[id]
}

// States returns every module's state.
func (m *Manager) States() map[string]State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.states)
}

// Enabled reports whether a module is meant to be on.
func (m *Manager) Enabled(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled[id]
}

// Failed returns the modules that didn't start, with the reason.
func (m *Manager) Failed() map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.failed)
}

// Resource returns the ID of a module's core.module resource.
func (m *Manager) Resource(id string) (store.ID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rid, ok := m.ids[id]
	return rid, ok
}

func (m *Manager) remember(obj *store.Object[ModuleSpec, ModuleStatus]) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ids[obj.Spec.Module] = obj.ID
	m.enabled[obj.Spec.Module] = obj.Spec.Enabled
	state := obj.Status.State
	if state == "" {
		state = StateOff
	}
	m.states[obj.Spec.Module] = state
}

func (m *Manager) setState(id string, s State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[id] = s
}

// systemActor is who the manager acts as.
func systemActor() *auth.Actor { return auth.System("modules") }

// Boot loads the module states from the store, creates the core.module
// resources of modules the panel hasn't seen before, as one change set,
// and starts every module that is on, dependencies first. A module that
// fails to start keeps its state and is reported by Failed; the others
// start regardless.
func (m *Manager) Boot(ctx context.Context) error {
	svc := m.opts.Services
	if svc.Store == nil {
		return errors.New("modules: the manager needs a store")
	}
	known := map[string]bool{}
	cursor := ""
	for {
		page, next, err := Kind.List(ctx, svc.Store, store.Query{Cursor: cursor, Limit: store.MaxLimit})
		if err != nil {
			return err
		}
		for _, obj := range page {
			if _, ok := m.reg.Get(obj.Spec.Module); !ok {
				// A module this binary no longer has; it stays off.
				continue
			}
			m.remember(obj)
			known[obj.Spec.Module] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	var create []changeset.Change
	added := map[string]store.ID{}
	for _, mod := range m.reg.All() {
		man := mod.Manifest()
		if known[man.ID] {
			continue
		}
		spec, _ := json.Marshal(ModuleSpec{Module: man.ID, Enabled: man.Required})
		id := store.NewID(Kind.Info().Prefix)
		create = append(create, changeset.Change{Op: changeset.OpCreate, Kind: Kind.Name(), ID: id, Scope: store.ServerScope(), Spec: spec})
		added[man.ID] = id
	}
	if len(create) > 0 {
		if svc.Pipeline == nil {
			return errors.New("modules: the manager needs the change pipeline to add modules")
		}
		if _, err := svc.Pipeline.Submit(ctx, changeset.Request{Actor: systemActor(), Title: "Add the modules of this version", Changes: create}); err != nil {
			return fmt.Errorf("modules: adding new modules: %w", err)
		}
		m.mu.Lock()
		maps.Copy(m.ids, added)
		m.mu.Unlock()
	}
	var ids []string
	for id, st := range m.States() {
		if st.IsOn() && m.Enabled(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var errs []error
	for _, id := range dependencyOrder(m.reg, ids) {
		if err := m.start(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Shutdown stops every module that runs, dependents first, as the panel
// stops. Their states don't change, so they start again at the next boot.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	var ids []string
	for id, on := range m.started {
		if on {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	sort.Strings(ids)
	order := dependencyOrder(m.reg, ids)
	for i := len(order) - 1; i >= 0; i-- {
		_ = m.stop(ctx, order[i])
	}
}

// start runs a module's Starter unless it already runs.
func (m *Manager) start(ctx context.Context, id string) error {
	mod, ok := m.reg.Get(id)
	if !ok {
		return ErrUnknownModule.New("module", id)
	}
	m.mu.Lock()
	if m.started[id] {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	var err error
	if s, ok := mod.(Starter); ok {
		if startErr := s.Start(ctx, m.opts.Services); startErr != nil {
			m.opts.Log.Error("starting a module failed", "module", id, "error", startErr)
			err = ErrStartFailed.Wrap(startErr, "module", mod.Manifest().Name, "reason", i18n.From(startErr).Message)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.failed[id] = err
		return err
	}
	delete(m.failed, id)
	m.started[id] = true
	return nil
}

// stop runs a module's Stopper when it runs.
func (m *Manager) stop(ctx context.Context, id string) error {
	mod, ok := m.reg.Get(id)
	if !ok {
		return ErrUnknownModule.New("module", id)
	}
	m.mu.Lock()
	running := m.started[id]
	m.mu.Unlock()
	if !running {
		return nil
	}
	if s, ok := mod.(Stopper); ok {
		if err := s.Stop(ctx); err != nil {
			m.opts.Log.Warn("stopping a module failed", "module", id, "error", err)
			return ErrStopFailed.Wrap(err, "module", mod.Manifest().Name, "reason", i18n.From(err).Message)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.started, id)
	return nil
}

// Started reports whether a module's work in the panel runs.
func (m *Manager) Started(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started[id]
}

// notify runs the observe hooks of a module's lifecycle point.
func (m *Manager) notify(ctx context.Context, point hooks.Point, t Transition) {
	if m.opts.Services.Hooks == nil {
		return
	}
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		actor = systemActor()
	}
	m.opts.Services.Hooks.Notify(ctx, point, actor, t)
}

// dependencyOrder sorts ids so each comes after the modules it needs that
// are among them.
func dependencyOrder(reg *Registry, ids []string) []string {
	var out []string
	done := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		if done[id] || !slices.Contains(ids, id) {
			return
		}
		done[id] = true
		if mod, ok := reg.Get(id); ok {
			for _, dep := range mod.Manifest().Dependencies.Requires {
				visit(dep)
			}
			for _, dep := range mod.Manifest().Dependencies.RequiresAny {
				visit(dep)
			}
		}
		out = append(out, id)
	}
	for _, id := range ids {
		visit(id)
	}
	return out
}

func (m *Manager) manifest(id string) (*Manifest, error) {
	mod, ok := m.reg.Get(id)
	if !ok {
		return nil, ErrUnknownModule.New("module", id)
	}
	return mod.Manifest(), nil
}

func (m *Manager) resourceOf(id string) (store.ID, error) {
	man, err := m.manifest(id)
	if err != nil {
		return "", err
	}
	rid, ok := m.Resource(id)
	if !ok {
		return "", ErrNotReady.New("module", man.Name)
	}
	return rid, nil
}

func action(rid store.ID, name string, input any) changeset.Change {
	c := changeset.Change{Op: changeset.OpAction, Kind: Kind.Name(), ID: rid, Action: name}
	if input != nil {
		c.Input, _ = json.Marshal(input)
	}
	return c
}

// TurnOnRequest builds the change set that turns a module on, together
// with the modules it needs that are off, dependencies first. A module
// that needs any one of several others, none of them on, needs the person
// to choose which.
func (m *Manager) TurnOnRequest(id string) (changeset.Request, error) {
	man, err := m.manifest(id)
	if err != nil {
		return changeset.Request{}, err
	}
	if m.Enabled(id) {
		return changeset.Request{}, ErrAlreadyOn.New("module", man.Name)
	}
	var need []string
	var visit func(id string) error
	visit = func(id string) error {
		if slices.Contains(need, id) || m.Enabled(id) {
			return nil
		}
		man, err := m.manifest(id)
		if err != nil {
			return err
		}
		for _, dep := range man.Dependencies.Requires {
			if err := visit(dep); err != nil {
				return err
			}
		}
		if anyOf := man.Dependencies.RequiresAny; len(anyOf) > 0 && !slices.ContainsFunc(anyOf, func(d string) bool { return m.Enabled(d) || slices.Contains(need, d) }) {
			var names []string
			for _, d := range anyOf {
				if dm, err := m.manifest(d); err == nil {
					names = append(names, dm.Name)
				}
			}
			return ErrDependency.New("module", man.Name, "needs", "one of "+strings.Join(names, ", "))
		}
		need = append(need, id)
		return nil
	}
	if err := visit(id); err != nil {
		return changeset.Request{}, err
	}
	return m.turnOnRequest(need)
}

func (m *Manager) turnOnRequest(ids []string) (changeset.Request, error) {
	req := changeset.Request{}
	var names []string
	for _, id := range ids {
		rid, err := m.resourceOf(id)
		if err != nil {
			return changeset.Request{}, err
		}
		man, _ := m.manifest(id)
		names = append(names, man.Name)
		req.Changes = append(req.Changes, action(rid, ActionTurnOn, nil))
	}
	switch len(names) {
	case 1:
		req.Title = "Turn on " + names[0]
	case 2:
		req.Title = "Turn on " + names[1] + " and " + names[0]
	default:
		req.Title = fmt.Sprintf("Turn on %s and %d more modules", names[len(names)-1], len(names)-1)
	}
	return req, nil
}

// TurnOffRequest builds the change set that turns a module off.
func (m *Manager) TurnOffRequest(id string) (changeset.Request, error) {
	man, err := m.manifest(id)
	if err != nil {
		return changeset.Request{}, err
	}
	if !m.Enabled(id) {
		return changeset.Request{}, ErrAlreadyOff.New("module", man.Name)
	}
	rid, err := m.resourceOf(id)
	if err != nil {
		return changeset.Request{}, err
	}
	return changeset.Request{Title: "Turn off " + man.Name, Changes: []changeset.Change{action(rid, ActionTurnOff, nil)}}, nil
}

// RemoveRequest builds the change set that removes a module: unless
// keepData is set, a delete of every resource of its kinds, whose
// reconcilers remove what they set up, then the removal itself, which
// uninstalls the module's software. confirm is what the person typed,
// which must be the module's id or name.
func (m *Manager) RemoveRequest(ctx context.Context, id, confirm string, keepData bool) (changeset.Request, error) {
	man, err := m.manifest(id)
	if err != nil {
		return changeset.Request{}, err
	}
	if !confirms(man, confirm) {
		return changeset.Request{}, ErrConfirm.New("name", man.Name)
	}
	rid, err := m.resourceOf(id)
	if err != nil {
		return changeset.Request{}, err
	}
	req := changeset.Request{Title: "Remove " + man.Name}
	if !keepData {
		for _, kind := range m.KindsOf(id) {
			if err := m.each(ctx, m.opts.Services.Store, kind, func(r *store.Resource) {
				req.Changes = append(req.Changes, changeset.Change{Op: changeset.OpDelete, Kind: kind, ID: r.ID, Revision: r.Revision})
			}); err != nil {
				return changeset.Request{}, err
			}
		}
	} else {
		req.Title += ", keeping its data"
	}
	req.Changes = append(req.Changes, action(rid, ActionRemove, RemoveInput{Confirm: confirm, KeepData: keepData}))
	return req, nil
}

func confirms(man *Manifest, typed string) bool {
	typed = strings.TrimSpace(typed)
	return strings.EqualFold(typed, man.ID) || strings.EqualFold(typed, man.Name)
}

// each calls fn for every resource of a kind that isn't deleted.
func (m *Manager) each(ctx context.Context, rd store.Reader, kind string, fn func(r *store.Resource)) error {
	if rd == nil {
		return nil
	}
	cursor := ""
	for {
		page, err := rd.List(ctx, store.Query{Kind: kind, Cursor: cursor, Limit: store.MaxLimit})
		if err != nil {
			return err
		}
		for _, r := range page.Items {
			if !r.Deleted() {
				fn(r)
			}
		}
		if page.Next == "" {
			return nil
		}
		cursor = page.Next
	}
}

// usage counts the resources of a module's kinds.
func (m *Manager) usage(ctx context.Context, rd store.Reader, id string) (Usage, error) {
	u := Usage{}
	for _, kind := range m.KindsOf(id) {
		n := 0
		if err := m.each(ctx, rd, kind, func(*store.Resource) { n++ }); err != nil {
			return u, err
		}
		if n > 0 {
			if u.Kinds == nil {
				u.Kinds = map[string]int{}
			}
			u.Kinds[kind] = n
			u.Resources += n
		}
	}
	return u, nil
}

// planned returns the spec a module has in the planned state rd shows, or
// nil when it has no core.module resource yet.
func (m *Manager) planned(ctx context.Context, rd store.Reader, id string) (*ModuleSpec, error) {
	rid, ok := m.Resource(id)
	if !ok || rd == nil {
		return nil, nil
	}
	obj, err := Kind.Get(ctx, rd, rid)
	if store.ErrNotFound.Is(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &obj.Spec, nil
}

// plannedOn reports whether a module is meant to be on in the planned state.
func (m *Manager) plannedOn(ctx context.Context, rd store.Reader, id string) (bool, error) {
	spec, err := m.planned(ctx, rd, id)
	if err != nil {
		return false, err
	}
	if spec == nil {
		return m.Enabled(id), nil
	}
	return spec.Enabled, nil
}

// dependents lists the names of modules meant to be on that need id and
// would be left without anything they need.
func (m *Manager) dependents(ctx context.Context, rd store.Reader, id string) ([]string, error) {
	var out []string
	for _, other := range m.reg.All() {
		o := other.Manifest()
		if o.ID == id {
			continue
		}
		on, err := m.plannedOn(ctx, rd, o.ID)
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		if slices.Contains(o.Dependencies.Requires, id) {
			out = append(out, o.Name)
			continue
		}
		if anyOf := o.Dependencies.RequiresAny; slices.Contains(anyOf, id) {
			still := false
			for _, dep := range anyOf {
				if dep == id {
					continue
				}
				on, err := m.plannedOn(ctx, rd, dep)
				if err != nil {
					return nil, err
				}
				still = still || on
			}
			if !still {
				out = append(out, o.Name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
