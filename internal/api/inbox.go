package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/inbox"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// InboxAction is something to do about an inbox item, which the panel
// offers as a button.
type InboxAction struct {
	Type  string `json:"type" enum:"open_resource,open_job,open_changeset,retry_job,undo_changeset,open_events,docs" doc:"What the action does"`
	Title string `json:"title" doc:"The button's label, in English"`
	// Target is what the action works on.
	Target string `json:"target,omitempty" doc:"What the action works on: a resource, job or change set ID, an event type pattern, or a page of the documentation"`
}

// InboxDismissal says when someone dismissed an item, and until when.
type InboxDismissal struct {
	At    time.Time  `json:"at" doc:"When the item was dismissed"`
	Until *time.Time `json:"until,omitempty" doc:"When a snoozed item comes back; a dismissed one without it comes back when it changes"`
}

// InboxItem is one thing that needs someone's attention.
type InboxItem struct {
	ID        string            `json:"id" doc:"Names the item while it lasts, such as job:job_01..."`
	Category  string            `json:"category" enum:"job,resource,alert,update,certificate,suggestion" doc:"What the item is about"`
	Severity  events.Severity   `json:"severity" enum:"info,notice,warning,error,critical" doc:"How urgent it is"`
	Code      string            `json:"code" doc:"Names the message the panel shows in the person's language, inbox.<code>, filled in from params; title and message are the English text"`
	Params    map[string]string `json:"params,omitempty" doc:"The values code's message is filled in with"`
	Title     string            `json:"title" doc:"What needs attention, in English"`
	Message   string            `json:"message,omitempty" doc:"More about it, in English"`
	Time      time.Time         `json:"time" doc:"When it started needing attention, or last happened"`
	Count     int               `json:"count,omitempty" doc:"How often it happened, for alerts that repeat"`
	Subject   store.ID          `json:"subject,omitempty" doc:"The resource the item is about"`
	Kind      string            `json:"kind,omitempty" doc:"The subject's kind"`
	Scope     store.Scope       `json:"scope" doc:"The owner the item belongs to"`
	Job       store.ID          `json:"job,omitempty" doc:"The job the item is about"`
	ChangeSet store.ID          `json:"changeSet,omitempty" doc:"The change set the item is about"`
	Event     *events.Event     `json:"event,omitempty" doc:"The latest event, for alerts"`
	Actions   []InboxAction     `json:"actions" doc:"What can be done about it, from the inbox"`
	Source    string            `json:"source" doc:"What found the item: core, or the id of the module that did"`
	Version   string            `json:"version" doc:"Changes when the item does; a dismissal holds until it changes"`
	Dismissal *InboxDismissal   `json:"dismissal,omitempty" doc:"Set on items you dismissed, when those are asked for"`
}

// InboxProblem says why part of the inbox couldn't be read.
type InboxProblem struct {
	Source  string         `json:"source" doc:"What couldn't be read, such as updates"`
	Code    string         `json:"code" doc:"The error's code, for the panel to translate"`
	Message string         `json:"message" doc:"What went wrong, in English"`
	Params  map[string]any `json:"params,omitempty" doc:"The values the code's message is filled in with"`
}

// InboxChange tells a client of the inbox stream to read the inbox again.
type InboxChange struct {
	Reason string `json:"reason" enum:"checked,event,resource,dismissal" doc:"What changed: a check of updates, certificates or modules finished, something happened, a resource's state changed, or an item was dismissed or brought back"`
}

func inboxItemOf(it inbox.Item) InboxItem {
	out := InboxItem{
		ID: it.ID, Category: string(it.Category), Severity: it.Severity, Code: it.Code, Params: it.Params,
		Title: it.Title, Message: it.Message, Time: it.Time, Count: it.Count, Subject: it.Subject, Kind: it.Kind,
		Scope: it.Scope, Job: it.Job, ChangeSet: it.ChangeSet, Event: it.Event, Source: it.Source, Version: it.Version,
		Actions: make([]InboxAction, 0, len(it.Actions)),
	}
	for _, a := range it.Actions {
		out.Actions = append(out.Actions, InboxAction{Type: string(a.Type), Title: a.Title, Target: a.Target})
	}
	if d := it.Dismissal; d != nil {
		out.Dismissal = &InboxDismissal{At: d.At, Until: d.Until}
	}
	return out
}

type listInboxInput struct {
	Dismissed bool `query:"dismissed" doc:"Also list the items you dismissed or snoozed, marked as such"`
}

type inboxOutput struct {
	Body struct {
		Items []InboxItem `json:"items" doc:"What needs attention"`
		// Dismissed counts the items left out because they were dismissed.
		Dismissed   int            `json:"dismissed" doc:"How many items you dismissed or snoozed that would show otherwise"`
		Unavailable []InboxProblem `json:"unavailable,omitempty" doc:"Parts of the inbox that couldn't be read, and why"`
		Checking    []string       `json:"checking,omitempty" doc:"Parts of the inbox being checked for the first time; the stream says when to read it again"`
	}
}

// Dismissing and bringing back items take dryRun and an Idempotency-Key as
// every mutating call does. Neither goes through the change pipeline, so a
// dry run checks the request without making it, and a repeat is safe as it
// is, which is why the key needs no keeping.

type inboxRestoreInput struct {
	ID             string `path:"id" maxLength:"512" doc:"The item's ID"`
	DryRun         bool   `query:"dryRun" doc:"Only say whether there is a dismissal to take back"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Any unique string. Bringing an item back again changes nothing, so a repeat is safe with or without it"`
}

type inboxRestoreOutput struct {
	Body struct {
		Restored bool `json:"restored" doc:"Whether there was a dismissal to take back, or with dryRun=true whether there is one"`
	}
}

// dismissBody says until when to snooze an item.
type dismissBody struct {
	Until *time.Time `json:"until,omitempty" doc:"Show the item again at this time, even if it hasn't changed, within a year; without it the item comes back once it changes"`
}

type dismissInput struct {
	ID             string `path:"id" maxLength:"512" doc:"The item's ID"`
	DryRun         bool   `query:"dryRun" doc:"Check the item and the time without dismissing anything"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Any unique string. Dismissing an item again only replaces its dismissal, so a repeat is safe with or without it"`
	// The body is optional: without it the item is dismissed until it
	// changes.
	Body *dismissBody
}

// maxSnooze bounds how far ahead an item can be snoozed.
const maxSnooze = 366 * 24 * time.Hour

// inboxQuery says whose inbox to read: the signed-in actor's, narrowed to
// the owners their grants reach.
func (s *Server) inboxQuery(ctx context.Context) (inbox.Query, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return inbox.Query{}, err
	}
	r, err := grantScopes(ctx, s.authorizer(), actor, "")
	if err != nil {
		return inbox.Query{}, err
	}
	return inbox.Query{Actor: actor, All: r.all, Scopes: r.scopes, Now: s.opts.Now()}, nil
}

// inboxVisible lets through the items the signed-in actor may see, checking
// each one the way reads of what it is about are checked. It records
// nothing, as lists don't.
func (s *Server) inboxVisible(ctx context.Context) func(inbox.Item) bool {
	authz := s.authorizer()
	return func(it inbox.Item) bool {
		switch a := it.Access; {
		case a.Resource != nil:
			return canRead(ctx, authz, *a.Resource)
		case a.Job != nil:
			return s.mayUseJob(ctx, PermJobRead, a.Job) == nil
		case a.Permission != "":
			return allowed(ctx, authz, a.Permission, auth.Target{Scope: a.Scope}) == nil
		}
		return false
	}
}

func (s *Server) inbox() (*inbox.Service, error) {
	if s.opts.Inbox == nil {
		return nil, i18n.ErrUnavailable.New("what", "The inbox")
	}
	return s.opts.Inbox, nil
}

// findInboxItem returns an item of the signed-in actor's inbox, dismissed
// or not; items they don't have are not found.
func (s *Server) findInboxItem(ctx context.Context, svc *inbox.Service, id string) (inbox.Item, error) {
	q, err := s.inboxQuery(ctx)
	if err != nil {
		return inbox.Item{}, err
	}
	res, err := svc.List(ctx, q, s.inboxVisible(ctx), true)
	if err != nil {
		return inbox.Item{}, err
	}
	for _, it := range res.Items {
		if it.ID == id {
			return it, nil
		}
	}
	return inbox.Item{}, i18n.ErrNotFound.New("what", "The inbox item "+id)
}

func (s *Server) registerInbox() {
	tags := []string{"Inbox"}
	op := huma.Operation{
		OperationID: "listInbox", Method: http.MethodGet, Path: Prefix + "/inbox", Tags: tags, Security: Protected,
		Summary: "List what needs your attention",
		Description: "Lists what needs your attention, most urgent first: work that failed, resources that aren't working, alerts, updates waiting to be installed, " +
			"certificates that expire soon or couldn't be renewed, and modules that would help. Each item says what happened and offers what to do about it. " +
			"You see the items about what you may read, and not the ones you dismissed, unless dismissed=true.",
	}
	// What it answers is filtered to what the caller may read, so it needs
	// nothing more than a sign-in.
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *listInboxInput) (*inboxOutput, error) {
		svc, err := s.inbox()
		if err != nil {
			return nil, err
		}
		q, err := s.inboxQuery(ctx)
		if err != nil {
			return nil, err
		}
		res, err := svc.List(ctx, q, s.inboxVisible(ctx), in.Dismissed)
		if err != nil {
			return nil, err
		}
		out := &inboxOutput{}
		out.Body.Items = make([]InboxItem, 0, len(res.Items))
		for _, it := range res.Items {
			out.Body.Items = append(out.Body.Items, inboxItemOf(it))
		}
		out.Body.Dismissed = res.Dismissed
		for _, p := range res.Unavailable {
			out.Body.Unavailable = append(out.Body.Unavailable, InboxProblem(p))
		}
		out.Body.Checking = res.Checking
		return out, nil
	})

	op = huma.Operation{
		OperationID: "dismissInboxItem", Method: http.MethodPost, Path: Prefix + "/inbox/{id}/dismiss", Tags: tags, Security: Protected,
		Summary: "Dismiss an inbox item",
		Description: "Hides an item of your inbox until it changes, such as when the same job fails again, or snoozes it until the time you give. " +
			"It only changes what you see: others still see the item, and nothing on the server changes. " +
			"Dismissing it again replaces the dismissal; with dryRun=true the item and the time are checked and nothing is dismissed.",
		DefaultStatus: http.StatusNoContent,
	}
	Describe(&op, Info{Operation: "dismiss", Risk: store.RiskLow, Reversible: true})
	huma.Register(s.api, op, func(ctx context.Context, in *dismissInput) (*struct{}, error) {
		svc, err := s.inbox()
		if err != nil {
			return nil, err
		}
		var until *time.Time
		if in.Body != nil {
			until = in.Body.Until
		}
		if until != nil {
			now := s.opts.Now()
			if !until.After(now) || until.Sub(now) > maxSnooze {
				return nil, i18n.Invalid(i18n.Field("body.until", i18n.ErrBadValue, "reason", "snooze an item until a time within the next year"))
			}
			utc := until.UTC()
			until = &utc
		}
		item, err := s.findInboxItem(ctx, svc, in.ID)
		if err != nil {
			return nil, err
		}
		// The item is the caller's and the time is good: all a dry run checks.
		if in.DryRun {
			return nil, nil
		}
		actor, _ := Actor(ctx)
		if err := svc.Dismiss(ctx, inbox.Person(actor), item, until); err != nil {
			return nil, err
		}
		return nil, nil
	})

	op = huma.Operation{
		OperationID: "restoreInboxItem", Method: http.MethodDelete, Path: Prefix + "/inbox/{id}/dismiss", Tags: tags, Security: Protected,
		Summary: "Bring back a dismissed inbox item",
		Description: "Shows an item you dismissed or snoozed in your inbox again. The answer says whether there was a dismissal to take back; " +
			"bringing back an item that isn't dismissed changes nothing. With dryRun=true it only says whether there is one.",
	}
	Describe(&op, Info{Operation: "restore", Risk: store.RiskLow, Reversible: true})
	huma.Register(s.api, op, func(ctx context.Context, in *inboxRestoreInput) (*inboxRestoreOutput, error) {
		svc, err := s.inbox()
		if err != nil {
			return nil, err
		}
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		out := &inboxRestoreOutput{}
		if in.DryRun {
			_, out.Body.Restored, err = svc.Dismissed(ctx, inbox.Person(actor), in.ID)
		} else {
			out.Body.Restored, err = svc.Restore(ctx, inbox.Person(actor), in.ID)
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "streamInbox", Method: http.MethodGet, Path: Prefix + "/inbox/stream", Tags: tags, Security: Protected,
		Summary: "Follow changes to your inbox",
		Description: "Streams a changed event, as Server-Sent Events, whenever what your inbox shows may have changed: something you can see failed or recovered, " +
			"an alert came in, a check of updates, certificates or modules finished, or you dismissed an item elsewhere. Read the inbox again when one arrives.",
	}
	Describe(&op, Info{Risk: store.RiskRead, Stream: true})
	registerStream(s, op, map[string]any{"changed": InboxChange{}}, func(ctx context.Context, _ *struct{}) (stream, error) {
		svc, err := s.inbox()
		if err != nil {
			return nil, err
		}
		q, err := s.inboxQuery(ctx)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, out send, ready func() error) error {
			return s.followInbox(ctx, svc, q, out, ready)
		}, nil
	})
}

// inboxSettle is how long the inbox stream waits for more changes of a kind
// before it tells the client, so a burst of them is read once.
const inboxSettle = 300 * time.Millisecond

// inboxReasons are what the stream says changed, in the order it tells
// changes that settle together.
var inboxReasons = []string{"checked", "event", "resource", "dismissal"}

// followInbox tells a client when its inbox may have changed: when the
// sources the inbox keeps found something new, when the timeline has an
// event the inbox shows, when a resource the client can see starts or
// stops failing, and when the person dismissed or brought back an item,
// elsewhere as much as here.
func (s *Server) followInbox(ctx context.Context, svc *inbox.Service, q inbox.Query, out send, ready func() error) error {
	checked, dismissed, stop := svc.Changes(inbox.Person(q.Actor))
	defer stop()
	var timeline <-chan events.Event
	if log := s.opts.Timeline; log != nil {
		filter := events.Filter{}
		if !q.All {
			filter.Scopes = q.Scopes
		}
		sub := log.Subscribe(filter, 256)
		defer sub.Close()
		timeline = sub.C
	}
	var notices <-chan store.Notice
	if st := s.store(); st != nil {
		w := st.Watch(store.WatchFilter{}, 256)
		defer w.Close()
		notices = w.C
	}
	// The resources that fail now, so it is noticed when they stop.
	failing := map[store.ID]bool{}
	if res, err := svc.List(ctx, q, s.inboxVisible(ctx), true); err == nil {
		for _, it := range res.Items {
			if it.Category == inbox.CategoryResource {
				failing[it.Subject] = true
			}
		}
	}
	if err := ready(); err != nil {
		return err
	}
	// Each kind of change settles on its own, so the reason a change gives
	// is always what happened, while a burst of one kind is told once.
	due := map[string]time.Time{}
	var timer *time.Timer
	var fire <-chan time.Time
	schedule := func() {
		if timer != nil {
			timer.Stop()
		}
		timer, fire = nil, nil
		var next time.Time
		for _, at := range due {
			if next.IsZero() || at.Before(next) {
				next = at
			}
		}
		if !next.IsZero() {
			timer = time.NewTimer(time.Until(next))
			fire = timer.C
		}
	}
	mark := func(why string) {
		if _, waiting := due[why]; !waiting {
			due[why] = time.Now().Add(inboxSettle)
			schedule()
		}
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-checked:
			mark("checked")
		case <-dismissed:
			mark("dismissal")
		case e, ok := <-timeline:
			if !ok {
				timeline = nil
				continue
			}
			if inboxWorthy(e) && allowed(ctx, s.authorizer(), PermEventRead, auth.Target{Scope: e.Scope}) == nil {
				mark("event")
			}
		case n, ok := <-notices:
			if !ok {
				notices = nil
				continue
			}
			bad := n.Op != store.NoticePurged && n.Op != store.NoticeDeleted && (n.Phase == store.PhaseFailed || n.Phase == store.PhaseDegraded)
			if bad == failing[n.ID] || !canRead(ctx, s.authorizer(), store.Meta{ID: n.ID, Kind: n.Kind, Scope: n.Scope}) {
				continue
			}
			if bad {
				failing[n.ID] = true
			} else {
				delete(failing, n.ID)
			}
			mark("resource")
		case <-fire:
			now := time.Now()
			for _, why := range inboxReasons {
				if at, waiting := due[why]; waiting && !at.After(now) {
					delete(due, why)
					if err := out("changed", "", InboxChange{Reason: why}); err != nil {
						return err
					}
				}
			}
			schedule()
		}
	}
}

// inboxWorthy reports whether an event may change what an inbox shows: an
// alert, a change set that failed or was undone, or a job's event.
func inboxWorthy(e events.Event) bool {
	return e.Severity.AtLeast(events.SeverityWarning) || e.Job != "" ||
		e.Type == events.ChangeSetFailed.Name || e.Type == events.ChangeSetUndone.Name || e.Type == events.ChangeSetApplied.Name
}
