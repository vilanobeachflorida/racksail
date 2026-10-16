package changeset

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Storage keeps change sets, the requests previews came from and
// idempotency keys. The engine is the only one writing to it.
type Storage interface {
	// Save stores a change set, replacing what is stored under its ID.
	// request is the request a preview came from, without secret values;
	// it is nil for change sets that were applied.
	Save(ctx context.Context, cs *ChangeSet, request *StoredRequest) error
	// Get returns a kept change set.
	Get(ctx context.Context, id store.ID) (*ChangeSet, error)
	// Request returns the request a kept preview came from.
	Request(ctx context.Context, id store.ID) (*StoredRequest, error)
	// Update changes a kept change set and returns it as stored. When fn
	// returns an error nothing changes and Update returns it.
	Update(ctx context.Context, id store.ID, fn func(cs *ChangeSet) error) (*ChangeSet, error)
	// List returns kept change sets matching q, newest first, and the
	// cursor of the next page.
	List(ctx context.Context, q ListQuery) ([]*ChangeSet, string, error)
	// Prune drops dry runs and finished change sets that have outlived the
	// retention, oldest first, together with their idempotency keys, and
	// returns them. Change sets still being applied, and those in keep, are
	// kept.
	Prune(ctx context.Context, r Retention, now time.Time, keep func(id store.ID) bool) ([]*ChangeSet, error)
	// Mentions reports whether a kept change set, other than a dry run,
	// contains the text, as a reference to a secret.
	Mentions(ctx context.Context, text string) (bool, error)
	// ReserveKey claims an idempotency key for a request starting now. When
	// a request with the key already produced a change set that is kept, it
	// returns that change set's ID and claims nothing; busy reports that a
	// request with the key is still being processed.
	ReserveKey(ctx context.Context, key string, now time.Time) (earlier store.ID, busy bool, err error)
	// SettleKey records the change set a request with the key produced, or
	// releases the key when id is empty, so a retry runs again.
	SettleKey(ctx context.Context, key string, id store.ID, now time.Time) error
}

// StoredRequest is what is kept of the request a preview came from, so the
// preview can be applied as it was planned: the changes as asked for,
// without secret values, with the IDs the plan chose for new resources.
type StoredRequest struct {
	Title   string   `json:"title,omitempty"`
	Changes []Change `json:"changes"`
	// HadSecrets says the request carried secret values, which aren't
	// kept, so the preview can't be applied by its ID.
	HadSecrets bool `json:"hadSecrets,omitempty"`
}

// ListQuery selects change sets.
type ListQuery struct {
	State State
	// Resource keeps the change sets that touched a resource.
	Resource store.ID
	// Actor keeps the change sets of a user, token, automation or add-on,
	// or made for a user.
	Actor store.ID
	// DryRuns lists previews instead of change sets that were applied.
	DryRuns bool
	// UndoOf keeps the change sets that undo another.
	UndoOf store.ID
	// Visible, when set, keeps only change sets someone may see.
	Visible *Visibility
	Cursor  string
	Limit   int
}

// Visibility narrows a list to the change sets someone may see: those Person
// made, or that were made for them, and those whose every resource belongs
// to one of Scopes or is one of Objects. It may keep a few they can't read,
// as when a grant on a scope doesn't cover every kind, so lists check each
// change set they return as well.
type Visibility struct {
	Person  store.ID
	Scopes  []store.Scope
	Objects []store.ID
}

func (v *Visibility) shows(cs *ChangeSet) bool {
	if v.Person != "" && jobs.Of(cs.Actor) == v.Person {
		return true
	}
	if len(cs.Changes) == 0 {
		return false
	}
	for _, c := range cs.Changes {
		if !slices.Contains(v.Scopes, c.Scope) && !slices.Contains(v.Objects, c.ID) {
			return false
		}
	}
	return true
}

func (q ListQuery) limit() int {
	if q.Limit <= 0 || q.Limit > store.MaxLimit {
		return store.DefaultLimit
	}
	return q.Limit
}

func (q ListQuery) matches(cs *ChangeSet) bool {
	switch {
	case cs.DryRun != q.DryRuns:
		return false
	case q.State != "" && cs.State != q.State:
		return false
	case q.UndoOf != "" && cs.UndoOf != q.UndoOf:
		return false
	case q.Actor != "" && (cs.Actor == nil || (cs.Actor.ID != q.Actor && cs.Actor.User != q.Actor)):
		return false
	case q.Resource != "" && !slices.ContainsFunc(cs.Changes, func(c ResourceChange) bool { return c.ID == q.Resource }):
		return false
	case q.Visible != nil && !q.Visible.shows(cs):
		return false
	}
	return true
}

// IdempotencyWindow is how long an Idempotency-Key keeps answering with the
// change set it first produced, as long as that change set is kept.
const IdempotencyWindow = 24 * time.Hour

// reservationTimeout is how long a key stays claimed by a request that never
// said what it produced, as when the panel stopped in the middle of it.
const reservationTimeout = 10 * time.Minute

// keyEntry is a claimed idempotency key.
type keyEntry struct {
	// set is empty while the first request is still being processed.
	set  store.ID
	time time.Time
}

// MemoryStorage keeps change sets in memory, for tests and tools.
type MemoryStorage struct {
	mu       sync.Mutex
	sets     map[store.ID]*ChangeSet
	requests map[store.ID]*StoredRequest
	keys     map[string]keyEntry
}

// NewMemoryStorage returns an empty MemoryStorage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{sets: map[store.ID]*ChangeSet{}, requests: map[store.ID]*StoredRequest{}, keys: map[string]keyEntry{}}
}

func notFound(id store.ID) error { return i18n.ErrNotFound.New("what", "Change "+string(id)) }

// Save implements Storage.
func (m *MemoryStorage) Save(ctx context.Context, cs *ChangeSet, request *StoredRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sets[cs.ID] = cs.Clone()
	if request != nil {
		m.requests[cs.ID] = request.clone()
	} else {
		delete(m.requests, cs.ID)
	}
	return nil
}

// Get implements Storage.
func (m *MemoryStorage) Get(ctx context.Context, id store.ID) (*ChangeSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cs, ok := m.sets[id]
	if !ok {
		return nil, notFound(id)
	}
	return cs.Clone(), nil
}

// Request implements Storage.
func (m *MemoryStorage) Request(ctx context.Context, id store.ID) (*StoredRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return nil, notFound(id)
	}
	return r.clone(), nil
}

// Update implements Storage.
func (m *MemoryStorage) Update(ctx context.Context, id store.ID, fn func(cs *ChangeSet) error) (*ChangeSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept, ok := m.sets[id]
	if !ok {
		return nil, notFound(id)
	}
	cs := kept.Clone()
	if err := fn(cs); err != nil {
		return nil, err
	}
	m.sets[id] = cs
	return cs.Clone(), nil
}

// List implements Storage.
func (m *MemoryStorage) List(ctx context.Context, q ListQuery) ([]*ChangeSet, string, error) {
	var before store.ID
	if q.Cursor != "" {
		id, err := store.DecodeCursor(q.Cursor)
		if err != nil {
			return nil, "", err
		}
		before = id
	}
	m.mu.Lock()
	var out []*ChangeSet
	for _, cs := range m.sets {
		if (before == "" || cs.ID < before) && q.matches(cs) {
			out = append(out, cs.Clone())
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit := q.limit(); len(out) > limit {
		out = out[:limit]
		return out, store.EncodeCursor(out[limit-1].ID), nil
	}
	return out, "", nil
}

// Prune implements Storage.
func (m *MemoryStorage) Prune(ctx context.Context, r Retention, now time.Time, keep func(id store.ID) bool) ([]*ChangeSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var previews, done []*ChangeSet
	for _, cs := range m.sets {
		switch {
		case keep(cs.ID):
		case cs.DryRun:
			previews = append(previews, cs)
		case finished(cs.State):
			done = append(done, cs)
		}
	}
	var evicted []*ChangeSet
	drop := func(list []*ChangeSet, ttl time.Duration, most int) {
		// IDs sort by creation time.
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		n := 0
		for n < len(list) && (now.Sub(list[n].CreatedAt) > ttl || len(list)-n > most) {
			n++
		}
		for _, cs := range list[:n] {
			delete(m.sets, cs.ID)
			delete(m.requests, cs.ID)
			evicted = append(evicted, cs)
		}
	}
	drop(previews, r.Previews, r.MaxPreviews)
	drop(done, r.Finished, r.MaxFinished)
	for key, ent := range m.keys {
		if now.Sub(ent.time) > IdempotencyWindow {
			delete(m.keys, key)
			continue
		}
		if _, ok := m.sets[ent.set]; ent.set != "" && !ok {
			delete(m.keys, key)
		}
	}
	return evicted, nil
}

// Mentions implements Storage.
func (m *MemoryStorage) Mentions(ctx context.Context, text string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cs := range m.sets {
		if cs.DryRun {
			continue
		}
		for _, c := range cs.Changes {
			if strings.Contains(string(c.Before), text) || strings.Contains(string(c.After), text) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ReserveKey implements Storage.
func (m *MemoryStorage) ReserveKey(ctx context.Context, key string, now time.Time) (store.ID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ent, ok := m.keys[key]; ok && now.Sub(ent.time) <= IdempotencyWindow {
		switch {
		case ent.set == "" && now.Sub(ent.time) <= reservationTimeout:
			return "", true, nil
		case ent.set != "":
			if _, kept := m.sets[ent.set]; kept {
				return ent.set, false, nil
			}
		}
	}
	m.keys[key] = keyEntry{time: now}
	return "", false, nil
}

// SettleKey implements Storage.
func (m *MemoryStorage) SettleKey(ctx context.Context, key string, id store.ID, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		delete(m.keys, key)
		return nil
	}
	m.keys[key] = keyEntry{set: id, time: now}
	return nil
}

func (r *StoredRequest) clone() *StoredRequest {
	if r == nil {
		return nil
	}
	c := *r
	c.Changes = make([]Change, len(r.Changes))
	for i, ch := range r.Changes {
		c.Changes[i] = ch.clone()
	}
	return &c
}
