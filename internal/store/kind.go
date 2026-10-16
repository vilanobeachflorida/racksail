package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Risk ranks how much harm an operation can do. It decides how changes are
// confirmed in the UI and whether the assistant may apply them on its own.
type Risk string

// Risk levels, from harmless to dangerous.
const (
	// RiskRead operations change nothing.
	RiskRead Risk = "read"
	// RiskLow changes are reversible and limited to the resource itself.
	RiskLow Risk = "low"
	// RiskMedium changes are reversible but visible, such as a restart.
	RiskMedium Risk = "medium"
	// RiskHigh changes delete data or affect many resources.
	RiskHigh Risk = "high"
	// RiskCritical changes can lock people out or take the server down.
	RiskCritical Risk = "critical"
)

var riskRank = map[Risk]int{RiskRead: 0, RiskLow: 1, RiskMedium: 2, RiskHigh: 3, RiskCritical: 4}

// Valid reports whether r is a known risk level.
func (r Risk) Valid() bool { _, ok := riskRank[r]; return ok }

// Rank orders risk levels from 0 (read) to 4 (critical).
func (r Risk) Rank() int { return riskRank[r] }

// MaxRisk returns the higher of two risk levels.
func MaxRisk(a, b Risk) Risk {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// Action is an operation on a resource beyond create, update and delete,
// such as deploying a site or scrubbing a pool. Actions become API operations
// (POST .../{id}/actions/<name>), CLI commands, assistant tools, hook points
// and events.
type Action struct {
	// Name is lowercase words joined by underscores, for example "deploy".
	Name string
	// Title is the label shown on buttons and in menus.
	Title string
	// Summary is one line for API docs, CLI help and assistant tools.
	Summary string
	// Description explains what the action does and when to use it.
	Description string
	Risk        Risk
	// Past is the past tense used in event names ("deployed" gives
	// web.site.deployed). When empty it is derived from Name.
	Past string
	// Input is a zero value of the action's input type, or nil when the action
	// takes no input. Its JSON Schema documents and validates requests.
	Input any
	// Output is a zero value of the action's result type, or nil.
	Output any
	// Long actions run as a job; the API answers 202 with the job id.
	Long bool
	// Destructive actions need a typed confirmation in the UI.
	Destructive bool
	// Reversible actions can be undone with a new change set, which lets the
	// assistant apply them when auto-approve is on.
	Reversible bool
}

// EventName returns the event emitted after the action succeeds on kind.
func (a Action) EventName(kind string) string {
	past := a.Past
	if past == "" {
		past = pastTense(a.Name)
	}
	return kind + "." + past
}

// Index declares a spec field that the store indexes, so lists can filter on
// it, sort by it and search it, and, when Unique is set, two resources can't
// share a value.
type Index struct {
	// Name identifies the index in queries, for example "domain". It is a
	// camelCase word, and lists take it as a query parameter of its own.
	Name string
	// Path is the field path in the spec, with dots for nested objects. When
	// the field is an array every element is indexed.
	Path string
	// Unique rejects a second resource with the same value.
	Unique bool
	// PerScope makes uniqueness apply within each owner instead of across the
	// whole installation.
	PerScope bool
	// Fold compares values case-insensitively, as for domain names.
	Fold bool
	// Conflict is the error returned when a unique value is taken. Its message
	// can use {value}. When nil, core.already_exists is used.
	Conflict *i18n.Def
}

// UIHints tell the generic screens how to present a kind until the module
// ships a custom screen.
type UIHints struct {
	// Icon is a lucide icon name, for example "globe".
	Icon string `json:"icon,omitempty"`
	// TitleField is the spec path shown as the resource's title.
	TitleField string `json:"titleField,omitempty"`
	// SubtitleField is the spec path shown under the title.
	SubtitleField string               `json:"subtitleField,omitempty"`
	Columns       []Column             `json:"columns,omitempty"`
	Groups        []FieldGroup         `json:"groups,omitempty"`
	Fields        map[string]FieldHint `json:"fields,omitempty"`
	// Hidden kinds get no generic screen or navigation entry.
	Hidden bool `json:"hidden,omitempty"`
}

// Column is a column in the generic list screen.
type Column struct {
	Field string `json:"field"`
	// Format is one of text, date, bytes, duration, status, badge or link.
	Format string `json:"format,omitempty"`
}

// FieldGroup groups fields on the generic form.
type FieldGroup struct {
	Name     string   `json:"name"`
	Fields   []string `json:"fields"`
	Advanced bool     `json:"advanced,omitempty"`
}

// FieldHint adjusts how one field is shown on the generic form.
type FieldHint struct {
	// Widget overrides the default input, for example textarea, code, password,
	// select, toggle, size, duration or resource.
	Widget      string `json:"widget,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Advanced    bool   `json:"advanced,omitempty"`
	// Sensitive fields hold secrets. Their value is a reference to one of
	// the resource's own secrets (a secrets.Ref), never the secret itself:
	// values arrive in a change's Secrets, and change sets show the field
	// redacted.
	Sensitive bool `json:"sensitive,omitempty"`
	// CreateOnly fields can be set on create and are read-only afterwards.
	CreateOnly bool `json:"createOnly,omitempty"`
	Order      int  `json:"order,omitempty"`
	// Kind is the kind of the resources a field with the resource widget
	// names by ID, such as databases.database, so forms can offer them.
	Kind string `json:"kind,omitempty"`
}

// Backup says whether resources of the kind are covered by backups, restores
// and the portable account archive. Every kind is covered unless it opts out
// with a reason; what data belongs to a resource is contributed by its module.
type Backup struct {
	Exclude bool
	Reason  string
}

// Risks gives the risk of creating, updating and deleting resources of a
// kind. Empty fields default to low, low and high.
type Risks struct {
	Create Risk
	Update Risk
	Delete Risk
}

// KindInfo describes a kind. Modules fill it in once, when they define the
// kind; everything generated for the kind is derived from it.
type KindInfo struct {
	// Name is <module>.<name> in lowercase, for example "web.site".
	Name string
	// Prefix is the TypeID prefix, short and unique, for example "site".
	Prefix string
	Scope  ScopeLevel
	// Title is the singular display name, "Site".
	Title string
	// Plural is lowercase and used in API paths: /api/v1/web/sites.
	Plural string
	// Description is written for people: it documents the API, labels CLI
	// help and describes assistant tools.
	Description string
	Actions     []Action
	Indexes     []Index
	Risks       Risks
	UI          UIHints
	Backup      Backup
	// Singleton kinds have at most one resource per scope, like settings.
	Singleton bool
}

// Module returns the module part of the kind name.
func (k *KindInfo) Module() string {
	module, _, _ := strings.Cut(k.Name, ".")
	return module
}

// Action returns the declared action with the given name.
func (k *KindInfo) Action(name string) (Action, bool) {
	for _, a := range k.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

// Kind is the untyped view of a defined kind, used by generic code such as
// the API server, the change pipeline and the hook registry.
type Kind interface {
	Info() *KindInfo
	// SpecType and StatusType are the Go types of the spec and status.
	SpecType() reflect.Type
	StatusType() reflect.Type
	// Default fills defaults into the spec of a new resource.
	Default(ctx context.Context, r *Resource) error
	// Validate checks a resource. old is nil on create.
	Validate(ctx context.Context, r, old *Resource) error
	// Err reports a problem with the kind's definition.
	Err() error
}

// KindOf is a kind with spec type S and status type T. T must embed Status.
type KindOf[S, T any] struct {
	info      KindInfo
	err       error
	spec      *huma.Schema
	defaults  func(ctx context.Context, obj *Object[S, T]) error
	validator func(ctx context.Context, obj, old *Object[S, T]) error
}

// Define describes a kind. It is meant for package-level variables in a
// module:
//
//	var Site = store.Define[SiteSpec, SiteStatus](store.KindInfo{
//		Name: "web.site", Prefix: "site", Scope: store.ScopeAccount,
//		Title: "Site", Plural: "sites",
//		Description: "A website served by the edge.",
//	})
//
// Problems with the definition are reported by Err and refuse registration,
// so a broken kind stops the binary at startup and fails the tests.
func Define[S, T any](info KindInfo) *KindOf[S, T] {
	k := &KindOf[S, T]{info: info}
	if k.info.Risks.Create == "" {
		k.info.Risks.Create = RiskLow
	}
	if k.info.Risks.Update == "" {
		k.info.Risks.Update = RiskLow
	}
	if k.info.Risks.Delete == "" {
		k.info.Risks.Delete = RiskHigh
	}
	errs := []error{checkKind(&k.info, reflect.TypeFor[S](), reflect.TypeFor[T]())}
	if spec, err := generateSchema(reflect.TypeFor[S](), k.info.Title+"Spec"); err != nil {
		errs = append(errs, fmt.Errorf("kind %q: %w", info.Name, err))
	} else {
		k.spec = spec
	}
	for _, a := range k.info.Actions {
		if a.Input == nil {
			continue
		}
		if t := reflect.TypeOf(a.Input); t.Kind() != reflect.Struct {
			errs = append(errs, fmt.Errorf("kind %q: action %q: the input must be a struct, not %s", info.Name, a.Name, t))
		} else if _, err := generateSchema(t, a.Name+"Input"); err != nil {
			errs = append(errs, fmt.Errorf("kind %q: action %q: %w", info.Name, a.Name, err))
		}
	}
	k.err = errors.Join(errs...)
	return k
}

// WithDefaults sets the function that fills defaults on create.
func (k *KindOf[S, T]) WithDefaults(fn func(ctx context.Context, obj *Object[S, T]) error) *KindOf[S, T] {
	k.defaults = fn
	return k
}

// WithValidator sets the kind's own validation. old is nil on create.
func (k *KindOf[S, T]) WithValidator(fn func(ctx context.Context, obj, old *Object[S, T]) error) *KindOf[S, T] {
	k.validator = fn
	return k
}

// Info returns the kind's description.
func (k *KindOf[S, T]) Info() *KindInfo { return &k.info }

// Name returns the kind name.
func (k *KindOf[S, T]) Name() string { return k.info.Name }

// SpecType returns the Go type of the spec.
func (k *KindOf[S, T]) SpecType() reflect.Type { return reflect.TypeFor[S]() }

// StatusType returns the Go type of the status.
func (k *KindOf[S, T]) StatusType() reflect.Type { return reflect.TypeFor[T]() }

// Err reports a problem with the definition.
func (k *KindOf[S, T]) Err() error { return k.err }

// New returns a typed object of this kind for scope with the given spec. The
// store assigns the ID when it is created.
func (k *KindOf[S, T]) New(scope Scope, spec S) *Object[S, T] {
	return &Object[S, T]{Meta: Meta{Kind: k.info.Name, Scope: scope}, Spec: spec}
}

// Decode converts a stored resource of this kind to its typed form.
func (k *KindOf[S, T]) Decode(r *Resource) (*Object[S, T], error) {
	if r.Kind != k.info.Name {
		return nil, fmt.Errorf("resource %s is a %s, not a %s", r.ID, r.Kind, k.info.Name)
	}
	obj := &Object[S, T]{Meta: r.Meta}
	if len(r.Spec) > 0 {
		if err := json.Unmarshal(r.Spec, &obj.Spec); err != nil {
			return nil, fmt.Errorf("decoding spec of %s: %w", r.ID, err)
		}
	}
	if len(r.Status) > 0 {
		if err := json.Unmarshal(r.Status, &obj.Status); err != nil {
			return nil, fmt.Errorf("decoding status of %s: %w", r.ID, err)
		}
	}
	return obj, nil
}

// Encode converts a typed object to a resource for storing.
func (k *KindOf[S, T]) Encode(obj *Object[S, T]) (*Resource, error) {
	spec, err := json.Marshal(obj.Spec)
	if err != nil {
		return nil, fmt.Errorf("encoding spec: %w", err)
	}
	status, err := json.Marshal(obj.Status)
	if err != nil {
		return nil, fmt.Errorf("encoding status: %w", err)
	}
	r := &Resource{Meta: obj.Meta, Spec: spec, Status: status}
	r.Kind = k.info.Name
	return r, nil
}

// Get reads one resource of this kind. A resource of another kind is
// reported as not found, as when an ID for one kind is passed where another
// is expected.
func (k *KindOf[S, T]) Get(ctx context.Context, rd Reader, id ID) (*Object[S, T], error) {
	r, err := rd.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Kind != k.info.Name {
		return nil, ErrNotFound.New("what", string(id))
	}
	return k.Decode(r)
}

// List reads a page of resources of this kind. The query's Kind is set for
// the caller.
func (k *KindOf[S, T]) List(ctx context.Context, rd Reader, q Query) ([]*Object[S, T], string, error) {
	q.Kind = k.info.Name
	page, err := rd.List(ctx, q)
	if err != nil {
		return nil, "", err
	}
	out := make([]*Object[S, T], 0, len(page.Items))
	for _, r := range page.Items {
		obj, err := k.Decode(r)
		if err != nil {
			return nil, "", err
		}
		out = append(out, obj)
	}
	return out, page.Next, nil
}

// Default implements Kind.
func (k *KindOf[S, T]) Default(ctx context.Context, r *Resource) error {
	if k.defaults == nil {
		return nil
	}
	obj, err := k.Decode(r)
	if err != nil {
		return err
	}
	if err := k.defaults(ctx, obj); err != nil {
		return err
	}
	spec, err := json.Marshal(obj.Spec)
	if err != nil {
		return err
	}
	r.Spec = spec
	return nil
}

// Validate implements Kind. It checks the scope and labels, then the spec
// against the JSON Schema of S and that it decodes into S, and finally runs
// the kind's own validator.
func (k *KindOf[S, T]) Validate(ctx context.Context, r, old *Resource) error {
	var fields []i18n.FieldError
	if r.Scope.Level != k.info.Scope {
		fields = append(fields, i18n.Field("scope.level", i18n.ErrBadValue, "reason", fmt.Sprintf("%s resources are owned at %s level", k.info.Title, k.info.Scope)))
	} else if err := r.Scope.Validate(); err != nil {
		fields = append(fields, i18n.Field("scope", i18n.ErrBadValue, "reason", err.Error()))
	}
	if err := ValidateLabels(r.Labels); err != nil {
		fields = append(fields, i18n.Field("labels", i18n.ErrBadValue, "reason", err.Error()))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	if fields := validateDocument(k.spec, r.Spec, "spec"); len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	obj, err := k.Decode(r)
	if err != nil {
		return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", "the spec doesn't match the kind's schema"))
	}
	if k.validator == nil {
		return nil
	}
	var prev *Object[S, T]
	if old != nil {
		if prev, err = k.Decode(old); err != nil {
			return err
		}
	}
	return k.validator(ctx, obj, prev)
}

// SensitiveFields lists the spec paths of a kind's sensitive fields, sorted.
func SensitiveFields(info *KindInfo) []string {
	var out []string
	for path, hint := range info.UI.Fields {
		if hint.Sensitive {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// Sensitive field paths become part of secret names, which are limited to
// 128 characters, so they are kept short and simple.
const maxSensitivePath = 48

var (
	sensitivePathPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*)*$`)
	kindNamePattern      = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`)
	pluralPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	actionNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	indexNamePattern     = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
	// Actions named like the operations every kind has would be confused
	// with them, in the API and in hook points.
	reservedActions = map[string]bool{
		"create": true, "update": true, "delete": true, "get": true, "list": true, "read": true,
		"history": true, "fail": true, "drift": true,
	}
	// Verbs of the events every kind emits, which no action may produce.
	reservedEventVerbs = map[string]bool{"created": true, "updated": true, "deleted": true, "failed": true, "drifted": true}
	// Lists take every index as a query parameter, next to these.
	reservedIndexNames = map[string]bool{
		"cursor": true, "limit": true, "sort": true, "scope": true, "label": true, "phase": true, "q": true,
		"fields": true, "includeDeleted": true, "server": true, "id": true, "ids": true,
	}
	statusType = reflect.TypeFor[Status]()
)

func checkKind(info *KindInfo, spec, status reflect.Type) error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("kind %q: "+format, append([]any{info.Name}, args...)...))
	}
	if !kindNamePattern.MatchString(info.Name) {
		bad("name must be <module>.<name> in lowercase")
	}
	if !ValidPrefix(info.Prefix) || len(info.Prefix) > 16 {
		bad("prefix %q must be up to 16 lowercase letters", info.Prefix)
	}
	if !info.Scope.Valid() {
		bad("unknown scope level %q", info.Scope)
	}
	if strings.TrimSpace(info.Title) == "" || strings.TrimSpace(info.Description) == "" {
		bad("title and description are required")
	}
	if !pluralPattern.MatchString(info.Plural) {
		bad("plural %q must be lowercase words joined by hyphens", info.Plural)
	}
	if spec.Kind() != reflect.Struct {
		bad("spec type %s must be a struct", spec)
	}
	if status != statusType && !embedsStatus(status) {
		bad("status type %s must embed store.Status", status)
	}
	for _, r := range []Risk{info.Risks.Create, info.Risks.Update, info.Risks.Delete} {
		if !r.Valid() {
			bad("unknown risk %q", r)
		}
	}
	seen := map[string]bool{}
	emitted := map[string]string{}
	for _, a := range info.Actions {
		verb := strings.TrimPrefix(a.EventName(info.Name), info.Name+".")
		switch {
		case !actionNamePattern.MatchString(a.Name):
			bad("action %q must be lowercase words joined by underscores", a.Name)
		case reservedActions[a.Name]:
			bad("action name %q is reserved", a.Name)
		case seen[a.Name]:
			bad("action %q is declared twice", a.Name)
		case !a.Risk.Valid():
			bad("action %q needs a risk level", a.Name)
		case strings.TrimSpace(a.Summary) == "":
			bad("action %q needs a summary", a.Name)
		case a.Past != "" && !actionNamePattern.MatchString(a.Past):
			bad("action %q: past tense %q must be lowercase words joined by underscores", a.Name, a.Past)
		case reservedEventVerbs[verb]:
			bad("action %q: its event %s.%s is one every kind emits; set Past to another word", a.Name, info.Name, verb)
		case emitted[verb] != "":
			bad("actions %q and %q both emit %s.%s; set Past on one of them", emitted[verb], a.Name, info.Name, verb)
		}
		seen[a.Name] = true
		if emitted[verb] == "" {
			emitted[verb] = a.Name
		}
	}
	indexes := map[string]bool{}
	for _, ix := range info.Indexes {
		switch {
		case ix.Name == "" || ix.Path == "":
			bad("indexes need a name and a path")
		case !indexNamePattern.MatchString(ix.Name):
			bad("index name %q must be a camelCase word", ix.Name)
		case reservedIndexNames[ix.Name]:
			bad("index name %q is taken by a list parameter", ix.Name)
		case indexes[ix.Name]:
			bad("index %q is declared twice", ix.Name)
		}
		indexes[ix.Name] = true
	}
	if info.Backup.Exclude && strings.TrimSpace(info.Backup.Reason) == "" {
		bad("opting out of backups needs a reason")
	}
	for _, path := range SensitiveFields(info) {
		if len(path) > maxSensitivePath || !sensitivePathPattern.MatchString(path) {
			bad("sensitive field %q must be a path of camelCase field names joined by dots, at most %d characters", path, maxSensitivePath)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(info.UI.Fields)) {
		hint := info.UI.Fields[path]
		switch {
		case hint.Widget == "resource" && !kindNamePattern.MatchString(hint.Kind):
			bad("field %q shows resources, so its hint names their kind", path)
		case hint.Kind != "" && hint.Widget != "resource":
			bad("field %q names a kind but doesn't show resources", path)
		}
	}
	return errors.Join(errs...)
}

func embedsStatus(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type == statusType && f.Tag.Get("json") == "" {
			return true
		}
	}
	return false
}

// irregularPast holds action verbs whose past tense the spelling rules in
// pastOf don't produce. Prefixed forms (rerun, unset, rebuild) are derived
// from these.
var irregularPast = map[string]string{
	"run": "ran", "send": "sent", "build": "built", "set": "set", "put": "put", "begin": "began",
	"do": "done", "wake": "woken", "sleep": "slept", "shut": "shut", "cut": "cut", "freeze": "frozen",
	"spin": "spun", "split": "split", "bind": "bound", "write": "written", "read": "read",
	"make": "made", "take": "taken", "hide": "hidden", "keep": "kept", "hold": "held",
	"feed": "fed", "find": "found", "lead": "led", "quit": "quit", "let": "let", "shed": "shed",
	// Words that look like a prefix and a short verb but aren't.
	"render": "rendered", "reckon": "reckoned",
}

// doublingPast are verbs stressed on their last syllable, which double
// their final consonant although the rules only double one-syllable verbs.
var doublingPast = map[string]bool{
	"submit": true, "commit": true, "permit": true, "omit": true, "emit": true, "admit": true,
	"transmit": true, "transfer": true, "prefer": true, "defer": true, "confer": true, "infer": true,
	"occur": true, "incur": true, "control": true, "patrol": true, "compel": true, "expel": true,
	"propel": true, "equip": true, "forget": true,
}

// verbParticles complete phrasal verbs and stay after the verb: spin_down
// gives spun_down, not down_spun.
var verbParticles = map[string]bool{
	"up": true, "down": true, "on": true, "off": true, "in": true, "out": true,
	"over": true, "back": true, "away": true,
}

// pastTense derives an event verb from an action name, the verb first:
// "deploy" gives "deployed", "reset_password" gives "password_reset" (the
// object moves in front) and "spin_down" gives "spun_down" (particles stay
// behind). Kinds set Action.Past for anything these rules get wrong.
func pastTense(name string) string {
	words := strings.Split(name, "_")
	rest := words[1:]
	var particles []string
	for len(rest) > 0 && verbParticles[rest[0]] {
		particles = append(particles, rest[0])
		rest = rest[1:]
	}
	out := append(append(slices.Clone(rest), pastOf(words[0])), particles...)
	return strings.Join(out, "_")
}

// pastOf puts one verb in the past tense.
func pastOf(verb string) string {
	if past, ok := irregularPast[verb]; ok {
		return past
	}
	for _, prefix := range []string{"re", "un"} {
		if base, ok := strings.CutPrefix(verb, prefix); ok {
			if past, ok := irregularPast[base]; ok {
				return prefix + past
			}
			if doublingPast[base] || doublesFinalConsonant(base) {
				return verb + verb[len(verb)-1:] + "ed"
			}
		}
	}
	switch {
	case strings.HasSuffix(verb, "e"):
		return verb + "d"
	case strings.HasSuffix(verb, "y") && len(verb) > 1 && !isVowel(verb[len(verb)-2]):
		return verb[:len(verb)-1] + "ied"
	case doublingPast[verb] || doublesFinalConsonant(verb):
		return verb + verb[len(verb)-1:] + "ed"
	}
	return verb + "ed"
}

func isVowel(b byte) bool { return strings.IndexByte("aeiou", b) >= 0 }

// doublesFinalConsonant covers short one-vowel verbs like stop, scrub and
// plan, which double their last letter.
func doublesFinalConsonant(v string) bool {
	if len(v) < 3 || len(v) > 5 {
		return false
	}
	vowels := 0
	for i := 0; i < len(v); i++ {
		if isVowel(v[i]) {
			vowels++
		}
	}
	last, mid, before := v[len(v)-1], v[len(v)-2], v[len(v)-3]
	return vowels == 1 && isVowel(mid) && !isVowel(last) && !isVowel(before) && strings.IndexByte("wxy", last) < 0
}
