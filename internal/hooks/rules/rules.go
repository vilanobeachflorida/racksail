// Package rules runs the panel's rules: one-line policies written in CEL
// (Common Expression Language), stored as resources and evaluated inside
// the panel with a cost limit.
//
// Validate rules refuse a change when their expression is true, with the
// rule's message. Default rules fill in, or override, fields of a resource
// when it is created. Rules belong to the server, a reseller or an account,
// as core.policy, core.reseller_policy and core.account_policy resources,
// and apply to what their owner owns: a reseller's rules to the reseller's
// customers, an account's to the account.
//
// The engine compiles every enabled rule and attaches it to the hook points
// of the kinds and operations it names, as a validate hook or, for default
// rules, a modify hook at the create plan points, owned by rule:<id>. So
// what a rule contributes is labelled in the preview like any hook's, and a
// rule that keeps failing is switched off by the circuit breaker.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Errors.
var (
	// ErrRefused is how a validate rule refuses a change; its message is the
	// rule's own.
	ErrRefused = i18n.Define("rules.refused", http.StatusForbidden, "{message}")
	// ErrBadRule reports an expression that doesn't compile, or a rule that
	// doesn't make sense.
	ErrBadRule = i18n.Define("rules.bad_rule", http.StatusUnprocessableEntity, "The rule can't be used: {reason}.")
)

// Status is what the engine last did with a rule.
type Status struct {
	store.Status
	// Points are the hook points, or patterns of them, the rule is attached
	// to while it is enabled.
	Points []string `json:"points,omitempty" doc:"The hook points the rule is attached to while it is enabled"`
	// Problems explain why some of them couldn't be attached, such as a
	// kind that doesn't exist.
	Problems []string `json:"problems,omitempty" doc:"Why the rule couldn't be attached everywhere it names"`
}

var description = "A rule written in CEL that refuses changes or fills in fields of new resources. " +
	"Validate rules refuse a change when their expression is true, with their message; default rules fill in fields, " +
	"or override them, when a resource is created. Expressions see request, resource, oldResource, account, plan, user and server."

func uiHints() store.UIHints {
	return store.UIHints{
		Icon: "scale", TitleField: "name", SubtitleField: "description",
		Columns: []store.Column{{Field: "spec.name"}, {Field: "spec.type", Format: "badge"}, {Field: "spec.kinds", Format: "badge"}, {Field: "spec.enabled", Format: "badge"}, {Field: "status.phase", Format: "status"}},
		Fields: map[string]store.FieldHint{
			"name": {Order: 0}, "type": {Order: 1, Widget: "select"}, "expression": {Order: 2, Widget: "code"},
			"message": {Order: 3, Widget: "textarea"}, "defaults": {Order: 4}, "kinds": {Order: 5}, "operations": {Order: 6},
			"enabled": {Order: 7, Widget: "toggle"}, "priority": {Advanced: true},
		},
	}
}

// Rules can refuse every change of their owner, so changing one is never
// a small thing.
var risks = store.Risks{Create: store.RiskMedium, Update: store.RiskMedium, Delete: store.RiskMedium}

// The kinds of rules, one for each owner a rule can have.
var (
	Policy = store.Define[hooks.PolicySpec, Status](store.KindInfo{
		Name: "core.policy", Prefix: "pol", Scope: store.ScopeServer, Title: "Rule", Plural: "policies",
		Description: description + " Server rules apply to every change on the server.",
		Risks:       risks, UI: uiHints(),
	}).WithValidator(validatePolicy[Status])
	ResellerPolicy = store.Define[hooks.PolicySpec, Status](store.KindInfo{
		Name: "core.reseller_policy", Prefix: "rpol", Scope: store.ScopeReseller, Title: "Reseller rule", Plural: "reseller-policies",
		Description: description + " A reseller's rules apply to the reseller's own resources and its customers' accounts.",
		Risks:       risks, UI: uiHints(),
	}).WithValidator(validatePolicy[Status])
	AccountPolicy = store.Define[hooks.PolicySpec, Status](store.KindInfo{
		Name: "core.account_policy", Prefix: "apol", Scope: store.ScopeAccount, Title: "Account rule", Plural: "account-policies",
		Description: description + " An account's rules apply to changes in that account.",
		Risks:       risks, UI: uiHints(),
	}).WithValidator(validatePolicy[Status])
)

// Kinds are the kinds of rules.
func Kinds() []*store.KindOf[hooks.PolicySpec, Status] {
	return []*store.KindOf[hooks.PolicySpec, Status]{Policy, ResellerPolicy, AccountPolicy}
}

// IsRuleKind reports whether a kind is one of the kinds of rules.
func IsRuleKind(kind string) bool {
	return kind == Policy.Name() || kind == ResellerPolicy.Name() || kind == AccountPolicy.Name()
}

func init() {
	for _, k := range Kinds() {
		store.RegisterKind(k)
	}
}

// env is compiled once; environments are safe for concurrent use.
var celEnv = sync.OnceValues(newCELEnv)

func validatePolicy[T any](ctx context.Context, obj, old *store.Object[hooks.PolicySpec, T]) error {
	if _, err := compileRule(obj.Spec, DefaultCostLimit); err != nil {
		var fe *fieldError
		if errors.As(err, &fe) {
			return fe.invalid("spec")
		}
		return i18n.Invalid(i18n.Field("spec", i18n.ErrBadValue, "reason", err.Error()))
	}
	return nil
}

// fieldError locates what is wrong with a rule. schema is what the rule's
// JSON Schema found, when that was it.
type fieldError struct {
	path, reason string
	schema       error
}

// invalid is the problem of a rule at prefix, such as spec: what its JSON
// Schema found in the catalog's words, or the field that is wrong.
func (e *fieldError) invalid(prefix string) error {
	if e.schema != nil {
		return i18n.Invalid(i18n.JSONSchemaFields(prefix, e.schema)...)
	}
	path := prefix
	if e.path != "" {
		path += "." + e.path
	}
	return i18n.Invalid(i18n.Field(path, i18n.ErrBadValue, "reason", e.reason))
}

func (e *fieldError) Error() string {
	if e.schema != nil {
		// Written to go inside rules.bad_rule's sentence.
		var found []string
		for _, f := range i18n.JSONSchemaFields("", e.schema) {
			what, ok := f.Params["reason"].(string)
			if !ok {
				what = strings.TrimSuffix(f.Message, ".")
				what = strings.ToLower(what[:1]) + what[1:]
			}
			found = append(found, f.Path+": "+what)
		}
		return strings.Join(found, "; ")
	}
	return e.path + ": " + e.reason
}

// compiled is a rule ready to run.
type compiled struct {
	spec     hooks.PolicySpec
	validate *program
	defaults []compiledDefault
}

type compiledDefault struct {
	field    string
	override bool
	prg      *program
}

// compileRule checks a rule and compiles its expressions.
func compileRule(spec hooks.PolicySpec, limit uint64) (*compiled, error) {
	if err := spec.Validate(); err != nil {
		return nil, &fieldError{schema: err}
	}
	e, err := celEnv()
	if err != nil {
		return nil, err
	}
	c := &compiled{spec: spec}
	switch spec.Type {
	case hooks.PolicyValidate:
		if len(spec.Defaults) > 0 {
			return nil, &fieldError{path: "defaults", reason: "validate rules have an expression and a message, not defaults"}
		}
		if c.validate, err = compile(e, spec.Expression, cel.BoolType, limit); err != nil {
			return nil, &fieldError{path: "expression", reason: err.Error()}
		}
	case hooks.PolicyDefault:
		if spec.Expression != "" {
			return nil, &fieldError{path: "expression", reason: "default rules fill in fields with defaults, and have no expression of their own"}
		}
		for _, op := range spec.Operations {
			if op != "create" {
				return nil, &fieldError{path: "operations", reason: "default rules apply when resources are created"}
			}
		}
		seen := map[string]bool{}
		for i, d := range spec.Defaults {
			if seen[d.Field] {
				return nil, &fieldError{path: fmt.Sprintf("defaults[%d].field", i), reason: d.Field + " has a default twice"}
			}
			seen[d.Field] = true
			prg, err := compile(e, d.Expression, nil, limit)
			if err != nil {
				return nil, &fieldError{path: fmt.Sprintf("defaults[%d].expression", i), reason: err.Error()}
			}
			c.defaults = append(c.defaults, compiledDefault{field: d.Field, override: d.Override, prg: prg})
		}
	}
	return c, nil
}

// points returns the hook points, or patterns, a rule attaches to.
func (c *compiled) points() []hooks.Point {
	kinds := c.spec.Kinds
	if len(kinds) == 0 {
		kinds = []string{""}
	}
	var out []hooks.Point
	switch c.spec.Type {
	case hooks.PolicyValidate:
		ops := c.spec.Operations
		if len(ops) == 0 {
			ops = []string{""}
		}
		for _, k := range kinds {
			for _, op := range ops {
				out = append(out, hooks.KindPattern(k, op, string(changeset.StageValidate)))
			}
		}
	case hooks.PolicyDefault:
		for _, k := range kinds {
			out = append(out, hooks.KindPattern(k, "create", string(changeset.StagePlan)))
		}
	}
	return out
}

// Facts answers what rule expressions ask about beyond the change itself:
// the account a resource belongs to, its hosting plan, the person making
// the change and the server.
type Facts interface {
	// Account returns the account resource of an account, or nil.
	Account(ctx context.Context, id store.ID) (map[string]any, error)
	// Plan returns the hosting plan of an account, or nil when it has none.
	Plan(ctx context.Context, account map[string]any) (map[string]any, error)
	// Server describes the machine.
	Server(ctx context.Context) (map[string]any, error)
}

// StoreFacts reads accounts and plans from the store and the machine from
// its facts.
type StoreFacts struct {
	Store store.Reader
	// Machine returns the facts of the panel's server; nil leaves server
	// empty.
	Machine func(ctx context.Context) (system.MachineFacts, error)
}

// Account implements Facts: any resource with the account's ID.
func (f StoreFacts) Account(ctx context.Context, id store.ID) (map[string]any, error) {
	if f.Store == nil || id == "" {
		return nil, nil
	}
	r, err := f.Store.Get(ctx, id)
	if store.ErrNotFound.Is(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resourceDoc(r.Meta, r.Spec)
}

// Plan implements Facts: the resource the account's spec names in plan.
func (f StoreFacts) Plan(ctx context.Context, account map[string]any) (map[string]any, error) {
	spec, _ := account["spec"].(map[string]any)
	ref, _ := spec["plan"].(string)
	if f.Store == nil || !store.ID(ref).Valid() {
		return nil, nil
	}
	r, err := f.Store.Get(ctx, store.ID(ref))
	if store.ErrNotFound.Is(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resourceDoc(r.Meta, r.Spec)
}

// Server implements Facts.
func (f StoreFacts) Server(ctx context.Context) (map[string]any, error) {
	if f.Machine == nil {
		return map[string]any{}, nil
	}
	m, err := f.Machine(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"hostname": m.Hostname, "fqdn": m.FQDN, "arch": m.Arch, "memoryBytes": float64(m.MemoryBytes),
		"cpus": float64(m.CPU.Threads), "kernel": m.Kernel, "os": m.OS.PrettyName, "virtualization": m.Virtualization,
	}, nil
}

// resourceDoc is how expressions see a resource.
func resourceDoc(meta store.Meta, spec json.RawMessage) (map[string]any, error) {
	doc := map[string]any{
		"id": string(meta.ID), "kind": meta.Kind,
		"scope":  map[string]any{"level": string(meta.Scope.Level), "id": string(meta.Scope.ID)},
		"labels": map[string]any{},
		"spec":   map[string]any{},
	}
	for k, v := range meta.Labels {
		doc["labels"].(map[string]any)[k] = v
	}
	if len(spec) > 0 {
		var s any
		if err := json.Unmarshal(spec, &s); err != nil {
			return nil, err
		}
		if s != nil {
			doc["spec"] = s
		}
	}
	return doc, nil
}

func actorDoc(a *auth.Actor) map[string]any {
	if a == nil {
		return map[string]any{"type": "anonymous", "id": "", "name": "", "user": "", "source": "", "roles": []any{}}
	}
	roles := []any{}
	for _, g := range a.Grants {
		if !slices.Contains(roles, any(string(g.Role))) {
			roles = append(roles, string(g.Role))
		}
	}
	return map[string]any{
		"type": string(a.Type), "id": string(a.ID), "name": a.Name, "user": string(a.User), "source": string(a.Source),
		"roles": roles,
	}
}

// Options are what an Engine needs.
type Options struct {
	Registry *hooks.Registry
	Facts    Facts
	// Ownership says which reseller an account belongs to, for resellers'
	// rules; nil until the module that keeps resellers says.
	Ownership func() auth.Ownership
	// CostLimit bounds each evaluation; DefaultCostLimit when zero.
	CostLimit uint64
}

// Engine compiles rules and attaches them to the hook pipeline.
type Engine struct {
	opts Options
}

// NewEngine returns an engine.
func NewEngine(opts Options) *Engine {
	if opts.CostLimit == 0 {
		opts.CostLimit = DefaultCostLimit
	}
	if opts.Facts == nil {
		opts.Facts = StoreFacts{}
	}
	return &Engine{opts: opts}
}

// Owner is the hook owner a rule's hooks are registered under.
func Owner(id store.ID) string { return hooks.OwnerRule + ":" + string(id) }

// attach registers a rule's hooks, replacing those it had. It returns the
// points it attached to and the problems it met.
func (e *Engine) attach(id store.ID, owner store.Scope, spec hooks.PolicySpec) ([]string, []string, error) {
	e.opts.Registry.Unregister(Owner(id))
	if !spec.Enabled {
		return nil, nil, nil
	}
	c, err := compileRule(spec, e.opts.CostLimit)
	if err != nil {
		return nil, nil, ErrBadRule.Wrap(err, "reason", err.Error())
	}
	var points, problems []string
	for _, p := range c.points() {
		reg := hooks.Registration{Point: p, Owner: Owner(id), Label: spec.Name, Priority: spec.Priority}
		switch spec.Type {
		case hooks.PolicyValidate:
			reg.Kind = hooks.Validate
			reg.OnValidate = func(ctx context.Context, hc *hooks.Context) error { return e.runValidate(ctx, c, owner, hc) }
		case hooks.PolicyDefault:
			reg.Kind = hooks.Modify
			reg.OnModify = func(ctx context.Context, hc *hooks.Context) error { return e.runDefaults(ctx, c, owner, hc) }
		}
		if err := e.opts.Registry.Register(reg); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", p, i18n.From(err).Message))
			continue
		}
		points = append(points, string(p))
	}
	return points, problems, nil
}

// detach takes a rule's hooks out of the pipeline.
func (e *Engine) detach(id store.ID) { e.opts.Registry.Unregister(Owner(id)) }

// covers reports whether a rule owned by owner applies to a resource of
// target.
func (e *Engine) covers(ctx context.Context, owner, target store.Scope) (bool, error) {
	switch owner.Level {
	case store.ScopeServer:
		return true, nil
	case store.ScopeReseller:
		if target == owner {
			return true, nil
		}
		if target.Level != store.ScopeAccount || e.opts.Ownership == nil {
			return false, nil
		}
		own := e.opts.Ownership()
		if own == nil {
			return false, nil
		}
		reseller, err := own.ResellerOf(ctx, target.ID)
		return reseller == owner.ID, err
	case store.ScopeAccount:
		return target == owner, nil
	}
	return false, nil
}

// operationOf names a change's operation as rules do.
func operationOf(c changeset.ResourceChange) string {
	if c.Op == changeset.OpAction {
		return c.Action
	}
	return string(c.Op)
}

// variables builds what expressions see for one change.
func (e *Engine) variables(ctx context.Context, actor *auth.Actor, c changeset.ResourceChange) (map[string]any, error) {
	meta := store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope, Labels: c.Labels}
	resource, err := resourceDoc(meta, c.After)
	if err != nil {
		return nil, err
	}
	var old any
	if c.Op != changeset.OpCreate {
		oldMeta := meta
		if c.LabelsBefore != nil {
			oldMeta.Labels = c.LabelsBefore
		}
		doc, err := resourceDoc(oldMeta, c.Before)
		if err != nil {
			return nil, err
		}
		old = doc
	}
	source := ""
	if actor != nil {
		source = string(actor.Source)
	}
	vars := map[string]any{
		"request":     map[string]any{"operation": operationOf(c), "kind": c.Kind, "source": source, "actor": actorDoc(actor)},
		"resource":    resource,
		"oldResource": old,
		"account":     nil,
		"plan":        nil,
		"user":        actorDoc(actor),
	}
	if c.Scope.Level == store.ScopeAccount {
		account, err := e.opts.Facts.Account(ctx, c.Scope.ID)
		if err != nil {
			return nil, err
		}
		if account != nil {
			vars["account"] = account
			plan, err := e.opts.Facts.Plan(ctx, account)
			if err != nil {
				return nil, err
			}
			if plan != nil {
				vars["plan"] = plan
			}
		}
	}
	server, err := e.opts.Facts.Server(ctx)
	if err != nil {
		return nil, err
	}
	vars["server"] = server
	return vars, nil
}

// applies reports whether a rule applies to a change: its kinds,
// operations and owner.
func (e *Engine) applies(ctx context.Context, c *compiled, owner store.Scope, ch changeset.ResourceChange) (bool, error) {
	if len(c.spec.Kinds) > 0 && !slices.Contains(c.spec.Kinds, ch.Kind) {
		return false, nil
	}
	if c.spec.Type == hooks.PolicyValidate && len(c.spec.Operations) > 0 && !slices.Contains(c.spec.Operations, operationOf(ch)) {
		return false, nil
	}
	if c.spec.Type == hooks.PolicyDefault && ch.Op != changeset.OpCreate {
		return false, nil
	}
	// Rules never judge rules, so a rule can't stop its own repair.
	if IsRuleKind(ch.Kind) {
		return false, nil
	}
	return e.covers(ctx, owner, ch.Scope)
}

// decide evaluates a validate rule on one change. It returns the rule's
// message when the change is refused.
func (e *Engine) decide(ctx context.Context, c *compiled, actor *auth.Actor, ch changeset.ResourceChange) (bool, uint64, error) {
	vars, err := e.variables(ctx, actor, ch)
	if err != nil {
		return false, 0, err
	}
	out, cost, err := c.validate.eval(ctx, vars)
	if err != nil {
		return false, cost, fmt.Errorf("evaluating the rule: %w", err)
	}
	refuse, ok := out.(bool)
	if !ok {
		return false, cost, fmt.Errorf("the rule's expression gave %T, not true or false", out)
	}
	return refuse, cost, nil
}

func (e *Engine) runValidate(ctx context.Context, c *compiled, owner store.Scope, hc *hooks.Context) error {
	if hc.Change == nil {
		return nil
	}
	ok, err := e.applies(ctx, c, owner, *hc.Change)
	if err != nil || !ok {
		return err
	}
	refuse, _, err := e.decide(ctx, c, hc.Actor, *hc.Change)
	if err != nil {
		return err
	}
	if refuse {
		return ErrRefused.New("message", c.spec.Message)
	}
	return nil
}

// FieldResult is what a default rule did with one field.
type FieldResult struct {
	Field string `json:"field"`
	Value any    `json:"value,omitempty"`
	// Set says the field was filled in or overridden; a field the request
	// already set and the default doesn't override is left as it was.
	Set bool `json:"set"`
}

// fill evaluates a default rule's fields for one change and returns the
// new spec.
func (e *Engine) fill(ctx context.Context, c *compiled, actor *auth.Actor, ch changeset.ResourceChange) (json.RawMessage, []FieldResult, uint64, error) {
	vars, err := e.variables(ctx, actor, ch)
	if err != nil {
		return nil, nil, 0, err
	}
	var spec map[string]any
	if len(ch.After) > 0 {
		if err := json.Unmarshal(ch.After, &spec); err != nil {
			return nil, nil, 0, err
		}
	}
	if spec == nil {
		spec = map[string]any{}
	}
	var results []FieldResult
	var total uint64
	for _, d := range c.defaults {
		value, cost, err := d.prg.eval(ctx, vars)
		total += cost
		if err != nil {
			return nil, nil, total, fmt.Errorf("evaluating the default of %s: %w", d.field, err)
		}
		set := setField(spec, d.field, value, d.override)
		results = append(results, FieldResult{Field: d.field, Value: value, Set: set})
	}
	raw, err := json.Marshal(spec)
	return raw, results, total, err
}

// setField writes a value at a dotted path, creating the objects on the
// way. Unless override is set, a field that already has a value keeps it.
func setField(doc map[string]any, path string, value any, override bool) bool {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			if cur[p] != nil && !override {
				return false
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	last := parts[len(parts)-1]
	if v, ok := cur[last]; ok && v != nil && !override {
		return false
	}
	cur[last] = value
	return true
}

func (e *Engine) runDefaults(ctx context.Context, c *compiled, owner store.Scope, hc *hooks.Context) error {
	for _, i := range hc.Changes {
		ch := hc.ChangeSet.Changes[i]
		ok, err := e.applies(ctx, c, owner, ch)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		after, _, _, err := e.fill(ctx, c, hc.Actor, ch)
		if err != nil {
			return err
		}
		hc.ChangeSet.Changes[i].After = after
	}
	return nil
}

// Reconcilers attach each kind's rules to the hook pipeline as they are
// stored and take them out as they are deleted. Rules change nothing on the
// machine.
func (e *Engine) Reconcilers() []reconcile.Reconciler {
	var out []reconcile.Reconciler
	for _, k := range Kinds() {
		out = append(out, reconcile.Typed(k, ruleReconciler{e: e}))
	}
	return out
}

type ruleReconciler struct{ e *Engine }

func (ruleReconciler) Plan(ctx context.Context, obj *store.Object[hooks.PolicySpec, Status], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (r ruleReconciler) Apply(ctx context.Context, obj *store.Object[hooks.PolicySpec, Status], plan reconcile.Plan, ex reconcile.Executor) (Status, error) {
	st := obj.Status
	if obj.Deleted() {
		r.e.detach(obj.ID)
		return st, nil
	}
	points, problems, attachErr := r.e.attach(obj.ID, obj.Scope, obj.Spec)
	st.Points, st.Problems = points, problems
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, time.Now()); err != nil {
		return st, err
	}
	switch {
	case attachErr != nil:
		e := i18n.From(attachErr)
		st.Phase, st.Message, st.Code = store.PhaseFailed, e.Message, e.Code
	case len(problems) > 0:
		st.Phase, st.Message = store.PhaseDegraded, "Some of what the rule names doesn't exist: "+strings.Join(problems, "; ")
	case !obj.Spec.Enabled:
		st.Message = "The rule is off."
	}
	return st, attachErr
}

// Load attaches every stored rule that is on, as the panel starts. A rule
// that can't be attached is skipped and reported in its status the next
// time it is reconciled.
func (e *Engine) Load(ctx context.Context, rd store.Reader) error {
	var errs []error
	for _, k := range Kinds() {
		cursor := ""
		for {
			page, next, err := k.List(ctx, rd, store.Query{Cursor: cursor, Limit: store.MaxLimit})
			if err != nil {
				return err
			}
			for _, obj := range page {
				if _, _, err := e.attach(obj.ID, obj.Scope, obj.Spec); err != nil {
					errs = append(errs, fmt.Errorf("rule %s: %w", obj.ID, err))
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	return errors.Join(errs...)
}
