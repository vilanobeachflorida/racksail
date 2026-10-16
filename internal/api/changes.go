package api

import (
	"context"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// ChangeParams are the parameters of every operation that changes
// something: dryRun previews the change set without applying it, and an
// Idempotency-Key makes a retry return the first change set instead of
// applying twice. Embed it in the operation's input.
type ChangeParams struct {
	DryRun         bool   `query:"dryRun" doc:"Plan the change and return its change set without applying it"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Any unique string; a retry with the same key returns the first change set instead of applying the change again"`
}

// ChangeSetOutput answers an operation that changes something with its
// change set: 200 for a dry run, and 202 once it is applied to the store,
// while the job named in the change set carries out the system changes.
type ChangeSetOutput struct {
	Status int
	Body   *changeset.ChangeSet
}

// ChangeSetResponses documents the answers of an operation that answers
// with a ChangeSetOutput: the change set a dry run plans, with 200, with
// 202 the change set once it is applied, as applied describes, and the
// problem details of a refusal. An operation registered with huma.Register
// outside this package needs them, since huma only documents the status it
// answers with by default.
func ChangeSetResponses(a huma.API, applied string) map[string]*huma.Response {
	return ChangeResponses(a, typeOfChangeSet, "The change set without applying it, for a dry run", applied)
}

// ChangeResponses is ChangeSetResponses for an operation that answers with
// the change set inside a body of its own, of type body: the preview of a
// dry run with 200, and with 202 what it answers once the change is applied.
func ChangeResponses(a huma.API, body reflect.Type, preview, applied string) map[string]*huma.Response {
	schema := a.OpenAPI().Components.Schemas.Schema(body, true, "")
	return map[string]*huma.Response{
		"200":     jsonResponse(preview, schema),
		"202":     jsonResponse(applied, schema),
		"default": problemResponse(a),
	}
}

// Submit sends a request through the change pipeline for the signed-in
// actor, with the operation's change parameters, and builds the answer. A
// person in the panel confirms it's them first when what the changes do
// needs it, such as giving a user a new email address, whichever operation
// sends them: the pipeline asks once they are planned, hooks' changes
// included. The pipeline looks the idempotency key up before it reads
// anything, so a request built without reading anything gets the first
// change set back on a retry; one built from what the store holds goes
// through SubmitFunc.
func Submit(ctx context.Context, p changeset.Pipeline, params ChangeParams, req changeset.Request) (*ChangeSetOutput, error) {
	if p == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	req.Actor, req.DryRun, req.IdempotencyKey = actor, params.DryRun, params.IdempotencyKey
	req.Confirm = confirmerFor(ctx, actor, req.Confirm)
	cs, err := p.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	return changeSetAnswer(cs), nil
}

// SubmitFunc is Submit for a request that build makes from what is there
// now, such as the modules a module needs that are still off, or checks
// against it first, such as whether the module is on. A retry with the
// first request's Idempotency-Key gets the first change set before build
// runs (Replayed): by then the first request has changed what build reads,
// and build would refuse the retry or ask for something else.
func SubmitFunc(ctx context.Context, p changeset.Pipeline, params ChangeParams, build func() (changeset.Request, error)) (*ChangeSetOutput, error) {
	if earlier, err := Replayed(ctx, p, params); earlier != nil || err != nil {
		return earlier, err
	}
	req, err := build()
	if err != nil {
		return nil, err
	}
	return Submit(ctx, p, params, req)
}

// Replayed returns the answer of an earlier request of the signed-in actor
// with the same Idempotency-Key, the change set it produced, or nil when
// there was none; while that request is still being processed it gives
// changeset.duplicate_request. A dry run takes no key, so it is never
// answered this way. Operations that read or check anything to build their
// request ask it before they do, as SubmitFunc does; one whose answer holds
// more than the change set builds that answer around it.
func Replayed(ctx context.Context, p changeset.Pipeline, params ChangeParams) (*ChangeSetOutput, error) {
	if params.DryRun || params.IdempotencyKey == "" {
		return nil, nil
	}
	if p == nil {
		return nil, i18n.ErrUnavailable.New("what", "The change pipeline")
	}
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := p.Replay(ctx, actor, changeset.CallSubmit, params.IdempotencyKey)
	if err != nil || cs == nil {
		return nil, err
	}
	return changeSetAnswer(cs), nil
}

// changeSetAnswer answers with a change set: 200 for a dry run, 202 for one
// that was applied.
func changeSetAnswer(cs *changeset.ChangeSet) *ChangeSetOutput {
	status := http.StatusAccepted
	if cs.DryRun {
		status = http.StatusOK
	}
	return &ChangeSetOutput{Status: status, Body: cs}
}
