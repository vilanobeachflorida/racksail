package events

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Core event types: what the core itself publishes, beyond the events every
// kind gets.
var (
	PanelStarted     = Type{Name: "core.panel.started", Description: "The panel started.", Severity: SeverityNotice}
	HookDisabled     = Type{Name: "core.hook.disabled", Description: "A hook failed too many times in a row and was switched off.", Severity: SeverityWarning}
	ChangeSetApplied = Type{Name: "core.changeset.applied", Description: "A change set was applied and its result verified.", Severity: SeverityInfo, Data: ChangeSetData{}}
	ChangeSetFailed  = Type{Name: "core.changeset.failed", Description: "Applying a change set failed; it can be undone.", Severity: SeverityError, Data: ChangeSetData{}}
	ChangeSetUndone  = Type{Name: "core.changeset.undone", Description: "A change set was undone by a new one.", Severity: SeverityNotice, Data: ChangeSetData{}}
)

// ChangeSetData is the data of the core.changeset events.
type ChangeSetData struct {
	Title string `json:"title" doc:"What the change set does"`
	// Resources are the resources the change set touched.
	Resources []store.ID `json:"resources" doc:"The resources the change set touched"`
	// UndoneBy is the change set that undid this one.
	UndoneBy store.ID `json:"undoneBy,omitempty"`
}

var registry = struct {
	sync.RWMutex
	types map[string]Type
	errs  []error
}{types: map[string]Type{}}

func init() {
	for _, t := range []Type{PanelStarted, HookDisabled, ChangeSetApplied, ChangeSetFailed, ChangeSetUndone} {
		RegisterType(t)
	}
}

// RegisterType adds an event type the core publishes, from an init
// function. Modules declare theirs with modules.EventProvider instead.
// Problems are reported by Err.
func RegisterType(t Type) {
	registry.Lock()
	defer registry.Unlock()
	switch {
	case !ValidType(t.Name):
		registry.errs = append(registry.errs, fmt.Errorf("events: invalid event type %q", t.Name))
	case !t.Severity.Valid():
		registry.errs = append(registry.errs, fmt.Errorf("events: %s has no valid severity", t.Name))
	case t.Description == "":
		registry.errs = append(registry.errs, fmt.Errorf("events: %s has no description", t.Name))
	case registry.types[t.Name].Name != "":
		registry.errs = append(registry.errs, fmt.Errorf("events: %s is registered twice", t.Name))
	default:
		registry.types[t.Name] = t
	}
}

// Err reports problems found while event types were registered.
func Err() error {
	registry.RLock()
	defer registry.RUnlock()
	return errors.Join(registry.errs...)
}

// Catalog lists every event type: the core's own, those of every kind, and
// the extra ones modules declare, sorted by name. Webhooks, automations and
// the docs offer these.
func Catalog(kinds []store.Kind, extra ...[]Type) []Type {
	seen := map[string]bool{}
	var out []Type
	add := func(t Type) {
		if !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	registry.RLock()
	for _, t := range registry.types {
		add(t)
	}
	registry.RUnlock()
	for _, k := range kinds {
		for _, t := range KindTypes(k) {
			add(t)
		}
	}
	for _, list := range extra {
		for _, t := range list {
			add(t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
