package hooks

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
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
	Hooks []Kind      `yaml:"hooks,omitempty" json:"hooks,omitempty" doc:"The hook levels the add-on may use: observe, validate, modify and system"`
	Read  []DataScope `yaml:"read,omitempty" json:"read,omitempty" doc:"The kinds, and the fields of them, the add-on may see"`
	Write []DataScope `yaml:"write,omitempty" json:"write,omitempty" doc:"The kinds, and the fields of them, the add-on may change, which it may also see"`
	// Secrets grants secrets:read, shown with a red warning.
	Secrets bool `yaml:"secrets,omitempty" json:"secrets,omitempty" doc:"secrets.read: the add-on may read the secrets of the resources it may see"`
	// Network lists the hosts the add-on may reach; none by default.
	Network   []string  `yaml:"network,omitempty" json:"network,omitempty" doc:"The hosts, addresses or networks the add-on may reach; none when empty"`
	Resources Resources `yaml:"resources,omitempty" json:"resources,omitzero" doc:"The most CPU and memory the add-on's process may use"`
	// Override grants core.override, which only a server admin can approve.
	Override bool `yaml:"override,omitempty" json:"override,omitempty" doc:"core.override: the add-on's system hooks may loosen what hooks otherwise can't, which only a server admin can approve"`
}

// DataScope grants access to a kind, optionally to some of it only. A
// scope that names nothing but the kind covers all of it: every field and
// label, and for changing, deleting resources and every action too. A scope
// that names fields, labels, deleting or actions covers those and nothing
// more, so an add-on allowed one field can't delete a resource, run an
// action or relabel it unless its scope says so.
type DataScope struct {
	Kind   string   `yaml:"kind" json:"kind" doc:"The kind, such as web.site"`
	Fields []string `yaml:"fields,omitempty" json:"fields,omitempty" doc:"The fields of the spec, as dotted paths such as cdn or tls.mode"`
	// Labels, Delete and Actions are what a scope with limits allows
	// beyond its fields. Reading only knows labels.
	Labels  bool     `yaml:"labels,omitempty" json:"labels,omitempty" doc:"The resources' labels, which the add-on may see, or change when the scope is for changing"`
	Delete  bool     `yaml:"delete,omitempty" json:"delete,omitempty" doc:"For changing: the add-on may delete resources of the kind"`
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty" doc:"For changing: the actions of the kind the add-on may run, such as purge"`
}

// Whole reports whether a scope covers all of its kind: it names no
// fields, labels, deleting or actions to limit it to.
func (s DataScope) Whole() bool {
	return len(s.Fields) == 0 && !s.Labels && !s.Delete && len(s.Actions) == 0
}

// Resources caps the add-on's process.
type Resources struct {
	CPU      string `yaml:"cpu,omitempty" json:"cpu,omitempty" doc:"The most CPU time, in percent of one CPU, such as 20%"`
	MemoryMB int    `yaml:"memoryMB,omitempty" json:"memoryMB,omitempty" doc:"The most memory, in MB"`
}

// UIExtension is a declarative addition to the UI.
type UIExtension struct {
	Type  string `yaml:"type" json:"type" doc:"Where the entry goes: nav, tab, card, command, settings or column"`
	Label string `yaml:"label" json:"label" doc:"What the panel calls the entry"`
	Icon  string `yaml:"icon,omitempty" json:"icon,omitempty" doc:"The entry's icon, by its name in Lucide, such as shield"`
	Kind  string `yaml:"kind,omitempty" json:"kind,omitempty" doc:"The kind whose screens a tab or column goes on"`
	Path  string `yaml:"path,omitempty" json:"path,omitempty" doc:"For a column, the add-on's GET route that answers its values; otherwise the page under the add-on's /ui/ the entry shows"`
}

// AddonRoute is an API route the add-on serves under /api/v1/addons/<id>/.
type AddonRoute struct {
	Method  string `yaml:"method" json:"method"`
	Path    string `yaml:"path" json:"path"`
	Summary string `yaml:"summary" json:"summary"`
}

// Matches reports whether a request of method for path, under the
// add-on's own, is the route's, where {name} in the route stands for one
// segment.
func (rt AddonRoute) Matches(method, path string) bool {
	if !strings.EqualFold(rt.Method, method) {
		return false
	}
	ps, xs := strings.Split(strings.Trim(rt.Path, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(xs) {
		return false
	}
	for i := range ps {
		if strings.HasPrefix(ps[i], "{") && strings.HasSuffix(ps[i], "}") && xs[i] != "" {
			continue
		}
		if ps[i] != xs[i] {
			return false
		}
	}
	return true
}

// screenAreas are the first segments of the paths under an add-on's own
// where the panel serves its screens and the frames it shows them in, so
// no route of its own may start with them.
var screenAreas = []string{"ui", "frames"}

// AddonKind is a kind an add-on adds, described by JSON Schema. Its
// resources go through the change pipeline like any other, with hook points
// and API operations of their own, and the add-on plans and applies them
// over the add-on protocol, as a reconciler does.
type AddonKind struct {
	Name        string `yaml:"name" json:"name" doc:"<namespace>.<name>, such as guard.rule; every kind of an add-on shares its namespace"`
	Prefix      string `yaml:"prefix" json:"prefix" doc:"What the IDs of its resources start with, before the underscore"`
	Scope       string `yaml:"scope" json:"scope" doc:"Who its resources belong to: the server, a reseller or an account"`
	Title       string `yaml:"title" json:"title" doc:"Its name for people, such as Guard rule"`
	Plural      string `yaml:"plural" json:"plural" doc:"Its plural, in lowercase words joined by hyphens, such as rules"`
	Description string `yaml:"description" json:"description" doc:"What its resources are, for the API reference, CLI help and forms"`
	// Schema and Status are JSON Schema documents.
	Schema  map[string]any `yaml:"schema" json:"schema" doc:"The JSON Schema of its resources' settings"`
	Status  map[string]any `yaml:"status,omitempty" json:"status,omitempty" doc:"The JSON Schema of the status fields the add-on reports for its resources"`
	Actions []AddonAction  `yaml:"actions,omitempty" json:"actions,omitempty" doc:"Its actions beyond creating, changing and deleting"`
}

// AddonAction is an action of a kind an add-on adds.
type AddonAction struct {
	Name        string         `yaml:"name" json:"name" doc:"Lowercase words joined by underscores, such as purge"`
	Title       string         `yaml:"title,omitempty" json:"title,omitempty" doc:"The label of its button"`
	Summary     string         `yaml:"summary" json:"summary" doc:"What it does, in one line"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty" doc:"What it does and when to use it"`
	Risk        string         `yaml:"risk" json:"risk" doc:"How much harm it can do: low, medium, high or critical"`
	Destructive bool           `yaml:"destructive,omitempty" json:"destructive,omitempty" doc:"Whether it removes data, so people type a confirmation first"`
	Reversible  bool           `yaml:"reversible,omitempty" json:"reversible,omitempty" doc:"Whether it can be undone"`
	Input       map[string]any `yaml:"input,omitempty" json:"input,omitempty" doc:"The JSON Schema of its input; it takes none when empty"`
}

// Namespace is the part of the kind's name before the dot.
func (k AddonKind) Namespace() string {
	ns, _, _ := strings.Cut(k.Name, ".")
	return ns
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
	for _, s := range m.Permissions.Read {
		if s.Delete || len(s.Actions) > 0 {
			return nil, fmt.Errorf("add-on %s asks to delete or run actions on %s with a permission to read it; that takes a permission to write it", m.ID, s.Kind)
		}
	}
	if err := m.checkKinds(); err != nil {
		return nil, err
	}
	if err := m.checkUI(); err != nil {
		return nil, err
	}
	return &m, nil
}

// checkUI checks the entries a manifest adds to the panel. Tabs and columns
// go on a kind's screens, so they name the kind, and the panel asks one of
// the add-on's own GET routes for a column's values. Routes stay out of
// where the panel serves the add-on's screens.
func (m *AddonManifest) checkUI() error {
	for _, r := range m.Routes {
		first, _, _ := strings.Cut(strings.Trim(r.Path, "/"), "/")
		if slices.Contains(screenAreas, first) {
			return fmt.Errorf("add-on %s: the route %s %s is under /%s/, where the panel serves the add-on's screens", m.ID, r.Method, r.Path, first)
		}
	}
	for _, e := range m.UI {
		if (e.Type == "tab" || e.Type == "column") && strings.TrimSpace(e.Kind) == "" {
			return fmt.Errorf("add-on %s: the %s %q needs the kind whose screens it goes on", m.ID, e.Type, e.Label)
		}
		if e.Type != "column" {
			continue
		}
		path := strings.Trim(e.Path, "/")
		if path == "" || !slices.ContainsFunc(m.Routes, func(r AddonRoute) bool {
			return r.Method == http.MethodGet && strings.Trim(r.Path, "/") == path
		}) {
			return fmt.Errorf("add-on %s: the column %q needs the path of one of its GET routes, which answers its values", m.ID, e.Label)
		}
	}
	return nil
}

// checkKinds checks the kinds a manifest adds against each other. Adding
// kinds with reconcilers of their own is what system hooks do, so it takes
// that level; the kinds share one namespace, which the core's own kinds
// don't use.
func (m *AddonManifest) checkKinds() error {
	if len(m.Kinds) == 0 {
		return nil
	}
	if !slices.Contains(m.Permissions.Hooks, System) {
		return fmt.Errorf("add-on %s adds kinds, which takes the system hook level", m.ID)
	}
	names, prefixes, plurals := map[string]bool{}, map[string]bool{}, map[string]bool{}
	ns := m.Kinds[0].Namespace()
	for _, k := range m.Kinds {
		switch {
		case k.Namespace() != ns:
			return fmt.Errorf("add-on %s adds %s and %s; its kinds share the part of their names before the dot", m.ID, m.Kinds[0].Name, k.Name)
		case ns == "core":
			return fmt.Errorf("add-on %s adds %s, but core kinds are the panel's own", m.ID, k.Name)
		case names[k.Name] || prefixes[k.Prefix] || plurals[k.Plural]:
			return fmt.Errorf("add-on %s adds %s with a name, prefix or plural another of its kinds has", m.ID, k.Name)
		case strings.TrimSpace(k.Title) == "" || strings.TrimSpace(k.Description) == "":
			return fmt.Errorf("add-on %s adds %s without a title or a description", m.ID, k.Name)
		}
		names[k.Name], prefixes[k.Prefix], plurals[k.Plural] = true, true, true
		for _, a := range k.Actions {
			switch a.Risk {
			case "low", "medium", "high", "critical":
			default:
				return fmt.Errorf("add-on %s: the %s action of %s needs a risk of low, medium, high or critical", m.ID, a.Name, k.Name)
			}
		}
	}
	return nil
}

// AccountInstalled reports whether the manifest can be installed by an
// account owner. Such add-ons only affect their account: they take part in
// changes through the points of the change pipeline, where the runtime only
// shows them what belongs to the account, and never at lifecycle, identity
// or provider points, which belong to the whole server. They can never hold
// system hooks or core.override either.
func (m *AddonManifest) AccountInstalled() error {
	if slices.Contains(m.Permissions.Hooks, System) {
		return fmt.Errorf("add-on %s needs system hooks, which only a server admin can grant", m.ID)
	}
	if m.Permissions.Override {
		return fmt.Errorf("add-on %s asks for core.override, which only a server admin can grant", m.ID)
	}
	if len(m.Provides) > 0 {
		return fmt.Errorf("add-on %s provides %s, and what add-ons provide serves the whole server, so only a server admin can install it", m.ID, m.Provides[0])
	}
	if len(m.Kinds) > 0 {
		return fmt.Errorf("add-on %s adds %s, and the kinds add-ons add serve the whole server, so only a server admin can install it", m.ID, m.Kinds[0].Name)
	}
	for _, h := range m.Hooks {
		if !AccountPoint(Point(h.Point)) {
			return fmt.Errorf("add-on %s hooks %s, which belongs to the whole server; an account's add-ons only take part in changes, at points such as web.site.create.validate", m.ID, h.Point)
		}
	}
	return nil
}

// AccountPoint reports whether an add-on an account owner installed may
// attach to p: a point of the change pipeline, or a pattern that only names
// such points, so its stage is written out. Lifecycle points such as
// core.boot, identity points such as auth.login.validate and the points of
// capabilities all belong to the whole server.
func AccountPoint(p Point) bool {
	_, _, _, ok := ParseKindPoint(p)
	return ok && (!IsPattern(p) || validPattern(p))
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
	Name        string     `json:"name" yaml:"name" doc:"A short name for the rule, shown when it refuses a change"`
	Description string     `json:"description,omitempty" yaml:"description,omitempty" doc:"What the rule is for, in a sentence or two"`
	Type        PolicyType `json:"type" yaml:"type" enum:"validate,default" doc:"validate rules refuse changes; default rules fill in or override fields of new resources"`
	// Kinds and Operations narrow the rule; empty means all.
	Kinds      []string `json:"kinds,omitempty" yaml:"kinds,omitempty" doc:"The kinds the rule applies to, such as web.site; every kind when empty"`
	Operations []string `json:"operations,omitempty" yaml:"operations,omitempty" doc:"The operations it applies to, such as create, update, delete or an action; every one when empty"`
	// Expression is true when a validate rule must refuse the change.
	Expression string         `json:"expression,omitempty" yaml:"expression,omitempty" doc:"For validate rules: a CEL expression that is true when the change must be refused"`
	Message    string         `json:"message,omitempty" yaml:"message,omitempty" doc:"For validate rules: what the person making a refused change is told"`
	Defaults   []FieldDefault `json:"defaults,omitempty" yaml:"defaults,omitempty" doc:"For default rules: the fields filled in, each from a CEL expression"`
	Enabled    bool           `json:"enabled,omitempty" yaml:"enabled,omitempty" doc:"Whether the rule takes part in changes"`
	Priority   int            `json:"priority,omitempty" yaml:"priority,omitempty" doc:"Where the rule runs among hooks, lower first"`
}

// FieldDefault fills a field on create from a CEL expression.
type FieldDefault struct {
	Field      string `json:"field" yaml:"field" doc:"The spec field filled in, as a dotted path such as headers.security"`
	Expression string `json:"expression" yaml:"expression" doc:"A CEL expression whose value the field gets"`
	Override   bool   `json:"override,omitempty" yaml:"override,omitempty" doc:"Replace a value the new resource already has, rather than only fill in an empty field"`
}

// Validate checks a rule's structure against the policy schema. Compiling
// the expressions is the rule engine's job.
func (p PolicySpec) Validate() error {
	return spec.Validate(spec.Policy, p)
}
