package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Memory is a Store that keeps everything in memory. It backs development
// mode and tests, and is the reference for how every Store behaves.
type Memory struct {
	kinds *KindRegistry
	now   func() time.Time

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
	defer m.mu.Unlock()
	tx := &memTx{kinds: m.kinds, state: m.state.clone(), meta: meta, now: m.now().UTC()}
	err := fn(tx)
	tx.closed = true
	if err != nil {
		return err
	}
	m.state = tx.state
	return nil
}

// Close implements Store.
func (m *Memory) Close() error { return nil }

var errTxClosed = errors.New("store: transaction already finished")

type memTx struct {
	kinds  *KindRegistry
	state  memState
	meta   WriteMeta
	now    time.Time
	closed bool
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
	if r.ID == "" {
		r.ID = NewID(info.Prefix)
	} else if !r.ID.Valid() || r.ID.Prefix() != info.Prefix {
		return i18n.Invalid(i18n.Field("id", i18n.ErrBadValue, "reason", fmt.Sprintf("a %s id starts with %q", info.Title, info.Prefix+"_")))
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
	r.Generation, r.Revision = 1, 1
	r.CreatedAt, r.UpdatedAt, r.DeletedAt = tx.now, tx.now, nil
	stored := r.Clone()
	tx.state.resources[r.ID] = stored
	tx.record(stored, false)
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
		delete(tx.state.unique, key)
	}
	if err := tx.claim(info, newKeys, cur.ID); err != nil {
		// Keep the transaction consistent in case the caller carries on.
		for _, key := range oldKeys {
			tx.state.unique[key] = cur.ID
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
	next := cur.Clone()
	next.Status = slices.Clone(status)
	tx.state.resources[id] = next
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
			if tx.state.unique[key] == id {
				delete(tx.state.unique, key)
			}
		}
	}
	delete(tx.state.resources, id)
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

func (tx *memTx) claim(info *KindInfo, keys []string, id ID) error {
	for _, key := range keys {
		if owner, taken := tx.state.unique[key]; taken && owner != id {
			index, value := splitUniqueKey(key)
			if ix := findIndex(info, index); ix != nil && ix.Conflict != nil {
				return ix.Conflict.New("value", value)
			}
			return ErrExists.New("what", fmt.Sprintf("%s with %s %s", info.Title, index, value))
		}
	}
	for _, key := range keys {
		tx.state.unique[key] = id
	}
	return nil
}

func (tx *memTx) record(r *Resource, deleted bool) {
	v := Version{
		Generation: r.Generation,
		Spec:       slices.Clone(r.Spec),
		Labels:     maps.Clone(r.Labels),
		ChangeSet:  tx.meta.ChangeSet,
		Actor:      tx.meta.Actor,
		Time:       tx.now,
		Deleted:    deleted,
	}
	// Copy before appending so the committed state never shares an array
	// with a transaction that might be thrown away.
	tx.state.history[r.ID] = append(slices.Clone(tx.state.history[r.ID]), v)
}

func checkMeta(info *KindInfo, r *Resource) error {
	var fields []i18n.FieldError
	if r.Scope.Level != info.Scope {
		fields = append(fields, i18n.Field("scope.level", i18n.ErrBadValue, "reason", fmt.Sprintf("%s resources are owned at %s level", info.Title, info.Scope)))
	} else if err := r.Scope.Validate(); err != nil {
		fields = append(fields, i18n.Field("scope", i18n.ErrBadValue, "reason", err.Error()))
	}
	if err := ValidateLabels(r.Labels); err != nil {
		fields = append(fields, i18n.Field("labels", i18n.ErrBadValue, "reason", err.Error()))
	}
	if r.Server != "" && !r.Server.Valid() {
		fields = append(fields, i18n.Field("server", i18n.ErrBadValue, "reason", "not a valid id"))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

func get(s memState, id ID) (*Resource, error) {
	r, ok := s.resources[id]
	if !ok {
		return nil, ErrNotFound.New("what", string(id))
	}
	return r.Clone(), nil
}

func list(reg *KindRegistry, s memState, q Query) (*Page, error) {
	k, ok := reg.Get(q.Kind)
	if !ok {
		return nil, i18n.ErrUnknownName.New("what", "kind", "name", q.Kind)
	}
	info := k.Info()
	for name := range q.Index {
		if findIndex(info, name) == nil {
			return nil, i18n.ErrUnknownName.New("what", "index", "name", name)
		}
	}
	var after ID
	if q.Cursor != "" {
		id, err := DecodeCursor(q.Cursor)
		if err != nil {
			return nil, err
		}
		after = id
	}
	var matched []*Resource
	for _, r := range s.resources {
		if r.Kind != q.Kind || (after != "" && r.ID <= after) {
			continue
		}
		if r.Deleted() && !q.IncludeDeleted {
			continue
		}
		if q.Scope != nil && r.Scope != *q.Scope {
			continue
		}
		if q.Server != nil && r.Server != *q.Server {
			continue
		}
		if !labelsMatch(r.Labels, q.Labels) || !indexMatch(info, r, q.Index) {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	limit := q.EffectiveLimit()
	page := &Page{}
	if len(matched) > limit {
		matched = matched[:limit]
		page.Next = EncodeCursor(matched[limit-1].ID)
	}
	page.Items = make([]*Resource, len(matched))
	for i, r := range matched {
		page.Items[i] = r.Clone()
	}
	return page, nil
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func indexMatch(info *KindInfo, r *Resource, want map[string]string) bool {
	for name, value := range want {
		ix := findIndex(info, name)
		values, err := indexValues(r.Spec, ix)
		if err != nil {
			return false
		}
		if ix.Fold {
			value = strings.ToLower(value)
		}
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func findIndex(info *KindInfo, name string) *Index {
	for i := range info.Indexes {
		if info.Indexes[i].Name == name {
			return &info.Indexes[i]
		}
	}
	return nil
}

func uniqueKeys(info *KindInfo, r *Resource) ([]string, error) {
	var keys []string
	for i := range info.Indexes {
		ix := &info.Indexes[i]
		if !ix.Unique {
			continue
		}
		values, err := indexValues(r.Spec, ix)
		if err != nil {
			return nil, err
		}
		scope := ""
		if ix.PerScope {
			scope = r.Scope.String()
		}
		seen := map[string]bool{}
		for _, v := range values {
			if seen[v] {
				return nil, i18n.Invalid(i18n.Field("spec."+ix.Path, i18n.ErrBadValue, "reason", fmt.Sprintf("%q appears more than once", v)))
			}
			seen[v] = true
			keys = append(keys, strings.Join([]string{info.Name, scope, ix.Name, v}, "\x00"))
		}
	}
	return keys, nil
}

func splitUniqueKey(key string) (index, value string) {
	parts := strings.SplitN(key, "\x00", 4)
	if len(parts) != 4 {
		return "", key
	}
	return parts[2], parts[3]
}

// indexValues extracts the values of an indexed field from a spec.
func indexValues(spec json.RawMessage, ix *Index) ([]string, error) {
	var doc any
	if len(spec) > 0 {
		if err := json.Unmarshal(spec, &doc); err != nil {
			return nil, fmt.Errorf("decoding spec: %w", err)
		}
	}
	nodes := []any{doc}
	for _, part := range strings.Split(ix.Path, ".") {
		var next []any
		for _, n := range flatten(nodes) {
			if m, ok := n.(map[string]any); ok {
				if v, ok := m[part]; ok {
					next = append(next, v)
				}
			}
		}
		nodes = next
	}
	var out []string
	for _, n := range flatten(nodes) {
		var s string
		switch v := n.(type) {
		case string:
			s = v
		case float64:
			s = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			s = strconv.FormatBool(v)
		default:
			continue
		}
		if s == "" {
			continue
		}
		if ix.Fold {
			s = strings.ToLower(s)
		}
		out = append(out, s)
	}
	return out, nil
}

func flatten(nodes []any) []any {
	var out []any
	for _, n := range nodes {
		if arr, ok := n.([]any); ok {
			out = append(out, flatten(arr)...)
			continue
		}
		out = append(out, n)
	}
	return out
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
