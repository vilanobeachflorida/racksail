package events

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// ID prefixes for events and audit records, reserved in the store.
const (
	EventPrefix = store.PrefixEvent
	AuditPrefix = store.PrefixAudit
)

// Severity ranks events for alerts and the attention inbox.
type Severity string

// Severities, from least to most urgent.
const (
	SeverityInfo     Severity = "info"
	SeverityNotice   Severity = "notice"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

var severityRank = map[Severity]int{SeverityInfo: 0, SeverityNotice: 1, SeverityWarning: 2, SeverityError: 3, SeverityCritical: 4}

// AtLeast reports whether s is as urgent as min.
func (s Severity) AtLeast(min Severity) bool { return severityRank[s] >= severityRank[min] }

// Event is something that happened.
type Event struct {
	ID       store.ID  `json:"id"`
	Type     string    `json:"type" doc:"<kind>.<past-tense verb>, such as web.site.created"`
	Time     time.Time `json:"time"`
	Severity Severity  `json:"severity" enum:"info,notice,warning,error,critical"`
	// Subject is the resource the event is about.
	Subject store.ID    `json:"subject,omitempty"`
	Kind    string      `json:"kind,omitempty"`
	Scope   store.Scope `json:"scope"`
	Actor   *auth.Actor `json:"actor,omitempty"`
	// ChangeSet and Job link the event to what caused it.
	ChangeSet store.ID `json:"changeSet,omitempty"`
	Job       store.ID `json:"job,omitempty"`
	// Message is an English summary; the UI renders its own from the type.
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var typePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*){2,}$`)

// ValidType reports whether name looks like <module>.<name>.<verb>.
func ValidType(name string) bool { return typePattern.MatchString(name) }

// Type describes an event type for webhooks, automations and the docs.
type Type struct {
	Name        string
	Description string
	Severity    Severity
	// Data is a zero value of the event's data, or nil.
	Data any
}

// KindTypes returns the event types every kind has: created, updated,
// deleted, one per action, and failed for when its reconciler fails.
func KindTypes(k store.Kind) []Type {
	info := k.Info()
	types := []Type{
		{Name: info.Name + ".created", Description: info.Title + " created.", Severity: SeverityInfo},
		{Name: info.Name + ".updated", Description: info.Title + " changed.", Severity: SeverityInfo},
		{Name: info.Name + ".deleted", Description: info.Title + " deleted.", Severity: SeverityNotice},
		{Name: info.Name + ".failed", Description: "Applying a " + strings.ToLower(info.Title) + " failed.", Severity: SeverityError},
	}
	for _, a := range info.Actions {
		types = append(types, Type{Name: a.EventName(info.Name), Description: a.Summary, Severity: SeverityInfo, Data: a.Output})
	}
	return types
}

// Filter selects events.
type Filter struct {
	// Types are glob patterns such as "web.site.*".
	Types       []string
	Scope       *store.Scope
	Subject     store.ID
	MinSeverity Severity
}

// Match reports whether e passes the filter.
func (f Filter) Match(e Event) bool {
	if len(f.Types) > 0 && !slices.ContainsFunc(f.Types, func(p string) bool {
		ok, _ := path.Match(p, e.Type)
		return ok || (strings.HasSuffix(p, ".*") && strings.HasPrefix(e.Type, strings.TrimSuffix(p, "*")))
	}) {
		return false
	}
	if f.Scope != nil && e.Scope != *f.Scope {
		return false
	}
	if f.Subject != "" && e.Subject != f.Subject {
		return false
	}
	return f.MinSeverity == "" || e.Severity.AtLeast(f.MinSeverity)
}

// Bus delivers events to subscribers.
type Bus interface {
	// Publish delivers an event. It fills in the ID and time when missing.
	Publish(ctx context.Context, e Event) error
	// Subscribe returns events matching f. Slow subscribers miss events
	// rather than slowing everyone down; they can catch up from the timeline.
	Subscribe(f Filter, buffer int) *Subscription
}

// Subscription receives events until it is closed.
type Subscription struct {
	C      <-chan Event
	ch     chan Event
	filter Filter
	close  func()
	once   sync.Once
	// Dropped counts events that didn't fit in the buffer.
	dropped int64
	mu      sync.Mutex
}

// Close stops the subscription.
func (s *Subscription) Close() { s.once.Do(s.close) }

// Dropped returns how many events the subscriber missed.
func (s *Subscription) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Outcome is the result of an audited action.
type Outcome string

// Outcomes.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeDenied  Outcome = "denied"
	OutcomeFailed  Outcome = "failed"
)

// AuditRecord records one action and who took it.
type AuditRecord struct {
	ID    store.ID   `json:"id"`
	Time  time.Time  `json:"time"`
	Actor auth.Actor `json:"actor"`
	// Action is a permission name such as web.site.update or auth.login.
	Action    string      `json:"action"`
	Kind      string      `json:"kind,omitempty"`
	Subject   store.ID    `json:"subject,omitempty"`
	Scope     store.Scope `json:"scope"`
	ChangeSet store.ID    `json:"changeSet,omitempty"`
	Outcome   Outcome     `json:"outcome" enum:"success,denied,failed"`
	// Reason explains a denial or failure.
	Reason  string `json:"reason,omitempty"`
	Summary string `json:"summary"`
	// Details holds a redacted summary of what changed.
	Details json.RawMessage `json:"details,omitempty"`
}

// AuditLog stores audit records. Hooks can't turn it off.
type AuditLog interface {
	Record(ctx context.Context, r AuditRecord) error
	QueryAudit(ctx context.Context, q Query) ([]AuditRecord, string, error)
}

// Timeline stores events for the event timeline.
type Timeline interface {
	QueryEvents(ctx context.Context, q Query) ([]Event, string, error)
}

// Query selects timeline events or audit records, newest first.
type Query struct {
	Since   time.Time
	Until   time.Time
	Subject store.ID
	Scope   *store.Scope
	// Types or actions as glob patterns.
	Types  []string
	Cursor string
	Limit  int
}

// Memory is an in-memory bus, timeline and audit log with bounded retention.
type Memory struct {
	mu     sync.RWMutex
	subs   map[*Subscription]bool
	events []Event
	audit  []AuditRecord
	retain int
	now    func() time.Time
}

// NewMemory keeps the last retain events and audit records.
func NewMemory(retain int) *Memory {
	if retain <= 0 {
		retain = 10000
	}
	return &Memory{subs: map[*Subscription]bool{}, retain: retain, now: time.Now}
}

// Publish implements Bus and records the event in the timeline.
func (m *Memory) Publish(ctx context.Context, e Event) error {
	if !ValidType(e.Type) {
		return fmt.Errorf("events: invalid event type %q", e.Type)
	}
	if e.ID == "" {
		e.ID = store.NewID(EventPrefix)
	}
	if e.Time.IsZero() {
		e.Time = m.now().UTC()
	}
	if e.Severity == "" {
		e.Severity = SeverityInfo
	}
	m.mu.Lock()
	m.events = append(m.events, e)
	if len(m.events) > m.retain {
		m.events = slices.Clone(m.events[len(m.events)-m.retain:])
	}
	subs := make([]*Subscription, 0, len(m.subs))
	for s := range m.subs {
		subs = append(subs, s)
	}
	m.mu.Unlock()
	for _, s := range subs {
		if !s.filter.Match(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
		}
	}
	return nil
}

// Subscribe implements Bus.
func (m *Memory) Subscribe(f Filter, buffer int) *Subscription {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	s := &Subscription{C: ch, ch: ch, filter: f}
	s.close = func() {
		m.mu.Lock()
		delete(m.subs, s)
		m.mu.Unlock()
		close(ch)
	}
	m.mu.Lock()
	m.subs[s] = true
	m.mu.Unlock()
	return s
}

// Record implements AuditLog.
func (m *Memory) Record(ctx context.Context, r AuditRecord) error {
	if r.ID == "" {
		r.ID = store.NewID(AuditPrefix)
	}
	if r.Time.IsZero() {
		r.Time = m.now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, r)
	if len(m.audit) > m.retain {
		m.audit = slices.Clone(m.audit[len(m.audit)-m.retain:])
	}
	return nil
}

func (q Query) limit() int {
	if q.Limit <= 0 || q.Limit > store.MaxLimit {
		return store.DefaultLimit
	}
	return q.Limit
}

func (q Query) matches(id store.ID, t time.Time, subject store.ID, scope store.Scope, name string) bool {
	if q.Cursor != "" {
		before, err := store.DecodeCursor(q.Cursor)
		if err != nil || id >= before {
			return false
		}
	}
	if !q.Since.IsZero() && t.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !t.Before(q.Until) {
		return false
	}
	if q.Subject != "" && subject != q.Subject {
		return false
	}
	if q.Scope != nil && scope != *q.Scope {
		return false
	}
	return len(q.Types) == 0 || (Filter{Types: q.Types}).Match(Event{Type: name})
}

// QueryEvents implements Timeline.
func (m *Memory) QueryEvents(ctx context.Context, q Query) ([]Event, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Event
	for i := len(m.events) - 1; i >= 0; i-- {
		e := m.events[i]
		if q.matches(e.ID, e.Time, e.Subject, e.Scope, e.Type) {
			out = append(out, e)
		}
	}
	return page(out, q.limit(), func(e Event) store.ID { return e.ID })
}

// QueryAudit implements AuditLog.
func (m *Memory) QueryAudit(ctx context.Context, q Query) ([]AuditRecord, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []AuditRecord
	for i := len(m.audit) - 1; i >= 0; i-- {
		r := m.audit[i]
		if q.matches(r.ID, r.Time, r.Subject, r.Scope, r.Action) {
			out = append(out, r)
		}
	}
	return page(out, q.limit(), func(r AuditRecord) store.ID { return r.ID })
}

func page[T any](items []T, limit int, id func(T) store.ID) ([]T, string, error) {
	if len(items) <= limit {
		return items, "", nil
	}
	items = items[:limit]
	return items, store.EncodeCursor(id(items[limit-1])), nil
}
