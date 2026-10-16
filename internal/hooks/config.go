package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Errors of configuration patches.
var (
	// ErrInvariant refuses a patch that loosens a protected setting without
	// core.override.
	ErrInvariant = i18n.Define("hooks.invariant", http.StatusForbidden, "{hook} tried to loosen {setting}, which only a hook with core.override may do.")
	// ErrConfigCheck refuses a patch the service's own configuration test
	// doesn't accept.
	ErrConfigCheck = i18n.Define("hooks.config_check", http.StatusUnprocessableEntity, "The configuration {hook} patched doesn't pass {service}'s own check: {reason}.")
	// ErrRolledBack reports a configuration that was put back after the
	// service didn't accept the new one or stopped working with it.
	ErrRolledBack = i18n.Define("hooks.rolled_back", http.StatusUnprocessableEntity, "{service} didn't work with the new configuration, so the previous one was put back: {reason}.")
)

// ConfigService describes generated configuration that system hooks may
// patch at its point: the service's own configuration test, which every
// patched document has to pass before it is kept, and the invariants hooks
// can only make stricter. Modules declare one for every
// system.<service>.<part> point they offer.
type ConfigService struct {
	// Point is system.<service>.<part>, such as system.caddy.config.
	Point       Point
	Description string
	// Check is the service's own configuration test, run on each patched
	// document: the module renders the document and has the service read it,
	// so a patch the service wouldn't load never reaches it. Nil skips it.
	Check func(ctx context.Context, doc map[string]any) error
	// Invariants are settings hooks may make stricter but only loosen with
	// core.override, such as the minimum sandbox of a service.
	Invariants []Invariant
}

// Invariant is a setting of a configuration document that hooks may make
// stricter, but only loosen when they hold core.override.
type Invariant struct {
	// Path locates the setting, with dots between keys, such as
	// Service.NoNewPrivileges.
	Path        string
	Description string
	// Looser reports whether after is weaker than before. A setting that
	// isn't there is nil.
	Looser func(before, after any) bool
}

// KeepTrue protects a setting that is on: turning it off, or taking it
// away, loosens it. Settings may be true or the strings yes, on and 1.
func KeepTrue(path, description string) Invariant {
	return Invariant{Path: path, Description: description, Looser: func(before, after any) bool {
		return truthy(before) && !truthy(after)
	}}
}

// KeepValue protects a setting from any change: hooks can't make it
// stricter either, because nothing is stricter than what Racksail sets.
func KeepValue(path, description string) Invariant {
	return Invariant{Path: path, Description: description, Looser: func(before, after any) bool {
		return !reflect.DeepEqual(normalize(before), normalize(after))
	}}
}

// AtMost protects a limit: raising it, or taking it away, loosens it.
func AtMost(path, description string) Invariant {
	return Invariant{Path: path, Description: description, Looser: func(before, after any) bool {
		b, okB := number(before)
		if !okB {
			return false
		}
		a, okA := number(after)
		return !okA || a > b
	}}
}

// AtLeast protects a minimum: lowering it, or taking it away, loosens it.
func AtLeast(path, description string) Invariant {
	return Invariant{Path: path, Description: description, Looser: func(before, after any) bool {
		b, okB := number(before)
		if !okB {
			return false
		}
		a, okA := number(after)
		return !okA || a < b
	}}
}

// KeepItems protects a list of restrictions, such as denied addresses or
// filtered system calls: taking any of them away loosens it, adding more
// makes it stricter.
func KeepItems(path, description string) Invariant {
	return Invariant{Path: path, Description: description, Looser: func(before, after any) bool {
		have := items(after)
		for _, want := range items(before) {
			if !slices.Contains(have, want) {
				return true
			}
		}
		return false
	}}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "yes", "true", "on", "1":
			return true
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func items(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case []string:
		return x
	case string:
		return strings.Fields(x)
	}
	return nil
}

// normalize makes values that differ only in their Go types compare equal.
func normalize(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return v
	}
	return out
}

// lookup returns the value at a dotted path of a document, or nil.
func lookup(doc map[string]any, path string) any {
	var cur any = doc
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

// DeclareConfig declares a system.<service>.<part> point with what its
// patches are checked against.
func (r *Registry) DeclareConfig(svc ConfigService) error {
	if !IsConfigPoint(svc.Point) {
		return fmt.Errorf("hooks: %q isn't a system.<service>.<part> point", svc.Point)
	}
	for _, inv := range svc.Invariants {
		if inv.Path == "" || inv.Looser == nil {
			return fmt.Errorf("hooks: an invariant of %s needs a path and a comparison", svc.Point)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if svc.Description == "" {
		svc.Description = r.points[svc.Point]
	}
	r.points[svc.Point] = svc.Description
	r.configs[svc.Point] = svc
	return nil
}

// ConfigService returns what a configuration point's patches are checked
// against.
func (r *Registry) ConfigService(p Point) (ConfigService, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svc, ok := r.configs[p]
	return svc, ok
}

// PatchContribution is one system hook's patch that was kept.
type PatchContribution struct {
	Owner string `json:"owner"`
	Label string `json:"label,omitempty"`
	// Changed lists the settings the patch changed.
	Changed []string `json:"changed"`
	// Loosened lists the invariants it loosened, which it could only do
	// with core.override.
	Loosened []string `json:"loosened,omitempty"`
}

// PatchRefusal is a patch that was thrown away, with why.
type PatchRefusal struct {
	Owner  string    `json:"owner"`
	Label  string    `json:"label,omitempty"`
	Code   i18n.Code `json:"code"`
	Reason string    `json:"reason"`
}

// PatchResult is a configuration document after the system hooks of its
// point patched it.
type PatchResult struct {
	Point  Point          `json:"point"`
	Config map[string]any `json:"config"`
	// Applied are the patches that were kept, in the order they ran.
	Applied []PatchContribution `json:"applied,omitempty"`
	// Refused are the patches thrown away because they failed, ran out of
	// time, loosened an invariant without core.override or didn't pass the
	// service's check. The document is as it was before each of them.
	Refused []PatchRefusal `json:"refused,omitempty"`
}

// Effects labels a file written from the patched document with every hook
// whose patch it carries, for the preview: one write effect per
// contributor, the core's own write coming from the reconciler's steps.
func (pr *PatchResult) Effects(target string) []system.Effect {
	var out []system.Effect
	for _, c := range pr.Applied {
		detail := "patched " + strings.Join(c.Changed, ", ")
		if len(c.Loosened) > 0 {
			detail += "; loosened with core.override: " + strings.Join(c.Loosened, ", ")
		}
		out = append(out, system.Effect{Type: system.EffectFile, Action: "patch", Target: target, Detail: detail, Contributor: c.Owner})
	}
	return out
}

// PatchConfig offers a configuration document to the system hooks of a
// service, in order. Each hook patches its own copy of the result of the
// one before, so a hook that fails or runs out of time leaves nothing
// behind, even if it goes on running. A patch is only kept when it loosens
// none of the point's invariants, unless the hook holds core.override, and
// when the patched document passes the service's own check; otherwise it is
// rolled back and counts as a failure of the hook. A hook whose failure
// mode is block stops the change instead.
func (r *Runner) PatchConfig(ctx context.Context, service, part string, config map[string]any) (*PatchResult, error) {
	point := Point("system." + service + "." + part)
	svc, _ := r.Registry.ConfigService(point)
	result := &PatchResult{Point: point, Config: copyConfig(config)}
	for _, reg := range r.Registry.For(point, System) {
		patch := &ConfigPatch{Service: service, Config: copyConfig(result.Config)}
		err := call(ctx, reg.Deadline, func(ctx context.Context) error { return reg.OnConfig(ctx, patch) })
		var loosened []string
		if err == nil && patch.Config == nil {
			err = errors.New("the hook returned no configuration")
		}
		if err == nil {
			for _, inv := range svc.Invariants {
				if inv.Looser(lookup(result.Config, inv.Path), lookup(patch.Config, inv.Path)) {
					loosened = append(loosened, inv.Path)
				}
			}
			if len(loosened) > 0 && !reg.Override {
				err = ErrInvariant.New("hook", reg.Name(), "setting", loosened[0])
			}
		}
		if err == nil && svc.Check != nil {
			if checkErr := svc.Check(ctx, copyConfig(patch.Config)); checkErr != nil {
				err = ErrConfigCheck.Wrap(checkErr, "hook", reg.Name(), "service", service, "reason", i18n.From(checkErr).Message)
			}
		}
		if blocked := r.settle(ctx, reg, err, false); blocked != nil {
			return nil, blocked
		}
		if err != nil {
			e := i18n.From(err)
			reason := e.Message
			if e.Code == i18n.ErrInternal.Code {
				reason = err.Error()
			}
			result.Refused = append(result.Refused, PatchRefusal{Owner: reg.Owner, Label: reg.Label, Code: e.Code, Reason: reason})
			continue
		}
		changed, err := changedSettings(result.Config, patch.Config)
		if err != nil {
			return nil, err
		}
		if len(changed) == 0 {
			continue
		}
		result.Config = patch.Config
		result.Applied = append(result.Applied, PatchContribution{Owner: reg.Owner, Label: reg.Label, Changed: changed, Loosened: loosened})
		if len(loosened) > 0 {
			r.overridden(ctx, reg, point, loosened, patch.Config)
		}
	}
	return result, nil
}

// changedSettings lists the paths that differ between two documents.
func changedSettings(before, after map[string]any) ([]string, error) {
	a, err := json.Marshal(before)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(after)
	if err != nil {
		return nil, err
	}
	diffs, err := changeset.Diff(a, b)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Path)
	}
	return out, nil
}

// overridden logs and records a patch that loosened invariants with
// core.override. A patch that is planned again and again, as at every
// resync, is recorded once.
func (r *Runner) overridden(ctx context.Context, reg Registration, point Point, loosened []string, doc map[string]any) {
	raw, _ := json.Marshal(doc)
	sum := sha256.Sum256(append([]byte(reg.Owner+"\x00"+string(point)+"\x00"), raw...))
	key := hex.EncodeToString(sum[:])
	r.mu.Lock()
	if r.overrides == nil {
		r.overrides = map[string]bool{}
	}
	first := !r.overrides[key]
	r.overrides[key] = true
	r.mu.Unlock()
	if !first {
		return
	}
	message := fmt.Sprintf("%s loosened %s on %s with core.override", reg.Name(), strings.Join(loosened, ", "), point)
	r.log().Warn("a hook used core.override", "owner", reg.Owner, "point", point, "loosened", loosened)
	if r.Audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"point": point, "loosened": loosened})
	if err := r.Audit.Record(context.WithoutCancel(ctx), events.AuditRecord{
		Actor: hookActor(reg.Owner, nil), Action: string(auth.PermOverride), Scope: store.ServerScope(),
		Outcome: events.OutcomeSuccess, Summary: message, Details: details,
	}); err != nil {
		r.log().Error("writing an audit record", "action", auth.PermOverride, "error", err)
	}
}

// Rollout puts a new configuration file in place the way patched
// configuration needs: the service checks the file before it uses it, then
// picks it up, and its health checks run. If any of that fails, the
// previous file is put back and the service picks that up again, so a patch
// that breaks a service never stays.
type Rollout struct {
	// Service names the service for messages, such as Caddy.
	Service string
	// Write puts the new file in place. Restore puts the previous one back,
	// or removes the new one when there was none.
	Write   agent.Step
	Restore agent.Step
	// Test runs the service's own configuration test on what Write wrote;
	// it may be empty.
	Test []agent.Step
	// Reload has the service use the file, such as ReloadUnit.
	Reload []agent.Step
	// Checks confirm the service still works with it.
	Checks []reconcile.HealthCheck
}

// Steps are what applying the rollout runs when everything works, for a
// plan and its preview.
func (r Rollout) Steps() []agent.Step {
	steps := append([]agent.Step{r.Write}, r.Test...)
	return append(steps, r.Reload...)
}

// Apply runs the rollout: the steps with ex, then the checks with checker
// for the machine server. When a step or a check fails, it restores the
// previous file, has the service pick it up again and returns ErrRolledBack
// with what failed.
func (r Rollout) Apply(ctx context.Context, ex reconcile.Executor, checker reconcile.Checker, server store.ID) error {
	failure := func() error {
		if _, err := ex.Run(ctx, r.Steps()); err != nil {
			return err
		}
		for _, c := range r.Checks {
			if err := checker.Check(ctx, server, c); err != nil {
				return err
			}
		}
		return nil
	}()
	if failure == nil {
		return nil
	}
	// Putting the previous file back must happen even when ctx ended.
	restore := context.WithoutCancel(ctx)
	if _, err := ex.Run(restore, append([]agent.Step{r.Restore}, r.Reload...)); err != nil {
		return errors.Join(ErrRolledBack.Wrap(failure, "service", r.Service, "reason", i18n.From(failure).Message), fmt.Errorf("putting the previous configuration back: %w", err))
	}
	return ErrRolledBack.Wrap(failure, "service", r.Service, "reason", i18n.From(failure).Message)
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
