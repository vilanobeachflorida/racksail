package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Permissions of the core's own records. Everything about a resource is
// checked with its kind's permissions instead.
const (
	PermChangeSetRead auth.Permission = "core.changeset.read"
	PermJobRead       auth.Permission = "core.job.read"
	PermJobCancel     auth.Permission = "core.job.cancel"
	PermJobRetry      auth.Permission = "core.job.retry"
	PermEventRead     auth.Permission = "core.event.read"
	PermAuditRead     auth.Permission = "core.audit.read"
)

// sameParty reports whether two actors are, or act for, the same person.
func sameParty(a, b *auth.Actor) bool {
	p := jobs.Of(a)
	return p != "" && p == jobs.Of(b)
}

// canSeeChangeSet reports whether the signed-in actor may see a change set:
// one they made, any when they may read every change set, or one whose
// every resource they may read. Lists use it to leave out the others, so it
// records nothing.
func (s *Server) canSeeChangeSet(ctx context.Context, cs *changeset.ChangeSet) bool {
	actor, err := Actor(ctx)
	if err != nil {
		return false
	}
	if sameParty(actor, cs.Actor) {
		return true
	}
	authz := s.authorizer()
	if allowed(ctx, authz, PermChangeSetRead, auth.Target{Scope: store.ServerScope()}) == nil {
		return true
	}
	if len(cs.Changes) == 0 {
		return false
	}
	for _, c := range cs.Changes {
		if !canRead(ctx, authz, store.Meta{ID: c.ID, Kind: c.Kind, Scope: c.Scope}) {
			return false
		}
	}
	return true
}

// changeSetVisibility narrows a list to the change sets the signed-in actor
// may see, roughly: nil when they may read every change set or the query
// can't be narrowed, and otherwise their own and those whose resources are
// all in scopes or resources their grants reach. canSeeChangeSet decides
// about each one.
func (s *Server) changeSetVisibility(ctx context.Context) (*changeset.Visibility, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	authz := s.authorizer()
	if allowed(ctx, authz, PermChangeSetRead, auth.Target{Scope: store.ServerScope()}) == nil {
		return nil, nil
	}
	v := &changeset.Visibility{Person: jobs.Of(actor)}
	if authz == nil {
		return v, nil
	}
	if !narrowable(authz, actor) {
		// Every change set is read, and canSeeChangeSet decides.
		return nil, nil
	}
	// Grants of any permission count here, since reading a resource is a
	// permission of its kind; canSeeChangeSet checks each kind.
	r, err := grantScopes(ctx, authz, actor, "")
	if err != nil {
		return nil, err
	}
	if r.all {
		// A grant on the whole server may cover the resources of any change
		// set, so every one is read and canSeeChangeSet decides.
		return nil, nil
	}
	v.Scopes = r.scopes
	for _, g := range actor.Grants {
		if g.Object != "" && g.Role.Valid() {
			v.Objects = append(v.Objects, g.Object)
		}
	}
	return v, nil
}

// replay answers a retry of an undo or of applying a preview with the change
// set the first request with the key produced, before anything is read for
// it: by then the change set it was about may be gone, or the person may
// have to confirm it's them again, and the retry still gets the first
// answer. It returns nil when the key is new.
func (s *Server) replay(ctx context.Context, actor *auth.Actor, call changeset.Call, key string) (*ChangeSetOutput, error) {
	p := s.pipeline()
	if p == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	cs, err := p.Replay(ctx, actor, call, key)
	if err != nil || cs == nil {
		return nil, err
	}
	return changeSetAnswer(cs), nil
}

// changeSet reads a change set the signed-in actor may see. Others are
// reported as not found, and the refusal is recorded in the audit log.
func (s *Server) changeSet(ctx context.Context, id store.ID) (*changeset.ChangeSet, error) {
	p := s.pipeline()
	if p == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	cs, err := p.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.canSeeChangeSet(ctx, cs) {
		refused(ctx, PermChangeSetRead, "", cs.ID, cs.Owner(), i18n.ErrForbidden.New("action", string(PermChangeSetRead)))
		return nil, i18n.ErrNotFound.New("what", "Change "+string(id))
	}
	return cs, nil
}

type listChangeSetsInput struct {
	State    string   `query:"state" enum:"planned,applying,applied,failed,undone" doc:"Only change sets in this state"`
	Resource store.ID `query:"resource" doc:"Only change sets that touched this resource"`
	Actor    store.ID `query:"actor" doc:"Only change sets this user, token or automation made, or that were made for this user"`
	Previews bool     `query:"previews" doc:"List previews, dry runs that can still be applied, instead of applied change sets"`
	Cursor   string   `query:"cursor" doc:"Continues a list where the page before it ended"`
	Limit    int      `query:"limit" minimum:"0" maximum:"1000" doc:"How many to return; 50 when left out"`
}

type changeSetList struct {
	Body struct {
		ChangeSets []*changeset.ChangeSet `json:"changeSets" doc:"The change sets on this page, newest first"`
		Next       string                 `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type changeSetInput struct {
	ID store.ID `path:"id" doc:"The change set's ID, which starts with chg_"`
}

type changeSetOutput struct {
	Body *changeset.ChangeSet
}

type submitInput struct {
	ChangeParams
	Body struct {
		Title   string             `json:"title,omitempty" maxLength:"200" doc:"What the change set does; made up from the changes when left out"`
		Changes []changeset.Change `json:"changes" minItems:"1" maxItems:"500" doc:"The changes, planned in order: each sees what the ones before it create, change or delete"`
	}
}

type applyInput struct {
	ID             store.ID `path:"id" doc:"The preview's ID"`
	DryRun         bool     `query:"dryRun" doc:"Plan the preview again and answer with it, without applying it, when applying it would still do what it shows"`
	IdempotencyKey string   `header:"Idempotency-Key" maxLength:"128" doc:"Any unique string; a retry with the same key returns the first change set instead of applying it again"`
}

type undoInput struct {
	ChangeParams
	ID store.ID `path:"id" doc:"The change set to undo"`
}

func (s *Server) registerChangeSets() {
	tags := []string{"Changes"}
	op := huma.Operation{
		OperationID: "listChangeSets", Method: http.MethodGet, Path: Prefix + "/changesets", Tags: tags, Security: Protected,
		Summary: "List changes",
		Description: "Lists the change sets you can see, newest first: what changed, who changed it, and whether it can still be undone. With previews=true it lists previews that can still be applied. " +
			"You see the change sets you made and those whose every resource you may read; core.changeset.read on the server shows all of them.",
	}
	// Anyone signed in may list and read the change sets they can see, so
	// the catalog lists no permission for these.
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *listChangeSetsInput) (*changeSetList, error) {
		p := s.pipeline()
		if p == nil {
			return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
		}
		visible, err := s.changeSetVisibility(ctx)
		if err != nil {
			return nil, err
		}
		q := changeset.ListQuery{State: changeset.State(in.State), Resource: in.Resource, Actor: in.Actor, DryRuns: in.Previews, Visible: visible}
		list, next, err := fill(in.Cursor, in.Limit, func(cursor string, limit int) ([]*changeset.ChangeSet, string, error) {
			q.Cursor, q.Limit = cursor, limit
			return p.List(ctx, q)
		}, func(cs *changeset.ChangeSet) bool { return s.canSeeChangeSet(ctx, cs) }, func(cs *changeset.ChangeSet) store.ID { return cs.ID })
		if err != nil {
			return nil, err
		}
		out := &changeSetList{}
		out.Body.ChangeSets = list
		out.Body.Next = next
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getChangeSet", Method: http.MethodGet, Path: Prefix + "/changesets/{id}", Tags: tags, Security: Protected,
		Summary:     "Get a change",
		Description: "Returns a change set: the changes with their field-level differences, what they do to the server, what hooks and add-ons added, and how applying it went, change by change.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *changeSetInput) (*changeSetOutput, error) {
		cs, err := s.changeSet(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &changeSetOutput{Body: cs}, nil
	})

	op = huma.Operation{
		OperationID: "submitChangeSet", Method: http.MethodPost, Path: Prefix + "/changesets", Tags: tags, Security: Protected,
		Summary: "Make several changes at once",
		Description: "Plans several changes to resources as one change set, previewed, applied and undone together: creating a database and the site that uses it, say. " +
			"Choose IDs for new resources up front to refer to them from later changes; an ID a deleted resource had can't be used again. With dryRun=true the answer is the preview.",
		Responses: map[string]*huma.Response{
			"200": jsonResponse("The change set without applying it, for a dry run", s.schemaOf(typeOfChangeSet, "ChangeSet")),
			"202": jsonResponse("The change set, applied to the settings; its job carries out the rest", s.schemaOf(typeOfChangeSet, "ChangeSet")),
		},
	}
	s.withProblems(&op)
	Describe(&op, Info{Operation: "submit", Risk: store.RiskHigh, DryRun: true, Reversible: true})
	huma.Register(s.api, op, func(ctx context.Context, in *submitInput) (*ChangeSetOutput, error) {
		p := s.pipeline()
		if p == nil {
			return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
		}
		// Submit asks for what the changes need confirming, critical ones
		// included, once they are planned.
		return Submit(ctx, p, in.ChangeParams, changeset.Request{Title: in.Body.Title, Changes: in.Body.Changes})
	})

	op = huma.Operation{
		OperationID: "applyChangeSet", Method: http.MethodPost, Path: Prefix + "/changesets/{id}/apply", Tags: tags, Security: Protected,
		Summary: "Apply a preview",
		Description: "Applies a preview, a change set made with dryRun=true, as it was shown. The changes are planned again for you; when they would now do something else, " +
			"nothing is applied and the problem names a new preview to look at. A preview that carried secret values can't be applied this way; send the change again. " +
			"A preview is applied once: while it is being applied, applying it again answers changeset.applying. " +
			"With dryRun=true the preview is planned again and nothing is applied: the answer is the preview when applying it would still do what it shows.",
		DefaultStatus: http.StatusAccepted,
		Responses: map[string]*huma.Response{
			"200": jsonResponse("The preview, which applying would still carry out as it shows, for a dry run", s.schemaOf(typeOfChangeSet, "ChangeSet")),
			"202": jsonResponse("The change set, applied to the settings; its job carries out the rest", s.schemaOf(typeOfChangeSet, "ChangeSet")),
		},
	}
	s.withProblems(&op)
	Describe(&op, Info{Operation: "apply", Risk: store.RiskHigh, DryRun: true, Reversible: true})
	huma.Register(s.api, op, func(ctx context.Context, in *applyInput) (*ChangeSetOutput, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		if !in.DryRun {
			if earlier, err := s.replay(ctx, actor, changeset.CallApply, in.IdempotencyKey); earlier != nil || err != nil {
				return earlier, err
			}
		}
		if _, err := s.changeSet(ctx, in.ID); err != nil {
			return nil, err
		}
		// What applying it needs confirming is judged by the preview as it is
		// planned again for the actor, which is what gets applied; a dry run
		// applies nothing, so it isn't asked about.
		cs, err := s.pipeline().Apply(ctx, changeset.ApplyRequest{Actor: actor, ChangeSet: in.ID, DryRun: in.DryRun, IdempotencyKey: in.IdempotencyKey, Confirm: s.confirmPlan(actor)})
		if err != nil {
			return nil, err
		}
		status := http.StatusAccepted
		if in.DryRun {
			status = http.StatusOK
		}
		return &ChangeSetOutput{Status: status, Body: cs}, nil
	})

	op = huma.Operation{
		OperationID: "undoChangeSet", Method: http.MethodPost, Path: Prefix + "/changesets/{id}/undo", Tags: tags, Security: Protected,
		Summary: "Undo a change",
		Description: "Makes a new change set that puts back the settings an applied or failed change set replaced, and applies it like any change: " +
			"creates become deletes and deletes bring the resource back. Settings changed after that change set are replaced too; changedSince lists the resources that changed since. " +
			"With dryRun=true the answer is the preview.",
		Responses: map[string]*huma.Response{
			"200": jsonResponse("The undo's change set without applying it, for a dry run", s.schemaOf(typeOfChangeSet, "ChangeSet")),
			"202": jsonResponse("The undo's change set, applied to the settings; its job carries out the rest", s.schemaOf(typeOfChangeSet, "ChangeSet")),
		},
	}
	s.withProblems(&op)
	Describe(&op, Info{Operation: "undo", Risk: store.RiskMedium, DryRun: true, Reversible: true})
	huma.Register(s.api, op, func(ctx context.Context, in *undoInput) (*ChangeSetOutput, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		if !in.DryRun {
			if earlier, err := s.replay(ctx, actor, changeset.CallUndo, in.IdempotencyKey); earlier != nil || err != nil {
				return earlier, err
			}
		}
		if _, err := s.changeSet(ctx, in.ID); err != nil {
			return nil, err
		}
		// An undo is judged by the changes it makes, as planned, rather than
		// by those it undoes.
		cs, err := s.pipeline().Undo(ctx, changeset.UndoRequest{Actor: actor, ChangeSet: in.ID, DryRun: in.DryRun, IdempotencyKey: in.IdempotencyKey, Confirm: s.confirmPlan(actor)})
		if err != nil {
			return nil, err
		}
		status := http.StatusAccepted
		if cs.DryRun {
			status = http.StatusOK
		}
		return &ChangeSetOutput{Status: status, Body: cs}, nil
	})
}
