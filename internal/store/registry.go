package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// KindRegistry holds the defined kinds by name and TypeID prefix.
type KindRegistry struct {
	mu       sync.RWMutex
	byName   map[string]Kind
	byPrefix map[string]string // prefix -> kind name or reserved purpose
	// byPath and byOperation say which kind has each API path and operation
	// ID, which no two kinds may share.
	byPath      map[string]string
	byOperation map[string]string
}

// NewKindRegistry returns an empty registry.
func NewKindRegistry() *KindRegistry {
	return &KindRegistry{byName: map[string]Kind{}, byPrefix: map[string]string{}, byPath: map[string]string{}, byOperation: map[string]string{}}
}

// Register adds a kind. It fails when the kind's definition has problems, or
// its name, its prefix, its API path or the ID of one of the operations the
// API generates for it is another kind's: two kinds of a module with the
// same plural, or a kind named site_history beside a kind site, whose history
// operation would have its get operation's ID.
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
	if owner, ok := r.byPath[info.Path()]; ok {
		return fmt.Errorf("kind %q: its operations would be served at %s, which are %s's; give it another plural", info.Name, info.Path(), owner)
	}
	ids := info.OperationIDs()
	for _, id := range ids {
		if owner, ok := r.byOperation[id]; ok {
			return fmt.Errorf("kind %q: its operation %s would have the ID of an operation of %s; rename the kind or the action", info.Name, id, owner)
		}
	}
	r.byName[info.Name] = k
	r.byPrefix[info.Prefix] = info.Name
	r.byPath[info.Path()] = info.Name
	for _, id := range ids {
		r.byOperation[id] = info.Name
	}
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

// OwnerKind returns the kind whose resources own the resources of a scope
// level: core.account owns accounts' resources, and core.reseller, or a
// module's kind named reseller that isn't an account's, as the module that
// keeps resellers defines it, owns resellers'. The panel's screens choose
// owners by the same rule. An owner belongs to itself, as an account's scope
// is the account.
func (r *KindRegistry) OwnerKind(level ScopeLevel) (Kind, bool) {
	if level != ScopeAccount && level != ScopeReseller {
		return nil, false
	}
	if k, ok := r.Get("core." + string(level)); ok {
		return k, true
	}
	for _, k := range r.All() {
		info := k.Info()
		if strings.HasSuffix(info.Name, "."+string(level)) && info.Scope != ScopeAccount {
			return k, true
		}
	}
	return nil, false
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
