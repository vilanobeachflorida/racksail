package rules

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// TestRequest asks what a rule would do with a change, without saving the
// rule or making the change.
type TestRequest struct {
	Rule hooks.PolicySpec `json:"rule" doc:"The rule to try, as it would be saved"`
	// Owner is who the rule would belong to: the server, a reseller or an
	// account.
	Owner store.Scope `json:"owner" doc:"Who the rule would belong to; it only applies to what its owner owns"`
	// Change describes the change to try it on.
	Operation string            `json:"operation" pattern:"^[a-z][a-z0-9_]*$" doc:"create, update, delete or an action's name" example:"create"`
	Kind      string            `json:"kind" pattern:"^[a-z][a-z0-9]*\\.[a-z][a-z0-9_]*$" doc:"The kind of the resource" example:"web.site"`
	Scope     store.Scope       `json:"scope" doc:"Who the resource belongs to"`
	Labels    map[string]string `json:"labels,omitempty" doc:"The resource's labels as the change leaves them"`
	// Spec is the resource as the change leaves it, and OldSpec as it was
	// before, for anything but a create.
	Spec    json.RawMessage `json:"spec,omitempty" doc:"The resource's settings as the change leaves them"`
	OldSpec json.RawMessage `json:"oldSpec,omitempty" doc:"The resource's settings before the change; leave out for a create"`
}

// TestResult is what the rule would do.
type TestResult struct {
	// Applies says the rule would run for the change: its kinds,
	// operations and owner include it.
	Applies bool `json:"applies" doc:"Whether the rule would run for this change"`
	// Refused and Message are a validate rule's verdict.
	Refused bool   `json:"refused,omitempty" doc:"Whether a validate rule would refuse the change"`
	Message string `json:"message,omitempty" doc:"What the person making the change would be told"`
	// Spec and Fields are what a default rule would fill in.
	Spec   json.RawMessage `json:"spec,omitempty" doc:"The resource's settings after a default rule filled them in"`
	Fields []FieldResult   `json:"fields,omitempty"`
	// Cost is how much of the cost limit the evaluation used.
	Cost      uint64 `json:"cost" doc:"How much the evaluation cost, in CEL's cost units"`
	CostLimit uint64 `json:"costLimit" doc:"The most one evaluation may cost"`
	// Error is why the rule couldn't be evaluated; at the rule's points
	// that counts as a failure of its hook.
	Error string `json:"error,omitempty" doc:"Why evaluating the rule failed"`
	// Points are where the rule would be attached.
	Points []string `json:"points,omitempty" doc:"The hook points the rule would be attached to"`
}

// Test runs a rule on a change it describes, as the hook pipeline would,
// for the rule tester. A rule that doesn't compile is an error; one that
// fails while it runs is reported in the result.
func (e *Engine) Test(ctx context.Context, actor *auth.Actor, req TestRequest) (*TestResult, error) {
	c, err := compileRule(req.Rule, e.opts.CostLimit)
	if err != nil {
		var fe *fieldError
		if errors.As(err, &fe) {
			path := "body.rule"
			if fe.path != "" {
				path += "." + fe.path
			}
			return nil, i18n.Invalid(i18n.Field(path, i18n.ErrBadValue, "reason", fe.reason))
		}
		return nil, err
	}
	if err := req.Owner.Validate(); err != nil {
		return nil, i18n.Invalid(i18n.Field("body.owner", i18n.ErrBadValue, "reason", err.Error()))
	}
	if err := req.Scope.Validate(); err != nil {
		return nil, i18n.Invalid(i18n.Field("body.scope", i18n.ErrBadValue, "reason", err.Error()))
	}
	ch := changeset.ResourceChange{Kind: req.Kind, ID: store.ID(""), Scope: req.Scope, Labels: req.Labels, After: req.Spec, Before: req.OldSpec}
	switch req.Operation {
	case "create":
		ch.Op, ch.Before = changeset.OpCreate, nil
	case "update":
		ch.Op = changeset.OpUpdate
	case "delete":
		ch.Op = changeset.OpDelete
	default:
		ch.Op, ch.Action = changeset.OpAction, req.Operation
	}
	if len(ch.After) == 0 {
		ch.After = json.RawMessage("{}")
	}
	res := &TestResult{CostLimit: e.opts.CostLimit}
	for _, p := range c.points() {
		res.Points = append(res.Points, string(p))
	}
	ok, err := e.applies(ctx, c, req.Owner, ch)
	if err != nil {
		return nil, err
	}
	res.Applies = ok
	if !ok {
		return res, nil
	}
	switch req.Rule.Type {
	case hooks.PolicyValidate:
		refused, cost, err := e.decide(ctx, c, actor, ch)
		res.Cost = cost
		if err != nil {
			res.Error = err.Error()
			return res, nil
		}
		res.Refused = refused
		if refused {
			res.Message = req.Rule.Message
		}
	case hooks.PolicyDefault:
		spec, fields, cost, err := e.fill(ctx, c, actor, ch)
		res.Cost = cost
		if err != nil {
			res.Error = err.Error()
			return res, nil
		}
		res.Spec, res.Fields = spec, fields
	}
	return res, nil
}
