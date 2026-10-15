package modules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sort"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Profile is a bundle of modules the setup wizard turns on.
type Profile string

// Profiles.
const (
	AllInOne           Profile = "all-in-one"
	WebHosting         Profile = "web-hosting"
	HomeServer         Profile = "home-server"
	VirtualizationHost Profile = "virtualization-host"
	MailServer         Profile = "mail-server"
	DNSServer          Profile = "dns-server"
	Minimal            Profile = "minimal"
)

// ProfileInfo describes a profile in the setup wizard.
type ProfileInfo struct {
	ID          Profile `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
}

// Profiles lists the profiles in the order the wizard shows them.
func Profiles() []ProfileInfo {
	return []ProfileInfo{
		{AllInOne, "All-in-one", "Websites, email, DNS, databases, apps, storage and virtual machines on one server."},
		{WebHosting, "Web hosting", "Websites, databases, email and DNS for you or your customers."},
		{HomeServer, "Home server", "Storage, shares, apps and virtual machines for a home or small office."},
		{VirtualizationHost, "Virtualization host", "Virtual machines and containers on your own hardware."},
		{MailServer, "Mail server", "Email with its own DNS."},
		{DNSServer, "DNS server", "Authoritative DNS for your domains."},
		{Minimal, "Minimal", "Only the network module. Turn on what you need later."},
	}
}

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

// Error codes.
var (
	ErrUnknownModule = i18n.Define("modules.unknown", http.StatusNotFound, "There is no module called {module}.")
	ErrHardware      = i18n.Define("modules.hardware", http.StatusConflict, "{module} can't be turned on here: {reason}.")
	ErrDependency    = i18n.Define("modules.dependency", http.StatusConflict, "{module} needs {needs} to be on first.")
	ErrRequired      = i18n.Define("modules.required", http.StatusConflict, "{module} is required and can't be turned off.")
	ErrInUse         = i18n.Define("modules.in_use", http.StatusConflict, "{module} can't be turned off while {dependents} need it.")
	ErrBusy          = i18n.Define("modules.busy", http.StatusConflict, "{module} is already being turned on or off. Try again when that has finished.")
	ErrStartFailed   = i18n.Define("modules.start_failed", http.StatusInternalServerError, "{module} couldn't start. {reason}")
	ErrStopFailed    = i18n.Define("modules.stop_failed", http.StatusInternalServerError, "{module} couldn't stop. {reason}")
)

// Transition is what observe hooks at modules.<id>.on and modules.<id>.off
// receive as their data.
type Transition struct {
	Module string `json:"module"`
	From   State  `json:"from"`
	To     State  `json:"to"`
}

// HardwareProblems explains why a module can't run on a machine. An empty
// result means it can.
func HardwareProblems(man *Manifest, facts system.MachineFacts) []string {
	var out []string
	if len(man.Hardware.Arch) > 0 && facts.Arch != "" && !slices.Contains(man.Hardware.Arch, facts.Arch) {
		out = append(out, fmt.Sprintf("it runs on %v, this machine is %s", man.Hardware.Arch, facts.Arch))
	}
	if mem := int64(man.Hardware.MinMemoryMB) << 20; mem > 0 && facts.MemoryBytes > 0 && facts.MemoryBytes < mem {
		out = append(out, fmt.Sprintf("it needs %d MB of memory, this machine has %d MB", man.Hardware.MinMemoryMB, facts.MemoryBytes>>20))
	}
	for _, f := range man.Hardware.Features {
		switch {
		case f == "virtualization" && !facts.CPU.Virtualization:
			out = append(out, "the processor has no hardware virtualization (VT-x or AMD-V), or it is turned off in the firmware")
		case f == "kvm" && !facts.KVM:
			out = append(out, "/dev/kvm isn't available")
		case f == "iommu" && !facts.IOMMU:
			out = append(out, "no IOMMU is active")
		case f == "tpm" && !facts.TPM:
			out = append(out, "there is no TPM")
		case f == "uefi" && !facts.Firmware.UEFI:
			out = append(out, "the machine didn't boot with UEFI")
		}
	}
	return out
}

// ManagerOptions are the dependencies of a Manager.
type ManagerOptions struct {
	// Facts describes the machine, for hardware requirements. Without it,
	// they aren't checked.
	Facts func(ctx context.Context) (system.MachineFacts, error)
	// Services are what modules get when they start. When their Hooks are
	// set, turning a module on or off notifies modules.<id>.on and
	// modules.<id>.off.
	Services *Services
	Log      *slog.Logger
}

// Manager tracks which modules are on, enforces the rules for turning them
// on and off (hardware requirements, dependencies and required modules),
// and starts and stops them.
type Manager struct {
	reg  *Registry
	opts ManagerOptions

	mu     sync.Mutex
	states map[string]State
	busy   map[string]bool
	failed map[string]error
}

// NewManager returns a manager with every module off except the required
// ones, which are always on. Nothing is started until Boot or TurnOn.
func NewManager(reg *Registry, opts ManagerOptions) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Services == nil {
		opts.Services = &Services{}
	}
	m := &Manager{reg: reg, opts: opts, states: map[string]State{}, busy: map[string]bool{}, failed: map[string]error{}}
	for _, mod := range reg.All() {
		state := StateOff
		if mod.Manifest().Required {
			state = StateOn
		}
		m.states[mod.Manifest().ID] = state
	}
	return m
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
	out := make(map[string]State, len(m.states))
	for k, v := range m.states {
		out[k] = v
	}
	return out
}

func (m *Manager) isOn(id string) bool {
	s := m.states[id]
	return s == StateOn || s == StateActive
}

// CanTurnOn reports why a module can't be turned on, or nil.
func (m *Manager) CanTurnOn(ctx context.Context, id string) error {
	mod, ok := m.reg.Get(id)
	if !ok {
		return ErrUnknownModule.New("module", id)
	}
	man := mod.Manifest()
	if m.opts.Facts != nil {
		facts, err := m.opts.Facts(ctx)
		if err != nil {
			return err
		}
		if problems := HardwareProblems(man, facts); len(problems) > 0 {
			return ErrHardware.New("module", man.Name, "reason", problems[0])
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, dep := range man.Dependencies.Requires {
		if !m.isOn(dep) {
			return ErrDependency.New("module", man.Name, "needs", dep)
		}
	}
	if anyOf := man.Dependencies.RequiresAny; len(anyOf) > 0 && !slices.ContainsFunc(anyOf, m.isOn) {
		return ErrDependency.New("module", man.Name, "needs", fmt.Sprintf("one of %v", anyOf))
	}
	return nil
}

// TurnOn turns a module on after the checks pass: it starts the module and
// then notifies modules.<id>.on. A module that fails to start stays off.
func (m *Manager) TurnOn(ctx context.Context, id string) error {
	if err := m.CanTurnOn(ctx, id); err != nil {
		return err
	}
	mod, _ := m.reg.Get(id)
	m.mu.Lock()
	if m.busy[id] {
		m.mu.Unlock()
		return ErrBusy.New("module", mod.Manifest().Name)
	}
	if m.isOn(id) {
		m.mu.Unlock()
		return nil
	}
	from := m.states[id]
	m.busy[id] = true
	m.mu.Unlock()

	err := m.start(ctx, mod)
	m.mu.Lock()
	delete(m.busy, id)
	if err == nil {
		m.states[id] = StateOn
		delete(m.failed, id)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.notify(ctx, hooks.ModuleOn(id), Transition{Module: id, From: from, To: StateOn})
	return nil
}

// TurnOff turns a module off, keeping its data: it stops the module and
// then notifies modules.<id>.off. Required modules and modules others
// depend on stay on, and so does a module that fails to stop.
func (m *Manager) TurnOff(ctx context.Context, id string) error {
	mod, ok := m.reg.Get(id)
	if !ok {
		return ErrUnknownModule.New("module", id)
	}
	if mod.Manifest().Required {
		return ErrRequired.New("module", mod.Manifest().Name)
	}
	m.mu.Lock()
	if m.busy[id] {
		m.mu.Unlock()
		return ErrBusy.New("module", mod.Manifest().Name)
	}
	var dependents []string
	for _, other := range m.reg.All() {
		o := other.Manifest()
		if !m.isOn(o.ID) {
			continue
		}
		if slices.Contains(o.Dependencies.Requires, id) {
			dependents = append(dependents, o.Name)
		}
		if anyOf := o.Dependencies.RequiresAny; slices.Contains(anyOf, id) {
			stillMet := slices.ContainsFunc(anyOf, func(dep string) bool { return dep != id && m.isOn(dep) })
			if !stillMet {
				dependents = append(dependents, o.Name)
			}
		}
	}
	if len(dependents) > 0 {
		m.mu.Unlock()
		sort.Strings(dependents)
		return ErrInUse.New("module", mod.Manifest().Name, "dependents", fmt.Sprint(dependents))
	}
	if !m.isOn(id) {
		m.mu.Unlock()
		return nil
	}
	from := m.states[id]
	m.busy[id], m.states[id] = true, StateStopping
	m.mu.Unlock()

	err := m.stop(ctx, mod)
	m.mu.Lock()
	delete(m.busy, id)
	if err != nil {
		m.states[id] = from
	} else {
		m.states[id] = StateOff
		delete(m.failed, id)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.notify(ctx, hooks.ModuleOff(id), Transition{Module: id, From: from, To: StateOff})
	return nil
}

// Boot starts every module that is on or active, dependencies first, as the
// panel starts. A module that fails to start keeps its state and is
// reported by Failed; the others start regardless.
func (m *Manager) Boot(ctx context.Context) error {
	var ids []string
	for id, state := range m.States() {
		if state == StateOn || state == StateActive {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var errs []error
	for _, id := range dependencyOrder(m.reg, ids) {
		mod, _ := m.reg.Get(id)
		err := m.start(ctx, mod)
		m.mu.Lock()
		if err != nil {
			m.failed[id] = err
		} else {
			delete(m.failed, id)
		}
		m.mu.Unlock()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Shutdown stops every module that is on or active, dependents first, as
// the panel stops. Their states don't change, so they start again at the
// next boot.
func (m *Manager) Shutdown(ctx context.Context) {
	var ids []string
	for id, state := range m.States() {
		if state == StateOn || state == StateActive {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	order := dependencyOrder(m.reg, ids)
	for i := len(order) - 1; i >= 0; i-- {
		mod, _ := m.reg.Get(order[i])
		_ = m.stop(ctx, mod)
	}
}

// Failed returns the modules that didn't start at boot, with the reason.
func (m *Manager) Failed() map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.failed)
}

func (m *Manager) start(ctx context.Context, mod Module) error {
	s, ok := mod.(Starter)
	if !ok {
		return nil
	}
	if err := s.Start(ctx, m.opts.Services); err != nil {
		m.opts.Log.Error("starting a module failed", "module", mod.Manifest().ID, "error", err)
		return ErrStartFailed.Wrap(err, "module", mod.Manifest().Name, "reason", i18n.From(err).Message)
	}
	return nil
}

func (m *Manager) stop(ctx context.Context, mod Module) error {
	s, ok := mod.(Stopper)
	if !ok {
		return nil
	}
	if err := s.Stop(ctx); err != nil {
		m.opts.Log.Warn("stopping a module failed", "module", mod.Manifest().ID, "error", err)
		return ErrStopFailed.Wrap(err, "module", mod.Manifest().Name, "reason", i18n.From(err).Message)
	}
	return nil
}

// notify runs the observe hooks of a module's lifecycle point.
func (m *Manager) notify(ctx context.Context, point hooks.Point, t Transition) {
	if m.opts.Services.Hooks == nil {
		return
	}
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		actor = auth.System("modules")
	}
	m.opts.Services.Hooks.Notify(ctx, point, actor, t)
}

// ProfileModules lists the modules a profile turns on, in an order where
// dependencies come first.
func (m *Manager) ProfileModules(p Profile) []string {
	var ids []string
	for _, mod := range m.reg.All() {
		man := mod.Manifest()
		if man.Required || slices.Contains(man.Profiles, p) {
			ids = append(ids, man.ID)
		}
	}
	return dependencyOrder(m.reg, ids)
}

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

// ApplyProfile turns on a profile's modules. Modules whose hardware
// requirements aren't met, such as virtual machines without hardware
// virtualization, are skipped and returned.
func (m *Manager) ApplyProfile(ctx context.Context, p Profile) (skipped []string, err error) {
	for _, id := range m.ProfileModules(p) {
		if err := m.TurnOn(ctx, id); err != nil {
			if i18n.CodeOf(err) == ErrHardware.Code || i18n.CodeOf(err) == ErrDependency.Code {
				skipped = append(skipped, id)
				continue
			}
			return skipped, err
		}
	}
	return skipped, nil
}
