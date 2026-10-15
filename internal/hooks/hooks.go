package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
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

// StaticPoints are the hook points that don't come from kinds.
var StaticPoints = map[Point]string{
	"core.boot":                "Racksail has started.",
	"core.update.before":       "Racksail is about to update itself.",
	"core.update.after":        "Racksail has updated itself.",
	"backups.run.before":       "A backup is about to run.",
	"backups.run.after":        "A backup has finished.",
	"migration.cutover.before": "A migration is about to switch over.",
	"migration.cutover.after":  "A migration has switched over.",
	"auth.login.validate":      "Someone is signing in; login policies can refuse it.",
	"auth.provider":            "External identity providers, such as LDAP or OIDC.",
	"auth.mfa":                 "Second-factor methods.",
	"provide.dns":              "External DNS providers.",
	"provide.registrar":        "Domain registrars.",
	"provide.backup-target":    "Backup destinations.",
	"provide.notifier":         "Notification channels.",
	"provide.ai":               "Assistant model providers.",
	"provide.billing":          "Billing systems.",
	"provide.importer":         "Migration importers.",
}

// ModulePoints returns the lifecycle points of a module.
func ModulePoints(id string) []Point {
	return []Point{Point("modules." + id + ".on"), Point("modules." + id + ".off")}
}

var configPointPattern = regexp.MustCompile(`^system\.[a-z0-9-]+\.[a-z0-9-]+$`)

// Context is what validate and modify hooks see.
type Context struct {
	Point     Point
	ChangeSet *changeset.ChangeSet
	// Change is the change being validated; nil for modify hooks.
	Change *changeset.ResourceChange
	Actor  *auth.Actor
}

// ConfigPatch is a structured change to generated service configuration,
// offered to system hooks at system.<service>.config points.
type ConfigPatch struct {
	Service string `json:"service"`
	// Config is the service's configuration as a structured document.
	Config map[string]any `json:"config"`
}

// Registration attaches one handler to one hook point.
type Registration struct {
	Point Point
	Kind  Kind
	// Owner identifies who registered it: "module:<id>" or "addon:<id>".
	Owner    string
	Priority int
	Deadline time.Duration
	Failure  FailureMode
	// Security marks validate hooks that enforce a security policy; they
	// block by default when they fail.
	Security bool

	OnValidate func(ctx context.Context, c *Context) error
	OnModify   func(ctx context.Context, c *Context) error
	OnObserve  func(ctx context.Context, cs *changeset.ChangeSet)
	OnConfig   func(ctx context.Context, p *ConfigPatch) error
}

func (r *Registration) check() error {
	if r.Owner == "" || (!strings.HasPrefix(r.Owner, "module:") && !strings.HasPrefix(r.Owner, "addon:") && !strings.HasPrefix(r.Owner, "rule:")) {
		return fmt.Errorf("hook owner %q must be module:<id>, addon:<id> or rule:<id>", r.Owner)
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
	case Provide, Extend:
		return fmt.Errorf("hook %s: %s hooks are registered with their capability, not on a point", r.Owner, r.Kind)
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
	list := append(r.regs[reg.Point], reg)
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority < list[j].Priority
		}
		return list[i].Owner < list[j].Owner
	})
	r.regs[reg.Point] = list
	return nil
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

// Validate implements changeset.Hooks.
func (r *Runner) Validate(ctx context.Context, point string, cs *changeset.ChangeSet, change *changeset.ResourceChange) error {
	for _, reg := range r.Registry.For(Point(point), Validate) {
		hc := &Context{Point: Point(point), ChangeSet: cs, Change: change, Actor: cs.Actor}
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

// Modify implements changeset.Hooks. Each hook works on a copy; what it
// changed is labelled with its source, and two hooks changing the same field
// of the same resource are recorded as a conflict.
func (r *Runner) Modify(ctx context.Context, point string, cs *changeset.ChangeSet) error {
	touched := map[string][]changeset.Source{}
	for _, reg := range r.Registry.For(Point(point), Modify) {
		work, err := clone(cs)
		if err != nil {
			return err
		}
		hc := &Context{Point: Point(point), ChangeSet: work, Actor: cs.Actor}
		err = call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnModify(ctx, hc) })
		if err := r.settle(ctx, reg, err, false); err != nil {
			return err
		}
		if err != nil {
			continue
		}
		ownerType, ownerName, _ := strings.Cut(reg.Owner, ":")
		source := changeset.Source{Type: ownerType, Name: ownerName, Hook: point}
		contribution := changeset.Contribution{Source: source, Summary: reg.Owner + " changed the plan"}
		for i := range work.Changes {
			if i >= len(cs.Changes) {
				work.Changes[i].Source = source
				contribution.Changes = append(contribution.Changes, i)
				continue
			}
			diffs, err := changeset.Diff(cs.Changes[i].After, work.Changes[i].After)
			if err != nil {
				return err
			}
			for _, d := range diffs {
				key := string(work.Changes[i].ID) + "\x00" + d.Path
				touched[key] = append(touched[key], source)
			}
			if len(diffs) > 0 {
				contribution.Changes = append(contribution.Changes, i)
			}
		}
		for i := len(cs.Effects); i < len(work.Effects); i++ {
			if work.Effects[i].Contributor == "" {
				work.Effects[i].Contributor = reg.Owner
			}
			contribution.Effects = append(contribution.Effects, i)
		}
		if len(contribution.Changes) > 0 || len(contribution.Effects) > 0 {
			work.Contributions = append(work.Contributions, contribution)
		}
		*cs = *work
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

// Applied implements changeset.Hooks. Observe hooks run in the background
// and can't affect the change.
func (r *Runner) Applied(ctx context.Context, point string, cs *changeset.ChangeSet) {
	for _, reg := range r.Registry.For(Point(point), Observe) {
		go func(reg Registration) {
			err := call(context.WithoutCancel(ctx), reg.Deadline, func(ctx context.Context) error {
				reg.OnObserve(ctx, cs)
				return nil
			})
			if err := r.settle(context.WithoutCancel(ctx), reg, err, false); err != nil {
				r.log().Warn("observe hook failed", "owner", reg.Owner, "error", err)
			}
		}(reg)
	}
}

// PatchConfig offers a configuration document to the system hooks of a
// service, in order. Each patch is applied on top of the previous one.
func (r *Runner) PatchConfig(ctx context.Context, service, part string, config map[string]any) (map[string]any, error) {
	point := Point("system." + service + "." + part)
	patch := &ConfigPatch{Service: service, Config: config}
	for _, reg := range r.Registry.For(point, System) {
		err := call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnConfig(ctx, patch) })
		if err := r.settle(ctx, reg, err, false); err != nil {
			return nil, err
		}
	}
	return patch.Config, nil
}

func clone(cs *changeset.ChangeSet) (*changeset.ChangeSet, error) {
	raw, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	var c changeset.ChangeSet
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	// Grants aren't serialized, so the actor is carried over as it is.
	c.Actor = cs.Actor
	return &c, nil
}
