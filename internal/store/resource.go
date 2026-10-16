package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// ScopeLevel says who owns resources of a kind.
type ScopeLevel string

// Scope levels.
const (
	// ScopeServer resources belong to the installation and are managed by
	// server admins, for example storage pools and IP addresses.
	ScopeServer ScopeLevel = "server"
	// ScopeReseller resources belong to a reseller, for example their plans.
	ScopeReseller ScopeLevel = "reseller"
	// ScopeAccount resources belong to one account, for example sites.
	ScopeAccount ScopeLevel = "account"
)

// Valid reports whether l is a known scope level.
func (l ScopeLevel) Valid() bool {
	return l == ScopeServer || l == ScopeReseller || l == ScopeAccount
}

// Scope names the owner of a resource.
type Scope struct {
	Level ScopeLevel `json:"level" enum:"server,reseller,account" doc:"Who owns the resource"`
	// ID is the reseller or account that owns the resource. It is empty at
	// server level.
	ID ID `json:"id,omitempty" doc:"The owning reseller or account; empty for server-wide resources"`
}

// ServerScope is the scope of installation-wide resources.
func ServerScope() Scope { return Scope{Level: ScopeServer} }

// ResellerScope is the scope of resources owned by a reseller.
func ResellerScope(id ID) Scope { return Scope{Level: ScopeReseller, ID: id} }

// AccountScope is the scope of resources owned by an account.
func AccountScope(id ID) Scope { return Scope{Level: ScopeAccount, ID: id} }

// Validate checks that the level is known and the owner is set when needed.
func (s Scope) Validate() error {
	switch s.Level {
	case ScopeServer:
		if s.ID != "" {
			return fmt.Errorf("server scope takes no owner id, got %q", s.ID)
		}
	case ScopeReseller, ScopeAccount:
		if !s.ID.Valid() {
			return fmt.Errorf("%s scope needs a valid owner id, got %q", s.Level, s.ID)
		}
	default:
		return fmt.Errorf("unknown scope level %q", s.Level)
	}
	return nil
}

// String formats the scope as "server", "reseller:<id>" or "account:<id>".
func (s Scope) String() string {
	if s.Level == ScopeServer || s.ID == "" {
		return string(s.Level)
	}
	return string(s.Level) + ":" + string(s.ID)
}

// ParseScope reads a scope written by Scope.String.
func ParseScope(text string) (Scope, error) {
	level, id, _ := strings.Cut(text, ":")
	s := Scope{Level: ScopeLevel(level), ID: ID(id)}
	if err := s.Validate(); err != nil {
		return Scope{}, err
	}
	return s, nil
}

// Meta is the part of a resource that is the same for every kind.
type Meta struct {
	ID    ID     `json:"id" doc:"Immutable TypeID"`
	Kind  string `json:"kind" doc:"Kind name, <module>.<name>"`
	Scope Scope  `json:"scope" doc:"Who owns the resource: the server, a reseller or an account"`
	// Server is the cluster server the resource is placed on. Empty means the
	// server running the panel.
	Server     ID                `json:"server,omitempty" doc:"Cluster server the resource is placed on; empty for the panel's own server"`
	Labels     map[string]string `json:"labels,omitempty" doc:"Free-form labels for grouping and filtering"`
	Generation int64             `json:"generation" doc:"Increases whenever the desired state changes"`
	// Revision increases on every write to the spec, labels or placement and
	// guards against lost updates. Status writes don't change it.
	Revision  int64      `json:"revision" doc:"Increases on every write except status updates; send it back to detect conflicting edits"`
	CreatedAt time.Time  `json:"createdAt" doc:"When the resource was made"`
	UpdatedAt time.Time  `json:"updatedAt" doc:"When its settings, labels or status last changed"`
	DeletedAt *time.Time `json:"deletedAt,omitempty" doc:"Set when the resource has been deleted and its system state is being removed"`
}

// Deleted reports whether the resource is marked as deleted.
func (m *Meta) Deleted() bool { return m.DeletedAt != nil }

// Resource is a stored resource with its spec and status as JSON. Generic
// code works with Resource; module code converts to the typed Object with
// its kind's Decode.
type Resource struct {
	Meta
	Spec   json.RawMessage `json:"spec" doc:"The settings: what should exist, which Racksail puts in place"`
	Status json.RawMessage `json:"status,omitempty" doc:"What Racksail last found and did about it"`
}

// Object is the typed view of a resource of a kind with spec S and status T.
type Object[S, T any] struct {
	Meta
	Spec   S `json:"spec"`
	Status T `json:"status"`
}

// Clone returns a deep copy of r.
func (r *Resource) Clone() *Resource {
	if r == nil {
		return nil
	}
	c := *r
	c.Labels = maps.Clone(r.Labels)
	c.Spec = bytes.Clone(r.Spec)
	c.Status = bytes.Clone(r.Status)
	if r.DeletedAt != nil {
		t := *r.DeletedAt
		c.DeletedAt = &t
	}
	return &c
}

// CommonStatus decodes the part of the status every kind shares.
func (r *Resource) CommonStatus() (Status, error) {
	var s Status
	if len(r.Status) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(r.Status, &s); err != nil {
		return s, fmt.Errorf("decoding status of %s: %w", r.ID, err)
	}
	return s, nil
}

// Phase is the overall state of a resource as seen by its reconciler.
type Phase string

// Phases shared by every kind. Kinds put finer detail in their own status
// fields and conditions.
const (
	PhasePending  Phase = "pending"
	PhaseApplying Phase = "applying"
	PhaseReady    Phase = "ready"
	PhaseDegraded Phase = "degraded"
	PhaseFailed   Phase = "failed"
	PhaseDeleting Phase = "deleting"
)

// ConditionStatus is the state of one condition.
type ConditionStatus string

// Condition states.
const (
	ConditionTrue    ConditionStatus = "true"
	ConditionFalse   ConditionStatus = "false"
	ConditionUnknown ConditionStatus = "unknown"
)

// Condition reports one aspect of a resource's state, such as "Ready" or
// "CertificateIssued".
type Condition struct {
	Type    string          `json:"type" doc:"What the condition is about, in CamelCase"`
	Status  ConditionStatus `json:"status" enum:"true,false,unknown" doc:"Whether the condition holds"`
	Reason  string          `json:"reason,omitempty" doc:"Machine-readable reason in CamelCase"`
	Message string          `json:"message,omitempty" doc:"English explanation"`
	// Code lets the UI translate Message.
	Code               i18n.Code `json:"code,omitempty" doc:"Names the message, for the panel to translate"`
	ObservedGeneration int64     `json:"observedGeneration,omitempty" doc:"The generation it was found at"`
	LastTransitionTime time.Time `json:"lastTransitionTime" doc:"When it last changed between holding and not"`
}

// Status is the part of a status every kind shares. Kind status types embed
// it so its fields appear at the top level of the status object:
//
//	type SiteStatus struct {
//		store.Status
//		URL string `json:"url,omitempty"`
//	}
type Status struct {
	Phase   Phase  `json:"phase,omitempty" enum:"pending,applying,ready,degraded,failed,deleting" doc:"Where it is: waiting, being put in place, ready, working only in part, failed or being deleted"`
	Message string `json:"message,omitempty" doc:"English summary of the current state"`
	// Code lets the UI translate Message.
	Code               i18n.Code   `json:"code,omitempty" doc:"Names the message, for the panel to translate"`
	ObservedGeneration int64       `json:"observedGeneration,omitempty" doc:"The generation the reconciler last acted on"`
	Conditions         []Condition `json:"conditions,omitempty" doc:"What the reconciler found, one condition each"`
}

// CommonStatus returns s. Through embedding it gives generic code access to
// the shared fields of any kind's status.
func (s *Status) CommonStatus() *Status { return s }

// Condition returns the condition of the given type.
func (s *Status) Condition(typ string) (Condition, bool) {
	for _, c := range s.Conditions {
		if c.Type == typ {
			return c, true
		}
	}
	return Condition{}, false
}

// IsTrue reports whether the condition of the given type is true.
func (s *Status) IsTrue(typ string) bool {
	c, ok := s.Condition(typ)
	return ok && c.Status == ConditionTrue
}

// SetCondition adds or replaces the condition of c.Type. The transition time
// only moves when the condition's status changes. It reports whether anything
// changed.
func (s *Status) SetCondition(c Condition, now time.Time) bool {
	for i, old := range s.Conditions {
		if old.Type != c.Type {
			continue
		}
		if old.Status == c.Status {
			c.LastTransitionTime = old.LastTransitionTime
		} else {
			c.LastTransitionTime = now.UTC()
		}
		if old == c {
			return false
		}
		s.Conditions[i] = c
		return true
	}
	c.LastTransitionTime = now.UTC()
	s.Conditions = append(s.Conditions, c)
	return true
}

var (
	labelKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._/-]{0,61}[a-z0-9])?$`)
	maxLabelValue   = 255
	maxLabels       = 64
)

// ValidateLabels checks label keys and values.
func ValidateLabels(labels map[string]string) error {
	if len(labels) > maxLabels {
		return fmt.Errorf("at most %d labels are allowed", maxLabels)
	}
	for k, v := range labels {
		if !labelKeyPattern.MatchString(k) {
			return fmt.Errorf("label key %q must be lowercase letters, digits, '.', '_', '-' or '/', starting and ending with a letter or digit", k)
		}
		if len(v) > maxLabelValue {
			return fmt.Errorf("label %q is longer than %d characters", k, maxLabelValue)
		}
	}
	return nil
}
