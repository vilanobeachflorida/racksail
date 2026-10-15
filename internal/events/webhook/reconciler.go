package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Reconciler returns the reconciler of core.webhook. Webhooks live in the
// panel, so it has nothing to do on the machine: it marks a webhook ready
// once its settings are stored, and its actions send deliveries through d.
func Reconciler(d *Dispatcher) reconcile.Reconciler {
	return reconcile.Typed(Kind, webhookReconciler{d: d})
}

type webhookReconciler struct {
	d *Dispatcher
}

// Plan has nothing to do on the machine. It refuses a URL written with an
// address deliveries may not go to, so the preview says so rather than
// every delivery failing later.
func (r webhookReconciler) Plan(ctx context.Context, obj *store.Object[WebhookSpec, WebhookStatus], req reconcile.Request) (reconcile.Plan, error) {
	if r.d != nil && !obj.Deleted() {
		if err := r.d.guard.checkURL(obj.Spec.URL); err != nil {
			return reconcile.Plan{}, i18n.Invalid(i18n.Field("spec.url", i18n.ErrBadValue, "reason", err.Error()))
		}
	}
	return reconcile.Plan{}, nil
}

func (r webhookReconciler) Apply(ctx context.Context, obj *store.Object[WebhookSpec, WebhookStatus], plan reconcile.Plan, ex reconcile.Executor) (WebhookStatus, error) {
	st := obj.Status
	now := time.Now()
	if r.d != nil {
		now = r.d.opts.Now()
	}
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, now); err != nil {
		return st, err
	}
	// A webhook whose receiver keeps failing stays degraded until a
	// delivery gets through, whatever else changed.
	health(&st, obj.Generation, now)
	return st, nil
}

func (webhookReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (r webhookReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if r.d == nil {
		return nil, errors.New("webhook: there is no dispatcher to send deliveries")
	}
	switch req.Action {
	case "test":
		return r.d.Test(ctx, req.Resource.ID)
	case "redeliver":
		var in RedeliverInput
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return nil, err
		}
		return r.d.Redeliver(ctx, req.Resource.ID, in.Delivery)
	}
	return nil, fmt.Errorf("webhook: there is no action called %q", req.Action)
}
