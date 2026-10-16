package modules

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/hooks/addons"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Registry holds the modules compiled into the binary.
type Registry struct {
	mu   sync.RWMutex
	mods map[string]Module
	errs []error
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{mods: map[string]Module{}} }

// Add registers a module and its kinds in kinds.
func (r *Registry) Add(m Module, kinds *store.KindRegistry) error {
	err := r.add(m, kinds)
	if err != nil {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
	return err
}

func (r *Registry) add(m Module, kinds *store.KindRegistry) error {
	if e, ok := m.(interface{ ManifestErr() error }); ok && e.ManifestErr() != nil {
		return fmt.Errorf("modules: %T: %w", m, e.ManifestErr())
	}
	man := m.Manifest()
	if man == nil {
		return fmt.Errorf("modules: %T has no manifest", m)
	}
	r.mu.Lock()
	if _, ok := r.mods[man.ID]; ok {
		r.mu.Unlock()
		return fmt.Errorf("modules: %s is registered twice", man.ID)
	}
	r.mods[man.ID] = m
	r.mu.Unlock()
	if kp, ok := m.(KindProvider); ok && kinds != nil {
		var errs []error
		for _, k := range kp.Kinds() {
			if k.Info().Module() != man.ID {
				errs = append(errs, fmt.Errorf("modules: %s declares kind %s, which belongs to another module", man.ID, k.Info().Name))
				continue
			}
			if err := kinds.Register(k); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return nil
}

// Get returns a module by id.
func (r *Registry) Get(id string) (Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.mods[id]
	return m, ok
}

// All returns every module sorted by id.
func (r *Registry) All() []Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Module, 0, len(r.mods))
	for _, m := range r.mods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest().ID < out[j].Manifest().ID })
	return out
}

// IDs returns every module id, sorted.
func (r *Registry) IDs() []string {
	var ids []string
	for _, m := range r.All() {
		ids = append(ids, m.Manifest().ID)
	}
	return ids
}

// Err reports registration problems and dependencies on modules that don't
// exist.
func (r *Registry) Err() error {
	r.mu.RLock()
	errs := slices.Clone(r.errs)
	mods := r.mods
	for id, m := range mods {
		d := m.Manifest().Dependencies
		for _, dep := range slices.Concat(d.Requires, d.RequiresAny, d.Uses) {
			if _, ok := mods[dep]; !ok {
				errs = append(errs, fmt.Errorf("modules: %s depends on %s, which isn't a module", id, dep))
			}
		}
	}
	r.mu.RUnlock()
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}

// Ownership returns what the module that keeps resellers says about which
// reseller an account belongs to, or nil when no module says. Two modules
// saying it is an error.
func (r *Registry) Ownership(svc *Services) (auth.Ownership, error) {
	var found auth.Ownership
	var by string
	for _, m := range r.All() {
		op, ok := m.(OwnershipProvider)
		if !ok {
			continue
		}
		id := m.Manifest().ID
		if found != nil {
			return nil, fmt.Errorf("modules: %s and %s both say which reseller an account belongs to", by, id)
		}
		if found = op.Ownership(svc); found == nil {
			return nil, fmt.Errorf("modules: %s provides no ownership", id)
		}
		by = id
	}
	return found, nil
}

// Accounts returns what the module that keeps accounts says about where
// their add-ons run, or nil when no module says. Two modules saying it is
// an error.
func (r *Registry) Accounts(svc *Services) (addons.Accounts, error) {
	var found addons.Accounts
	var by string
	for _, m := range r.All() {
		ap, ok := m.(AccountProvider)
		if !ok {
			continue
		}
		id := m.Manifest().ID
		if found != nil {
			return nil, fmt.Errorf("modules: %s and %s both say where accounts' add-ons run", by, id)
		}
		if found = ap.Accounts(svc); found == nil {
			return nil, fmt.Errorf("modules: %s provides no accounts", id)
		}
		by = id
	}
	return found, nil
}

// The modules compiled into the binary. modules/all imports every one.
var registry = NewRegistry()

// Register adds a module to the process-wide registry and its kinds to the
// process-wide kind registry. Modules call it from an init function.
// Problems are reported by Default().Err().
func Register(m Module) {
	_ = registry.Add(m, store.Kinds())
}

// Default returns the process-wide registry.
func Default() *Registry { return registry }
