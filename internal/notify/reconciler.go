package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Reconcilers returns the reconcilers of the channel and relay kinds.
// Both live in the panel, so they have nothing to do on the machine: they
// mark resources ready once stored, and their test actions send through s.
func Reconcilers(s *Service) []reconcile.Reconciler {
	return []reconcile.Reconciler{
		reconcile.Typed(ChannelKind, channelReconciler{s: s}),
		reconcile.Typed(RelayKind, relayReconciler{s: s}),
	}
}

type channelReconciler struct {
	s *Service
}

// Plan refuses a type nothing provides, so the preview says so rather than
// every notification failing later.
func (r channelReconciler) Plan(ctx context.Context, obj *store.Object[ChannelSpec, ChannelStatus], req reconcile.Request) (reconcile.Plan, error) {
	if r.s != nil && !obj.Deleted() && !r.s.knownType(obj.Spec.Type) {
		return reconcile.Plan{}, i18n.Invalid(i18n.Field("spec.type", ErrUnknownChannelType, "type", obj.Spec.Type))
	}
	return reconcile.Plan{}, nil
}

func (r channelReconciler) Apply(ctx context.Context, obj *store.Object[ChannelSpec, ChannelStatus], plan reconcile.Plan, ex reconcile.Executor) (ChannelStatus, error) {
	st := obj.Status
	now := r.s.opts.Now()
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, now); err != nil {
		return st, err
	}
	channelHealth(&st, obj.Generation, now)
	return st, nil
}

func (channelReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (r channelReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if r.s == nil {
		return nil, errors.New("notify: there is no service to send through")
	}
	if req.Action != "test" {
		return nil, fmt.Errorf("notify: channels have no action called %q", req.Action)
	}
	d, err := r.s.Test(ctx, req.Resource.ID)
	if err != nil {
		return nil, err
	}
	return ChannelTestOutput{Notification: d}, nil
}

type relayReconciler struct {
	s *Service
}

func (relayReconciler) Plan(ctx context.Context, obj *store.Object[RelaySpec, RelayStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (r relayReconciler) Apply(ctx context.Context, obj *store.Object[RelaySpec, RelayStatus], plan reconcile.Plan, ex reconcile.Executor) (RelayStatus, error) {
	st := obj.Status
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, r.s.opts.Now()); err != nil {
		return st, err
	}
	return st, nil
}

func (relayReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (r relayReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if r.s == nil {
		return nil, errors.New("notify: there is no service to send through")
	}
	if req.Action != "test" {
		return nil, fmt.Errorf("notify: the mail relay has no action called %q", req.Action)
	}
	var in RelayTestInput
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return nil, err
	}
	return r.s.TestRelay(ctx, in.To, in.Locale), nil
}
