package changeset

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Errors about what modify hooks contributed.
var (
	ErrHookTampered       = i18n.Define("changeset.hook_tampered", http.StatusInternalServerError, "A hook changed {what}, which hooks can't change, so the change was stopped.")
	ErrBadContribution    = i18n.Define("changeset.bad_contribution", http.StatusUnprocessableEntity, "{source} added a change hooks can't add: {reason}.")
	ErrContributionDenied = i18n.Define("changeset.contribution_denied", http.StatusForbidden, "{source} added a change you aren't allowed to make: {reason}")
)

// ChangedPart names the first part of b, other than its spec and its diff,
// that differs from a, or returns "" when there is none. Modify hooks may
// only change the specs of the changes they're given; the hook runner and the
// engine both use it to hold them to that.
func ChangedPart(a, b ResourceChange) string {
	switch {
	case a.Op != b.Op:
		return "operation"
	case a.Action != b.Action:
		return "action"
	case a.Kind != b.Kind:
		return "kind"
	case a.ID != b.ID:
		return "resource"
	case a.Scope != b.Scope:
		return "scope"
	case a.Server != b.Server:
		return "server"
	case a.Revision != b.Revision:
		return "revision"
	case !maps.Equal(a.Labels, b.Labels):
		return "labels"
	case !maps.Equal(a.LabelsBefore, b.LabelsBefore):
		return "previous labels"
	case !sameJSON(a.Before, b.Before):
		return "previous spec"
	case !sameJSON(a.Input, b.Input):
		return "action input"
	case a.Source != b.Source:
		return "source"
	case !sameJSON(a.Steps, b.Steps):
		return "steps"
	case !sameJSON(a.Checks, b.Checks):
		return "checks"
	case !bytes.Equal(a.Output, b.Output):
		return "output"
	}
	return ""
}

// sameJSON reports whether two values encode to the same JSON document,
// whatever the order of object keys, so a step's typed input and the same
// input decoded into a map compare equal.
func sameJSON(a, b any) bool {
	da, errA := decodeAny(a)
	db, errB := decodeAny(b)
	return errA == nil && errB == nil && reflect.DeepEqual(da, db)
}

func decodeAny(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(raw, &out)
	return out, err
}

// contributor labels what a source contributed, such as "addon:cdn".
func contributor(s Source) string {
	if s.Name == "" {
		return s.Type
	}
	return s.Type + ":" + s.Name
}

// recheck makes sure modify hooks didn't get around the checks every change
// goes through. The requested changes must still be what was asked for, apart
// from their specs, and the effects planned for them must still be there. A
// change whose spec a hook rewrote, and every change a hook added, is
// authorized for the actor, validated, run past the validate hooks and diffed
// again. Requested changes are planned again from the first one a hook
// rewrote, and added changes are planned too, so the preview lists what the
// change set as the hooks left it does to the machine.
func (e *Engine) recheck(ctx context.Context, p *draft, actor *auth.Actor, requested []ResourceChange, effects []system.Effect) error {
	cs := p.cs
	if len(cs.Changes) < len(requested) {
		return ErrHookTampered.New("what", "the list of changes")
	}
	if len(cs.Effects) < len(effects) || !slices.Equal(cs.Effects[:len(effects)], effects) {
		return ErrHookTampered.New("what", "the planned effects")
	}
	// The planned state of a resource is the one its last change leaves.
	last := map[store.ID]int{}
	for i, c := range requested {
		last[c.ID] = i
	}
	rewritten := -1
	for i := range cs.Changes {
		c := &cs.Changes[i]
		var err error
		switch {
		case i >= len(requested):
			err = e.addContribution(ctx, p, actor, c)
		case ChangedPart(requested[i], *c) != "":
			return ErrHookTampered.New("what", fmt.Sprintf("the %s of change %d", ChangedPart(requested[i], *c), i))
		case !bytes.Equal(requested[i].After, c.After):
			err = e.recheckChange(ctx, p, actor, c, last[c.ID] == i)
			if rewritten < 0 {
				rewritten = i
			}
		}
		if err != nil {
			return atChange(err, i, len(cs.Changes))
		}
	}
	if rewritten >= 0 {
		return e.replan(ctx, p, rewritten, len(effects))
	}
	return nil
}

// replan plans the requested changes again from the one at first on, after
// modify hooks rewrote its spec: each against the state the change set
// leaves at that point, as they were planned to begin with, so the preview
// lists what the specs the hooks wrote do to the machine rather than what
// was asked for. base is where the requested changes' effects ended; what
// follows them, the effects hooks added and those of the changes they added,
// stays after them, and contributions keep pointing at their own.
func (e *Engine) replan(ctx context.Context, p *draft, first, base int) error {
	cs := p.cs
	view := store.NewOverlay(e.opts.Store, e.opts.Kinds)
	var effects []system.Effect
	for i := range p.planned {
		pc, c := &p.planned[i], cs.Changes[i]
		if i >= first {
			pc.state.Spec = bytes.Clone(c.After)
		}
		view.Put(pc.state)
		if i >= first {
			pc.plan = nil
			if reconciles(pc.rec, c) {
				planned, err := e.planEffects(ctx, p, view, pc.rec, pc.state)
				if err != nil {
					return atChange(err, i, len(cs.Changes))
				}
				pc.plan = planned
			}
		}
		effects = append(effects, pc.effects()...)
	}
	shift := len(effects) - base
	cs.Effects = append(effects, cs.Effects[base:]...)
	for i := range cs.Contributions {
		for j, k := range cs.Contributions[i].Effects {
			if k >= base {
				cs.Contributions[i].Effects[j] = k + shift
			}
		}
	}
	return nil
}

// recheckChange checks a requested change whose spec a modify hook rewrote.
// latest says whether it is the last change of its resource, whose state the
// rest of the plan sees.
func (e *Engine) recheckChange(ctx context.Context, p *draft, actor *auth.Actor, c *ResourceChange, latest bool) error {
	if c.Op == OpDelete {
		return ErrHookTampered.New("what", "the spec of a resource being deleted")
	}
	kind, ok := e.opts.Kinds.Get(c.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", c.Kind)
	}
	planned, err := p.view.Get(ctx, c.ID)
	if err != nil {
		return err
	}
	next := planned.Clone()
	next.Spec, next.Labels = c.After, maps.Clone(c.Labels)
	var old *store.Resource
	if c.Op != OpCreate {
		old = planned.Clone()
		old.Spec, old.Labels = c.Before, maps.Clone(c.LabelsBefore)
	}
	if err := e.recheckAccess(ctx, p, actor, c, kind, next, old); err != nil {
		return err
	}
	if latest {
		p.view.Put(next)
	}
	return nil
}

// addContribution checks a change a modify hook added: only creates,
// updates and deletes, with what it records about the resource taken from
// the planned state rather than from the hook.
func (e *Engine) addContribution(ctx context.Context, p *draft, actor *auth.Actor, c *ResourceChange) error {
	source := contributor(c.Source)
	bad := func(reason string) error { return ErrBadContribution.New("source", source, "reason", reason) }
	switch {
	case c.Source.Type == "" || c.Source == RequestSource:
		return bad("it doesn't say which hook it comes from")
	case c.Op != OpCreate && c.Op != OpUpdate && c.Op != OpDelete:
		return bad("hooks can add creates, updates and deletes, not actions")
	case c.Action != "" || len(c.Input) > 0 || len(c.Steps) > 0 || len(c.Checks) > 0:
		return bad("it carries an action's input, steps or checks")
	}
	kind, ok := e.opts.Kinds.Get(c.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", c.Kind)
	}
	info := kind.Info()
	risk := info.Risks.Update
	var old, next *store.Resource
	switch c.Op {
	case OpCreate:
		risk = info.Risks.Create
		if c.ID == "" {
			c.ID = store.NewID(info.Prefix)
		}
		if _, err := p.view.Get(ctx, c.ID); err == nil {
			return store.ErrExists.New("what", string(c.ID))
		} else if !store.ErrNotFound.Is(err) {
			return err
		}
		next = &store.Resource{
			Meta: store.Meta{ID: c.ID, Kind: info.Name, Scope: c.Scope, Server: c.Server, Labels: maps.Clone(c.Labels)},
			Spec: bytes.Clone(c.After),
		}
		if len(next.Spec) == 0 {
			next.Spec = json.RawMessage("{}")
		}
		if err := kind.Default(ctx, next); err != nil {
			return err
		}
		c.Before, c.LabelsBefore = nil, nil
	case OpUpdate, OpDelete:
		stored := !p.view.Planned(c.ID)
		r, err := p.view.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		if r.Kind != info.Name || r.Deleted() {
			return store.ErrNotFound.New("what", string(c.ID))
		}
		old, next = r, r.Clone()
		if c.Op == OpUpdate && c.Revision == 0 && stored {
			c.Revision = r.Revision
		}
		// The resource's own placement and previous state, whatever the hook
		// said, so authorization and undo see the truth.
		c.Scope, c.Server, c.Before, c.LabelsBefore = r.Scope, r.Server, r.Spec, nil
		if c.Op == OpUpdate {
			if c.After != nil {
				next.Spec = bytes.Clone(c.After)
			}
			if c.Labels != nil {
				next.Labels = maps.Clone(c.Labels)
			}
			c.LabelsBefore = r.Labels
		} else {
			risk = info.Risks.Delete
			now := e.opts.Now().UTC()
			next.DeletedAt = &now
		}
	}
	c.Kind, c.After, c.Labels = info.Name, next.Spec, next.Labels
	if err := e.recheckAccess(ctx, p, actor, c, kind, next, old); err != nil {
		return err
	}
	p.view.Put(next)
	if !risk.Valid() {
		risk = store.RiskMedium
	}
	p.cs.Risk = store.MaxRisk(p.cs.Risk, risk)
	rec, ok := e.reconciler(info.Name)
	if !ok {
		return nil
	}
	effects, err := e.planEffects(ctx, p, p.view, rec, next)
	if err != nil {
		return err
	}
	for i := range effects {
		effects[i].Contributor = source
	}
	p.cs.Effects = append(p.cs.Effects, effects...)
	return nil
}

// recheckAccess runs the checks of the validate stage for a change a hook
// contributed or rewrote: the authorizer, the kind's validation and the
// validate hooks. It then brings the change's diff up to date.
func (e *Engine) recheckAccess(ctx context.Context, p *draft, actor *auth.Actor, c *ResourceChange, kind store.Kind, next, old *store.Resource) (err error) {
	info := kind.Info()
	operation := string(c.Op)
	if c.Op == OpAction {
		operation = c.Action
	}
	perm := auth.KindPermission(info.Name, operation)
	target := auth.Target{Scope: next.Scope, Object: next.ID}
	if c.Op == OpCreate {
		target.Object = ""
	}
	if e.opts.Authorizer != nil {
		if err := e.opts.Authorizer.Authorize(ctx, actor, perm, target); err != nil {
			p.recorded = true
			e.audit(ctx, actor, string(perm), next, "", events.OutcomeDenied, err, "")
			if c.Source == RequestSource {
				return err
			}
			return ErrContributionDenied.Wrap(err, "source", contributor(c.Source), "reason", i18n.From(err).Message)
		}
	}
	// From here on the change is allowed, so whatever stops it is recorded.
	defer func() {
		if err != nil {
			e.refusal(ctx, p, actor, perm, next, err, "")
		}
	}()
	if c.Op != OpDelete {
		if err := validate(ctx, kind, next, old); err != nil {
			return err
		}
	}
	if e.opts.Hooks != nil {
		if err := e.opts.Hooks.Validate(ctx, HookPoint(info.Name, operation, StageValidate), p.cs, c); err != nil {
			return err
		}
	}
	c.Diff = nil
	if c.Op != OpDelete {
		diff, err := Diff(c.Before, c.After)
		if err != nil {
			return err
		}
		c.Diff = Redact(diff, store.SensitiveFields(info))
	}
	return nil
}
