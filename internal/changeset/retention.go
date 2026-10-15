package changeset

import (
	"context"
	"sort"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Retention bounds the change sets an Engine keeps. Change sets live in the
// engine's memory, so Get and Undo reach only the ones kept here, and none
// survive a restart of the panel.
type Retention struct {
	// Previews is how long dry runs stay available. The default is an hour.
	Previews time.Duration
	// MaxPreviews caps the number of dry runs kept. The default is 200.
	MaxPreviews int
	// Finished is how long applied, failed and undone change sets stay
	// available. The default is seven days.
	Finished time.Duration
	// MaxFinished caps the number of finished change sets kept. The default
	// is 1000.
	MaxFinished int
}

func (r Retention) withDefaults() Retention {
	if r.Previews <= 0 {
		r.Previews = time.Hour
	}
	if r.MaxPreviews <= 0 {
		r.MaxPreviews = 200
	}
	if r.Finished <= 0 {
		r.Finished = 7 * 24 * time.Hour
	}
	if r.MaxFinished <= 0 {
		r.MaxFinished = 1000
	}
	return r
}

// IdempotencyWindow is how long an Idempotency-Key keeps answering with the
// change set it first produced, as long as that change set is kept.
const IdempotencyWindow = 24 * time.Hour

type idemEntry struct {
	// set is empty while the first request is still being processed.
	set  store.ID
	time time.Time
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
	now := e.opts.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if ent, ok := e.idem[key]; ok && now.Sub(ent.time) <= IdempotencyWindow {
		if ent.set == "" {
			return nil, ErrDuplicateRequest.New()
		}
		if cs, ok := e.sets[ent.set]; ok {
			return cs.Clone(), nil
		}
	}
	e.idem[key] = idemEntry{time: now}
	return nil, nil
}

// settle records what a request with an idempotency key produced; an empty
// id releases the key, so a retry after a failure runs again.
func (e *Engine) settle(key string, id store.ID) {
	if key == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if id == "" {
		delete(e.idem, key)
		return
	}
	e.idem[key] = idemEntry{set: id, time: e.opts.Now()}
}

// save keeps a copy of cs, then drops what has outlived the retention and
// deletes the secret versions only dropped change sets used.
func (e *Engine) save(ctx context.Context, cs *ChangeSet) {
	now := e.opts.Now()
	e.mu.Lock()
	e.sets[cs.ID] = cs.Clone()
	evicted := e.prune(now)
	var candidates, kept map[secrets.Ref]bool
	if e.opts.Secrets != nil && len(evicted) > 0 {
		if candidates = e.secretsOf(evicted, true); len(candidates) > 0 {
			kept = e.secretsOf(e.retained(), false)
		}
	}
	e.mu.Unlock()
	if len(candidates) > 0 {
		e.collectSecrets(ctx, candidates, kept)
	}
}

// update changes the kept copy of a change set.
func (e *Engine) update(id store.ID, fn func(cs *ChangeSet)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cs, ok := e.sets[id]; ok {
		fn(cs)
	}
}

func (e *Engine) retained() []*ChangeSet {
	out := make([]*ChangeSet, 0, len(e.sets))
	for _, cs := range e.sets {
		out = append(out, cs)
	}
	return out
}

func finished(s State) bool {
	return s == StateApplied || s == StateFailed || s == StateUndone
}

// prune drops dry runs and finished change sets that are too old or over
// their cap, oldest first, along with their idempotency keys. Change sets
// still being applied or undone are kept. e.mu must be held.
func (e *Engine) prune(now time.Time) []*ChangeSet {
	var previews, done []*ChangeSet
	for _, cs := range e.sets {
		switch {
		case e.undoing[cs.ID]:
		case cs.DryRun:
			previews = append(previews, cs)
		case finished(cs.State):
			done = append(done, cs)
		}
	}
	var evicted []*ChangeSet
	drop := func(list []*ChangeSet, ttl time.Duration, max int) {
		// IDs sort by creation time.
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		n := 0
		for n < len(list) && (now.Sub(list[n].CreatedAt) > ttl || len(list)-n > max) {
			n++
		}
		for _, cs := range list[:n] {
			delete(e.sets, cs.ID)
			evicted = append(evicted, cs)
		}
	}
	drop(previews, e.retention.Previews, e.retention.MaxPreviews)
	drop(done, e.retention.Finished, e.retention.MaxFinished)
	for key, ent := range e.idem {
		if now.Sub(ent.time) > IdempotencyWindow {
			delete(e.idem, key)
			continue
		}
		if _, ok := e.sets[ent.set]; ent.set != "" && !ok {
			delete(e.idem, key)
		}
	}
	return evicted
}
