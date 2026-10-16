package sim

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// journalCap bounds the simulated journal; older entries are dropped, as
// journald rotates.
const journalCap = 20000

// logEntry is a journal entry with the namespace it was written to and its
// sequence number, which is what cursors point at.
type logEntry struct {
	system.JournalEntry
	Namespace string `json:"namespace,omitempty"`
	Seq       uint64 `json:"seq"`
}

// Log writes a journal entry as unit's main process would. Programs
// simulated with RegisterProgram use it for their output, and tests for
// log lines they look for.
func (m *Machine) Log(unit string, priority int, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pid := 0
	if rt, ok := m.units[unit]; ok {
		pid = rt.pid
	}
	m.logLocked(unit, identifierOf(unit), pid, priority, message)
	m.changed()
}

func identifierOf(unit string) string {
	return strings.TrimSuffix(strings.TrimSuffix(unit, ".service"), ".socket")
}

// logLocked appends an entry. What running services logged in the meantime
// goes first, so the journal stays in time order. The caller holds m.mu.
func (m *Machine) logLocked(unit, identifier string, pid, priority int, message string) {
	m.generateActivity()
	m.nextLog++
	ns := ""
	if content, ok := m.unitFile(unit); ok {
		ns = unitDirective(content, "LogNamespace")
	}
	m.journal = append(m.journal, logEntry{
		JournalEntry: system.JournalEntry{
			Time: m.now(), Unit: unit, Identifier: identifier, PID: pid, Priority: priority,
			Message: message, Cursor: "s=" + strconv.FormatUint(m.nextLog, 10),
		},
		Namespace: ns,
		Seq:       m.nextLog,
	})
	if len(m.journal) > journalCap+journalCap/10 {
		m.journal = append([]logEntry(nil), m.journal[len(m.journal)-journalCap:]...)
	}
}

// logSystemd writes what systemd itself logs about a unit. The caller holds
// m.mu.
func (m *Machine) logSystemd(unit string, priority int, format string, args ...any) {
	m.logLocked(unit, "systemd", 1, priority, fmt.Sprintf(format, args...))
}

// describe returns a unit's description, as systemd uses it in messages.
func (m *Machine) describe(unit string) string {
	if content, ok := m.unitFile(unit); ok {
		if d := unitDirective(content, "Description"); d != "" {
			return d
		}
	}
	return unit
}

// activityLines are what simulated services log while they run.
var activityLines = []string{
	"Accepted connection from 127.0.0.1",
	"GET / HTTP/1.1 200",
	"GET /health HTTP/1.1 200",
	"Processed request in 12ms",
	"Reloaded configuration cache",
	"Worker pool: 2 busy, 6 idle",
}

// generateActivity adds the lines running services would have logged since
// they were last looked at, a few per minute each. The caller holds m.mu.
func (m *Machine) generateActivity() {
	now := m.now()
	for name, rt := range m.units {
		if rt.active != "active" || rt.pid == 0 || !strings.HasSuffix(name, ".service") {
			continue
		}
		last, ok := m.activity[name]
		if !ok || last.Before(rt.since) {
			last = rt.since
		}
		ns := ""
		if content, ok := m.unitFile(name); ok {
			ns = unitDirective(content, "LogNamespace")
		}
		period := time.Duration(20+hash(name)%40) * time.Second
		for i := 0; last.Add(period).Before(now) && i < 50; i++ {
			last = last.Add(period)
			line := activityLines[(hash(name)+uint64(last.Unix()))%uint64(len(activityLines))]
			m.nextLog++
			m.journal = append(m.journal, logEntry{
				JournalEntry: system.JournalEntry{
					Time: last, Unit: name, Identifier: identifierOf(name), PID: rt.pid, Priority: system.PriorityInfo,
					Message: line, Cursor: "s=" + strconv.FormatUint(m.nextLog, 10),
				},
				Namespace: ns,
				Seq:       m.nextLog,
			})
		}
		if last.Add(period).Before(now) {
			// Skip what wasn't generated rather than falling behind for ever.
			last = now
		}
		m.activity[name] = last
	}
}

type simJournal struct{ m *Machine }

func (j simJournal) Read(ctx context.Context, q system.JournalQuery) (system.JournalPage, error) {
	if err := q.Validate(); err != nil {
		return system.JournalPage{}, err
	}
	m := j.m
	if err := m.lock(ctx); err != nil {
		return system.JournalPage{}, err
	}
	defer m.mu.Unlock()
	m.generateActivity()
	var after uint64
	if q.After != "" {
		n, err := strconv.ParseUint(strings.TrimPrefix(q.After, "s="), 10, 64)
		if err != nil || !strings.HasPrefix(q.After, "s=") {
			return system.JournalPage{}, invalid("journal cursor %q", q.After)
		}
		after = n
	}
	var matching []logEntry
	for _, e := range m.journal {
		if e.Seq <= after || e.Namespace != q.Namespace || !q.Matches(e.JournalEntry) {
			continue
		}
		matching = append(matching, e)
	}
	limit := q.EffectiveLimit()
	switch {
	case q.Newest:
		// Newest first.
		for i, k := 0, len(matching)-1; i < k; i, k = i+1, k-1 {
			matching[i], matching[k] = matching[k], matching[i]
		}
		matching = matching[:min(limit, len(matching))]
	case q.After != "":
		matching = matching[:min(limit, len(matching))]
	default:
		matching = matching[max(0, len(matching)-limit):]
	}
	page := system.JournalPage{Entries: make([]system.JournalEntry, len(matching))}
	for i, e := range matching {
		page.Entries[i] = e.JournalEntry
	}
	if len(matching) > 0 {
		page.Cursor = matching[len(matching)-1].Cursor
	}
	return page, nil
}
