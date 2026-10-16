package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

const progJournalctl = "/usr/bin/journalctl"

type journal struct{ h *host }

// journalTime renders a time as journalctl reads it: seconds since the
// epoch after an @.
func journalTime(t time.Time) string {
	return fmt.Sprintf("@%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

func journalArgs(q system.JournalQuery) []string {
	args := []string{"--output=json", "--no-pager", "--quiet"}
	if q.Namespace != "" {
		args = append(args, "--namespace="+q.Namespace)
	}
	for _, u := range q.Units {
		args = append(args, "--unit="+u)
	}
	if !q.Since.IsZero() {
		args = append(args, "--since="+journalTime(q.Since))
	}
	if !q.Until.IsZero() {
		args = append(args, "--until="+journalTime(q.Until))
	}
	if q.Priority != nil {
		args = append(args, "--priority="+strconv.Itoa(*q.Priority))
	}
	if q.Grep != "" {
		// journalctl takes a regular expression; the query is plain text.
		args = append(args, "--grep="+regexp.QuoteMeta(q.Grep), "--case-sensitive=false")
	}
	switch {
	case q.After != "":
		args = append(args, "--after-cursor="+q.After)
		if q.Newest {
			args = append(args, "--reverse")
		}
	case q.Newest:
		args = append(args, "--reverse", "--lines="+strconv.Itoa(q.EffectiveLimit()))
	default:
		args = append(args, "--lines="+strconv.Itoa(q.EffectiveLimit()))
	}
	return args
}

func (j *journal) Read(ctx context.Context, q system.JournalQuery) (system.JournalPage, error) {
	if err := q.Validate(); err != nil {
		return system.JournalPage{}, err
	}
	out, err := j.h.run.Run(ctx, system.Command{Path: progJournalctl, Args: journalArgs(q), Timeout: time.Minute, MaxOutput: 32 << 20})
	// journalctl exits with 1 when nothing matches a --grep.
	var exitErr *system.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode == 1 && len(bytes.TrimSpace(out.Stdout)) == 0) {
		return system.JournalPage{}, fmt.Errorf("reading the journal: %w", err)
	}
	return parseJournal(out.Stdout, q.EffectiveLimit()), nil
}

// parseJournal reads journalctl's JSON lines, at most limit entries. A
// line cut off by the output limit is ignored.
func parseJournal(out []byte, limit int) system.JournalPage {
	page := system.JournalPage{Entries: []system.JournalEntry{}}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(page.Entries) >= limit {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Realtime   string          `json:"__REALTIME_TIMESTAMP"`
			Cursor     string          `json:"__CURSOR"`
			Unit       string          `json:"_SYSTEMD_UNIT"`
			UnitAbout  string          `json:"UNIT"`
			Identifier string          `json:"SYSLOG_IDENTIFIER"`
			PID        string          `json:"_PID"`
			Priority   string          `json:"PRIORITY"`
			Message    json.RawMessage `json:"MESSAGE"`
		}
		if json.Unmarshal(line, &raw) != nil {
			continue
		}
		usec, _ := strconv.ParseInt(raw.Realtime, 10, 64)
		e := system.JournalEntry{
			Time:       time.UnixMicro(usec).UTC(),
			Unit:       raw.Unit,
			Identifier: raw.Identifier,
			PID:        atoi(raw.PID),
			Priority:   system.PriorityInfo,
			Message:    journalMessage(raw.Message),
			Cursor:     raw.Cursor,
		}
		// systemd's own messages about a unit carry it in UNIT.
		if raw.UnitAbout != "" && (e.Unit == "" || e.Unit == "init.scope") {
			e.Unit = raw.UnitAbout
		}
		if p, err := strconv.Atoi(raw.Priority); err == nil {
			e.Priority = p
		}
		page.Entries = append(page.Entries, e)
		page.Cursor = e.Cursor
	}
	return page
}

// journalMessage decodes MESSAGE, which journalctl writes as an array of
// bytes when it isn't valid text.
func journalMessage(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []int
	if json.Unmarshal(raw, &b) == nil {
		buf := make([]byte, 0, len(b))
		for _, c := range b {
			buf = append(buf, byte(c))
		}
		return strings.ToValidUTF8(string(buf), "�")
	}
	return ""
}
