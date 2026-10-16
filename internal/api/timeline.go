package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/api/httpapi"
	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/modules"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// reachOf works out the reach of a permission. scope, when given, is the
// one owner the caller asks about; without one, the actor needs the
// permission somewhere. A refusal is recorded in the audit log.
func (s *Server) reachOf(ctx context.Context, perm auth.Permission, scope *store.Scope) (reach, error) {
	if scope != nil {
		if err := Authorize(ctx, s.authorizer(), perm, auth.Target{Scope: *scope}); err != nil {
			return reach{}, err
		}
		return reach{scopes: []store.Scope{*scope}}, nil
	}
	r, err := s.grantReach(ctx, perm)
	if err == nil && !r.all && len(r.scopes) == 0 {
		err = i18n.ErrForbidden.New("action", string(perm))
		refused(ctx, perm, "", "", store.ServerScope(), err)
	}
	return r, err
}

// grantReach is what the actor's grants reach with a permission: every
// scope, or some, a reseller's customers' accounts included, or none.
func (s *Server) grantReach(ctx context.Context, perm auth.Permission) (reach, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return reach{}, err
	}
	authz := s.authorizer()
	if allowed(ctx, authz, perm, auth.Target{Scope: store.ServerScope()}) == nil {
		return reach{all: true}, nil
	}
	if authz == nil {
		return reach{}, nil
	}
	return grantScopes(ctx, authz, actor, perm)
}

// TimelineFilter holds the filters lists of events and audit records share.
type TimelineFilter struct {
	Scope     string    `query:"scope" doc:"Only those of one owner: server, reseller:<id> or account:<id>"`
	Subject   store.ID  `query:"subject" doc:"Only those about this resource"`
	Kind      string    `query:"kind" doc:"Only those about resources of this kind"`
	ChangeSet store.ID  `query:"changeSet" doc:"Only those of this change set"`
	Since     time.Time `query:"since" doc:"Only those from this time on"`
	Until     time.Time `query:"until" doc:"Only those before this time"`
}

// PageParams page through a list.
type PageParams struct {
	Cursor string `query:"cursor" doc:"Continues a list where the page before it ended"`
	Limit  int    `query:"limit" minimum:"0" maximum:"1000" doc:"How many to return; 50 when left out"`
}

// query turns the filters into a query within what the actor may read.
func (s *Server) timelineQuery(ctx context.Context, perm auth.Permission, f TimelineFilter, patterns []string) (events.Query, error) {
	var scope *store.Scope
	if f.Scope != "" {
		sc, err := store.ParseScope(f.Scope)
		if err != nil {
			return events.Query{}, badParam("scope", "use server, reseller:<id> or account:<id>")
		}
		scope = &sc
	}
	for _, p := range patterns {
		if !events.ValidPattern(p) {
			return events.Query{}, badParam("type", "use lowercase names, dots and the wildcards * and ?")
		}
	}
	r, err := s.reachOf(ctx, perm, scope)
	if err != nil {
		return events.Query{}, err
	}
	q := events.Query{Since: f.Since, Until: f.Until, Subject: f.Subject, Kind: f.Kind, ChangeSet: f.ChangeSet, Types: patterns}
	if !r.all {
		q.Scopes = r.scopes
	}
	return q, nil
}

type listEventsInput struct {
	TimelineFilter
	PageParams
	Types       []string        `query:"type" explode:"true" doc:"Only events of these types, as patterns such as web.site.*; repeat it for several"`
	Job         store.ID        `query:"job" doc:"Only the events of this job"`
	MinSeverity events.Severity `query:"minSeverity" enum:"info,notice,warning,error,critical" doc:"Only events at least this urgent"`
}

type eventList struct {
	Body struct {
		Events []events.Event `json:"events"`
		Next   string         `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type streamEventsInput struct {
	Types       []string        `query:"type" explode:"true" doc:"Only events of these types, as patterns such as web.site.*; repeat it for several"`
	Scope       string          `query:"scope" doc:"Only events of one owner: server, reseller:<id> or account:<id>"`
	Subject     store.ID        `query:"subject" doc:"Only events about this resource"`
	MinSeverity events.Severity `query:"minSeverity" enum:"info,notice,warning,error,critical" doc:"Only events at least this urgent"`
	LastEventID store.ID        `header:"Last-Event-ID" doc:"Resume after this event, as browsers do when they reconnect"`
}

// EventType describes an event type.
type EventType struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Severity    events.Severity `json:"severity" enum:"info,notice,warning,error,critical"`
	// Data is the JSON Schema of the event's data, when it has any.
	Data json.RawMessage `json:"data,omitempty" doc:"The JSON Schema of the event's data"`
}

type eventTypesOutput struct {
	Body struct {
		Types []EventType `json:"types"`
	}
}

// AuditFilter holds the filters of the audit log's list and export.
type AuditFilter struct {
	TimelineFilter
	Actions []string       `query:"action" explode:"true" doc:"Only these actions, as patterns such as web.site.*; repeat it for several"`
	Actor   store.ID       `query:"actor" doc:"Only what this user, token, automation or add-on did, or did for this user"`
	Outcome events.Outcome `query:"outcome" enum:"success,denied,failed" doc:"Only records with this outcome"`
}

type listAuditInput struct {
	AuditFilter
	PageParams
}

type auditList struct {
	Body struct {
		Records []events.AuditRecord `json:"records"`
		Next    string               `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type exportAuditInput struct {
	AuditFilter
	Cursor string `query:"cursor" doc:"Start after the record a page of the audit log ended with, as its next cursor says"`
	Limit  int    `query:"limit" minimum:"0" maximum:"100000" doc:"Export at most this many records; up to 100,000 when left out"`
	Format string `query:"format" enum:"csv,json" default:"csv" doc:"csv for spreadsheets, or json for one record per line"`
}

// exportLimit bounds an audit export.
const exportLimit = 100_000

func (s *Server) eventTypes() []events.Type {
	var extra [][]events.Type
	if s.opts.Modules != nil {
		for _, m := range s.opts.Modules.All() {
			if ep, ok := m.(modules.EventProvider); ok {
				extra = append(extra, ep.Events())
			}
		}
	}
	return events.Catalog(s.kinds.All(), extra...)
}

// eventCatalog is the answer of GET /events/types, described again only
// when the event types change, as they do when an add-on adds or drops a
// kind while the panel runs.
type eventCatalog struct {
	mu sync.Mutex
	// key says which event types the list describes.
	key   string
	types []EventType
}

// eventTypeList describes every event type there is now, from the last
// description when the types are the same.
func (s *Server) eventTypeList() ([]EventType, error) {
	types := s.eventTypes()
	var key strings.Builder
	for _, t := range types {
		fmt.Fprintf(&key, "%s\x00%s\x00%s\x00%v\n", t.Name, t.Description, t.Severity, reflect.TypeOf(t.Data))
	}
	c := &s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.types != nil && c.key == key.String() {
		return c.types, nil
	}
	described, err := describeEventTypes(types)
	if err != nil {
		return nil, err
	}
	c.key, c.types = key.String(), described
	return described, nil
}

// describeEventTypes describes event types with a standalone schema of
// their data, in a registry of its own, so requests never add schemas to
// the document's registry while others read it.
func describeEventTypes(types []events.Type) ([]EventType, error) {
	reg := huma.NewMapRegistry("#/components/schemas/", httpapi.SchemaNamer)
	out := []EventType{}
	for _, t := range types {
		et := EventType{Name: t.Name, Description: t.Description, Severity: t.Severity}
		if t.Data != nil {
			data, err := standaloneIn(reg, reg.Schema(reflect.TypeOf(t.Data), true, camel(t.Name)+"Data"))
			if err != nil {
				return nil, fmt.Errorf("the data of %s: %w", t.Name, err)
			}
			et.Data = data
		}
		out = append(out, et)
	}
	return out, nil
}

func (s *Server) registerTimeline() {
	tags := []string{"Events"}
	op := huma.Operation{
		OperationID: "listEvents", Method: http.MethodGet, Path: Prefix + "/events", Tags: tags, Security: Protected,
		Summary:     "List events",
		Description: "Lists what happened, newest first: resources created, changed, deleted and failing, actions done, drift put back and more, from the event timeline.",
	}
	Describe(&op, Info{Permission: string(PermEventRead), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *listEventsInput) (*eventList, error) {
		log := s.opts.Timeline
		if log == nil {
			return nil, i18n.ErrUnavailable.New("what", "The event timeline")
		}
		q, err := s.timelineQuery(ctx, PermEventRead, in.TimelineFilter, in.Types)
		if err != nil {
			return nil, err
		}
		q.Job, q.MinSeverity, q.Cursor, q.Limit = in.Job, in.MinSeverity, in.Cursor, in.Limit
		list, next, err := log.QueryEvents(ctx, q)
		if err != nil {
			return nil, err
		}
		out := &eventList{}
		out.Body.Events = append([]events.Event{}, list...)
		out.Body.Next = next
		return out, nil
	})

	op = huma.Operation{
		OperationID: "streamEvents", Method: http.MethodGet, Path: Prefix + "/events/stream", Tags: tags, Security: Protected,
		Summary: "Follow events",
		Description: "Streams events as Server-Sent Events as they happen. Each carries its ID; a client that reconnects with Last-Event-ID " +
			"first gets what it missed, for as long as the timeline keeps it.",
	}
	Describe(&op, Info{Permission: string(PermEventRead), Risk: store.RiskRead, Stream: true})
	registerStream(s, op, map[string]any{"event": events.Event{}}, func(ctx context.Context, in *streamEventsInput) (stream, error) {
		log := s.opts.Timeline
		if log == nil {
			return nil, i18n.ErrUnavailable.New("what", "The event timeline")
		}
		for _, p := range in.Types {
			if !events.ValidPattern(p) {
				return nil, badParam("type", "use lowercase names, dots and the wildcards * and ?")
			}
		}
		var scope *store.Scope
		if in.Scope != "" {
			sc, err := store.ParseScope(in.Scope)
			if err != nil {
				return nil, badParam("scope", "use server, reseller:<id> or account:<id>")
			}
			scope = &sc
		}
		r, err := s.reachOf(ctx, PermEventRead, scope)
		if err != nil {
			return nil, err
		}
		var after int64
		if in.LastEventID != "" {
			if after, err = log.SeqOf(ctx, in.LastEventID); err != nil {
				// The timeline no longer has it; carry on from now.
				after = 0
			}
		}
		filter := events.Filter{Types: in.Types, Scope: scope, Subject: in.Subject, MinSeverity: in.MinSeverity}
		if !r.all {
			filter.Scopes = r.scopes
		}
		return func(ctx context.Context, out send, ready func() error) error {
			return followEvents(ctx, log, filter, after, out, ready)
		}, nil
	})

	op = huma.Operation{
		OperationID: "listEventTypes", Method: http.MethodGet, Path: Prefix + "/events/types", Tags: tags, Security: Protected,
		Summary:     "List event types",
		Description: "Lists every type of event Racksail publishes, with what it means, how urgent it is and the schema of its data, for choosing what webhooks and automations react to.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, _ *struct{}) (*eventTypesOutput, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		types, err := s.eventTypeList()
		if err != nil {
			return nil, err
		}
		out := &eventTypesOutput{}
		out.Body.Types = types
		return out, nil
	})

	op = huma.Operation{
		OperationID: "listAudit", Method: http.MethodGet, Path: Prefix + "/audit", Tags: tags, Security: Protected,
		Summary: "Read the audit log",
		Description: "Lists who did what, newest first, with the person, what acted for them " +
			"(the panel, the racksail command, an API token, an automation, an add-on or the assistant) and the request it came through. " +
			"Every change is there, as success. So is every attempt that was refused, as denied: a change, a read or a job's cancel or retry " +
			"the person wasn't allowed, or a change a policy hook stopped. A change that was allowed but couldn't be made, " +
			"such as one with settings that aren't valid, is failed.",
	}
	Describe(&op, Info{Permission: string(PermAuditRead), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *listAuditInput) (*auditList, error) {
		q, err := s.auditQuery(ctx, &in.AuditFilter)
		if err != nil {
			return nil, err
		}
		q.Cursor, q.Limit = in.Cursor, in.Limit
		list, next, err := s.services().Audit.QueryAudit(ctx, q)
		if err != nil {
			return nil, err
		}
		out := &auditList{}
		out.Body.Records = append([]events.AuditRecord{}, list...)
		out.Body.Next = next
		return out, nil
	})

	op = huma.Operation{
		OperationID: "exportAudit", Method: http.MethodGet, Path: Prefix + "/audit/export", Tags: tags, Security: Protected,
		Summary: "Export the audit log",
		Description: "Downloads the audit records the filters select, newest first, as CSV or as JSON with one record per line, " +
			"up to 100,000 records, or as many as limit says; narrow the time window, or start after a cursor, for more. " +
			"In the CSV, text that starts like a spreadsheet formula gets a leading apostrophe, so opening the file never runs it.",
		Responses: map[string]*huma.Response{"200": {
			Description: "The records",
			Content: map[string]*huma.MediaType{
				"text/csv":             {Schema: &huma.Schema{Type: huma.TypeString}},
				"application/x-ndjson": {Schema: &huma.Schema{Type: huma.TypeString}},
			},
		}},
	}
	Describe(&op, Info{Permission: string(PermAuditRead), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *exportAuditInput) (*huma.StreamResponse, error) {
		q, err := s.auditQuery(ctx, &in.AuditFilter)
		if err != nil {
			return nil, err
		}
		if err := events.CheckCursor(in.Cursor); err != nil {
			return nil, err
		}
		q.Cursor = in.Cursor
		most := exportLimit
		if in.Limit > 0 {
			most = min(in.Limit, exportLimit)
		}
		audit := s.services().Audit
		return &huma.StreamResponse{Body: func(hc huma.Context) {
			ext, ct := "csv", "text/csv; charset=utf-8"
			if in.Format == "json" {
				ext, ct = "ndjson", "application/x-ndjson"
			}
			hc.SetHeader("Content-Type", ct)
			hc.SetHeader("Content-Disposition", `attachment; filename="racksail-audit.`+ext+`"`)
			hc.SetStatus(http.StatusOK)
			if err := exportAudit(hc.Context(), audit, q, most, in.Format, hc.BodyWriter()); err != nil {
				s.opts.Log.Warn("exporting the audit log", "error", err)
			}
		}}, nil
	})
}

func (s *Server) auditQuery(ctx context.Context, in *AuditFilter) (events.Query, error) {
	if s.services().Audit == nil {
		return events.Query{}, i18n.ErrUnavailable.New("what", "The audit log")
	}
	q, err := s.timelineQuery(ctx, PermAuditRead, in.TimelineFilter, in.Actions)
	if err != nil {
		return events.Query{}, err
	}
	q.Actor, q.Outcome = in.Actor, in.Outcome
	return q, nil
}

// followEvents streams the events after seq, or from now when seq is zero,
// then live ones.
func followEvents(ctx context.Context, log *events.Log, filter events.Filter, after int64, out send, ready func() error) error {
	// Subscribing first means nothing published while catching up is lost;
	// what arrives twice is told apart by its sequence number.
	sub := log.Subscribe(filter, 256)
	defer sub.Close()
	catchUp := func() error {
		for {
			batch, err := log.EventsAfter(ctx, after, 200)
			if err != nil {
				return err
			}
			for _, e := range batch {
				after = e.Seq
				if filter.Match(e) {
					if err := out("event", string(e.ID), e); err != nil {
						return err
					}
				}
			}
			if len(batch) < 200 {
				return nil
			}
		}
	}
	if after > 0 {
		if err := catchUp(); err != nil {
			return err
		}
	} else {
		// A new stream starts from the latest event; the ones published
		// since subscribing came before the client saw it open.
		last, err := log.LastSeq(ctx)
		if err != nil {
			return err
		}
		after = last
	}
	if err := ready(); err != nil {
		return err
	}
	var dropped int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-sub.C:
			if !ok {
				return nil
			}
			// A client too slow for the subscription's buffer misses
			// events there; they are read back from the timeline instead.
			if d := sub.Dropped(); d != dropped {
				dropped = d
				if err := catchUp(); err != nil {
					return err
				}
				continue
			}
			if e.Seq <= after {
				continue
			}
			after = e.Seq
			if err := out("event", string(e.ID), e); err != nil {
				return err
			}
		}
	}
}

// exportAudit writes up to most of the records q selects.
func exportAudit(ctx context.Context, audit events.AuditLog, q events.Query, most int, format string, w io.Writer) error {
	q.Limit = min(store.MaxLimit, most)
	var cw *csv.Writer
	enc := json.NewEncoder(w)
	if format != "json" {
		cw = csv.NewWriter(w)
		cw.Write([]string{"id", "time", "outcome", "action", "kind", "subject", "scope", "actor", "for", "via", "address", "change set", "request", "summary", "reason"})
	}
	written := 0
	for {
		records, next, err := audit.QueryAudit(ctx, q)
		if err != nil {
			return err
		}
		for _, r := range records {
			if cw != nil {
				who := string(r.Actor.Type) + ":" + string(r.Actor.ID)
				if r.Actor.ID == "" {
					who = string(r.Actor.Type) + ":" + r.Actor.Name
				}
				row := []string{
					string(r.ID), r.Time.Format(time.RFC3339Nano), string(r.Outcome), r.Action, r.Kind, string(r.Subject), r.Scope.String(),
					who, string(r.Actor.User), string(r.Actor.Source), r.Actor.IP, string(r.ChangeSet), r.RequestID, r.Summary, r.Reason,
				}
				for i, cell := range row {
					row[i] = spreadsheetSafe(cell)
				}
				cw.Write(row)
			} else if err := enc.Encode(r); err != nil {
				return err
			}
			if written++; written >= most {
				break
			}
		}
		if cw != nil {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
		}
		if next == "" || written >= most {
			return nil
		}
		q.Cursor, q.Limit = next, min(store.MaxLimit, most-written)
	}
}

// spreadsheetSafe keeps a CSV cell from being read as a formula by the
// spreadsheet that opens the export: text that starts with a character that
// begins a formula gets an apostrophe in front, which spreadsheets show as
// text. Summaries, reasons, names and request IDs come from users.
func spreadsheetSafe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}
