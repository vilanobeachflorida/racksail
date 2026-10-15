package changeset

import (
	"context"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Retention bounds the change sets an Engine keeps. Get, Undo and Apply
// reach only the ones kept here. The snapshots taken before a change set was
// applied, and the secret versions only it used, are deleted with it.
type Retention struct {
	// Previews is how long dry runs stay available to be applied. The
	// default is an hour.
	Previews time.Duration
	// MaxPreviews caps the number of dry runs kept. The default is 1000.
	MaxPreviews int
	// Finished is how long applied, failed and undone change sets stay
	// available. The default is thirty days.
	Finished time.Duration
	// MaxFinished caps the number of finished change sets kept. The default
	// is 10,000.
	MaxFinished int
}

func (r Retention) withDefaults() Retention {
	if r.Previews <= 0 {
		r.Previews = time.Hour
	}
	if r.MaxPreviews <= 0 {
		r.MaxPreviews = 1000
	}
	if r.Finished <= 0 {
		r.Finished = 30 * 24 * time.Hour
	}
	if r.MaxFinished <= 0 {
		r.MaxFinished = 10_000
	}
	return r
}

func finished(s State) bool {
	return s == StateApplied || s == StateFailed || s == StateUndone
}

func idempotencyKey(op string, actor *auth.Actor, key string) string {
	if key == "" {
		return ""
	}
	return op + "\x00" + actor.String() + "\x00" + key
}

// reserve claims an idempotency key. It returns the change set an earlier
// request with the key produced, if there is one.
func (e *Engine) reserve(ctx context.Context, key string) (*ChangeSet, error) {
	if key == "" {
		return nil, nil
	}
	earlier, busy, err := e.storage.ReserveKey(ctx, key, e.opts.Now())
	switch {
	case err != nil:
		return nil, err
	case busy:
		return nil, ErrDuplicateRequest.New()
	case earlier != "":
		return e.storage.Get(ctx, earlier)
	}
	return nil, nil
}

// settle records what a request with an idempotency key produced; an empty
// id releases the key, so a retry after a failure runs again.
func (e *Engine) settle(ctx context.Context, key string, id store.ID) {
	if key == "" {
		return
	}
	if err := e.storage.SettleKey(context.WithoutCancel(ctx), key, id, e.opts.Now()); err != nil {
		e.opts.Log.Warn("recording an idempotency key", "error", err)
	}
}

// save keeps a change set, then drops what has outlived the retention, with
// the secret versions only dropped change sets used and the snapshots taken
// for them.
func (e *Engine) save(ctx context.Context, cs *ChangeSet, request *StoredRequest) error {
	ctx = context.WithoutCancel(ctx)
	if err := e.storage.Save(ctx, cs, request); err != nil {
		return err
	}
	evicted, err := e.storage.Prune(ctx, e.retention, e.opts.Now(), e.claimed)
	if err != nil {
		e.opts.Log.Warn("dropping old change sets", "error", err)
		return nil
	}
	if e.opts.Secrets != nil && len(evicted) > 0 {
		e.collectSecrets(ctx, e.secretsOf(evicted))
	}
	for _, old := range evicted {
		e.dropSnapshots(ctx, old.Snapshots)
	}
	return nil
}

// update changes a kept change set, whatever becomes of ctx.
func (e *Engine) update(ctx context.Context, id store.ID, fn func(cs *ChangeSet)) {
	_, err := e.storage.Update(context.WithoutCancel(ctx), id, func(cs *ChangeSet) error {
		fn(cs)
		return nil
	})
	if err != nil {
		e.opts.Log.Warn("updating a change set", "changeSet", id, "error", err)
	}
}

// secretsOf lists the secret versions the pipeline stored that the changes
// of the given change sets refer to.
func (e *Engine) secretsOf(sets []*ChangeSet) map[secrets.Ref]bool {
	out := map[secrets.Ref]bool{}
	for _, cs := range sets {
		if cs.DryRun {
			continue
		}
		for _, c := range cs.Changes {
			k, ok := e.opts.Kinds.Get(c.Kind)
			if !ok {
				continue
			}
			for _, ref := range append(secretRefs(k.Info(), c.Before), secretRefs(k.Info(), c.After)...) {
				if pipelineSecret(ref) {
					out[ref] = true
				}
			}
		}
	}
	return out
}
