package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Point names where a hook attaches, such as web.site.create.validate.
type Point string

// Kind is what a hook may do.
type Kind string

// Hook kinds, from least to most powerful.
const (
	// Observe reacts after something happened and can't block.
	Observe Kind = "observe"
	// Validate allows or denies a change with a reason.
	Validate Kind = "validate"
	// Modify adds to or changes the change set before the preview.
	Modify Kind = "modify"
	// System patches generated service configuration at plan time.
	System Kind = "system"
	// Provide implements a capability behind a standard interface.
	Provide Kind = "provide"
	// Extend adds API routes, screens, tabs, cards and settings.
	Extend Kind = "extend"
)

// FailureMode says what happens when a synchronous hook errors or times out.
type FailureMode string

// Failure modes.
const (
	// Block denies the change. It is the default for security policies.
	Block FailureMode = "block"
	// Allow lets the change through.
	Allow FailureMode = "allow"
)

// Deadlines for synchronous hooks.
const (
	DefaultDeadline = 2 * time.Second
	MaxDeadline     = 10 * time.Second
)

// Errors returned by hooks.
var (
	ErrDenied      = i18n.Define("hooks.denied", http.StatusForbidden, "{hook} stopped this change: {reason}")
	ErrHookFailed  = i18n.Define("hooks.failed", http.StatusServiceUnavailable, "{hook} didn't answer in time or failed, and it blocks changes when that happens.")
	ErrUnknownHook = i18n.Define("hooks.unknown_point", http.StatusUnprocessableEntity, "There is no hook point called {point}.")
	// ErrOverstep is how a modify hook fails when it changed more than the
	// specs of the changes its point is about, the changes it added and the
	// effects it added.
	ErrOverstep = i18n.Define("hooks.overstep", http.StatusUnprocessableEntity, "The hook changed {what}, which hooks can't change.")
)

// KindPoint returns the hook point of an operation on a kind at a stage.
func KindPoint(kind, operation string, stage changeset.Stage) Point {
	return Point(changeset.HookPoint(kind, operation, stage))
}

// KindPoints lists the hook points of every operation and stage of a kind.
func KindPoints(k store.Kind) []Point {
	info := k.Info()
	ops := []string{"create", "update", "delete"}
	for _, a := range info.Actions {
		ops = append(ops, a.Name)
	}
	var out []Point
	for _, op := range ops {
		for _, st := range []changeset.Stage{changeset.StageValidate, changeset.StagePlan, changeset.StageApplied} {
			out = append(out, KindPoint(info.Name, op, st))
		}
	}
	return out
}

// Lifecycle and identity points. The code that reaches each one runs it
// with Runner.Check (validate hooks) or Runner.Notify (observe hooks).
const (
	CoreBoot               Point = "core.boot"
	CoreUpdateBefore       Point = "core.update.before"
	CoreUpdateAfter        Point = "core.update.after"
	BackupsRunBefore       Point = "backups.run.before"
	BackupsRunAfter        Point = "backups.run.after"
	MigrationCutoverBefore Point = "migration.cutover.before"
	MigrationCutoverAfter  Point = "migration.cutover.after"
	AuthLoginValidate      Point = "auth.login.validate"
)

// StaticPoints are the hook points that don't come from kinds.
var StaticPoints = map[Point]string{
	CoreBoot:               "Racksail has started.",
	CoreUpdateBefore:       "Racksail is about to update itself.",
	CoreUpdateAfter:        "Racksail has updated itself.",
	BackupsRunBefore:       "A backup is about to run.",
	BackupsRunAfter:        "A backup has finished.",
	MigrationCutoverBefore: "A migration is about to switch over.",
	MigrationCutoverAfter:  "A migration has switched over.",
	AuthLoginValidate:      "Someone is signing in; login policies can refuse it.",
	AuthProvider:           "External identity providers, such as LDAP or OIDC.",
	AuthMFA:                "Second-factor methods.",
	ProvideDNS:             "External DNS providers.",
	ProvideRegistrar:       "Domain registrars.",
	ProvideBackupTarget:    "Backup destinations.",
	ProvideNotifier:        "Notification channels.",
	ProvideAI:              "Assistant model providers.",
	ProvideBilling:         "Billing systems.",
	ProvideImporter:        "Migration importers.",
	ExtendNav:              "Navigation entries added by add-ons.",
	ExtendTab:              "Tabs added by add-ons to a kind's detail screen.",
	ExtendCard:             "Home cards added by add-ons.",
	ExtendCommand:          "Command-bar actions added by add-ons.",
	ExtendSettings:         "Settings pages added by add-ons.",
	ExtendColumn:           "Table columns added by add-ons to a kind's list.",
	ExtendRoutes:           "API routes add-ons serve under /api/v1/addons/<id>/.",
}

// ModuleOn is the point the module manager notifies after a module has
// been turned on.
func ModuleOn(id string) Point { return Point("modules." + id + ".on") }

// ModuleOff is the point the module manager notifies after a module has
// been turned off.
func ModuleOff(id string) Point { return Point("modules." + id + ".off") }

// ModulePoints returns the lifecycle points of a module.
func ModulePoints(id string) []Point {
	return []Point{ModuleOn(id), ModuleOff(id)}
}

var configPointPattern = regexp.MustCompile(`^system\.[a-z0-9-]+\.[a-z0-9-]+$`)

// Context is what validate, modify and observe hooks see. Each hook gets
// its own copy of the change set and the actor.
type Context struct {
	Point Point
	Actor *auth.Actor
	// ChangeSet is the change set at the points of the change pipeline. A
	// modify hook may change the specs (After) of the changes in Changes, add
	// changes and add effects. Anything else it changes makes the hook fail,
	// and its work is thrown away. Validate and observe hooks can't change
	// anything.
	ChangeSet *changeset.ChangeSet
	// Change is the change a validate hook decides on, in ChangeSet.
	Change *changeset.ResourceChange
	// Changes are the indexes in ChangeSet.Changes of the changes a modify
	// or observe hook's point is about. Other changes of the same change set
	// can be read but belong to other points.
	Changes []int
	// Data describes what happened at lifecycle and identity points, such
	// as the module turned on at modules.<id>.on.
	Data any
}

// ConfigPatch is a structured change to generated service configuration,
// offered to system hooks at system.<service>.config points.
type ConfigPatch struct {
	Service string `json:"service"`
	// Config is the service's configuration as a structured document.
	Config map[string]any `json:"config"`
}

// Registration attaches one handler to one hook point. Exactly one of the
// handler fields is set, the one matching Kind: OnValidate, OnModify,
// OnObserve, OnConfig (system), Provider (provide) or Extension (extend).
type Registration struct {
	Point Point
	Kind  Kind
	// Owner identifies who registered it: "module:<id>", "addon:<id>" or
	// "rule:<id>".
	Owner    string
	Priority int
	Deadline time.Duration
	Failure  FailureMode
	// Security marks validate hooks that enforce a security policy; they
	// block by default when they fail.
	Security bool

	OnValidate func(ctx context.Context, c *Context) error
	OnModify   func(ctx context.Context, c *Context) error
	OnObserve  func(ctx context.Context, c *Context)
	OnConfig   func(ctx context.Context, p *ConfigPatch) error
	Provider   *Provider
	Extension  *Extension
}

func (r *Registration) check() error {
	if r.Owner == "" || (!strings.HasPrefix(r.Owner, "module:") && !strings.HasPrefix(r.Owner, "addon:") && !strings.HasPrefix(r.Owner, "rule:")) {
		return fmt.Errorf("hook owner %q must be module:<id>, addon:<id> or rule:<id>", r.Owner)
	}
	switch r.Kind {
	case Provide:
		return r.Provider.check(r.Point, r.Owner)
	case Extend:
		return r.Extension.check(r.Point, r.Owner)
	}
	if r.Deadline == 0 {
		r.Deadline = DefaultDeadline
	}
	if r.Deadline < 0 || r.Deadline > MaxDeadline {
		return fmt.Errorf("hook %s on %s: deadline must be at most %s", r.Owner, r.Point, MaxDeadline)
	}
	if r.Failure == "" {
		r.Failure = Allow
		if r.Kind == Validate && r.Security {
			r.Failure = Block
		}
	}
	var handler bool
	switch r.Kind {
	case Validate:
		handler = r.OnValidate != nil
	case Modify:
		handler = r.OnModify != nil
	case Observe:
		handler = r.OnObserve != nil
	case System:
		handler = r.OnConfig != nil && configPointPattern.MatchString(string(r.Point))
	default:
		return fmt.Errorf("hook %s: unknown hook kind %q", r.Owner, r.Kind)
	}
	if !handler {
		return fmt.Errorf("hook %s on %s: no %s handler, or the point doesn't take one", r.Owner, r.Point, r.Kind)
	}
	return nil
}

// Registry holds hook points and the registrations attached to them.
type Registry struct {
	mu       sync.RWMutex
	points   map[Point]string
	regs     map[Point][]Registration
	failures map[string]int
	disabled map[string]bool
}

// NewRegistry returns a registry with the static points.
func NewRegistry() *Registry {
	r := &Registry{points: map[Point]string{}, regs: map[Point][]Registration{}, failures: map[string]int{}, disabled: map[string]bool{}}
	for p, d := range StaticPoints {
		r.points[p] = d
	}
	return r
}

// DeclareKind adds the hook points of a kind.
func (r *Registry) DeclareKind(k store.Kind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range KindPoints(k) {
		r.points[p] = k.Info().Title + " hook point."
	}
}

// Declare adds a hook point a module offers beyond the generated ones, such
// as system.caddy.config.
func (r *Registry) Declare(p Point, description string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.points[p] = description
}

// Points lists every declared hook point, sorted.
func (r *Registry) Points() []Point {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Point, 0, len(r.points))
	for p := range r.points {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Known reports whether a point is declared.
func (r *Registry) Known(p Point) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.points[p]
	return ok
}

// Register attaches a hook. The point must be declared.
func (r *Registry) Register(reg Registration) error {
	if err := reg.check(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.points[reg.Point]; !ok {
		return ErrUnknownHook.New("point", string(reg.Point))
	}
	if reg.Kind == Provide {
		for _, other := range r.regs[reg.Point] {
			if other.Kind == Provide && other.Provider.Name == reg.Provider.Name {
				return fmt.Errorf("hook %s: %s already has a provider called %s, from %s", reg.Owner, reg.Point, reg.Provider.Name, other.Owner)
			}
		}
	}
	list := append(slices.Clone(r.regs[reg.Point]), reg)
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority < list[j].Priority
		}
		return list[i].Owner < list[j].Owner
	})
	r.regs[reg.Point] = list
	return nil
}

// Unregister removes every hook an owner registered, as when an add-on is
// removed or safe mode switches add-ons off.
func (r *Registry) Unregister(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for p, list := range r.regs {
		kept := slices.DeleteFunc(slices.Clone(list), func(reg Registration) bool { return reg.Owner == owner })
		if len(kept) == 0 {
			delete(r.regs, p)
		} else {
			r.regs[p] = kept
		}
	}
	for key := range r.failures {
		if strings.HasPrefix(key, owner+"\x00") {
			delete(r.failures, key)
			delete(r.disabled, key)
		}
	}
}

// For returns the hooks of a kind attached to a point, in running order.
// Hooks switched off by the circuit breaker are left out.
func (r *Registry) For(p Point, kind Kind) []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Registration
	for _, reg := range r.regs[p] {
		if reg.Kind == kind && !r.disabled[breakerKey(reg)] {
			out = append(out, reg)
		}
	}
	return out
}

// BreakerThreshold is how many failures in a row switch a hook off.
const BreakerThreshold = 5

func breakerKey(reg Registration) string { return reg.Owner + "\x00" + string(reg.Point) }

// result records a hook's outcome for the circuit breaker and reports
// whether it was just switched off.
func (r *Registry) result(reg Registration, failed bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := breakerKey(reg)
	if !failed {
		r.failures[key] = 0
		return false
	}
	r.failures[key]++
	if r.failures[key] >= BreakerThreshold && !r.disabled[key] {
		r.disabled[key] = true
		return true
	}
	return false
}

// Reset switches a hook back on after an admin has looked at it.
func (r *Registry) Reset(owner string, p Point) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := owner + "\x00" + string(p)
	delete(r.disabled, key)
	r.failures[key] = 0
}

// Runner runs hooks for the change pipeline. It implements changeset.Hooks.
type Runner struct {
	Registry *Registry
	// Events receives a notice when a hook is switched off.
	Events events.Bus
	Log    *slog.Logger
}

var _ changeset.Hooks = (*Runner)(nil)

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

// call runs fn with the hook's deadline and turns panics into errors.
func call(ctx context.Context, deadline time.Duration, fn func(ctx context.Context) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("hook panicked: %v", p)
			}
		}()
		done <- fn(ctx)
	}()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settle records a hook's outcome. It returns an error only for a failure
// (not a denial) of a hook that blocks when it fails.
func (r *Runner) settle(ctx context.Context, reg Registration, err error, denial bool) error {
	failed := err != nil && !denial
	if r.Registry.result(reg, failed) {
		r.log().Warn("hook switched off after repeated failures", "owner", reg.Owner, "point", reg.Point)
		if r.Events != nil {
			if pubErr := r.Events.Publish(ctx, events.Event{
				Type: "core.hook.disabled", Severity: events.SeverityWarning, Scope: store.ServerScope(),
				Message: fmt.Sprintf("%s was switched off on %s after failing %d times in a row.", reg.Owner, reg.Point, BreakerThreshold),
			}); pubErr != nil {
				r.log().Warn("publishing an event", "error", pubErr)
			}
		}
	}
	if !failed {
		return nil
	}
	r.log().Warn("hook failed", "owner", reg.Owner, "point", reg.Point, "error", err)
	if reg.Failure == Block {
		return ErrHookFailed.Wrap(err, "hook", reg.Owner)
	}
	return nil
}

// Validate implements changeset.Hooks. Each hook decides on its own copy of
// the change set, so none of them can change it.
func (r *Runner) Validate(ctx context.Context, point string, cs *changeset.ChangeSet, change *changeset.ResourceChange) error {
	at := -1
	for i := range cs.Changes {
		if &cs.Changes[i] == change {
			at = i
		}
	}
	return r.validate(ctx, Point(point), func() *Context {
		c := &Context{Point: Point(point), ChangeSet: cs.Clone(), Actor: cs.Actor.Clone()}
		switch {
		case at >= 0:
			c.Change = &c.ChangeSet.Changes[at]
		case change != nil:
			copied := change.Clone()
			c.Change = &copied
		}
		return c
	})
}

// Check runs the validate hooks of a point outside the change pipeline, such
// as login policies at auth.login.validate. data is what the hooks decide
// on. An error means a hook refused, or failed and blocks when it fails.
func (r *Runner) Check(ctx context.Context, point Point, actor *auth.Actor, data any) error {
	return r.validate(ctx, point, func() *Context { return &Context{Point: point, Actor: actor.Clone(), Data: data} })
}

// validate runs the validate hooks of a point, giving each a context of its
// own from newContext.
func (r *Runner) validate(ctx context.Context, point Point, newContext func() *Context) error {
	for _, reg := range r.Registry.For(point, Validate) {
		hc := newContext()
		err := call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnValidate(ctx, hc) })
		var denial *i18n.Error
		isDenial := err != nil && errors.As(err, &denial) && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded)
		if err := r.settle(ctx, reg, err, isDenial); err != nil {
			return err
		}
		if isDenial {
			return ErrDenied.Wrap(err, "hook", reg.Owner, "reason", denial.Message)
		}
	}
	return nil
}

// Modify implements changeset.Hooks. Each hook works on its own copy of the
// change set and may change the specs of the changes its point is about, add
// changes and add effects. A hook that changed anything else has failed, as
// if it had returned an error: its work is thrown away and its failure mode
// decides whether the change goes ahead. What a hook did is labelled with
// its source, and two hooks changing the same field of the same resource are
// recorded as a conflict.
func (r *Runner) Modify(ctx context.Context, point string, cs *changeset.ChangeSet, changes []int) error {
	touched := map[string][]changeset.Source{}
	for _, reg := range r.Registry.For(Point(point), Modify) {
		orig, err := clone(cs)
		if err != nil {
			return err
		}
		work, err := clone(cs)
		if err != nil {
			return err
		}
		hc := &Context{Point: Point(point), ChangeSet: work, Changes: slices.Clone(changes), Actor: cs.Actor.Clone()}
		err = call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnModify(ctx, hc) })
		if err == nil {
			err = overstep(orig, work, changes)
		}
		if err := r.settle(ctx, reg, err, false); err != nil {
			return err
		}
		if err != nil {
			continue
		}
		ownerType, ownerName, _ := strings.Cut(reg.Owner, ":")
		source := changeset.Source{Type: ownerType, Name: ownerName, Hook: point}
		contribution := changeset.Contribution{Source: source, Summary: reg.Owner + " changed the plan"}
		for _, i := range changes {
			diffs, err := changeset.Diff(cs.Changes[i].After, work.Changes[i].After)
			if err != nil {
				return err
			}
			if len(diffs) == 0 {
				continue
			}
			for _, d := range diffs {
				key := string(cs.Changes[i].ID) + "\x00" + d.Path
				touched[key] = append(touched[key], source)
			}
			cs.Changes[i].After = work.Changes[i].After
			contribution.Changes = append(contribution.Changes, i)
		}
		for _, added := range work.Changes[len(cs.Changes):] {
			// The label says where the change came from, whatever the hook
			// wrote there.
			added.Source = source
			contribution.Changes = append(contribution.Changes, len(cs.Changes))
			cs.Changes = append(cs.Changes, added)
		}
		for _, eff := range work.Effects[len(cs.Effects):] {
			eff.Contributor = reg.Owner
			contribution.Effects = append(contribution.Effects, len(cs.Effects))
			cs.Effects = append(cs.Effects, eff)
		}
		if len(contribution.Changes) > 0 || len(contribution.Effects) > 0 {
			cs.Contributions = append(cs.Contributions, contribution)
		}
	}
	keys := make([]string, 0, len(touched))
	for k := range touched {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sources := touched[key]
		if len(sources) < 2 {
			continue
		}
		id, path, _ := strings.Cut(key, "\x00")
		cs.Conflicts = append(cs.Conflicts, changeset.Conflict{
			Resource: store.ID(id), Path: path, Sources: sources,
			Message: fmt.Sprintf("%d hooks changed %s", len(sources), path),
		})
	}
	return nil
}

// overstep reports what a modify hook changed that it may not: anything but
// the specs of the changes its point is about (mine) and what it appended to
// the changes and effects. orig and work are copies of the change set made
// the same way, so they compare field by field.
func overstep(orig, work *changeset.ChangeSet, mine []int) error {
	refuse := func(what string) error { return ErrOverstep.New("what", what) }
	if len(work.Changes) < len(orig.Changes) {
		return refuse("the list of changes, by taking changes out")
	}
	if len(work.Effects) < len(orig.Effects) || !slices.Equal(work.Effects[:len(orig.Effects)], orig.Effects) {
		return refuse("the effects planned before it ran")
	}
	for i, before := range orig.Changes {
		after := work.Changes[i]
		if !bytes.Equal(after.After, before.After) {
			if !slices.Contains(mine, i) {
				return refuse(fmt.Sprintf("the spec of change %d, which its point isn't about", i))
			}
			if !json.Valid(after.After) {
				return refuse(fmt.Sprintf("the spec of change %d into something that isn't JSON", i))
			}
		}
		if part := changeset.ChangedPart(before, after); part != "" {
			return refuse(fmt.Sprintf("the %s of change %d", part, i))
		}
	}
	rest := func(cs *changeset.ChangeSet) ([]byte, error) {
		c := *cs
		c.Changes, c.Effects = nil, nil
		return json.Marshal(c)
	}
	a, errA := rest(orig)
	b, errB := rest(work)
	if errA != nil || errB != nil || !bytes.Equal(a, b) {
		return refuse("the change set itself, such as its risk, contributions or conflicts")
	}
	return nil
}

// Applied implements changeset.Hooks. Observe hooks run in the background
// and can't affect the change.
func (r *Runner) Applied(ctx context.Context, point string, cs *changeset.ChangeSet, changes []int) {
	r.observe(ctx, &Context{Point: Point(point), ChangeSet: cs, Changes: changes, Actor: cs.Actor})
}

// Notify runs the observe hooks of a lifecycle point, such as core.boot or
// modules.<id>.on, in the background. data describes what happened.
func (r *Runner) Notify(ctx context.Context, point Point, actor *auth.Actor, data any) {
	r.observe(ctx, &Context{Point: point, Actor: actor, Data: data})
}

func (r *Runner) observe(ctx context.Context, hc *Context) {
	ctx = context.WithoutCancel(ctx)
	for _, reg := range r.Registry.For(hc.Point, Observe) {
		// Observers run side by side, so each gets its own copy.
		c := *hc
		c.ChangeSet, c.Changes, c.Actor = hc.ChangeSet.Clone(), slices.Clone(hc.Changes), hc.Actor.Clone()
		go func(reg Registration) {
			err := call(ctx, reg.Deadline, func(ctx context.Context) error {
				reg.OnObserve(ctx, &c)
				return nil
			})
			if err := r.settle(ctx, reg, err, false); err != nil {
				r.log().Warn("observe hook failed", "owner", reg.Owner, "error", err)
			}
		}(reg)
	}
}

// PatchConfig offers a configuration document to the system hooks of a
// service, in order. Each hook patches its own copy of the result of the one
// before, so a hook that fails or runs out of time leaves nothing behind,
// even if it goes on running.
func (r *Runner) PatchConfig(ctx context.Context, service, part string, config map[string]any) (map[string]any, error) {
	point := Point("system." + service + "." + part)
	for _, reg := range r.Registry.For(point, System) {
		patch := &ConfigPatch{Service: service, Config: copyConfig(config)}
		err := call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnConfig(ctx, patch) })
		if err := r.settle(ctx, reg, err, false); err != nil {
			return nil, err
		}
		if err == nil && patch.Config != nil {
			config = patch.Config
		}
	}
	return config, nil
}

// copyConfig copies a configuration document, its nested maps and lists
// included, keeping the types of its values.
func copyConfig(config map[string]any) map[string]any {
	if config == nil {
		return map[string]any{}
	}
	return copyValue(reflect.ValueOf(config)).Interface().(map[string]any)
}

func copyValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Type()).Elem()
		c.Set(copyValue(v.Elem()))
		return c
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		c := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			c.SetMapIndex(iter.Key(), copyValue(iter.Value()))
		}
		return c
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			c.Index(i).Set(copyValue(v.Index(i)))
		}
		return c
	}
	return v
}

// clone copies a change set for a modify hook, through JSON, so that nothing
// in the copy, not even the inputs of action steps, is shared with cs.
func clone(cs *changeset.ChangeSet) (*changeset.ChangeSet, error) {
	raw, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	var c changeset.ChangeSet
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	// Grants aren't serialized, so the actor is copied as it is.
	c.Actor = cs.Actor.Clone()
	return &c, nil
}
