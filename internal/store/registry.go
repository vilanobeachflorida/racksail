package store

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// KindRegistry holds the defined kinds by name and TypeID prefix.
type KindRegistry struct {
	mu       sync.RWMutex
	byName   map[string]Kind
	byPrefix map[string]string // prefix -> kind name or reserved purpose
}

// NewKindRegistry returns an empty registry.
func NewKindRegistry() *KindRegistry {
	return &KindRegistry{byName: map[string]Kind{}, byPrefix: map[string]string{}}
}

// Register adds a kind. It fails when the kind's definition has problems or
// its name or prefix is already taken.
func (r *KindRegistry) Register(k Kind) error {
	if err := k.Err(); err != nil {
		return err
	}
	info := k.Info()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[info.Name]; ok {
		return fmt.Errorf("kind %q is registered twice", info.Name)
	}
	if owner, ok := r.byPrefix[info.Prefix]; ok {
		return fmt.Errorf("kind %q: prefix %q is already used by %s", info.Name, info.Prefix, owner)
	}
	r.byName[info.Name] = k
	r.byPrefix[info.Prefix] = info.Name
	return nil
}

// Replace swaps a registered kind for a new definition of it with the same
// name and prefix, as when the add-on that adds it is upgraded. Code that
// holds the old definition keeps a consistent one.
func (r *KindRegistry) Replace(k Kind) error {
	if err := k.Err(); err != nil {
		return err
	}
	info := k.Info()
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.byName[info.Name]
	if !ok {
		return fmt.Errorf("kind %q isn't registered", info.Name)
	}
	if old.Info().Prefix != info.Prefix {
		return fmt.Errorf("kind %q: its prefix %q can't become %q", info.Name, old.Info().Prefix, info.Prefix)
	}
	r.byName[info.Name] = k
	return nil
}

// Unregister removes a kind registered while the panel runs, such as one an
// add-on added, once nothing of it is left, and frees its prefix. Kinds
// registered as the binary starts are never removed.
func (r *KindRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byName[name]
	if !ok {
		return
	}
	delete(r.byName, name)
	if r.byPrefix[k.Info().Prefix] == name {
		delete(r.byPrefix, k.Info().Prefix)
	}
}

// Reserve claims a TypeID prefix for IDs that aren't resources, such as
// change sets or jobs, so no kind can take it.
func (r *KindRegistry) Reserve(prefix, purpose string) error {
	if !ValidPrefix(prefix) {
		return fmt.Errorf("invalid prefix %q", prefix)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner, ok := r.byPrefix[prefix]; ok {
		return fmt.Errorf("prefix %q is already used by %s", prefix, owner)
	}
	r.byPrefix[prefix] = purpose
	return nil
}

// Get returns the kind with the given name.
func (r *KindRegistry) Get(name string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.byName[name]
	return k, ok
}

// ForID returns the kind whose prefix matches id.
func (r *KindRegistry) ForID(id ID) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, ok := r.byPrefix[id.Prefix()]
	if !ok {
		return nil, false
	}
	k, ok := r.byName[name]
	return k, ok
}

// All returns every registered kind sorted by name.
func (r *KindRegistry) All() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.byName))
	for _, k := range r.byName {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().Name < out[j].Info().Name })
	return out
}

// TypeID prefixes of IDs that aren't resources. They are reserved in the
// process-wide registry so no kind can take them.
const (
	PrefixChangeSet = "chg"
	PrefixJob       = "job"
	PrefixEvent     = "evt"
	PrefixAudit     = "aud"
	PrefixSession   = "ses"
	PrefixToken     = "tok"
	PrefixRequest   = "req"
)

// The process-wide kind registry, filled while packages initialize.
var kinds = func() *KindRegistry {
	r := NewKindRegistry()
	for prefix, purpose := range map[string]string{
		PrefixChangeSet: "change sets", PrefixJob: "jobs", PrefixEvent: "events", PrefixAudit: "audit records",
		PrefixSession: "sessions", PrefixToken: "API tokens", PrefixRequest: "request IDs",
	} {
		r.byPrefix[prefix] = purpose
	}
	return r
}()

// Kinds returns the process-wide kind registry.
func Kinds() *KindRegistry { return kinds }

// registerErrs are the problems RegisterKind and ReservePrefix met while
// packages initialized.
var registerErrs = struct {
	sync.Mutex
	errs []error
}{}

func recordErr(err error) error {
	if err != nil {
		registerErrs.Lock()
		registerErrs.errs = append(registerErrs.errs, err)
		registerErrs.Unlock()
	}
	return err
}

// RegisterKind adds a kind to the process-wide registry, as the core does
// for its own kinds from init functions. Modules don't call it: their kinds
// are registered with them. Problems are also reported by Err.
func RegisterKind(k Kind) error { return recordErr(kinds.Register(k)) }

// ReservePrefix claims a TypeID prefix in the process-wide registry, for IDs
// that aren't resources. Problems are also reported by Err.
func ReservePrefix(prefix, purpose string) error { return recordErr(kinds.Reserve(prefix, purpose)) }

// Err reports the problems RegisterKind and ReservePrefix met, which every
// role checks before it starts.
func Err() error {
	registerErrs.Lock()
	defer registerErrs.Unlock()
	return errors.Join(registerErrs.errs...)
}
