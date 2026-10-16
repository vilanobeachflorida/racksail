package events

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
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

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool { _, ok := severityRank[s]; return ok }

// Event is something that happened.
type Event struct {
	ID       store.ID  `json:"id" doc:"The event's ID, which starts with evt_"`
	Type     string    `json:"type" doc:"<kind>.<past-tense verb>, such as web.site.created"`
	Time     time.Time `json:"time" doc:"When it happened"`
	Severity Severity  `json:"severity" enum:"info,notice,warning,error,critical" doc:"How urgent it is"`
	// Subject is the resource the event is about.
	Subject store.ID    `json:"subject,omitempty" doc:"The resource the event is about"`
	Kind    string      `json:"kind,omitempty" doc:"The kind of the subject"`
	Scope   store.Scope `json:"scope" doc:"Who the event belongs to, which decides who may see it"`
	Actor   *auth.Actor `json:"actor,omitempty" doc:"Who caused it, and what acted for them"`
	// ChangeSet and Job link the event to what caused it.
	ChangeSet store.ID `json:"changeSet,omitempty" doc:"The change set that caused it"`
	Job       store.ID `json:"job,omitempty" doc:"The job that caused it"`
	// Message is an English summary; the UI renders its own from the type.
	Message string          `json:"message" doc:"What happened, in English"`
	Data    json.RawMessage `json:"data,omitempty" doc:"What the event carries, as its type describes"`
	// Seq orders events as the timeline stored them. It is set when an
	// event is published and never reused, so it is what followers of the
	// timeline, such as webhooks and live streams, keep their place by.
	Seq int64 `json:"-"`
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
// deleted, failed for when its reconciler fails, drifted for when the machine
// stopped matching it, and one per action.
func KindTypes(k store.Kind) []Type {
	info := k.Info()
	title := strings.ToLower(info.Title)
	types := []Type{
		{Name: info.Name + ".created", Description: info.Title + " created.", Severity: SeverityInfo},
		{Name: info.Name + ".updated", Description: info.Title + " changed.", Severity: SeverityInfo},
		{Name: info.Name + ".deleted", Description: info.Title + " deleted.", Severity: SeverityNotice},
		{Name: info.Name + ".failed", Description: "Applying a " + title + " failed.", Severity: SeverityError},
		{Name: info.Name + ".drifted", Description: "The server no longer matched a " + title + "'s settings, and Racksail put them back.", Severity: SeverityWarning, Data: Drift{}},
	}
	for _, a := range info.Actions {
		types = append(types, Type{Name: a.EventName(info.Name), Description: a.Summary, Severity: SeverityInfo, Data: a.Output})
	}
	return types
}

// Drift is the data of <kind>.drifted events.
type Drift struct {
	// Source is resync when a periodic reconcile found and corrected the
	// drift, or agent when the agent reported it.
	Source string `json:"source" enum:"resync,agent" doc:"What noticed the drift"`
	// Corrected says whether Racksail has put the settings back yet.
	Corrected bool `json:"corrected" doc:"Whether the settings were put back"`
	// Changes lists what was put back, or what the agent saw change.
	Changes []string `json:"changes,omitempty" doc:"What differed"`
}

// Filter selects events.
type Filter struct {
	// Types are glob patterns such as "web.site.*".
	Types []string
	Scope *store.Scope
	// Scopes keeps events of any of these owners when it isn't empty.
	Scopes      []store.Scope
	Subject     store.ID
	MinSeverity Severity
}

// matchType reports whether an event type matches a glob pattern. A
// pattern ending in .* also matches any deeper type.
func matchType(pattern, name string) bool {
	ok, _ := path.Match(pattern, name)
	return ok || (strings.HasSuffix(pattern, ".*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")))
}

// MatchTypes reports whether name matches one of the patterns, or whether
// there are none.
func MatchTypes(patterns []string, name string) bool {
	return len(patterns) == 0 || slices.ContainsFunc(patterns, func(p string) bool { return matchType(p, name) })
}

// Match reports whether e passes the filter.
func (f Filter) Match(e Event) bool {
	switch {
	case !MatchTypes(f.Types, e.Type):
		return false
	case f.Scope != nil && e.Scope != *f.Scope:
		return false
	case len(f.Scopes) > 0 && !slices.Contains(f.Scopes, e.Scope):
		return false
	case f.Subject != "" && e.Subject != f.Subject:
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
	// dropped counts events that didn't fit in the buffer.
	dropped atomic.Int64
}

// Close stops the subscription. C is closed once no event can be sent to it
// any more.
func (s *Subscription) Close() { s.once.Do(s.close) }

// Dropped returns how many events the subscriber missed.
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

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
	ID    store.ID   `json:"id" doc:"The record's ID"`
	Time  time.Time  `json:"time" doc:"When it happened"`
	Actor auth.Actor `json:"actor" doc:"Who did it, and what acted for them"`
	// Action is a permission name such as web.site.update or auth.login.
	Action    string      `json:"action" doc:"What was done, named like the permission it needs, such as web.site.update"`
	Kind      string      `json:"kind,omitempty" doc:"The kind of the resource acted on"`
	Subject   store.ID    `json:"subject,omitempty" doc:"The resource acted on"`
	Scope     store.Scope `json:"scope" doc:"Who the resource belongs to, which decides who may see the record"`
	ChangeSet store.ID    `json:"changeSet,omitempty" doc:"The change set it was part of"`
	Outcome   Outcome     `json:"outcome" enum:"success,denied,failed" doc:"How it went: it was done, it was refused, or it was allowed but failed"`
	// Reason explains a denial or failure.
	Reason  string `json:"reason,omitempty" doc:"Why it was refused or failed"`
	Summary string `json:"summary" doc:"What was done, in English"`
	// Details holds a redacted summary of what changed.
	Details json.RawMessage `json:"details,omitempty" doc:"What changed, with secrets left out"`
	// RequestID is the API request the action came through, when it came
	// through one, so the record can be matched with the access log.
	RequestID string `json:"requestId,omitempty" doc:"The API request it came through, as the access log names it"`
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
	Kind    string
	Scope   *store.Scope
	// Scopes keeps entries of any of these owners when it isn't empty.
	Scopes    []store.Scope
	ChangeSet store.ID
	// Job keeps events of one job; audit records have none.
	Job store.ID
	// Types or actions as glob patterns.
	Types []string
	// MinSeverity keeps events at least this urgent.
	MinSeverity Severity
	// Actor keeps audit records of what this user, token, automation or
	// add-on did, or did for this user.
	Actor store.ID
	// Outcome keeps audit records with this outcome.
	Outcome Outcome
	Cursor  string
	Limit   int
}

func (q Query) limit() int {
	if q.Limit <= 0 || q.Limit > store.MaxLimit {
		return store.DefaultLimit
	}
	return q.Limit
}

// before returns the sequence number the cursor continues from: entries
// older than it.
func (q Query) before() (int64, error) {
	if q.Cursor == "" {
		return 0, nil
	}
	return decodeSeq(q.Cursor)
}

// CheckCursor refuses a cursor queries wouldn't accept, for callers that
// must refuse it before they start answering, such as an export that
// streams.
func CheckCursor(cursor string) error {
	_, err := Query{Cursor: cursor}.before()
	return err
}

func encodeSeq(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(seq, 10)))
}

func decodeSeq(cursor string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err == nil {
		if seq, err := strconv.ParseInt(string(raw), 10, 64); err == nil && seq > 0 {
			return seq, nil
		}
	}
	return 0, i18n.ErrBadRequest.New("reason", "the cursor is malformed")
}

// matchEvent reports whether an event passes a query, apart from the cursor.
func (q Query) matchEvent(e Event) bool {
	switch {
	case !q.Since.IsZero() && e.Time.Before(q.Since):
		return false
	case !q.Until.IsZero() && !e.Time.Before(q.Until):
		return false
	case q.Subject != "" && e.Subject != q.Subject:
		return false
	case q.Kind != "" && e.Kind != q.Kind:
		return false
	case q.Scope != nil && e.Scope != *q.Scope:
		return false
	case len(q.Scopes) > 0 && !slices.Contains(q.Scopes, e.Scope):
		return false
	case q.ChangeSet != "" && e.ChangeSet != q.ChangeSet:
		return false
	case q.Job != "" && e.Job != q.Job:
		return false
	case q.MinSeverity != "" && !e.Severity.AtLeast(q.MinSeverity):
		return false
	}
	return MatchTypes(q.Types, e.Type)
}

// matchAudit reports whether an audit record passes a query, apart from the
// cursor.
func (q Query) matchAudit(r AuditRecord) bool {
	switch {
	case !q.Since.IsZero() && r.Time.Before(q.Since):
		return false
	case !q.Until.IsZero() && !r.Time.Before(q.Until):
		return false
	case q.Subject != "" && r.Subject != q.Subject:
		return false
	case q.Kind != "" && r.Kind != q.Kind:
		return false
	case q.Scope != nil && r.Scope != *q.Scope:
		return false
	case len(q.Scopes) > 0 && !slices.Contains(q.Scopes, r.Scope):
		return false
	case q.ChangeSet != "" && r.ChangeSet != q.ChangeSet:
		return false
	case q.Actor != "" && r.Actor.ID != q.Actor && r.Actor.User != q.Actor:
		return false
	case q.Outcome != "" && r.Outcome != q.Outcome:
		return false
	}
	return MatchTypes(q.Types, r.Action)
}
