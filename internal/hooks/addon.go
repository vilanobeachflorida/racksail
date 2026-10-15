package hooks

import (
	"bytes"
	"fmt"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/spec"
)

// APIVersion is the hook API version this build speaks. Add-ons declare the
// versions they support; deprecated versions keep working for a published
// period.
const APIVersion = "v1"

// AddonManifest is an add-on's addon.yaml.
type AddonManifest struct {
	Format      int           `yaml:"format" json:"format"`
	ID          string        `yaml:"id" json:"id"`
	Name        string        `yaml:"name" json:"name"`
	Version     string        `yaml:"version" json:"version"`
	License     string        `yaml:"license" json:"license"`
	Source      string        `yaml:"source" json:"source"`
	Author      string        `yaml:"author" json:"author"`
	Description string        `yaml:"description" json:"description"`
	APIVersions []string      `yaml:"apiVersions" json:"apiVersions"`
	Hooks       []HookDecl    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Provides    []string      `yaml:"provides,omitempty" json:"provides,omitempty"`
	Permissions Permissions   `yaml:"permissions" json:"permissions"`
	UI          []UIExtension `yaml:"ui,omitempty" json:"ui,omitempty"`
	Routes      []AddonRoute  `yaml:"routes,omitempty" json:"routes,omitempty"`
	Kinds       []AddonKind   `yaml:"kinds,omitempty" json:"kinds,omitempty"`
}

// HookDecl is a hook an add-on registers.
type HookDecl struct {
	Point       string      `yaml:"point" json:"point"`
	Kind        Kind        `yaml:"kind" json:"kind"`
	Priority    int         `yaml:"priority,omitempty" json:"priority,omitempty"`
	Deadline    string      `yaml:"deadline,omitempty" json:"deadline,omitempty"`
	FailureMode FailureMode `yaml:"failureMode,omitempty" json:"failureMode,omitempty"`
	Security    bool        `yaml:"security,omitempty" json:"security,omitempty"`
}

// DeadlineDuration parses Deadline, defaulting to DefaultDeadline.
func (h HookDecl) DeadlineDuration() (time.Duration, error) {
	if h.Deadline == "" {
		return DefaultDeadline, nil
	}
	d, err := time.ParseDuration(h.Deadline)
	if err != nil || d <= 0 || d > MaxDeadline {
		return 0, fmt.Errorf("hook deadline %q must be at most %s", h.Deadline, MaxDeadline)
	}
	return d, nil
}

// Permissions are what an add-on may see and do. They are shown when it is
// installed, approved by the person installing it and enforced at runtime.
type Permissions struct {
	// Hooks are the hook levels the add-on may use.
	Hooks []Kind      `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Read  []DataScope `yaml:"read,omitempty" json:"read,omitempty"`
	Write []DataScope `yaml:"write,omitempty" json:"write,omitempty"`
	// Secrets grants secrets:read, shown with a red warning.
	Secrets bool `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	// Network lists the hosts the add-on may reach; none by default.
	Network   []string  `yaml:"network,omitempty" json:"network,omitempty"`
	Resources Resources `yaml:"resources,omitempty" json:"resources,omitzero"`
	// Override grants core.override, which only a server admin can approve.
	Override bool `yaml:"override,omitempty" json:"override,omitempty"`
}

// DataScope grants access to a kind, optionally to some fields only.
type DataScope struct {
	Kind   string   `yaml:"kind" json:"kind"`
	Fields []string `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// Resources caps the add-on's process.
type Resources struct {
	CPU      string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	MemoryMB int    `yaml:"memoryMB,omitempty" json:"memoryMB,omitempty"`
}

// UIExtension is a declarative addition to the UI.
type UIExtension struct {
	Type  string `yaml:"type" json:"type"`
	Label string `yaml:"label" json:"label"`
	Icon  string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Kind  string `yaml:"kind,omitempty" json:"kind,omitempty"`
	Path  string `yaml:"path,omitempty" json:"path,omitempty"`
}

// AddonRoute is an API route the add-on serves under /api/v1/addons/<id>/.
type AddonRoute struct {
	Method  string `yaml:"method" json:"method"`
	Path    string `yaml:"path" json:"path"`
	Summary string `yaml:"summary" json:"summary"`
}

// AddonKind is a kind an add-on adds, described by JSON Schema.
type AddonKind struct {
	Name        string         `yaml:"name" json:"name"`
	Prefix      string         `yaml:"prefix" json:"prefix"`
	Scope       string         `yaml:"scope" json:"scope"`
	Title       string         `yaml:"title" json:"title"`
	Plural      string         `yaml:"plural" json:"plural"`
	Description string         `yaml:"description" json:"description"`
	Schema      map[string]any `yaml:"schema" json:"schema"`
}

// ParseAddonManifest reads and checks an addon.yaml: against the schema,
// then for rules a schema can't express, such as hooks needing the matching
// permission level.
func ParseAddonManifest(data []byte) (*AddonManifest, error) {
	if err := spec.ValidateYAML(spec.Addon, data); err != nil {
		return nil, err
	}
	var m AddonManifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("reading the add-on manifest: %w", err)
	}
	if !slices.Contains(m.APIVersions, APIVersion) {
		return nil, fmt.Errorf("add-on %s supports hook API %v, this server speaks %s", m.ID, m.APIVersions, APIVersion)
	}
	for _, h := range m.Hooks {
		if !slices.Contains(m.Permissions.Hooks, h.Kind) {
			return nil, fmt.Errorf("add-on %s registers a %s hook on %s without the %s permission", m.ID, h.Kind, h.Point, h.Kind)
		}
		if _, err := h.DeadlineDuration(); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// AccountInstalled reports whether the manifest can be installed by an
// account owner: such add-ons only affect their account and can never hold
// system hooks or core.override.
func (m *AddonManifest) AccountInstalled() error {
	if slices.Contains(m.Permissions.Hooks, System) {
		return fmt.Errorf("add-on %s needs system hooks, which only a server admin can grant", m.ID)
	}
	if m.Permissions.Override {
		return fmt.Errorf("add-on %s asks for core.override, which only a server admin can grant", m.ID)
	}
	return nil
}

// PolicyType says what a rule does.
type PolicyType string

// Rule types.
const (
	PolicyValidate PolicyType = "validate"
	PolicyDefault  PolicyType = "default"
)

// PolicySpec is the spec of a core.policy resource: a CEL rule evaluated
// inside the panel with a cost limit.
type PolicySpec struct {
	Name        string     `json:"name" yaml:"name"`
	Description string     `json:"description,omitempty" yaml:"description,omitempty"`
	Type        PolicyType `json:"type" yaml:"type" enum:"validate,default"`
	// Kinds and Operations narrow the rule; empty means all.
	Kinds      []string `json:"kinds,omitempty" yaml:"kinds,omitempty"`
	Operations []string `json:"operations,omitempty" yaml:"operations,omitempty"`
	// Expression is true when a validate rule must refuse the change.
	Expression string         `json:"expression,omitempty" yaml:"expression,omitempty"`
	Message    string         `json:"message,omitempty" yaml:"message,omitempty"`
	Defaults   []FieldDefault `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	Enabled    bool           `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Priority   int            `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// FieldDefault fills a field on create from a CEL expression.
type FieldDefault struct {
	Field      string `json:"field" yaml:"field"`
	Expression string `json:"expression" yaml:"expression"`
	Override   bool   `json:"override,omitempty" yaml:"override,omitempty"`
}

// Validate checks a rule's structure against the policy schema. Compiling
// the expressions is the rule engine's job.
func (p PolicySpec) Validate() error {
	return spec.Validate(spec.Policy, p)
}
