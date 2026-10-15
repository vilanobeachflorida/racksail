package store

import (
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

// RegisterKind adds a kind to the process-wide registry. Modules don't call
// it directly: modules.Register registers the kinds a module declares.
func RegisterKind(k Kind) error { return kinds.Register(k) }

// ReservePrefix claims a TypeID prefix in the process-wide registry.
func ReservePrefix(prefix, purpose string) error { return kinds.Reserve(prefix, purpose) }
