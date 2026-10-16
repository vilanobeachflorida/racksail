package addons

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	schemakind "github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// The kinds an add-on adds are registered with the store while the add-on
// is installed, get hook points like every kind and the API's operations
// for kinds described by JSON Schema, and are reconciled through the
// add-on: it plans each resource, the agent runs the plan's steps, and the
// add-on applies the rest and reports the status. The steps may only touch
// the add-on's own directory for its kinds, which its process can read.

// KindDir is where the steps of an add-on's plans may write.
func KindDir(inst string) string { return AddonDir(inst, "") + "/kinds" }

// kindSteps are the agent operations an add-on's plans may run, all of
// them on paths in its kinds' directory.
var kindSteps = map[string]bool{
	agent.WriteFile.Name(): true, agent.EnsureDir.Name(): true, agent.RemovePath.Name(): true,
	agent.ReadFile.Name(): true, agent.StatPath.Name(): true, agent.ListDir.Name(): true,
}

// How long the add-on has to plan a resource, which previews wait for, and
// to apply one, which may call outside services.
const (
	kindPlanTimeout  = hooks.MaxDeadline
	kindApplyTimeout = 5 * time.Minute
)

// Errors of the kinds add-ons add.
var (
	ErrKindTaken   = i18n.Define("addons.kind_taken", http.StatusConflict, "{addon} adds {kind}, but {owner} has {taken} already.")
	ErrKindInUse   = i18n.Define("addons.kind_in_use", http.StatusConflict, "{addon} can't be {what} while resources of its {kind} kind exist. Delete them first.")
	ErrKindRemove  = i18n.Define("addons.kind_remove", http.StatusConflict, "{addon} can't be removed while resources of its {kind} kind exist. Delete them first, so it can undo what it did for them, or turn the add-on off and remove it with them.")
	ErrKindOff     = i18n.Define("addons.kind_off", http.StatusConflict, "{kind} resources are planned by {addon}, which isn't running. Turn it on, or leave safe mode, first.")
	ErrKindPlan    = i18n.Define("addons.kind_plan", http.StatusBadGateway, "{addon} couldn't plan the {kind}: {reason}")
	ErrKindApply   = i18n.Define("addons.kind_apply", http.StatusBadGateway, "{addon} couldn't apply the {kind}: {reason}")
	ErrKindStep    = i18n.Define("addons.kind_step", http.StatusForbidden, "{addon} planned {step} for a {kind}, which it may not: {reason}.")
	ErrKindSchema  = i18n.Define("addons.kind_schema", http.StatusUnprocessableEntity, "The {schema} of {kind} isn't a JSON Schema the panel can use: {reason}.")
	ErrKindStatus  = i18n.Define("addons.kind_status", http.StatusBadGateway, "{addon} reported a status for the {kind} that its status schema doesn't allow: {reason}.")
	ErrKindUnknown = i18n.Define("addons.kind_unknown", http.StatusNotFound, "No add-on adds {kind} any more.")
)

// refuseLoader keeps the schemas add-ons bring from reaching anything
// outside themselves: a $ref to a file or a URL fails.
type refuseLoader struct{}

func (refuseLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("the schema may only refer to itself, not %s", url)
}

// compileSchema compiles a JSON Schema document an add-on declares.
func compileSchema(doc map[string]any, name string) (*jsonschema.Schema, json.RawMessage, error) {
	if doc == nil {
		doc = map[string]any{"type": "object"}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(refuseLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "racksail-addon:" + name + ".json"
	if err := c.AddResource(url, parsed); err != nil {
		return nil, nil, err
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, nil, err
	}
	return sch, raw, nil
}

// fieldErrors turns what a schema found wrong with a document into field
// errors at paths under prefix, such as spec.domains[0].
func fieldErrors(prefix string, err error) []i18n.FieldError {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []i18n.FieldError{i18n.Field(prefix, i18n.ErrBadValue, "reason", err.Error())}
	}
	var out []i18n.FieldError
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		at := prefix
		for _, tok := range e.InstanceLocation {
			if _, err := strconv.Atoi(tok); err == nil {
				at += "[" + tok + "]"
			} else {
				at += "." + tok
			}
		}
		// A missing field is reported on the field, so forms can mark it.
		if req, ok := e.ErrorKind.(*schemakind.Required); ok {
			for _, name := range req.Missing {
				out = append(out, i18n.Field(at+"."+name, i18n.ErrRequired))
			}
			return
		}
		reason := e.Error()
		if i := strings.Index(reason, ": "); i >= 0 && strings.HasPrefix(reason, "at '") {
			reason = reason[i+2:]
		}
		out = append(out, i18n.Field(at, i18n.ErrBadValue, "reason", reason))
	}
	walk(ve)
	if len(out) == 0 {
		out = append(out, i18n.Field(prefix, i18n.ErrBadValue, "reason", ve.Error()))
	}
	return out
}

// validateDoc checks a JSON document against a schema.
func validateDoc(sch *jsonschema.Schema, doc json.RawMessage, prefix string) []i18n.FieldError {
	if len(bytes.TrimSpace(doc)) == 0 {
		doc = json.RawMessage("{}")
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return []i18n.FieldError{i18n.Field(prefix, i18n.ErrBadValue, "reason", "it isn't valid JSON")}
	}
	if err := sch.Validate(parsed); err != nil {
		return fieldErrors(prefix, err)
	}
	return nil
}

// Kind is a kind an add-on adds: a store.SchemaKind whose settings its
// manifest describes with JSON Schema.
type Kind struct {
	// Addon is the instance of the add-on that adds it, and Resource that
	// add-on's resource.
	Addon    string
	Resource store.ID
	// AddonName is the add-on's name, for messages.
	AddonName string
	info      store.KindInfo
	spec      *jsonschema.Schema
	status    *jsonschema.Schema
	inputs    map[string]*jsonschema.Schema
	specDoc   json.RawMessage
	statusDoc json.RawMessage
	inputDocs map[string]json.RawMessage
	// definition tells two definitions of the kind apart.
	definition string
}

var _ store.SchemaKind = (*Kind)(nil)

// NewKind builds a kind from the declaration in an add-on's manifest.
func NewKind(inst, addonName string, d hooks.AddonKind) (*Kind, error) {
	k := &Kind{Addon: inst, AddonName: addonName, inputs: map[string]*jsonschema.Schema{}, inputDocs: map[string]json.RawMessage{}}
	k.info = store.KindInfo{
		Name: d.Name, Prefix: d.Prefix, Scope: store.ScopeLevel(d.Scope), Title: d.Title, Plural: d.Plural,
		Description: strings.TrimSpace(d.Description) + " Added by " + addonName + ", which plans and applies it.",
		Risks:       store.Risks{Create: store.RiskLow, Update: store.RiskLow, Delete: store.RiskHigh},
		UI:          store.UIHints{Icon: "puzzle"},
	}
	var err error
	bad := func(what string, err error) error {
		return ErrKindSchema.Wrap(err, "schema", what, "kind", d.Name, "reason", err.Error())
	}
	if k.spec, k.specDoc, err = compileSchema(d.Schema, d.Name+".spec"); err != nil {
		return nil, bad("settings schema", err)
	}
	if k.status, k.statusDoc, err = compileSchema(d.Status, d.Name+".status"); err != nil {
		return nil, bad("status schema", err)
	}
	for _, a := range d.Actions {
		act := store.Action{
			Name: a.Name, Title: a.Title, Summary: a.Summary, Description: a.Description, Risk: store.Risk(a.Risk),
			Destructive: a.Destructive, Reversible: a.Reversible,
		}
		if a.Input != nil {
			sch, raw, err := compileSchema(a.Input, d.Name+"."+a.Name+".input")
			if err != nil {
				return nil, bad("input schema of "+a.Name, err)
			}
			// The pipeline checks that the input is an object; the
			// action's schema then says the rest.
			act.Input = map[string]any{}
			k.inputs[a.Name], k.inputDocs[a.Name] = sch, raw
		}
		k.info.Actions = append(k.info.Actions, act)
	}
	if err := store.CheckInfo(&k.info); err != nil {
		return nil, ErrManifest.Wrap(err, "reason", err.Error())
	}
	sum := sha256.New()
	enc := json.NewEncoder(sum)
	enc.Encode(d)
	k.definition = hex.EncodeToString(sum.Sum(nil))
	return k, nil
}

// Info implements store.Kind.
func (k *Kind) Info() *store.KindInfo { return &k.info }

// SpecType implements store.Kind: the settings are any JSON object, which
// the kind's schema describes.
func (k *Kind) SpecType() reflect.Type { return reflect.TypeFor[map[string]any]() }

// StatusType implements store.Kind.
func (k *Kind) StatusType() reflect.Type { return reflect.TypeFor[store.Status]() }

// Default implements store.Kind; the add-on fills in nothing.
func (k *Kind) Default(ctx context.Context, r *store.Resource) error { return nil }

// Err implements store.Kind: NewKind refuses a kind with problems.
func (k *Kind) Err() error { return nil }

// Validate implements store.Kind: the scope, the labels and the settings
// against the kind's schema.
func (k *Kind) Validate(ctx context.Context, r, old *store.Resource) error {
	var fields []i18n.FieldError
	if !k.info.OwnedAt(r.Scope.Level) {
		fields = append(fields, i18n.Field("scope.level", i18n.ErrBadValue, "reason", k.info.Title+" resources are owned at the "+string(k.info.Scope)+" level"))
	} else if err := r.Scope.Validate(); err != nil {
		fields = append(fields, i18n.Field("scope", i18n.ErrBadValue, "reason", err.Error()))
	}
	if err := store.ValidateLabels(r.Labels); err != nil {
		fields = append(fields, i18n.Field("labels", i18n.ErrBadValue, "reason", err.Error()))
	}
	fields = append(fields, validateDoc(k.spec, r.Spec, "spec")...)
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// validateInput checks an action's input against its schema.
func (k *Kind) validateInput(action string, input json.RawMessage) error {
	sch, ok := k.inputs[action]
	if !ok {
		return nil
	}
	if fields := validateDoc(sch, input, "input"); len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// commonStatusSchema is the JSON Schema of the status fields every kind
// shares, which the panel keeps for the kinds add-ons add too.
var commonStatusSchema = func() map[string]any {
	reg := huma.NewMapRegistry("#/$defs/", huma.DefaultSchemaNamer)
	s := reg.Schema(reflect.TypeFor[store.Status](), false, "Status")
	raw, _ := json.Marshal(s)
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	defs := map[string]any{}
	for name, sch := range reg.Map() {
		defs[name] = sch
	}
	if len(defs) > 0 {
		doc["$defs"] = defs
	}
	return doc
}()

// SpecSchema implements store.SchemaKind.
func (k *Kind) SpecSchema() json.RawMessage { return k.specDoc }

// StatusSchema implements store.SchemaKind: the fields every kind shares,
// with those the add-on reports.
func (k *Kind) StatusSchema() json.RawMessage {
	var own map[string]any
	json.Unmarshal(k.statusDoc, &own)
	doc := map[string]any{}
	for key, v := range commonStatusSchema {
		doc[key] = v
	}
	props := map[string]any{}
	if common, ok := commonStatusSchema["properties"].(map[string]any); ok {
		for key, v := range common {
			props[key] = v
		}
	}
	if theirs, ok := own["properties"].(map[string]any); ok {
		for key, v := range theirs {
			if _, shared := props[key]; !shared {
				props[key] = v
			}
		}
	}
	doc["properties"] = props
	doc["additionalProperties"] = true
	raw, _ := json.Marshal(doc)
	return raw
}

// InputSchema implements store.SchemaKind.
func (k *Kind) InputSchema(action string) json.RawMessage { return k.inputDocs[action] }

// kindsOf builds the kinds a manifest adds, for the add-on whose resource
// is resource.
func kindsOf(inst string, resource store.ID, man *hooks.AddonManifest) ([]*Kind, error) {
	var out []*Kind
	for _, d := range man.Kinds {
		k, err := NewKind(inst, man.Name, d)
		if err != nil {
			return nil, err
		}
		k.Resource = resource
		out = append(out, k)
	}
	return out, nil
}

// namespaceOwner says who keeps a namespace of kinds that no add-on may
// take: the panel, for the names it keeps for itself, or a module, for its
// own id. It is empty for the rest.
func (r *Runtime) namespaceOwner(ns string) string {
	switch {
	case slices.Contains(hooks.ReservedNamespaces, ns):
		return "the panel"
	case slices.Contains(r.opts.Modules, ns):
		return "the " + ns + " module"
	}
	return ""
}

// addedKinds is what the runtime registered for add-ons: the kinds by
// name, and the reconcilers it registered, once per kind name, which find
// the add-on that adds a kind each time they run.
type addedKinds struct {
	kinds       map[string]*Kind
	reconciling map[string]bool
}

// kindOf returns the kind of that name an add-on adds.
func (r *Runtime) kindOf(name string) (*Kind, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.added.kinds[name]
	return k, ok
}

// kindsAddedBy lists the kinds an add-on's instance adds, by name.
func (r *Runtime) kindsAddedBy(inst string) []*Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Kind
	for _, k := range r.added.kinds {
		if k.Addon == inst {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].info.Name < out[j].info.Name })
	return out
}

// LoadKinds registers the kinds of every installed add-on, so their
// resources can be read, changed and reconciled from the start, before the
// add-ons themselves are reconciled. A kind that can't be registered is
// logged and reported in its add-on's status.
func (r *Runtime) LoadKinds(ctx context.Context) {
	cursor := ""
	for {
		page, next, err := Addon.List(ctx, r.opts.Store, store.Query{Cursor: cursor, Limit: store.MaxLimit})
		if err != nil {
			r.opts.Log.Error("listing add-ons for their kinds", "error", err)
			return
		}
		for _, obj := range page {
			if obj.Deleted() {
				continue
			}
			man, err := Check(obj.Spec, false)
			if err != nil {
				continue
			}
			if problems := r.declareKinds(Instance(obj.Kind, obj.Spec.ID, obj.Scope), obj.ID, man); len(problems) > 0 {
				r.opts.Log.Warn("some kinds of an add-on couldn't be added", "addon", obj.Spec.ID, "problems", problems)
			}
		}
		if next == "" {
			return
		}
		cursor = next
	}
}

// declareKinds registers the kinds an add-on's manifest adds, replaces
// the definitions of those it added before, and drops those it no longer
// adds, which its own validate hook only lets go once no resource of them
// is left. resource is the add-on's resource. It returns what couldn't be
// registered, such as kinds in a namespace the panel or a module keeps.
func (r *Runtime) declareKinds(inst string, resource store.ID, man *hooks.AddonManifest) []string {
	kinds, err := kindsOf(inst, resource, man)
	if err != nil {
		return []string{i18n.From(err).Message}
	}
	var problems []string
	wanted := map[string]bool{}
	for _, k := range kinds {
		name := k.info.Name
		wanted[name] = true
		r.mu.Lock()
		had := r.added.kinds[name]
		r.mu.Unlock()
		switch owner := r.namespaceOwner(k.info.Module()); {
		case owner != "":
			problems = append(problems, name+": "+owner+" keeps the kinds named "+k.info.Module()+".*")
			continue
		case had != nil && had.Addon != inst:
			problems = append(problems, name+": another add-on adds it")
			continue
		case had != nil && had.definition == k.definition:
			continue
		case had != nil && had.info.Prefix == k.info.Prefix:
			if err := r.opts.Kinds.Replace(k); err != nil {
				problems = append(problems, name+": "+err.Error())
				continue
			}
		default:
			if had != nil {
				r.opts.Kinds.Unregister(name)
			}
			if err := r.opts.Kinds.Register(k); err != nil {
				problems = append(problems, name+": "+err.Error())
				continue
			}
		}
		r.opts.Registry.UndeclareKind(name)
		r.opts.Registry.DeclareKind(k)
		r.mu.Lock()
		r.added.kinds[name] = k
		register := !r.added.reconciling[name]
		r.added.reconciling[name] = true
		r.mu.Unlock()
		if register && r.opts.Reconcilers != nil {
			if err := r.opts.Reconcilers.Register(kindReconciler{r: r, kind: name}); err != nil {
				problems = append(problems, name+": "+err.Error())
			}
		}
	}
	for _, k := range r.kindsAddedBy(inst) {
		if !wanted[k.info.Name] {
			r.retireKind(k.info.Name)
		}
	}
	return problems
}

// retireKinds drops every kind an add-on added, once it is removed. Its
// resources still being deleted then have no add-on to finish them, so
// their reconciler finishes them without one, and they are queued for it.
func (r *Runtime) retireKinds(ctx context.Context, inst string) {
	for _, k := range r.kindsAddedBy(inst) {
		name := k.info.Name
		// The store lists a kind's resources while the kind is registered.
		var left []store.ID
		for cursor := ""; r.opts.Enqueue != nil; {
			page, err := r.opts.Store.List(ctx, store.Query{Kind: name, IncludeDeleted: true, Cursor: cursor, Limit: store.MaxLimit})
			if err != nil {
				r.opts.Log.Warn("listing what is left of a removed add-on's kind", "kind", name, "error", err)
				break
			}
			for _, res := range page.Items {
				if res.Deleted() {
					left = append(left, res.ID)
				}
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
		r.retireKind(name)
		for _, id := range left {
			r.opts.Enqueue(id, "its add-on was removed")
		}
	}
}

func (r *Runtime) retireKind(name string) {
	r.opts.Kinds.Unregister(name)
	r.opts.Registry.UndeclareKind(name)
	r.mu.Lock()
	delete(r.added.kinds, name)
	r.mu.Unlock()
}

// kindHost returns the running add-on that adds a kind.
func (r *Runtime) kindHost(name string) (*Kind, *installed, error) {
	k, ok := r.kindOf(name)
	if !ok {
		return nil, nil, ErrKindUnknown.New("kind", name)
	}
	r.mu.Lock()
	in := r.running[k.Addon]
	r.mu.Unlock()
	if in == nil {
		return k, nil, ErrKindOff.New("kind", k.info.Title, "addon", k.AddonName)
	}
	return k, in, nil
}

// kindResource is a resource as an add-on is sent it.
func kindResource(res *store.Resource) hooks.KindResource {
	return hooks.KindResource{
		ID: res.ID, Kind: res.Kind, Scope: res.Scope, Labels: res.Labels, Generation: res.Generation,
		Spec: res.Spec, Status: res.Status, Deleted: res.Deleted(),
	}
}

// checkSteps refuses a plan whose steps go beyond what an add-on's kinds
// may do on the server: the file operations of kindSteps, on paths in its
// kinds' directory, with files owned by root.
func checkSteps(k *Kind, inst string, steps []agent.Step) error {
	dir := KindDir(inst)
	for _, s := range steps {
		refuse := func(reason string) error {
			return ErrKindStep.New("addon", k.AddonName, "step", s.Op, "kind", k.info.Title, "reason", reason)
		}
		if !kindSteps[s.Op] {
			return refuse("its plans may only read and write files in " + dir)
		}
		raw, err := json.Marshal(s.Input)
		if err != nil {
			return refuse("its input isn't JSON")
		}
		var in struct {
			Path  string          `json:"path"`
			Owner json.RawMessage `json:"owner"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return refuse("its input isn't the operation's")
		}
		if in.Path != path.Clean(in.Path) || (in.Path != dir && !strings.HasPrefix(in.Path, dir+"/")) {
			return refuse("it may only touch files in " + dir)
		}
		if owner := strings.TrimSpace(string(in.Owner)); owner != "" && owner != "null" && owner != "{}" {
			return refuse("the files it writes belong to root")
		}
	}
	return nil
}

// contributed labels what a plan's effects say as the add-on's.
func contributed(inst string, effects []system.Effect) []system.Effect {
	out := make([]system.Effect, len(effects))
	for i, e := range effects {
		e.Contributor = hooks.OwnerAddon + ":" + inst
		out[i] = e
	}
	return out
}

// kindReconciler reconciles the resources of a kind an add-on adds, by
// asking the add-on that adds it now.
type kindReconciler struct {
	r    *Runtime
	kind string
}

func (k kindReconciler) Kind() string { return k.kind }

// plan asks the add-on for its plan of a resource, or of an action.
func (k kindReconciler) plan(ctx context.Context, res *store.Resource, action *hooks.KindAction) (hooks.KindPlan, error) {
	kind, in, err := k.r.kindHost(k.kind)
	if err != nil {
		return hooks.KindPlan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, kindPlanTimeout)
	defer cancel()
	plan, err := in.client.PlanKind(ctx, hooks.KindCall{Resource: kindResource(res), Action: action, Dir: KindDir(in.instance)})
	if err != nil {
		return hooks.KindPlan{}, ErrKindPlan.Wrap(err, "addon", kind.AddonName, "kind", kind.info.Title, "reason", err.Error())
	}
	if err := checkSteps(kind, in.instance, plan.Steps); err != nil {
		return hooks.KindPlan{}, err
	}
	plan.Effects = contributed(in.instance, plan.Effects)
	return plan, nil
}

// dropped reports whether a resource being deleted goes without the
// add-on that planned it: the add-on was removed, or is being removed while
// it doesn't run, so nothing is left to undo what it did for the resource.
// kind is the kind as the add-on added it, while it still does.
func (k kindReconciler) dropped(ctx context.Context, req reconcile.Request) (kind *Kind, gone bool) {
	if !req.Resource.Deleted() {
		return nil, false
	}
	kind, _, err := k.r.kindHost(k.kind)
	switch {
	case ErrKindUnknown.Is(err):
		return nil, true
	case ErrKindOff.Is(err) && req.Read != nil && kind.Resource != "":
		addon, err := req.Read.Get(ctx, kind.Resource)
		return kind, store.ErrNotFound.Is(err) || (err == nil && addon.Deleted())
	}
	return kind, false
}

// leftBehind is what the preview says of a resource deleted without its
// add-on: whatever the add-on made for it outside the panel stays.
func leftBehind(kind *Kind, res *store.Resource) []system.Effect {
	who := "Its add-on"
	if kind != nil {
		who = kind.AddonName
	}
	return []system.Effect{{
		Type: system.EffectProcess, Action: "keep", Target: string(res.ID),
		Detail:      "what " + who + " made for it outside the panel, since " + who + " goes without running to undo it",
		Contributor: hooks.OwnerCore + ":addons",
	}}
}

func (k kindReconciler) Plan(ctx context.Context, req reconcile.Request) (reconcile.Plan, error) {
	if kind, gone := k.dropped(ctx, req); gone {
		return reconcile.Plan{Effects: leftBehind(kind, req.Resource)}, nil
	}
	plan, err := k.plan(ctx, req.Resource, nil)
	if err != nil {
		return reconcile.Plan{}, err
	}
	return reconcile.Plan{Steps: plan.Steps, Effects: plan.Effects}, nil
}

// Apply runs the plan's steps, then has the add-on apply the rest and say
// how the resource is. Its own status fields go next to the ones every
// kind shares, which the panel keeps.
func (k kindReconciler) Apply(ctx context.Context, req reconcile.Request, plan reconcile.Plan, ex reconcile.Executor) (any, error) {
	res := req.Resource
	common, _ := res.CommonStatus()
	var own map[string]any
	json.Unmarshal(res.Status, &own)
	now := k.r.opts.Now()
	fail := func(err error) (any, error) {
		e := i18n.From(err)
		common.ObservedGeneration = res.Generation
		common.Phase, common.Message, common.Code = store.PhaseFailed, e.Message, e.Code
		common.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionFalse, Reason: "ApplyFailed", Message: e.Message, Code: e.Code, ObservedGeneration: res.Generation}, now)
		return statusWith(own, common), err
	}
	if _, gone := k.dropped(ctx, req); gone {
		// Nothing undoes it any more; the resource only leaves the store.
		common.ObservedGeneration = res.Generation
		return statusWith(own, common), nil
	}
	kind, in, err := k.r.kindHost(k.kind)
	if err != nil {
		return fail(err)
	}
	results, err := ex.Run(ctx, plan.Steps)
	if err != nil {
		return fail(err)
	}
	actx, cancel := context.WithTimeout(ctx, kindApplyTimeout)
	defer cancel()
	applied, err := in.client.ApplyKind(actx, hooks.KindCall{Resource: kindResource(res), Dir: KindDir(in.instance), Results: results})
	if err != nil {
		return fail(ErrKindApply.Wrap(err, "addon", kind.AddonName, "kind", kind.info.Title, "reason", err.Error()))
	}
	if applied.Status != nil {
		raw, _ := json.Marshal(applied.Status)
		if fields := validateDoc(kind.status, raw, "status"); len(fields) > 0 {
			return fail(ErrKindStatus.New("addon", kind.AddonName, "kind", kind.info.Title, "reason", fmt.Sprintf("%v at %s", fields[0].Params["reason"], fields[0].Path)))
		}
		own = applied.Status
	}
	common.ObservedGeneration = res.Generation
	common.Phase, common.Message, common.Code = store.PhaseReady, "", ""
	common.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionTrue, Reason: "Applied", ObservedGeneration: res.Generation}, now)
	return statusWith(own, common), nil
}

// PlanAction asks the add-on for its plan of an action, after checking the
// action's input against its schema.
func (k kindReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	kind, ok := k.r.kindOf(k.kind)
	if !ok {
		return reconcile.ActionPlan{}, ErrKindUnknown.New("kind", k.kind)
	}
	if err := kind.validateInput(req.Action, req.Input); err != nil {
		return reconcile.ActionPlan{}, err
	}
	plan, err := k.plan(ctx, req.Resource, &hooks.KindAction{Name: req.Action, Input: req.Input})
	if err != nil {
		return reconcile.ActionPlan{}, err
	}
	return reconcile.ActionPlan{Spec: plan.Spec, Plan: reconcile.Plan{Steps: plan.Steps, Effects: plan.Effects}}, nil
}

// ApplyAction has the add-on carry out an action once its steps have run;
// what it answers is the action's output.
func (k kindReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	kind, in, err := k.r.kindHost(k.kind)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, kindApplyTimeout)
	defer cancel()
	applied, err := in.client.ApplyKind(ctx, hooks.KindCall{Resource: kindResource(req.Resource), Action: &hooks.KindAction{Name: req.Action, Input: req.Input}, Dir: KindDir(in.instance)})
	if err != nil {
		return nil, ErrKindApply.Wrap(err, "addon", kind.AddonName, "kind", kind.info.Title, "reason", err.Error())
	}
	if len(applied.Output) == 0 {
		return nil, nil
	}
	return applied.Output, nil
}

// statusWith puts the status fields every kind shares over the add-on's
// own, which can't take their names.
func statusWith(own map[string]any, common store.Status) map[string]any {
	out := map[string]any{}
	for key, v := range own {
		out[key] = v
	}
	for _, key := range []string{"phase", "message", "code", "observedGeneration", "conditions"} {
		delete(out, key)
	}
	raw, _ := json.Marshal(common)
	var shared map[string]any
	json.Unmarshal(raw, &shared)
	for key, v := range shared {
		out[key] = v
	}
	return out
}

// kindHooks are the runtime's hooks for the kinds add-ons add: an add-on
// may only add kinds nobody else has, and may only drop them, or go while it
// runs, once none of their resources is left; their resources only change
// while the add-on runs to plan them, except that removing an add-on that
// doesn't run deletes them with it, since nothing else could.
func (r *Runtime) kindHooks() []hooks.Registration {
	const owner = hooks.OwnerCore + ":addons"
	var out []hooks.Registration
	for _, op := range []string{"create", "update", "delete"} {
		out = append(out, hooks.Registration{
			Point: hooks.KindPoint(Addon.Name(), op, changeset.StageValidate), Kind: hooks.Validate, Owner: owner, Security: true, Priority: -1000,
			OnValidate: r.checkKinds,
		})
	}
	out = append(out, hooks.Registration{
		Point: hooks.KindPoint(Addon.Name(), "delete", changeset.StagePlan), Kind: hooks.Modify, Owner: owner, Priority: -1000,
		Label: "Removing the add-on", Failure: hooks.Block, OnModify: r.dropWithAddon,
	})
	return append(out, hooks.Registration{
		Point: "*.*.*.validate", Kind: hooks.Validate, Owner: owner, Security: true, Priority: -900,
		OnValidate: func(ctx context.Context, c *hooks.Context) error {
			if c.Change == nil {
				return nil
			}
			k, _, err := r.kindHost(c.Change.Kind)
			switch {
			case ErrKindUnknown.Is(err):
				return nil
			case ErrKindOff.Is(err) && c.Change.Op == changeset.OpDelete && removes(c.ChangeSet, k):
				// Its add-on goes in the same change set, without running to
				// undo what it did.
				return nil
			}
			return err
		},
	})
}

// removes reports whether a change set removes the add-on that adds a kind.
func removes(cs *changeset.ChangeSet, k *Kind) bool {
	return cs != nil && k != nil && slices.ContainsFunc(cs.Changes, func(c changeset.ResourceChange) bool {
		return c.Op == changeset.OpDelete && c.Kind == Addon.Name() && c.ID == k.Resource
	})
}

// dropWithAddon deletes, with an add-on that doesn't run, the resources of
// the kinds it adds: nothing would plan them once it's gone, and it isn't
// there to undo what it did for them, which the preview says.
func (r *Runtime) dropWithAddon(ctx context.Context, c *hooks.Context) error {
	rd, ok := store.ReaderFrom(ctx)
	if !ok {
		rd = r.opts.Store
	}
	for _, i := range c.Changes {
		ch := c.ChangeSet.Changes[i]
		var spec AddonSpec
		if ch.Op != changeset.OpDelete || json.Unmarshal(ch.Before, &spec) != nil {
			continue
		}
		inst := Instance(ch.Kind, spec.ID, ch.Scope)
		if _, _, _, _, running := r.Running(inst); running {
			continue
		}
		for _, k := range r.kindsAddedBy(inst) {
			for cursor := ""; ; {
				page, err := rd.List(ctx, store.Query{Kind: k.info.Name, Cursor: cursor, Limit: store.MaxLimit})
				if err != nil {
					return err
				}
				for _, res := range page.Items {
					deleting := slices.ContainsFunc(c.ChangeSet.Changes, func(other changeset.ResourceChange) bool {
						return other.ID == res.ID && other.Op == changeset.OpDelete
					})
					if res.Deleted() || deleting {
						continue
					}
					c.ChangeSet.Changes = append(c.ChangeSet.Changes, changeset.ResourceChange{
						Op: changeset.OpDelete, Kind: res.Kind, ID: res.ID, Scope: res.Scope, Server: res.Server,
					})
				}
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
		}
	}
	return nil
}

// checkKinds refuses an add-on's change that would take a kind, a prefix
// or a namespace another add-on, a module or the panel has, or drop a kind,
// or change its prefix, while resources of it exist. Removing an add-on that
// runs waits for them too, so it can undo what it did for them; one that
// doesn't run can't, and its resources go with it.
func (r *Runtime) checkKinds(ctx context.Context, c *hooks.Context) error {
	ch := c.Change
	if ch == nil {
		return nil
	}
	var before, after AddonSpec
	json.Unmarshal(ch.Before, &before)
	inst := Instance(ch.Kind, before.ID, ch.Scope)
	var wanted []hooks.AddonKind
	name := addonName(before)
	if ch.Op != changeset.OpDelete {
		if json.Unmarshal(ch.After, &after) != nil {
			return nil
		}
		man, err := hooks.ParseAddonManifest([]byte(after.Manifest))
		if err != nil {
			// The kind's own validation says what is wrong with it.
			return nil
		}
		inst, name, wanted = Instance(ch.Kind, after.ID, ch.Scope), man.Name, man.Kinds
	}
	// What this add-on adds now may change; what others add may not be
	// taken.
	if err := r.checkTaken(inst, name, wanted); err != nil {
		return err
	}
	_, _, _, _, running := r.Running(inst)
	if ch.Op == changeset.OpDelete && !running {
		return nil
	}
	keep := map[string]string{}
	for _, d := range wanted {
		keep[d.Name] = d.Prefix
	}
	// The change set as it would leave the store: resources it deletes
	// before this change don't count.
	rd, ok := store.ReaderFrom(ctx)
	if !ok {
		rd = r.opts.Store
	}
	for _, k := range r.kindsAddedBy(inst) {
		if prefix, kept := keep[k.info.Name]; kept && prefix == k.info.Prefix {
			continue
		}
		left, err := r.resourcesLeft(ctx, rd, k.info.Name)
		if err != nil {
			return err
		}
		if !left {
			continue
		}
		if ch.Op == changeset.OpDelete {
			return ErrKindRemove.New("addon", name, "kind", k.info.Title)
		}
		return ErrKindInUse.New("addon", name, "what", "changed to a version that drops "+k.info.Name+" or changes its prefix", "kind", k.info.Title)
	}
	return nil
}

// resourcesLeft reports whether resources of a kind would be left without
// the add-on that plans them: any the change set leaves, and any still
// being deleted from before, which only the add-on can finish. Those the
// change set itself deletes, ahead of the add-on, are its to finish first.
func (r *Runtime) resourcesLeft(ctx context.Context, planned store.Reader, kind string) (bool, error) {
	page, err := planned.List(ctx, store.Query{Kind: kind, Limit: store.MaxLimit})
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(page.Items, func(res *store.Resource) bool { return !res.Deleted() }) {
		return true, nil
	}
	stored, err := r.opts.Store.List(ctx, store.Query{Kind: kind, IncludeDeleted: true, Limit: store.MaxLimit})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(stored.Items, func(res *store.Resource) bool { return res.Deleted() }), nil
}

// checkTaken refuses kinds that another add-on, a module or the panel
// itself has already: the same name or prefix, or names in a namespace
// someone else uses or keeps, as every module keeps its id.
func (r *Runtime) checkTaken(inst, name string, wanted []hooks.AddonKind) error {
	for _, d := range wanted {
		taken := func(owner, what string) error {
			return ErrKindTaken.New("addon", name, "kind", d.Name, "owner", owner, "taken", what)
		}
		if owner := r.namespaceOwner(d.Namespace()); owner != "" {
			return taken(owner, "the kinds named "+d.Namespace()+".*")
		}
		for _, k := range r.opts.Kinds.All() {
			info := k.Info()
			mine := false
			if ak, ok := k.(*Kind); ok && ak.Addon == inst {
				mine = true
			}
			if mine {
				continue
			}
			owner := "the panel"
			if ak, ok := k.(*Kind); ok {
				owner = ak.AddonName
			} else if info.Module() != "core" {
				owner = "the " + info.Module() + " module"
			}
			switch {
			case info.Name == d.Name:
				return taken(owner, "a kind called "+d.Name)
			case info.Prefix == d.Prefix:
				return taken(owner, "a kind whose IDs start with "+d.Prefix+"_")
			case info.Module() == d.Namespace():
				return taken(owner, "the kinds named "+d.Namespace()+".*")
			}
		}
	}
	return nil
}
