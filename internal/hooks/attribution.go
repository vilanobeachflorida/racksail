package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// hookActor is who a hook acts as in the audit log: the add-on, rule,
// module or part of the core that registered it, for the person whose
// change it took part in.
func hookActor(owner string, person *auth.Actor) auth.Actor {
	typ, name, _ := strings.Cut(owner, ":")
	a := auth.Actor{Type: auth.ActorSystem, Name: owner, Source: auth.SourceSystem}
	if typ == OwnerAddon {
		a.Type, a.Source, a.Name = auth.ActorAddon, auth.SourceAddon, name
	}
	if person != nil {
		a.User = person.User
		if a.User == "" && person.Type == auth.ActorUser {
			a.User = person.ID
		}
	}
	return a
}

// contributionDetails is what the audit log keeps about a contribution.
type contributionDetails struct {
	Source  changeset.Source `json:"source"`
	Changes []contributed    `json:"changes,omitempty"`
	Effects []system.Effect  `json:"effects,omitempty"`
}

type contributed struct {
	Op   changeset.Op `json:"op"`
	Kind string       `json:"kind"`
	ID   store.ID     `json:"id"`
}

// attribute records in the audit log, once the change set is applied, what
// hooks contributed through the plan point that matches an applied point:
// the specs they rewrote, the changes and effects they added. The effects
// reconcilers labelled with a contributor, such as configuration patches,
// are recorded with the change set's first applied point.
func (r *Runner) attribute(ctx context.Context, point Point, cs *changeset.ChangeSet, changes []int) {
	if r.Audit == nil || cs == nil {
		return
	}
	kind, op, stage, ok := ParseKindPoint(point)
	if !ok || stage != string(changeset.StageApplied) {
		return
	}
	plan := string(KindPoint(kind, op, changeset.StagePlan))
	ctx = context.WithoutCancel(ctx)
	labelled := map[int]bool{}
	for _, con := range cs.Contributions {
		for _, i := range con.Effects {
			labelled[i] = true
		}
		if con.Source.Hook != plan {
			continue
		}
		details := contributionDetails{Source: con.Source}
		subject := store.Resource{}
		for _, i := range con.Changes {
			if i < 0 || i >= len(cs.Changes) {
				continue
			}
			c := cs.Changes[i]
			details.Changes = append(details.Changes, contributed{Op: c.Op, Kind: c.Kind, ID: c.ID})
			if subject.ID == "" {
				subject.Meta = store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope}
			}
		}
		for _, i := range con.Effects {
			if i >= 0 && i < len(cs.Effects) {
				details.Effects = append(details.Effects, cs.Effects[i])
			}
		}
		owner := con.Source.Type + ":" + con.Source.Name
		r.recordContribution(ctx, cs, owner, plan, subject, con.Summary, details)
	}
	// Effects labelled by reconcilers rather than by modify hooks, recorded
	// once, with the first point the change set reaches.
	if len(cs.Changes) == 0 || !slices.Contains(changes, 0) || string(point) != firstApplied(cs) {
		return
	}
	byOwner := map[string][]system.Effect{}
	for i, eff := range cs.Effects {
		if eff.Contributor != "" && !labelled[i] {
			byOwner[eff.Contributor] = append(byOwner[eff.Contributor], eff)
		}
	}
	owners := make([]string, 0, len(byOwner))
	for o := range byOwner {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		if !ownerPattern.MatchString(owner) {
			continue
		}
		first := cs.Changes[0]
		subject := store.Resource{Meta: store.Meta{ID: first.ID, Kind: first.Kind, Scope: first.Scope}}
		typ, name, _ := strings.Cut(owner, ":")
		details := contributionDetails{Source: changeset.Source{Type: typ, Name: name}, Effects: byOwner[owner]}
		summary := fmt.Sprintf("%s changed %d settings the change set applied", owner, len(byOwner[owner]))
		r.recordContribution(ctx, cs, owner, "", subject, summary, details)
	}
}

// firstApplied returns the applied point of a change set's first change.
func firstApplied(cs *changeset.ChangeSet) string {
	c := cs.Changes[0]
	op := string(c.Op)
	if c.Op == changeset.OpAction {
		op = c.Action
	}
	return changeset.HookPoint(c.Kind, op, changeset.StageApplied)
}

func (r *Runner) recordContribution(ctx context.Context, cs *changeset.ChangeSet, owner, plan string, subject store.Resource, summary string, details contributionDetails) {
	raw, _ := json.Marshal(details)
	action := "core.hook.contribute"
	if plan != "" {
		action = plan
	}
	scope := subject.Scope
	if scope.Level == "" {
		scope = cs.Owner()
	}
	rec := events.AuditRecord{
		Actor: hookActor(owner, cs.Actor), Action: action, Kind: subject.Kind, Subject: subject.ID, Scope: scope,
		ChangeSet: cs.ID, Outcome: events.OutcomeSuccess, Summary: summary + " in " + cs.Title, Details: raw,
	}
	if err := r.Audit.Record(ctx, rec); err != nil {
		r.log().Error("writing an audit record", "action", action, "error", err)
	}
}
