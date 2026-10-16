package api

import (
	"context"
	"net/http"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
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

// Submit sends a request through the change pipeline for the signed-in
// actor, with the operation's change parameters, and builds the answer. A
// person in the panel confirms it's them first when one of the changes
// needs it, such as a user's new email address, whichever operation sends
// it.
func Submit(ctx context.Context, p changeset.Pipeline, params ChangeParams, req changeset.Request) (*ChangeSetOutput, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := confirmSubmitted(ctx, actor, params.DryRun, req.Changes); err != nil {
		return nil, err
	}
	req.Actor, req.DryRun, req.IdempotencyKey = actor, params.DryRun, params.IdempotencyKey
	cs, err := p.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	status := http.StatusAccepted
	if cs.DryRun {
		status = http.StatusOK
	}
	return &ChangeSetOutput{Status: status, Body: cs}, nil
}
