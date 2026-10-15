package webhook

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// storage keeps deliveries, their attempts and the dispatcher's place in
// the timeline.
type storage interface {
	// cursor returns the sequence number of the last event dispatched;
	// ok is false when the dispatcher never ran.
	cursor(ctx context.Context) (seq int64, ok bool, err error)
	// enqueue stores new deliveries and moves the cursor, together.
	enqueue(ctx context.Context, deliveries []pending, cursor int64) error
	// insert stores a delivery made outside the timeline, such as a test.
	insert(ctx context.Context, d *Delivery) error
	// due returns pending deliveries whose time has come, oldest first,
	// with their bodies.
	due(ctx context.Context, now time.Time, limit int) ([]*Delivery, error)
	// nextDue returns when the next pending delivery is due.
	nextDue(ctx context.Context) (time.Time, bool, error)
	// record stores the state of a delivery after an attempt, and the
	// attempt.
	record(ctx context.Context, d *Delivery, a DeliveryAttempt) error
	get(ctx context.Context, id store.ID) (*Delivery, error)
	list(ctx context.Context, webhook store.ID, state DeliveryState, cursor string, limit int) ([]*Delivery, string, error)
	// pending counts the deliveries waiting to be sent.
	pending(ctx context.Context) (int, error)
	// prune drops finished deliveries older than before, and the oldest
	// beyond max, and pending ones older than before that nothing will send,
	// as when recording their only attempt failed.
	prune(ctx context.Context, before time.Time, max int) error
}

// memoryStorage keeps deliveries in maps, for tests and tools.
type memoryStorage struct {
	mu         sync.Mutex
	seq        int64
	started    bool
	deliveries map[store.ID]*Delivery
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{deliveries: map[store.ID]*Delivery{}}
}

func cloneDelivery(d *Delivery) *Delivery {
	c := *d
	c.Payload = slices.Clone(d.Payload)
	c.Attempts = slices.Clone(d.Attempts)
	if d.NextAttempt != nil {
		t := *d.NextAttempt
		c.NextAttempt = &t
	}
	if d.FinishedAt != nil {
		t := *d.FinishedAt
		c.FinishedAt = &t
	}
	return &c
}

func (m *memoryStorage) cursor(ctx context.Context) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seq, m.started, nil
}

func (m *memoryStorage) enqueue(ctx context.Context, deliveries []pending, cursor int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range deliveries {
		d := p.delivery
		d.Payload = slices.Clone(p.payload)
		m.deliveries[d.ID] = &d
	}
	m.seq, m.started = cursor, true
	return nil
}

func (m *memoryStorage) insert(ctx context.Context, d *Delivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries[d.ID] = cloneDelivery(d)
	return nil
}

func (m *memoryStorage) due(ctx context.Context, now time.Time, limit int) ([]*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Delivery
	for _, d := range m.deliveries {
		if d.State == DeliveryPending && d.NextAttempt != nil && !d.NextAttempt.After(now) {
			out = append(out, cloneDelivery(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memoryStorage) nextDue(ctx context.Context) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var next time.Time
	for _, d := range m.deliveries {
		if d.State == DeliveryPending && d.NextAttempt != nil && (next.IsZero() || d.NextAttempt.Before(next)) {
			next = *d.NextAttempt
		}
	}
	return next, !next.IsZero(), nil
}

func (m *memoryStorage) record(ctx context.Context, d *Delivery, a DeliveryAttempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.deliveries[d.ID]
	if !ok {
		return ErrDeliveryNotFound.New("delivery", string(d.ID))
	}
	attempts := append(slices.Clone(cur.Attempts), a)
	next := cloneDelivery(d)
	next.Attempts = attempts
	next.Payload = cur.Payload
	m.deliveries[d.ID] = next
	return nil
}

func (m *memoryStorage) get(ctx context.Context, id store.ID) (*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliveries[id]
	if !ok {
		return nil, ErrDeliveryNotFound.New("delivery", string(id))
	}
	return cloneDelivery(d), nil
}

func (m *memoryStorage) list(ctx context.Context, webhook store.ID, state DeliveryState, cursor string, limit int) ([]*Delivery, string, error) {
	var before store.ID
	if cursor != "" {
		id, err := store.DecodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		before = id
	}
	m.mu.Lock()
	var out []*Delivery
	for _, d := range m.deliveries {
		if d.Webhook == webhook && (state == "" || d.State == state) && (before == "" || d.ID < before) {
			c := cloneDelivery(d)
			c.Payload, c.Attempts = nil, nil
			out = append(out, c)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
		return out, store.EncodeCursor(out[limit-1].ID), nil
	}
	return out, "", nil
}

func (m *memoryStorage) pending(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, d := range m.deliveries {
		if d.State == DeliveryPending {
			n++
		}
	}
	return n, nil
}

func (m *memoryStorage) prune(ctx context.Context, before time.Time, max int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var finished []*Delivery
	for id, d := range m.deliveries {
		if d.State == DeliveryPending {
			if d.NextAttempt == nil && d.CreatedAt.Before(before) {
				delete(m.deliveries, id)
			}
			continue
		}
		if d.CreatedAt.Before(before) {
			delete(m.deliveries, id)
			continue
		}
		finished = append(finished, d)
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].ID < finished[j].ID })
	for i := 0; i < len(finished)-max; i++ {
		delete(m.deliveries, finished[i].ID)
	}
	return nil
}
