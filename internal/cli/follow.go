package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/vilanobeachflorida/racksail/internal/api"
)

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	ID    string
	Event string
	Data  []byte
}

// readEvents reads Server-Sent Events until the stream ends or fn fails.
func readEvents(r io.Reader, fn func(sseEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var ev sseEvent
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				ev.Data = []byte(strings.Join(data, "\n"))
				if ev.Event == "" {
					ev.Event = "message"
				}
				if err := fn(ev); err != nil {
					return err
				}
			}
			ev, data = sseEvent{}, nil
		case strings.HasPrefix(line, ":"):
		default:
			name, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch name {
			case "id":
				ev.ID = value
			case "event":
				ev.Event = value
			case "data":
				data = append(data, value)
			}
		}
	}
	return sc.Err()
}

// jobView is what following a job needs of it.
type jobView struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Progress struct {
		Done    int64  `json:"done"`
		Total   int64  `json:"total"`
		Message string `json:"message"`
	} `json:"progress"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	ChangeSet  string     `json:"changeSet"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	raw        []byte
}

func (j *jobView) done() bool {
	return j != nil && (j.State == "succeeded" || j.State == "failed" || j.State == "canceled")
}

// logLine is one line of a job's log.
type logLine struct {
	Seq     int64          `json:"seq"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs"`
}

func (l logLine) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s %-5s %s", l.Time.Local().Format("15:04:05"), l.Level, l.Message)
	keys := make([]string, 0, len(l.Attrs))
	for k := range l.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, l.Attrs[k])
	}
	return b.String()
}

// reconnects is how often following a stream picks it up again after it
// drops, as when the panel restarts.
const reconnects = 5

// followJob shows a job's log and progress until it ends, and returns the
// job as it ended. quiet leaves the log out, for JSON and YAML output.
func followJob(ctx context.Context, c *Client, id string, w io.Writer, status *statusLine, quiet bool) (*jobView, error) {
	var job *jobView
	lastID := ""
	failures := 0
	for {
		h := http.Header{}
		if lastID != "" {
			h.Set("Last-Event-ID", lastID)
		}
		resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: api.Prefix + "/jobs/" + url.PathEscape(id) + "/stream", Header: h, Accept: "text/event-stream"})
		if err == nil {
			err = readEvents(resp.Body, func(ev sseEvent) error {
				if ev.ID != "" {
					lastID = ev.ID
				}
				switch ev.Event {
				case "log":
					var l logLine
					if json.Unmarshal(ev.Data, &l) == nil && !quiet {
						status.above(func() { fmt.Fprintln(w, l) })
					}
				case "job":
					j := &jobView{raw: ev.Data}
					if json.Unmarshal(ev.Data, j) == nil {
						job = j
						status.show(job)
					}
				}
				return nil
			})
			resp.Body.Close()
		}
		if job.done() {
			status.clear()
			return job, nil
		}
		if ctx.Err() != nil {
			status.clear()
			return job, stopped(id)
		}
		var p *Problem
		if errors.As(err, &p) {
			status.clear()
			return job, err
		}
		if failures++; failures > reconnects {
			status.clear()
			if err == nil {
				err = errors.New("the stream ended early")
			}
			return job, fmt.Errorf("lost track of job %s: %w; follow it again with racksail job watch %s", id, err, id)
		}
		select {
		case <-ctx.Done():
			status.clear()
			return job, stopped(id)
		case <-time.After(time.Duration(failures) * time.Second):
		}
	}
}

func stopped(id string) error {
	return fmt.Errorf("stopped following job %s, which goes on; follow it again with racksail job watch %s", id, id)
}

// jobResult turns how a job ended into what the command says: nothing for
// one that succeeded, an error for one that didn't.
func jobResult(job *jobView) error {
	switch job.State {
	case "succeeded":
		return nil
	case "canceled":
		return fmt.Errorf("%s was canceled", job.Title)
	}
	if job.Error != nil {
		return fmt.Errorf("%s failed: %s (%s)", job.Title, job.Error.Message, job.Error.Code)
	}
	return fmt.Errorf("%s failed", job.Title)
}

// tookFor says how long a finished job ran.
func tookFor(job *jobView) string {
	if job.StartedAt == nil || job.FinishedAt == nil {
		return ""
	}
	d := job.FinishedAt.Sub(*job.StartedAt)
	if d < time.Second {
		return " in " + d.Round(time.Millisecond).String()
	}
	return " in " + d.Round(100*time.Millisecond).String()
}

// statusLine is the line at the bottom of a terminal that shows a job's
// progress, redrawn as it changes, with log lines printed above it. On
// anything but a terminal it shows nothing.
type statusLine struct {
	mu    sync.Mutex
	w     *os.File
	text  string
	frame int
	stop  chan struct{}
}

var spinner = []string{"|", "/", "-", `\`}

// newStatusLine shows progress on w when it is a terminal.
func newStatusLine(w io.Writer) *statusLine {
	s := &statusLine{}
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		s.w = f
		s.stop = make(chan struct{})
		go s.spin()
	}
	return s
}

func (s *statusLine) spin() {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.mu.Lock()
			s.frame++
			s.draw()
			s.mu.Unlock()
		}
	}
}

// show puts a job's progress on the line.
func (s *statusLine) show(job *jobView) {
	if s == nil || s.w == nil {
		return
	}
	text := job.Title
	if job.State == "queued" {
		text += ": waiting to start"
	}
	if p := job.Progress; p.Total > 0 {
		text += fmt.Sprintf(" %d/%d", p.Done, p.Total)
	}
	if job.Progress.Message != "" {
		text += ": " + job.Progress.Message
	}
	s.mu.Lock()
	s.text = text
	s.draw()
	s.mu.Unlock()
}

// above writes something above the line.
func (s *statusLine) above(write func()) {
	if s == nil || s.w == nil {
		write()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(s.w, "\r\033[K")
	write()
	s.draw()
}

func (s *statusLine) draw() {
	if s.text == "" {
		return
	}
	line := spinner[s.frame%len(spinner)] + " " + s.text
	if width, _, err := term.GetSize(int(s.w.Fd())); err == nil && width > 4 && len([]rune(line)) >= width {
		line = string([]rune(line)[:width-2]) + "…"
	}
	fmt.Fprint(s.w, "\r\033[K"+line)
}

// clear takes the line away for good.
func (s *statusLine) clear() {
	if s == nil || s.w == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	if s.text != "" {
		fmt.Fprint(s.w, "\r\033[K")
	}
	s.text = ""
}

// watch prints a stream's events as they come, until it is interrupted.
// It picks the stream up again where it left off when it drops.
func watch(ctx context.Context, c *Client, req Request, w io.Writer, format string) error {
	lastID := ""
	failures := 0
	first := true
	for {
		r := req
		r.Header = http.Header{}
		for k, v := range req.Header {
			r.Header[k] = v
		}
		if lastID != "" {
			r.Header.Set("Last-Event-ID", lastID)
		}
		r.Accept = "text/event-stream"
		resp, err := c.Do(ctx, r)
		if err == nil {
			failures = 0
			err = readEvents(resp.Body, func(ev sseEvent) error {
				if ev.ID != "" {
					lastID = ev.ID
				}
				return printEvent(w, ev, format, &first)
			})
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil
		}
		var p *Problem
		if errors.As(err, &p) {
			return err
		}
		if failures++; failures > reconnects {
			if err == nil {
				err = errors.New("the stream keeps ending")
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(failures) * time.Second):
		}
	}
}

func printEvent(w io.Writer, ev sseEvent, format string, first *bool) error {
	switch format {
	case "json":
		line := struct {
			Event string          `json:"event"`
			ID    string          `json:"id,omitempty"`
			Data  json.RawMessage `json:"data"`
		}{ev.Event, ev.ID, ev.Data}
		raw, err := json.Marshal(line)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", raw)
		return err
	case "yaml":
		if !*first {
			fmt.Fprintln(w, "---")
		}
		*first = false
		raw, _ := json.Marshal(map[string]any{"event": ev.Event, "id": ev.ID, "data": json.RawMessage(ev.Data)})
		return writeYAML(w, raw)
	}
	v, err := decodeOrdered(ev.Data)
	if err != nil {
		_, err = fmt.Fprintf(w, "%s %s\n", ev.Event, ev.Data)
		return err
	}
	when := time.Now()
	if o, ok := v.(*object); ok {
		for _, k := range []string{"time", "updatedAt", "createdAt"} {
			if text, ok := o.get(k).(string); ok {
				if ts, err := time.Parse(time.RFC3339Nano, text); err == nil {
					when = ts
					break
				}
			}
		}
	}
	line := when.Local().Format("15:04:05") + "  "
	if ev.Event != "event" && ev.Event != "message" {
		line += ev.Event + "  "
	}
	_, err = fmt.Fprintln(w, clipLine(line+eventSummary(v)))
	return err
}

// eventSummary says what an event is about in one line: its type, what
// it's about and what happened.
func eventSummary(v any) string {
	o, ok := v.(*object)
	if !ok {
		return cell(v, "")
	}
	keys := []string{"type", "subject", "message", "title", "state"}
	if o.get("type") == nil {
		keys = []string{"kind", "id", "message", "title", "state"}
	}
	var parts []string
	for _, k := range keys {
		if text, ok := o.get(k).(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		raw, _ := json.Marshal(o)
		return string(raw)
	}
	return strings.Join(parts, "  ")
}

func clipLine(s string) string {
	s = strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
	if r := []rune(s); len(r) > 160 {
		return string(r[:159]) + "…"
	}
	return s
}
