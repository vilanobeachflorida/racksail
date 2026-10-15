package modules

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"sync"

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
)

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

// Manager tracks which modules are on and enforces the rules for turning
// them on and off: hardware requirements, dependencies and required modules.
type Manager struct {
	reg   *Registry
	facts func(ctx context.Context) (system.MachineFacts, error)

	mu     sync.Mutex
	states map[string]State
}

// NewManager returns a manager with every module off except the required
// ones, which are always on.
func NewManager(reg *Registry, facts func(ctx context.Context) (system.MachineFacts, error)) *Manager {
	m := &Manager{reg: reg, facts: facts, states: map[string]State{}}
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
	if m.facts != nil {
		facts, err := m.facts(ctx)
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

// TurnOn turns a module on after the checks pass.
func (m *Manager) TurnOn(ctx context.Context, id string) error {
	if err := m.CanTurnOn(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isOn(id) {
		m.states[id] = StateOn
	}
	return nil
}

// TurnOff turns a module off, keeping its data. Required modules and
// modules others depend on stay on.
func (m *Manager) TurnOff(ctx context.Context, id string) error {
	mod, ok := m.reg.Get(id)
	if !ok {
		return ErrUnknownModule.New("module", id)
	}
	if mod.Manifest().Required {
		return ErrRequired.New("module", mod.Manifest().Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
		sort.Strings(dependents)
		return ErrInUse.New("module", mod.Manifest().Name, "dependents", fmt.Sprint(dependents))
	}
	m.states[id] = StateOff
	return nil
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
