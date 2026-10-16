package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/theme"
)

// DeliveryPrefix is the TypeID prefix of notification deliveries.
const DeliveryPrefix = "ntf"

// NotificationState is where a delivery is.
type NotificationState string

// Notification states.
const (
	// DeliveryPending waits to be sent, or sent again.
	DeliveryPending NotificationState = "pending"
	// DeliveryHeld waits for quiet hours to end.
	DeliveryHeld NotificationState = "held"
	DeliverySent NotificationState = "sent"
	// DeliveryFailed gave up.
	DeliveryFailed NotificationState = "failed"
	// DeliveryDropped arrived during quiet hours that leave things out.
	DeliveryDropped NotificationState = "dropped"
	// DeliveryMerged went out in a summary of quiet hours.
	DeliveryMerged NotificationState = "merged"
)

// Notification is one message sent, or being sent, to one channel or person.
type Notification struct {
	ID store.ID `json:"id" doc:"The notification's ID, which starts with ntf_"`
	// Channel, Person or Address says where the message goes.
	Channel   store.ID          `json:"channel,omitempty" doc:"The channel it goes through"`
	Person    store.ID          `json:"person,omitempty" doc:"The person it is emailed to"`
	Address   string            `json:"address,omitempty" doc:"The address it is emailed to"`
	Event     store.ID          `json:"event,omitempty" doc:"The event it is about"`
	EventType string            `json:"eventType,omitempty" doc:"The type of the event it is about"`
	Title     string            `json:"title" doc:"What it says happened, in a few words"`
	Severity  events.Severity   `json:"severity" enum:"info,notice,warning,error,critical" doc:"How urgent it is"`
	State     NotificationState `json:"state" enum:"pending,held,sent,failed,dropped,merged" doc:"Where it is: waiting, held for quiet hours, sent, failed, left out in quiet hours or sent in a summary"`
	// Test deliveries are sent once, whatever the quiet hours.
	Test         bool       `json:"test,omitempty" doc:"Whether it is a test, which is sent once whatever the quiet hours"`
	AttemptCount int        `json:"attemptCount" doc:"How many times sending it was tried"`
	NextAttempt  *time.Time `json:"nextAttempt,omitempty" doc:"When it is sent, or sent again; for held deliveries, when quiet hours end"`
	LastError    string     `json:"lastError,omitempty" doc:"Why the last attempt failed"`
	// Summary is the delivery a held one went out in.
	Summary    store.ID   `json:"summary,omitempty" doc:"The summary it went out in"`
	CreatedAt  time.Time  `json:"createdAt" doc:"When it was made"`
	FinishedAt *time.Time `json:"finishedAt,omitempty" doc:"When it was sent or given up"`
	// Message is what is sent. Lists leave it out.
	Message *Message `json:"message,omitempty" doc:"What is sent; lists leave it out"`
}

// key is where a delivery goes, which held deliveries are summed up by.
func (d *Notification) key() string {
	switch {
	case d.Channel != "":
		return "channel:" + string(d.Channel)
	case d.Person != "":
		return "person:" + string(d.Person)
	}
	return "address:" + d.Address
}

// Schedule is how long a failed delivery waits before each new attempt.
// After the last, it has failed.
var Schedule = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour}

// Options configure a Service.
type Options struct {
	Store   store.Store
	Log     *events.Log
	Secrets secrets.Reader
	// Registry has the notifiers modules and add-ons provide.
	Registry *hooks.Registry
	// Directory finds the people preferences belong to; without one only
	// channels and addresses get notifications.
	Directory  Directory
	Authorizer auth.Authorizer
	// Theme returns the theme emails are dressed in; the default theme
	// when nil.
	Theme     func(ctx context.Context) (*theme.Package, error)
	BrandName string
	// Link returns the panel address for an event, which messages link
	// back to; nil leaves links out.
	Link func(e events.Event) string
	// AllowNetworks are the networks channels may reach besides public
	// addresses: webhooks.allow_networks in racksail.toml.
	AllowNetworks []netip.Prefix
	// HTTP replaces the guarded client, for tests.
	HTTP     *http.Client
	Schedule []time.Duration
	// Retention is how long finished deliveries are kept; thirty days by
	// default.
	Retention time.Duration
	Logger    *slog.Logger
	Now       func() time.Time
}

// Service routes events to channels and people, and delivers messages.
type Service struct {
	opts    Options
	db      *sqlStorage
	wake    chan struct{}
	running atomic.Bool
	// sending holds the deliveries being attempted, so one isn't sent
	// twice when the loop and a test overlap.
	sending sync.Map
}

func newService(st *sqlStorage, opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if len(opts.Schedule) == 0 {
		opts.Schedule = Schedule
	}
	if opts.Retention <= 0 {
		opts.Retention = 30 * 24 * time.Hour
	}
	if opts.HTTP == nil {
		opts.HTTP = Guard{Allow: opts.AllowNetworks}.Client()
	}
	return &Service{opts: opts, db: st, wake: make(chan struct{}, 1)}
}

// Env is what the service's channels are built with.
func (s *Service) Env() Env {
	return Env{
		Secrets: s.opts.Secrets, HTTP: s.opts.HTTP, Relay: s.relay, Theme: s.opts.Theme, BrandName: s.opts.BrandName,
	}
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Check is the service's health check: it fails while the service isn't
// running or can't read its deliveries.
func (s *Service) Check(ctx context.Context) error {
	if !s.running.Load() {
		return i18n.ErrUnavailable.New("what", "Notifications")
	}
	_, err := s.db.pending(ctx)
	return err
}

// Run routes and sends notifications until ctx is done. It starts where it
// left off; the first run starts with the events published from then on.
func (s *Service) Run(ctx context.Context) {
	s.running.Store(true)
	defer s.running.Store(false)
	sub := s.opts.Log.Subscribe(events.Filter{}, 256)
	defer sub.Close()
	if _, ok, err := s.db.cursor(ctx); err == nil && !ok {
		last, err := s.opts.Log.LastSeq(ctx)
		if err == nil {
			err = s.db.enqueue(ctx, nil, last)
		}
		if err != nil {
			s.opts.Logger.Error("starting notifications", "error", err)
		}
	}
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		if err := s.route(ctx); err != nil && ctx.Err() == nil {
			s.opts.Logger.Warn("routing events to notifications", "error", err)
		}
		wait := s.sendDue(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-sub.C:
			for drained := false; !drained; {
				select {
				case <-sub.C:
				default:
					drained = true
				}
			}
		case <-s.wake:
		case <-prune.C:
			if err := s.db.prune(ctx, s.opts.Now().Add(-s.opts.Retention)); err != nil {
				s.opts.Logger.Warn("dropping old notification deliveries", "error", err)
			}
		case <-timer.C:
		}
		timer.Stop()
	}
}

// route turns the events published since the last call into deliveries.
func (s *Service) route(ctx context.Context) error {
	for {
		cursor, _, err := s.db.cursor(ctx)
		if err != nil {
			return err
		}
		batch, err := s.opts.Log.EventsAfter(ctx, cursor, 200)
		if err != nil || len(batch) == 0 {
			return err
		}
		channels, err := s.channels(ctx)
		if err != nil {
			return err
		}
		prefs, err := s.allPreferences(ctx)
		if err != nil {
			return err
		}
		people := map[store.ID]*Person{}
		var out []*Notification
		for _, e := range batch {
			msg := s.messageOf(e)
			for _, c := range channels {
				if wantsChannel(c, e) {
					out = append(out, s.newDelivery(Notification{Channel: c.ID}, msg, e, c.Spec.QuietHours))
				}
			}
			for id, p := range prefs {
				if !p.Wants(e) {
					continue
				}
				person, err := s.person(ctx, people, id)
				if err != nil {
					return err
				}
				if person == nil || person.Email == "" || !mayRead(ctx, s.opts.Authorizer, person, e) {
					continue
				}
				out = append(out, s.newDelivery(Notification{Person: id}, msg, e, p.QuietHours))
			}
		}
		if err := s.db.enqueue(ctx, out, batch[len(batch)-1].Seq); err != nil {
			return err
		}
	}
}

func (s *Service) person(ctx context.Context, cache map[store.ID]*Person, id store.ID) (*Person, error) {
	if s.opts.Directory == nil {
		return nil, nil
	}
	if p, ok := cache[id]; ok {
		return p, nil
	}
	p, err := s.opts.Directory.Person(ctx, id)
	if err != nil {
		return nil, err
	}
	cache[id] = p
	return p, nil
}

// newDelivery builds a delivery of msg, held or dropped when quiet hours
// say so.
func (s *Service) newDelivery(to Notification, msg Message, e events.Event, quiet *QuietHours) *Notification {
	now := s.opts.Now().UTC()
	d := to
	d.ID = store.NewID(DeliveryPrefix)
	d.Event, d.EventType = e.ID, e.Type
	d.Title, d.Severity = msg.Title, msg.Severity
	d.State, d.CreatedAt = DeliveryPending, now
	d.NextAttempt = &now
	m := msg
	m.ID = string(d.ID)
	d.Message = &m
	if hold, until := quiet.Holds(msg.Severity, now); hold {
		if quiet.Drop {
			d.State, d.NextAttempt, d.FinishedAt = DeliveryDropped, nil, &now
		} else {
			u := until.UTC()
			d.State, d.NextAttempt = DeliveryHeld, &u
		}
	}
	return &d
}

// messageOf is the message an event becomes.
func (s *Service) messageOf(e events.Event) Message {
	title := strings.TrimSpace(e.Message)
	if title == "" {
		title = e.Type
	}
	var body strings.Builder
	body.WriteString(e.Message)
	fmt.Fprintf(&body, "\n\nEvent: %s\nWhen: %s", e.Type, e.Time.UTC().Format("2006-01-02 15:04:05 UTC"))
	if e.Subject != "" {
		fmt.Fprintf(&body, "\nAbout: %s", e.Subject)
	}
	if e.Actor != nil && e.Actor.Name != "" {
		fmt.Fprintf(&body, "\nBy: %s", e.Actor.Name)
	}
	msg := Message{Title: truncate(title, 160), Body: strings.TrimSpace(body.String()), Severity: e.Severity}
	if msg.Severity == "" {
		msg.Severity = events.SeverityInfo
	}
	if s.opts.Link != nil {
		if link := s.opts.Link(e); link != "" {
			msg.URL, msg.ActionLabel = link, "Open in the panel"
		}
	}
	ev := e
	msg.Event = &ev
	return msg
}

// channels lists every channel.
func (s *Service) channels(ctx context.Context) ([]*store.Object[ChannelSpec, ChannelStatus], error) {
	var out []*store.Object[ChannelSpec, ChannelStatus]
	q := store.Query{Limit: store.MaxLimit}
	for {
		page, next, err := ChannelKind.List(ctx, s.opts.Store, q)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		q.Cursor = next
	}
}

// relay returns the mail relay, or nil when none is set up.
func (s *Service) relay(ctx context.Context) (*Relay, error) {
	page, _, err := RelayKind.List(ctx, s.opts.Store, store.Query{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(page) == 0 || page[0].Deleted() {
		return nil, nil
	}
	spec := page[0].Spec
	r := &Relay{
		Host: spec.Host, Port: spec.Port, Security: spec.Security, Username: spec.Username,
		From: spec.From, FromName: spec.FromName, HELO: spec.HELO,
	}
	if r.Security == "" {
		r.Security = SecuritySTARTTLS
	}
	if r.FromName == "" {
		r.FromName = s.opts.BrandName
	}
	if spec.Password != "" {
		if s.opts.Secrets == nil {
			return nil, errors.New("the relay's password can't be read here")
		}
		v, err := s.opts.Secrets.Get(ctx, secrets.Ref(spec.Password))
		if err != nil {
			return nil, fmt.Errorf("reading the relay's password: %w", err)
		}
		r.Password = string(v)
	}
	return r, nil
}

// channel builds the channel a resource describes: a type built into the
// panel, or a notifier a module or add-on provides.
func (s *Service) channel(ctx context.Context, c *store.Object[ChannelSpec, ChannelStatus]) (Channel, error) {
	if _, ok := LookupType(c.Spec.Type); ok {
		return New(c.Spec.Type, c.Spec.Settings, s.Env())
	}
	if s.opts.Registry != nil {
		if _, ok := s.opts.Registry.Provider(hooks.ProvideNotifier, c.Spec.Type); ok {
			return hooks.Open[Channel](ctx, s.opts.Registry, hooks.ProvideNotifier, c.Spec.Type, c.Spec.Settings)
		}
	}
	return nil, ErrUnknownChannelType.New("type", c.Spec.Type)
}

// knownType reports whether a channel type exists, built in or provided.
func (s *Service) knownType(name string) bool {
	if _, ok := LookupType(name); ok {
		return true
	}
	if s.opts.Registry == nil {
		return false
	}
	_, ok := s.opts.Registry.Provider(hooks.ProvideNotifier, name)
	return ok
}

// ChannelType describes a channel type for people choosing one.
type ChannelType struct {
	Name        string `json:"name" doc:"The channel type, as channels name it"`
	Title       string `json:"title" doc:"What the channel type is called"`
	Description string `json:"description,omitempty" doc:"What a channel of the type sends to"`
	// Secrets are the settings that hold secrets.
	Secrets []string `json:"secrets,omitempty" doc:"The settings that hold secrets"`
	// Provider is who provides a notifier that isn't built in.
	Provider string `json:"provider,omitempty" doc:"The module or add-on that provides the notifier"`
	// Settings is a zero value of the settings type, for the form's schema.
	Settings any `json:"-"`
}

// Types lists the channel types: those built in, then the notifiers
// modules and add-ons provide.
func (s *Service) Types() []ChannelType {
	var out []ChannelType
	for _, t := range Types() {
		out = append(out, ChannelType{Name: t.Name, Title: t.Title, Description: t.Description, Secrets: t.Secrets, Settings: t.Settings})
	}
	if s.opts.Registry != nil {
		for _, reg := range s.opts.Registry.Providers(hooks.ProvideNotifier) {
			if _, builtin := LookupType(reg.Provider.Name); builtin {
				continue
			}
			out = append(out, ChannelType{
				Name: reg.Provider.Name, Title: reg.Provider.Title, Description: reg.Provider.Description,
				Provider: reg.Owner, Settings: reg.Provider.Settings,
			})
		}
	}
	return out
}

// sendDue sums up the held deliveries whose quiet hours are over, sends
// the deliveries whose time has come, and returns how long to wait for the
// next.
func (s *Service) sendDue(ctx context.Context) time.Duration {
	now := s.opts.Now()
	if err := s.release(ctx, now); err != nil {
		s.opts.Logger.Warn("sending what quiet hours held back", "error", err)
	}
	due, err := s.db.due(ctx, now, 100)
	if err != nil {
		s.opts.Logger.Warn("reading notification deliveries", "error", err)
		return 10 * time.Second
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, d := range due {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.attempt(ctx, d)
		}()
	}
	wg.Wait()
	if len(due) == 100 {
		return 0
	}
	next, ok, err := s.db.nextDue(ctx)
	if err != nil || !ok {
		return time.Hour
	}
	return max(next.Sub(s.opts.Now()), 0)
}

// release sends what quiet hours held back once they end: one delivery as
// it was, several as one summary.
func (s *Service) release(ctx context.Context, now time.Time) error {
	held, err := s.db.heldDue(ctx, now)
	if err != nil || len(held) == 0 {
		return err
	}
	groups := map[string][]*Notification{}
	var keys []string
	for _, d := range held {
		k := d.key()
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], d)
	}
	sort.Strings(keys)
	for _, k := range keys {
		group := groups[k]
		if len(group) == 1 {
			if err := s.db.setState(ctx, group[0].ID, DeliveryPending, now); err != nil {
				return err
			}
			continue
		}
		summary := s.summary(group, now)
		if err := s.db.merge(ctx, summary, group); err != nil {
			return err
		}
	}
	return nil
}

// summary is the delivery that sums up several held ones.
func (s *Service) summary(group []*Notification, now time.Time) *Notification {
	sev := events.SeverityInfo
	digest := make([]Message, 0, len(group))
	for _, d := range group {
		if d.Severity.AtLeast(sev) {
			sev = d.Severity
		}
		if d.Message != nil {
			m := *d.Message
			m.Event = nil
			digest = append(digest, m)
		}
	}
	at := now.UTC()
	first := group[0]
	d := &Notification{
		ID: store.NewID(DeliveryPrefix), Channel: first.Channel, Person: first.Person, Address: first.Address,
		Title: fmt.Sprintf("%d notifications from quiet hours", len(group)), Severity: sev,
		State: DeliveryPending, CreatedAt: at, NextAttempt: &at,
	}
	d.Message = &Message{
		ID: string(d.ID), Title: d.Title, Severity: sev,
		Body:   fmt.Sprintf("These arrived while quiet hours held notifications back, from %s.", group[0].CreatedAt.UTC().Format("2006-01-02 15:04 UTC")),
		Digest: digest,
	}
	return d
}

// attempt sends a delivery once and records what happened.
func (s *Service) attempt(ctx context.Context, d *Notification) *Notification {
	if _, busy := s.sending.LoadOrStore(d.ID, true); busy {
		return d
	}
	defer s.sending.Delete(d.ID)
	record := context.WithoutCancel(ctx)
	now := s.opts.Now().UTC()
	var sendErr error
	var channelID store.ID
	var final bool
	if d.Message == nil {
		full, err := s.db.get(record, d.ID)
		if err != nil {
			s.opts.Logger.Warn("reading a notification delivery", "delivery", d.ID, "error", err)
			return d
		}
		d = full
	}
	switch {
	case d.Channel != "":
		c, err := ChannelKind.Get(record, s.opts.Store, d.Channel)
		switch {
		case store.ErrNotFound.Is(err) || (err == nil && c.Deleted()):
			sendErr, final = errors.New("the channel was deleted"), true
		case err != nil:
			sendErr = fmt.Errorf("the channel couldn't be read: %s", i18n.From(err).Message)
		case c.Spec.Disabled && !d.Test:
			sendErr, final = errors.New("the channel is disabled"), true
		default:
			channelID = c.ID
			ch, err := s.channel(ctx, c)
			if err != nil {
				sendErr = err
			} else {
				sendErr = ch.Send(ctx, *d.Message)
			}
		}
	case d.Person != "":
		var person *Person
		var err error
		if s.opts.Directory != nil {
			person, err = s.opts.Directory.Person(record, d.Person)
		}
		switch {
		case err != nil:
			sendErr = err
		case person == nil || person.Email == "":
			sendErr, final = errors.New("the person has no email address any more"), true
		default:
			sendErr = SendEmail(ctx, s.Env(), []string{person.Email}, *d.Message)
		}
	default:
		sendErr = SendEmail(ctx, s.Env(), []string{d.Address}, *d.Message)
	}
	d.AttemptCount++
	d.NextAttempt, d.LastError = nil, ""
	switch {
	case sendErr == nil:
		d.State = DeliverySent
	case final || d.Test || d.AttemptCount > len(s.opts.Schedule) || ErrNoRelay.Is(sendErr) && d.AttemptCount > 2:
		d.State, d.LastError = DeliveryFailed, reasonOf(sendErr)
	default:
		next := now.Add(s.opts.Schedule[d.AttemptCount-1])
		d.State, d.LastError, d.NextAttempt = DeliveryPending, reasonOf(sendErr), &next
	}
	if d.State != DeliveryPending {
		d.FinishedAt = &now
	}
	if err := s.db.record(record, d); err != nil {
		s.opts.Logger.Warn("recording a notification delivery", "delivery", d.ID, "error", err)
	}
	if channelID != "" {
		s.noteResult(record, channelID, now, sendErr)
	}
	return d
}

// failingAfter is how many attempts in a row have to fail before a channel
// counts as degraded.
const failingAfter = 3

// noteResult records how a channel did in its status.
func (s *Service) noteResult(ctx context.Context, id store.ID, when time.Time, sendErr error) {
	err := s.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:notifications"}, func(tx store.Tx) error {
		c, err := ChannelKind.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		st := c.Status
		if sendErr == nil {
			st.LastSent, st.LastError, st.ConsecutiveFailures = &when, "", 0
		} else {
			st.LastError = reasonOf(sendErr)
			st.ConsecutiveFailures++
		}
		channelHealth(&st, c.Generation, s.opts.Now())
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, id, raw)
	})
	if err != nil && !store.ErrNotFound.Is(err) {
		s.opts.Logger.Warn("recording how a notification channel did", "channel", id, "error", err)
	}
}

// channelHealth sets the phase and the Ready condition from how sending
// went, once the channel's settings are in place.
func channelHealth(st *ChannelStatus, generation int64, now time.Time) {
	if st.ObservedGeneration < generation {
		return
	}
	if st.ConsecutiveFailures >= failingAfter {
		message := fmt.Sprintf("The last %d notifications couldn't be sent: %s", st.ConsecutiveFailures, st.LastError)
		st.Phase, st.Message, st.Code = store.PhaseDegraded, message, ""
		st.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionFalse, Reason: "SendingFails", Message: message, ObservedGeneration: generation}, now)
		return
	}
	st.Phase, st.Message, st.Code = store.PhaseReady, "", ""
	st.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionTrue, Reason: "Sending", ObservedGeneration: generation}, now)
}

// Recipient is who a message is for: a channel, a person by their
// preferences and quiet hours, or an email address.
type Recipient struct {
	Channel store.ID
	Person  store.ID
	Email   string
}

// Send queues a message that isn't about an event, such as a sign-in
// alert for a person or a welcome email for a customer, and returns its
// delivery. Quiet hours apply to channels and people; urgent messages go
// out at once.
func (s *Service) Send(ctx context.Context, to Recipient, msg Message) (*Notification, error) {
	if msg.Severity == "" {
		msg.Severity = events.SeverityInfo
	}
	var quiet *QuietHours
	var d Notification
	switch {
	case to.Channel != "":
		c, err := ChannelKind.Get(ctx, s.opts.Store, to.Channel)
		if err != nil {
			return nil, err
		}
		d.Channel, quiet = c.ID, c.Spec.QuietHours
	case to.Person != "":
		p, err := s.Preferences(ctx, to.Person)
		if err != nil {
			return nil, err
		}
		d.Person, quiet = to.Person, p.QuietHours
	case to.Email != "":
		if err := (EmailSettings{To: []string{to.Email}}).check(); len(err) > 0 {
			return nil, i18n.Invalid(i18n.Field("email", i18n.ErrBadValue, "reason", "not an email address"))
		}
		d.Address = to.Email
	default:
		return nil, errors.New("notify: a message needs a channel, a person or an address")
	}
	var e events.Event
	if msg.Event != nil {
		e = *msg.Event
	}
	dl := s.newDelivery(d, msg, e, quiet)
	if err := s.db.insert(ctx, dl); err != nil {
		return nil, err
	}
	s.signal()
	return dl, nil
}

// Test sends a channel a test message at once and returns how it went.
func (s *Service) Test(ctx context.Context, channel store.ID) (*Notification, error) {
	c, err := ChannelKind.Get(ctx, s.opts.Store, channel)
	if err != nil {
		return nil, err
	}
	now := s.opts.Now().UTC()
	d := &Notification{
		ID: store.NewID(DeliveryPrefix), Channel: c.ID, Title: "Test notification", Severity: events.SeverityInfo,
		State: DeliveryPending, Test: true, CreatedAt: now,
	}
	d.Message = &Message{
		ID: string(d.ID), Title: d.Title, Severity: events.SeverityInfo,
		Body: fmt.Sprintf("This is a test from %s. If it arrived, the %s channel works.", s.Env().brand(), c.Spec.Name),
	}
	if err := s.db.insert(ctx, d); err != nil {
		return nil, err
	}
	return s.attempt(ctx, d), nil
}

// TestRelay sends a test email through the relay at once.
func (s *Service) TestRelay(ctx context.Context, to string) RelayTestOutput {
	msg := Message{
		Title: "Test email", Severity: events.SeverityInfo,
		Body: fmt.Sprintf("This is a test from %s. If it arrived, the mail relay works.", s.Env().brand()),
	}
	err := SendEmail(ctx, s.Env(), []string{to}, msg)
	s.noteRelay(context.WithoutCancel(ctx), err)
	if err != nil {
		return RelayTestOutput{Error: reasonOf(err)}
	}
	return RelayTestOutput{Sent: true}
}

func (s *Service) noteRelay(ctx context.Context, sendErr error) {
	page, _, err := RelayKind.List(ctx, s.opts.Store, store.Query{Limit: 1})
	if err != nil || len(page) == 0 {
		return
	}
	id := page[0].ID
	err = s.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:notifications"}, func(tx store.Tx) error {
		r, err := RelayKind.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		st := r.Status
		now := s.opts.Now().UTC()
		if sendErr == nil {
			st.LastSent, st.LastError = &now, ""
		} else {
			st.LastError = reasonOf(sendErr)
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, id, raw)
	})
	if err != nil && !store.ErrNotFound.Is(err) {
		s.opts.Logger.Warn("recording how the mail relay did", "error", err)
	}
}

// DeliveryFilter selects deliveries.
type DeliveryFilter struct {
	Channel store.ID
	Person  store.ID
	State   NotificationState
	Cursor  string
	Limit   int
}

// Deliveries lists deliveries, newest first, without their messages.
func (s *Service) Deliveries(ctx context.Context, f DeliveryFilter) ([]*Notification, string, error) {
	if f.Limit <= 0 || f.Limit > store.MaxLimit {
		f.Limit = store.DefaultLimit
	}
	return s.db.list(ctx, f)
}

// Notification returns one delivery with its message.
func (s *Service) Notification(ctx context.Context, id store.ID) (*Notification, error) {
	return s.db.get(ctx, id)
}
