package store

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Memory is a Store that keeps everything in memory. It backs tests and
// tools that need a store for a moment, and behaves exactly as the SQLite
// store does; the same conformance tests run against both.
type Memory struct {
	kinds *KindRegistry
	now   func() time.Time
	hub   hub

	mu    sync.RWMutex
	state memState
}

type memState struct {
	resources map[ID]*Resource
	history   map[ID][]Version
	unique    map[string]ID
}

func (s memState) clone() memState {
	return memState{
		resources: maps.Clone(s.resources),
		history:   maps.Clone(s.history),
		unique:    maps.Clone(s.unique),
	}
}

// MemoryOption configures NewMemory.
type MemoryOption func(*Memory)

// WithClock sets the clock used for timestamps.
func WithClock(now func() time.Time) MemoryOption {
	return func(m *Memory) { m.now = now }
}

// NewMemory returns an empty in-memory store for the kinds in reg. Pass
// Kinds() for the process-wide registry.
func NewMemory(reg *KindRegistry, opts ...MemoryOption) *Memory {
	m := &Memory{
		kinds: reg,
		now:   time.Now,
		state: memState{resources: map[ID]*Resource{}, history: map[ID][]Version{}, unique: map[string]ID{}},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Get implements Reader.
func (m *Memory) Get(ctx context.Context, id ID) (*Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return get(m.state, id)
}

// List implements Reader.
func (m *Memory) List(ctx context.Context, q Query) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return list(m.kinds, m.state, q)
}

// History implements Store.
func (m *Memory) History(ctx context.Context, id ID) ([]Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.state.history[id]
	if !ok {
		return nil, ErrNotFound.New("what", string(id))
	}
	out := make([]Version, len(h))
	for i, v := range h {
		v.Spec = slices.Clone(v.Spec)
		v.Labels = maps.Clone(v.Labels)
		out[i] = v
	}
	return out, nil
}

// Write implements Store. Writes are serialized; fn sees its own changes and
// nothing is visible to others until it returns without an error.
func (m *Memory) Write(ctx context.Context, meta WriteMeta, fn func(tx Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	tx := &memTx{kinds: m.kinds, state: m.state.clone(), meta: meta, now: m.now().UTC()}
	err := fn(tx)
	tx.closed = true
	if err == nil {
		m.state = tx.state
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.hub.publish(tx.changes)
	return nil
}

// Watch implements Store.
func (m *Memory) Watch(f WatchFilter, buffer int) *Watcher { return m.hub.watch(f, buffer) }

// Close implements Store.
func (m *Memory) Close() error { return nil }

var errTxClosed = errors.New("store: transaction already finished")

type memTx struct {
	kinds   *KindRegistry
	state   memState
	meta    WriteMeta
	now     time.Time
	closed  bool
	changes []Change
}

func (tx *memTx) Get(ctx context.Context, id ID) (*Resource, error) {
	if tx.closed {
		return nil, errTxClosed
	}
	return get(tx.state, id)
}

func (tx *memTx) List(ctx context.Context, q Query) (*Page, error) {
	if tx.closed {
		return nil, errTxClosed
	}
	return list(tx.kinds, tx.state, q)
}

func (tx *memTx) Create(ctx context.Context, r *Resource) error {
	if tx.closed {
		return errTxClosed
	}
	k, ok := tx.kinds.Get(r.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", r.Kind)
	}
	info := k.Info()
	if err := checkNewID(info, r); err != nil {
		return err
	}
	if _, exists := tx.state.resources[r.ID]; exists {
		return ErrExists.New("what", string(r.ID))
	}
	if err := checkMeta(info, r); err != nil {
		return err
	}
	if info.Singleton {
		for _, other := range tx.state.resources {
			if other.Kind == info.Name && other.Scope == r.Scope && !other.Deleted() {
				return ErrExists.New("what", info.Title)
			}
		}
	}
	if len(r.Spec) == 0 {
		r.Spec = json.RawMessage("{}")
	}
	keys, err := uniqueKeys(info, r)
	if err != nil {
		return err
	}
	if err := tx.claim(info, keys, r.ID); err != nil {
		return err
	}
	// A resource created again with an ID it had before, as when a delete
	// is undone, carries on from the generations it had.
	r.Generation, r.Revision = 1, 1
	if h := tx.state.history[r.ID]; len(h) > 0 {
		r.Generation = h[len(h)-1].Generation + 1
	}
	r.CreatedAt, r.UpdatedAt, r.DeletedAt = tx.now, tx.now, nil
	stored := r.Clone()
	tx.state.resources[r.ID] = stored
	tx.record(stored, false)
	tx.changes = append(tx.changes, changeOf(ChangeCreated, stored, tx.meta.ChangeSet))
	return nil
}

func (tx *memTx) Update(ctx context.Context, r *Resource) error {
	if tx.closed {
		return errTxClosed
	}
	cur, err := tx.live(r.ID)
	if err != nil {
		return err
	}
	if r.Revision != 0 && r.Revision != cur.Revision {
		return ErrConflict.New("what", string(r.ID))
	}
	if r.Kind != cur.Kind {
		return i18n.Invalid(i18n.Field("kind", i18n.ErrImmutable))
	}
	if r.Scope != cur.Scope {
		return i18n.Invalid(i18n.Field("scope", i18n.ErrImmutable))
	}
	k, ok := tx.kinds.Get(cur.Kind)
	if !ok {
		return i18n.ErrUnknownName.New("what", "kind", "name", cur.Kind)
	}
	info := k.Info()
	if err := checkMeta(info, r); err != nil {
		return err
	}
	if len(r.Spec) == 0 {
		r.Spec = json.RawMessage("{}")
	}
	oldKeys, err := uniqueKeys(info, cur)
	if err != nil {
		return err
	}
	newKeys, err := uniqueKeys(info, r)
	if err != nil {
		return err
	}
	for _, key := range oldKeys {
		delete(tx.state.unique, key.String())
	}
	if err := tx.claim(info, newKeys, cur.ID); err != nil {
		// Keep the transaction consistent in case the caller carries on.
		for _, key := range oldKeys {
			tx.state.unique[key.String()] = cur.ID
		}
		return err
	}
	next := cur.Clone()
	specChanged := !jsonEqual(cur.Spec, r.Spec)
	placementChanged := cur.Server != r.Server
	next.Spec = slices.Clone(r.Spec)
	next.Labels = maps.Clone(r.Labels)
	next.Server = r.Server
	next.Revision++
	next.UpdatedAt = tx.now
	if specChanged || placementChanged {
		next.Generation++
	}
	tx.state.resources[next.ID] = next
	if specChanged || placementChanged {
		tx.record(next, false)
	}
	r.Meta = next.Meta
	r.Labels = maps.Clone(next.Labels)
	tx.changes = append(tx.changes, changeOf(ChangeUpdated, next, tx.meta.ChangeSet))
	return nil
}

func (tx *memTx) UpdateStatus(ctx context.Context, id ID, status json.RawMessage) error {
	if tx.closed {
		return errTxClosed
	}
	cur, ok := tx.state.resources[id]
	if !ok {
		return ErrNotFound.New("what", string(id))
	}
	if len(status) > 0 && !json.Valid(status) {
		return i18n.Invalid(i18n.Field("status", i18n.ErrBadValue, "reason", "it isn't valid JSON"))
	}
	next := cur.Clone()
	next.Status = slices.Clone(status)
	tx.state.resources[id] = next
	tx.changes = append(tx.changes, changeOf(ChangeStatus, next, tx.meta.ChangeSet))
	return nil
}

func (tx *memTx) Delete(ctx context.Context, id ID, revision int64) error {
	if tx.closed {
		return errTxClosed
	}
	cur, err := tx.live(id)
	if err != nil {
		return err
	}
	if revision != 0 && revision != cur.Revision {
		return ErrConflict.New("what", string(id))
	}
	next := cur.Clone()
	now := tx.now
	next.DeletedAt = &now
	next.Generation++
	next.Revision++
	next.UpdatedAt = now
	tx.state.resources[id] = next
	tx.record(next, true)
	tx.changes = append(tx.changes, changeOf(ChangeDeleted, next, tx.meta.ChangeSet))
	return nil
}

func (tx *memTx) Purge(ctx context.Context, id ID) error {
	if tx.closed {
		return errTxClosed
	}
	cur, ok := tx.state.resources[id]
	if !ok {
		return ErrNotFound.New("what", string(id))
	}
	if k, ok := tx.kinds.Get(cur.Kind); ok {
		keys, err := uniqueKeys(k.Info(), cur)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if tx.state.unique[key.String()] == id {
				delete(tx.state.unique, key.String())
			}
		}
	}
	delete(tx.state.resources, id)
	tx.changes = append(tx.changes, changeOf(ChangePurged, cur, tx.meta.ChangeSet))
	return nil
}

// live returns the stored resource unless it is missing or deleted.
func (tx *memTx) live(id ID) (*Resource, error) {
	cur, ok := tx.state.resources[id]
	if !ok || cur.Deleted() {
		return nil, ErrNotFound.New("what", string(id))
	}
	return cur, nil
}

func (tx *memTx) claim(info *KindInfo, keys []uniqueKey, id ID) error {
	for _, key := range keys {
		if owner, taken := tx.state.unique[key.String()]; taken && owner != id {
			return uniqueConflict(info, key)
		}
	}
	for _, key := range keys {
		tx.state.unique[key.String()] = id
	}
	return nil
}

func (tx *memTx) record(r *Resource, deleted bool) {
	v := Version{
		Generation: r.Generation,
		Spec:       slices.Clone(r.Spec),
		Labels:     maps.Clone(r.Labels),
		Server:     r.Server,
		ChangeSet:  tx.meta.ChangeSet,
		Actor:      tx.meta.Actor,
		Time:       tx.now,
		Deleted:    deleted,
	}
	// Copy before appending so the committed state never shares an array
	// with a transaction that might be thrown away.
	tx.state.history[r.ID] = append(slices.Clone(tx.state.history[r.ID]), v)
}

func get(s memState, id ID) (*Resource, error) {
	r, ok := s.resources[id]
	if !ok {
		return nil, ErrNotFound.New("what", string(id))
	}
	return r.Clone(), nil
}

func list(reg *KindRegistry, s memState, q Query) (*Page, error) {
	info, sorting, err := checkQuery(reg, q)
	if err != nil {
		return nil, err
	}
	var matched []*Resource
	for _, r := range s.resources {
		if queryMatches(info, r, q) {
			matched = append(matched, r)
		}
	}
	return pageOf(info, sorting, matched, q)
}
