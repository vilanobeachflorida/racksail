package events

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Retention bounds what the timeline and the audit log keep. The audit log
// is kept longer, because it answers who did what long after it happened.
type Retention struct {
	// Events is how long timeline events are kept; the default is 90 days.
	Events time.Duration
	// MaxEvents caps the number of events kept; the default is 500,000.
	MaxEvents int
	// Audit is how long audit records are kept; the default is 400 days.
	Audit time.Duration
	// MaxAudit caps the number of audit records kept; the default is
	// 2,000,000.
	MaxAudit int
}

func (r Retention) withDefaults() Retention {
	if r.Events <= 0 {
		r.Events = 90 * 24 * time.Hour
	}
	if r.MaxEvents <= 0 {
		r.MaxEvents = 500_000
	}
	if r.Audit <= 0 {
		r.Audit = 400 * 24 * time.Hour
	}
	if r.MaxAudit <= 0 {
		r.MaxAudit = 2_000_000
	}
	return r
}

// storage keeps events and audit records for a Log.
type storage interface {
	// appendEvent stores an event and sets its Seq.
	appendEvent(ctx context.Context, e *Event) error
	appendAudit(ctx context.Context, r *AuditRecord) error
	// repeatAudit counts n more repeats of a kept record, the last of them
	// at last, and reports whether the record was there.
	repeatAudit(ctx context.Context, id store.ID, n int, last time.Time) (bool, error)
	queryEvents(ctx context.Context, q Query) ([]Event, string, error)
	queryAudit(ctx context.Context, q Query) ([]AuditRecord, string, error)
	// eventsAfter returns up to limit events stored after seq, oldest first.
	eventsAfter(ctx context.Context, seq int64, limit int) ([]Event, error)
	// seqOf returns the sequence number of an event that is still kept.
	seqOf(ctx context.Context, id store.ID) (int64, error)
	lastSeq(ctx context.Context) (int64, error)
	prune(ctx context.Context, r Retention, now time.Time) error
}

// Log is the event bus, the timeline and the audit log. Events are stored
// before subscribers see them, so a subscriber that falls behind can always
// catch up from the timeline, in the same order.
type Log struct {
	storage   storage
	retention Retention
	denials   *denials
	now       func() time.Time
	log       *slog.Logger

	mu   sync.Mutex
	subs map[*Subscription]bool
}

// Options configure a Log.
type Options struct {
	Retention Retention
	// Denials bounds the records of refusals one actor adds to the audit
	// log.
	Denials Denials
	Log     *slog.Logger
	Now     func() time.Time
}

func newLog(s storage, opts Options) *Log {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Log{
		storage: s, retention: opts.Retention.withDefaults(), now: opts.Now, log: opts.Log, subs: map[*Subscription]bool{},
		denials: newDenials(opts.Denials),
	}
}

// NewMemory returns a log kept in memory that holds at most retain events
// and as many audit records, for tests and tools.
func NewMemory(retain int) *Log {
	if retain <= 0 {
		retain = 10000
	}
	return newLog(&memoryStorage{max: retain}, Options{Retention: Retention{MaxEvents: retain, MaxAudit: retain}})
}

var _ interface {
	Bus
	Timeline
	AuditLog
} = (*Log)(nil)

// Publish implements Bus: it records the event in the timeline and hands it
// to subscribers. Events are stored and handed out under one lock, so every
// subscriber sees them in the order of the timeline, and a subscription is
// never closed while an event is being sent to it. Sending never blocks.
func (l *Log) Publish(ctx context.Context, e Event) error {
	if !ValidType(e.Type) {
		return fmt.Errorf("events: invalid event type %q", e.Type)
	}
	if e.ID == "" {
		e.ID = store.NewID(EventPrefix)
	}
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	e.Time = e.Time.UTC()
	if e.Severity == "" {
		e.Severity = SeverityInfo
	}
	if !e.Severity.Valid() {
		return fmt.Errorf("events: unknown severity %q", e.Severity)
	}
	if e.Scope.Level == "" {
		e.Scope = store.ServerScope()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.storage.appendEvent(context.WithoutCancel(ctx), &e); err != nil {
		return fmt.Errorf("events: storing %s: %w", e.Type, err)
	}
	for s := range l.subs {
		if !s.filter.Match(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.dropped.Add(1)
		}
	}
	return nil
}

// Subscribe implements Bus.
func (l *Log) Subscribe(f Filter, buffer int) *Subscription {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	s := &Subscription{C: ch, ch: ch, filter: f}
	s.close = func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.subs, s)
		close(ch)
	}
	l.mu.Lock()
	l.subs[s] = true
	l.mu.Unlock()
	return s
}

// requestIDKey carries the ID of the API request an action came through.
type requestIDKey struct{}

// WithRequestID returns a context carrying the ID of the API request being
// served, which audit records made while serving it then name.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the ID of the API request ctx belongs to, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Record implements AuditLog. A record without a request ID gets the one
// ctx carries. A refusal that repeats one recorded within the window of
// Options.Denials only counts on that record, and so do an actor's
// refusals beyond the cap, together, so no caller floods the log.
func (l *Log) Record(ctx context.Context, r AuditRecord) error {
	if r.ID == "" {
		r.ID = store.NewID(AuditPrefix)
	}
	if r.RequestID == "" {
		r.RequestID = RequestID(ctx)
	}
	if r.Time.IsZero() {
		r.Time = l.now()
	}
	r.Time = r.Time.UTC()
	if r.Scope.Level == "" {
		r.Scope = store.ServerScope()
	}
	var err error
	if r.Outcome == OutcomeDenied {
		err = l.recordDenial(context.WithoutCancel(ctx), &r)
	} else {
		err = l.storage.appendAudit(context.WithoutCancel(ctx), &r)
	}
	if err != nil {
		return fmt.Errorf("events: recording %s: %w", r.Action, err)
	}
	return nil
}

// QueryEvents implements Timeline.
func (l *Log) QueryEvents(ctx context.Context, q Query) ([]Event, string, error) {
	if _, err := q.before(); err != nil {
		return nil, "", err
	}
	return l.storage.queryEvents(ctx, q)
}

// QueryAudit implements AuditLog.
func (l *Log) QueryAudit(ctx context.Context, q Query) ([]AuditRecord, string, error) {
	if _, err := q.before(); err != nil {
		return nil, "", err
	}
	return l.storage.queryAudit(ctx, q)
}

// EventsAfter returns up to limit events published after the one with
// sequence number seq, oldest first. Followers of the timeline call it with
// the Seq of the last event they handled.
func (l *Log) EventsAfter(ctx context.Context, seq int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > store.MaxLimit {
		limit = store.DefaultLimit
	}
	return l.storage.eventsAfter(ctx, seq, limit)
}

// SeqOf returns the sequence number of a kept event, so a live stream can
// resume after the last event its client saw.
func (l *Log) SeqOf(ctx context.Context, id store.ID) (int64, error) {
	return l.storage.seqOf(ctx, id)
}

// LastSeq returns the sequence number of the newest event, or zero.
func (l *Log) LastSeq(ctx context.Context) (int64, error) {
	return l.storage.lastSeq(ctx)
}

// Prune drops what has outlived the retention, and forgets the windows of
// refusals that have ended.
func (l *Log) Prune(ctx context.Context) error {
	now := l.now()
	l.denials.forget(now)
	return l.storage.prune(ctx, l.retention, now)
}

// Run prunes the timeline and the audit log every interval until ctx is
// done.
func (l *Log) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := l.Prune(ctx); err != nil && ctx.Err() == nil {
			l.log.Warn("pruning the timeline and the audit log", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

var patternChars = regexp.MustCompile(`^[a-z0-9_.*?]+$`)

// ValidPattern reports whether p can be used to match event types or audit
// actions: lowercase names, dots, underscores and the wildcards * and ?.
func ValidPattern(p string) bool { return patternChars.MatchString(p) }

// memoryStorage keeps everything in slices, oldest first, and at most max
// of each.
type memoryStorage struct {
	max    int
	mu     sync.RWMutex
	events []Event
	audit  []auditEntry
	seq    int64
}

type auditEntry struct {
	seq    int64
	record AuditRecord
}

func (m *memoryStorage) appendEvent(ctx context.Context, e *Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	e.Seq = m.seq
	m.events = append(m.events, *e)
	if len(m.events) > m.max {
		m.events = slices.Clone(m.events[len(m.events)-m.max:])
	}
	return nil
}

func (m *memoryStorage) appendAudit(ctx context.Context, r *AuditRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.audit = append(m.audit, auditEntry{seq: m.seq, record: *r})
	if len(m.audit) > m.max {
		m.audit = slices.Clone(m.audit[len(m.audit)-m.max:])
	}
	return nil
}

func (m *memoryStorage) repeatAudit(ctx context.Context, id store.ID, n int, last time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.audit) - 1; i >= 0; i-- {
		if r := &m.audit[i].record; r.ID == id {
			r.Repeats += n
			// Records handed out share LastTime, so it is replaced, never
			// changed.
			if t := last.UTC(); r.LastTime == nil || t.After(*r.LastTime) {
				r.LastTime = &t
			}
			return true, nil
		}
	}
	return false, nil
}

func (m *memoryStorage) queryEvents(ctx context.Context, q Query) ([]Event, string, error) {
	before, _ := q.before()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Event
	limit := q.limit()
	for i := len(m.events) - 1; i >= 0; i-- {
		e := m.events[i]
		if (before > 0 && e.Seq >= before) || !q.matchEvent(e) {
			continue
		}
		if len(out) == limit {
			return out, encodeSeq(out[len(out)-1].Seq), nil
		}
		out = append(out, e)
	}
	return out, "", nil
}

func (m *memoryStorage) queryAudit(ctx context.Context, q Query) ([]AuditRecord, string, error) {
	before, _ := q.before()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []AuditRecord
	var last int64
	limit := q.limit()
	for i := len(m.audit) - 1; i >= 0; i-- {
		a := m.audit[i]
		if (before > 0 && a.seq >= before) || !q.matchAudit(a.record) {
			continue
		}
		if len(out) == limit {
			return out, encodeSeq(last), nil
		}
		out = append(out, a.record)
		last = a.seq
	}
	return out, "", nil
}

func (m *memoryStorage) eventsAfter(ctx context.Context, seq int64, limit int) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i, _ := slices.BinarySearchFunc(m.events, seq, func(e Event, s int64) int {
		switch {
		case e.Seq <= s:
			return -1
		}
		return 1
	})
	end := min(i+limit, len(m.events))
	return slices.Clone(m.events[i:end]), nil
}

func (m *memoryStorage) seqOf(ctx context.Context, id store.ID) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].ID == id {
			return m.events[i].Seq, nil
		}
	}
	return 0, store.ErrNotFound.New("what", "Event "+string(id))
}

func (m *memoryStorage) lastSeq(ctx context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.events) == 0 {
		return 0, nil
	}
	return m.events[len(m.events)-1].Seq, nil
}

func (m *memoryStorage) prune(ctx context.Context, r Retention, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = slices.DeleteFunc(m.events, func(e Event) bool { return now.Sub(e.Time) > r.Events })
	if extra := len(m.events) - r.MaxEvents; extra > 0 {
		m.events = slices.Clone(m.events[extra:])
	}
	m.audit = slices.DeleteFunc(m.audit, func(a auditEntry) bool { return now.Sub(a.record.Time) > r.Audit })
	if extra := len(m.audit) - r.MaxAudit; extra > 0 {
		m.audit = slices.Clone(m.audit[extra:])
	}
	return nil
}
