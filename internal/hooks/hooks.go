package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// Kinds lists the hook kinds, from least to most powerful.
func Kinds() []Kind { return []Kind{Observe, Validate, Modify, System, Provide, Extend} }

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
	// ErrNotDisabled answers a request to switch on a hook the circuit
	// breaker didn't switch off.
	ErrNotDisabled = i18n.Define("hooks.not_disabled", http.StatusConflict, "{hook} on {point} isn't switched off.")
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
	// The Debian upgrade points surround an upgrade of the operating system
	// to its next major version, such as Debian 13 to 14. Validate hooks at
	// the check point can refuse to start it; observe hooks hear about the
	// rest.
	OSUpgradeCheck  Point = "core.os_upgrade.check"
	OSUpgradeBefore Point = "core.os_upgrade.before"
	OSUpgradeAfter  Point = "core.os_upgrade.after"
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
	OSUpgradeCheck:         "An upgrade of Debian to its next major version is being planned; hooks can refuse it.",
	OSUpgradeBefore:        "Debian is about to be upgraded to its next major version.",
	OSUpgradeAfter:         "Debian was upgraded to its next major version, or the upgrade was rolled back.",
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

// IsConfigPoint reports whether p is a system.<service>.<part> point,
// where system hooks patch generated configuration.
func IsConfigPoint(p Point) bool { return configPointPattern.MatchString(string(p)) }

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

// Owner types: who registered a hook.
const (
	OwnerModule = "module"
	OwnerAddon  = "addon"
	OwnerRule   = "rule"
	// OwnerCore is the core's own hooks, such as the module manager's
	// refusal to create resources of a module that is off.
	OwnerCore = "core"
)

// OwnerType returns the part of an owner before the colon, such as addon.
func OwnerType(owner string) string {
	t, _, _ := strings.Cut(owner, ":")
	return t
}

// Registration attaches one handler to one hook point. Exactly one of the
// handler fields is set, the one matching Kind: OnValidate, OnModify,
// OnObserve, OnConfig (system), Provider (provide) or Extension (extend).
type Registration struct {
	// Point is where the hook attaches. Validate, modify and observe hooks
	// may name several points with a pattern, where * stands for any part
	// of one segment: web.site.*.validate, or *.*.create.plan.
	Point Point
	Kind  Kind
	// Owner identifies who registered it: "module:<id>", "addon:<id>",
	// "rule:<id>" or "core:<part>".
	Owner    string
	Priority int
	Deadline time.Duration
	Failure  FailureMode
	// Security marks validate hooks that enforce a security policy; they
	// block by default when they fail.
	Security bool
	// Override says the hook holds core.override, which only a server admin
	// can grant: it may loosen the invariants of the configuration it
	// patches. Every time it does is logged and recorded in the audit log.
	// Rules never hold it.
	Override bool
	// Label names the hook for people, such as "Backup policy 1.2"; the
	// owner is used when it is empty.
	Label string

	OnValidate func(ctx context.Context, c *Context) error
	OnModify   func(ctx context.Context, c *Context) error
	OnObserve  func(ctx context.Context, c *Context)
	OnConfig   func(ctx context.Context, p *ConfigPatch) error
	Provider   *Provider
	Extension  *Extension
}

// Name is how the hook is shown to people.
func (r *Registration) Name() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Owner
}

var ownerPattern = regexp.MustCompile(`^(module|addon|rule|core):[a-z0-9][a-z0-9._-]*$`)

func (r *Registration) check() error {
	if !ownerPattern.MatchString(r.Owner) {
		return fmt.Errorf("hook owner %q must be module:<id>, addon:<id>, rule:<id> or core:<part>", r.Owner)
	}
	if r.Override && OwnerType(r.Owner) == OwnerRule {
		return fmt.Errorf("hook %s: rules can't hold core.override", r.Owner)
	}
	if IsPattern(r.Point) {
		if r.Kind != Validate && r.Kind != Modify && r.Kind != Observe {
			return fmt.Errorf("hook %s: only validate, modify and observe hooks can name points with a pattern, not %s hooks", r.Owner, r.Kind)
		}
		if !validPattern(r.Point) {
			return fmt.Errorf("hook %s: %q isn't a pattern of hook points", r.Owner, r.Point)
		}
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
	if r.Failure != Allow && r.Failure != Block {
		return fmt.Errorf("hook %s on %s: unknown failure mode %q", r.Owner, r.Point, r.Failure)
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
		handler = r.OnConfig != nil && IsConfigPoint(r.Point)
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
	patterns []Registration
	configs  map[Point]ConfigService
	breakers map[string]*breaker
	store    BreakerStore
	log      *slog.Logger
}

// breaker counts a hook's failures in a row, and says whether the circuit
// breaker switched it off.
type breaker struct {
	failures int
	disabled *DisabledHook
}

// NewRegistry returns a registry with the static points.
func NewRegistry() *Registry {
	r := &Registry{points: map[Point]string{}, regs: map[Point][]Registration{}, configs: map[Point]ConfigService{}, breakers: map[string]*breaker{}}
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

// Matching lists the declared points a pattern names, sorted. A point
// without wildcards names itself when it is declared.
func (r *Registry) Matching(pattern Point) []Point {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.matchingLocked(pattern)
}

func (r *Registry) matchingLocked(pattern Point) []Point {
	var out []Point
	if !IsPattern(pattern) {
		if _, ok := r.points[pattern]; ok {
			out = append(out, pattern)
		}
		return out
	}
	for p := range r.points {
		if MatchPoint(pattern, p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Register attaches a hook. The point must be declared; a pattern must name
// at least one declared point.
func (r *Registry) Register(reg Registration) error {
	if err := reg.check(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if IsPattern(reg.Point) {
		if len(r.matchingLocked(reg.Point)) == 0 {
			return ErrUnknownHook.New("point", string(reg.Point))
		}
		r.patterns = sortRegs(append(slices.Clone(r.patterns), reg))
		return nil
	}
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
	r.regs[reg.Point] = sortRegs(append(slices.Clone(r.regs[reg.Point]), reg))
	return nil
}

// sortRegs puts registrations in running order: priority, then owner, then
// the point they name, so the order never depends on when they registered.
func sortRegs(list []Registration) []Registration {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority < list[j].Priority
		}
		if list[i].Owner != list[j].Owner {
			return list[i].Owner < list[j].Owner
		}
		return list[i].Point < list[j].Point
	})
	return list
}

// Unregister removes every hook an owner registered, as when an add-on is
// removed, a rule is deleted or safe mode switches add-ons off. Whether the
// circuit breaker had switched any of them off is kept, so registering them
// again doesn't switch them back on.
func (r *Registry) Unregister(owner string) {
	r.UnregisterFunc(func(reg Registration) bool { return reg.Owner == owner })
}

// UnregisterFunc removes every hook for which drop returns true.
func (r *Registry) UnregisterFunc(drop func(Registration) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for p, list := range r.regs {
		kept := slices.DeleteFunc(slices.Clone(list), drop)
		if len(kept) == 0 {
			delete(r.regs, p)
		} else {
			r.regs[p] = kept
		}
	}
	r.patterns = slices.DeleteFunc(slices.Clone(r.patterns), drop)
}

// Owners lists the owners with hooks registered, sorted.
func (r *Registry) Owners() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]bool{}
	for _, list := range r.regs {
		for _, reg := range list {
			seen[reg.Owner] = true
		}
	}
	for _, reg := range r.patterns {
		seen[reg.Owner] = true
	}
	out := make([]string, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// For returns the hooks of a kind that run at a point, in running order:
// those attached to the point itself and those whose pattern names it.
// Hooks switched off by the circuit breaker are left out.
func (r *Registry) For(p Point, kind Kind) []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Registration
	for _, reg := range r.regs[p] {
		if reg.Kind == kind && !r.isDisabled(reg) {
			out = append(out, reg)
		}
	}
	matched := false
	for _, reg := range r.patterns {
		if reg.Kind == kind && MatchPoint(reg.Point, p) && !r.isDisabled(reg) {
			out = append(out, reg)
			matched = true
		}
	}
	if matched {
		out = sortRegs(out)
	}
	return out
}

func (r *Registry) isDisabled(reg Registration) bool {
	b := r.breakers[breakerKey(reg.Owner, reg.Point)]
	return b != nil && b.disabled != nil
}

// BreakerThreshold is how many failures in a row switch a hook off.
const BreakerThreshold = 5

func breakerKey(owner string, p Point) string { return owner + "\x00" + string(p) }

// result records a hook's outcome for the circuit breaker. It returns the
// record of the hook being switched off when this failure did it.
func (r *Registry) result(ctx context.Context, reg Registration, failure error) *DisabledHook {
	r.mu.Lock()
	key := breakerKey(reg.Owner, reg.Point)
	b := r.breakers[key]
	if b == nil {
		b = &breaker{}
		r.breakers[key] = b
	}
	if failure == nil {
		b.failures = 0
		r.mu.Unlock()
		return nil
	}
	b.failures++
	if b.failures < BreakerThreshold || b.disabled != nil {
		r.mu.Unlock()
		return nil
	}
	d := &DisabledHook{Owner: reg.Owner, Point: reg.Point, Failures: b.failures, Reason: i18n.From(failure).Message, At: time.Now().UTC()}
	if code := i18n.CodeOf(failure); code == "" || code == i18n.ErrInternal.Code {
		d.Reason = failure.Error()
	}
	b.disabled = d
	st, log := r.store, r.log
	r.mu.Unlock()
	if st != nil {
		if err := st.Disable(ctx, *d); err != nil && log != nil {
			log.Error("recording a hook the circuit breaker switched off", "owner", reg.Owner, "point", reg.Point, "error", err)
		}
	}
	return d
}

// Reset switches a hook back on after an admin has looked at it.
func (r *Registry) Reset(owner string, p Point) {
	_ = r.Enable(context.Background(), owner, p)
}

// Enable switches a hook the circuit breaker switched off back on, and
// forgets its failures.
func (r *Registry) Enable(ctx context.Context, owner string, p Point) error {
	r.mu.Lock()
	key := breakerKey(owner, p)
	b := r.breakers[key]
	if b == nil || b.disabled == nil {
		r.mu.Unlock()
		return ErrNotDisabled.New("hook", owner, "point", string(p))
	}
	delete(r.breakers, key)
	st := r.store
	r.mu.Unlock()
	if st != nil {
		return st.Enable(ctx, owner, p)
	}
	return nil
}

// Disabled lists the hooks the circuit breaker switched off, oldest first.
func (r *Registry) Disabled() []DisabledHook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []DisabledHook
	for _, b := range r.breakers {
		if b.disabled != nil {
			out = append(out, *b.disabled)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return breakerKey(out[i].Owner, out[i].Point) < breakerKey(out[j].Owner, out[j].Point)
	})
	return out
}

// Failures returns how many times in a row a hook has failed.
func (r *Registry) Failures(owner string, p Point) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if b := r.breakers[breakerKey(owner, p)]; b != nil {
		return b.failures
	}
	return 0
}

// UseBreakerStore keeps what the circuit breaker switches off in st, so a
// hook that was switched off stays off when the panel starts again, until
// an admin switches it back on. It loads what st already holds.
func (r *Registry) UseBreakerStore(ctx context.Context, st BreakerStore, log *slog.Logger) error {
	disabled, err := st.Disabled(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store, r.log = st, log
	for _, d := range disabled {
		d := d
		r.breakers[breakerKey(d.Owner, d.Point)] = &breaker{failures: d.Failures, disabled: &d}
	}
	return nil
}

// Runner runs hooks for the change pipeline. It implements changeset.Hooks.
type Runner struct {
	Registry *Registry
	// Events receives a notice when a hook is switched off.
	Events events.Bus
	// Audit records what hooks contributed to applied change sets, the
	// configuration patches that used core.override and the hooks the
	// circuit breaker switched off. It may be nil.
	Audit events.AuditLog
	Log   *slog.Logger

	mu sync.Mutex
	// overrides are the loosenings with core.override already recorded.
	overrides map[string]bool
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

// HookDisabledData is the data of core.hook.disabled events.
type HookDisabledData struct {
	Owner    string `json:"owner" doc:"Who registered the hook, such as addon:site-guard"`
	Point    Point  `json:"point" doc:"The hook point or pattern it was attached to"`
	Failures int    `json:"failures" doc:"How many times in a row it failed"`
	Reason   string `json:"reason" doc:"Why it failed the last time"`
}

// settle records a hook's outcome. It returns an error only for a failure
// (not a denial) of a hook that blocks when it fails.
func (r *Runner) settle(ctx context.Context, reg Registration, err error, denial bool) error {
	var failure error
	if err != nil && !denial {
		failure = err
	}
	if d := r.Registry.result(ctx, reg, failure); d != nil {
		r.switchedOff(ctx, reg, *d)
	}
	if failure == nil {
		return nil
	}
	r.log().Warn("hook failed", "owner", reg.Owner, "point", reg.Point, "error", err)
	if reg.Failure == Block {
		return ErrHookFailed.Wrap(err, "hook", reg.Name())
	}
	return nil
}

// switchedOff tells admins that the circuit breaker switched a hook off:
// a warning event, which notifications pass on to them, and an audit record.
func (r *Runner) switchedOff(ctx context.Context, reg Registration, d DisabledHook) {
	ctx = context.WithoutCancel(ctx)
	message := fmt.Sprintf("%s was switched off on %s after failing %d times in a row: %s", reg.Name(), reg.Point, d.Failures, d.Reason)
	r.log().Warn("hook switched off after repeated failures", "owner", reg.Owner, "point", reg.Point, "reason", d.Reason)
	if r.Events != nil {
		data, _ := json.Marshal(HookDisabledData{Owner: d.Owner, Point: d.Point, Failures: d.Failures, Reason: d.Reason})
		if pubErr := r.Events.Publish(ctx, events.Event{
			Type: events.HookDisabled.Name, Severity: events.HookDisabled.Severity, Scope: store.ServerScope(),
			Actor: auth.System("hooks"), Message: message, Data: data,
		}); pubErr != nil {
			r.log().Warn("publishing an event", "error", pubErr)
		}
	}
	if r.Audit != nil {
		if err := r.Audit.Record(ctx, events.AuditRecord{
			Actor: *auth.System("hooks"), Action: "core.hook.disable", Scope: store.ServerScope(),
			Outcome: events.OutcomeSuccess, Summary: message,
		}); err != nil {
			r.log().Error("writing an audit record", "action", "core.hook.disable", "error", err)
		}
	}
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
		hc.Point = point
		err := call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnValidate(ctx, hc) })
		var denial *i18n.Error
		isDenial := err != nil && errors.As(err, &denial) && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded)
		if err := r.settle(ctx, reg, err, isDenial); err != nil {
			return err
		}
		if isDenial {
			return ErrDenied.Wrap(err, "hook", reg.Name(), "reason", denial.Message)
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
		contribution := changeset.Contribution{Source: source, Summary: reg.Name() + " changed the plan"}
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
// and can't affect the change. It also records in the audit log what hooks
// contributed to the changes the point is about, attributed to them.
func (r *Runner) Applied(ctx context.Context, point string, cs *changeset.ChangeSet, changes []int) {
	r.attribute(ctx, Point(point), cs, changes)
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
