package system

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Journal reads the systemd journal.
type Journal interface {
	// Read returns entries matching the query, oldest first unless the
	// query asks for the newest first.
	Read(ctx context.Context, q JournalQuery) (JournalPage, error)
}

// JournalQuery selects journal entries.
type JournalQuery struct {
	// Units limits entries to these units, both what they logged and what
	// systemd logged about them.
	Units []string `json:"units,omitempty" doc:"Units whose entries to return"`
	// Namespace reads an account's own journal namespace instead of the
	// system journal.
	Namespace string    `json:"namespace,omitempty" doc:"Journal namespace, such as an account's"`
	Since     time.Time `json:"since,omitzero" doc:"Entries at or after this time"`
	Until     time.Time `json:"until,omitzero" doc:"Entries before this time"`
	// Priority keeps entries of this priority or more severe: 0 is
	// emergency, 3 error, 4 warning, 6 info and 7 debug.
	Priority *int `json:"priority,omitempty" doc:"Most verbose priority to include, from 0 (emergency) to 7 (debug)"`
	// Grep keeps entries whose message contains this text, ignoring case.
	Grep string `json:"grep,omitempty" doc:"Text the message must contain, ignoring case"`
	// After continues from the cursor of an earlier page.
	After string `json:"after,omitempty" doc:"Cursor to continue after"`
	// Limit caps the entries returned; zero means DefaultJournalLimit.
	Limit int `json:"limit,omitempty" doc:"Most entries to return"`
	// Newest returns the newest entries first.
	Newest bool `json:"newest,omitempty" doc:"Return the newest entries first"`
}

// JournalPage is one page of journal entries.
type JournalPage struct {
	Entries []JournalEntry `json:"entries"`
	// Cursor continues after the last entry of this page.
	Cursor string `json:"cursor,omitempty"`
}

// JournalEntry is one journal record.
type JournalEntry struct {
	Time       time.Time `json:"time"`
	Unit       string    `json:"unit,omitempty"`
	Identifier string    `json:"identifier,omitempty" doc:"The program that logged, SYSLOG_IDENTIFIER"`
	PID        int       `json:"pid,omitempty"`
	Priority   int       `json:"priority"`
	Message    string    `json:"message"`
	Cursor     string    `json:"cursor"`
}

// Journal limits.
const (
	DefaultJournalLimit = 200
	MaxJournalLimit     = 5000
)

var (
	namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	cursorPattern    = regexp.MustCompile(`^[A-Za-z0-9=;_.-]{1,512}$`)
)

// Validate checks a query before it reaches journalctl or the simulation.
func (q JournalQuery) Validate() error {
	for _, u := range q.Units {
		if !ValidUnitName(u) {
			return fmt.Errorf("unit name %q: %w", u, ErrInvalid)
		}
	}
	if q.Namespace != "" && !namespacePattern.MatchString(q.Namespace) {
		return fmt.Errorf("journal namespace %q: %w", q.Namespace, ErrInvalid)
	}
	if q.Priority != nil && (*q.Priority < 0 || *q.Priority > 7) {
		return fmt.Errorf("journal priority %d: %w", *q.Priority, ErrInvalid)
	}
	if len(q.Grep) > 256 || !utf8.ValidString(q.Grep) || strings.ContainsAny(q.Grep, "\x00\n") {
		return fmt.Errorf("journal search text: %w", ErrInvalid)
	}
	if q.After != "" && !cursorPattern.MatchString(q.After) {
		return fmt.Errorf("journal cursor: %w", ErrInvalid)
	}
	if q.Limit < 0 || q.Limit > MaxJournalLimit {
		return fmt.Errorf("journal limit %d must be between 0 and %d: %w", q.Limit, MaxJournalLimit, ErrInvalid)
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && !q.Until.After(q.Since) {
		return fmt.Errorf("journal range ends before it starts: %w", ErrInvalid)
	}
	return nil
}

// EffectiveLimit returns the number of entries to return.
func (q JournalQuery) EffectiveLimit() int {
	if q.Limit == 0 {
		return DefaultJournalLimit
	}
	return q.Limit
}

// Matches reports whether an entry passes the query's filters other than
// the cursor and the limit. Backends that filter in memory use it.
func (q JournalQuery) Matches(e JournalEntry) bool {
	if len(q.Units) > 0 {
		found := false
		for _, u := range q.Units {
			found = found || u == e.Unit
		}
		if !found {
			return false
		}
	}
	if !q.Since.IsZero() && e.Time.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !e.Time.Before(q.Until) {
		return false
	}
	if q.Priority != nil && e.Priority > *q.Priority {
		return false
	}
	if q.Grep != "" && !strings.Contains(strings.ToLower(e.Message), strings.ToLower(q.Grep)) {
		return false
	}
	return true
}

// Journal priorities, as syslog defines them.
const (
	PriorityEmergency = 0
	PriorityAlert     = 1
	PriorityCritical  = 2
	PriorityError     = 3
	PriorityWarning   = 4
	PriorityNotice    = 5
	PriorityInfo      = 6
	PriorityDebug     = 7
)
