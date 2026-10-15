package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// DeliveryState is where a delivery is.
type DeliveryState string

// Delivery states.
const (
	DeliveryPending   DeliveryState = "pending"
	DeliverySucceeded DeliveryState = "succeeded"
	DeliveryFailed    DeliveryState = "failed"
)

// Delivery is one event sent, or being sent, to one webhook.
type Delivery struct {
	ID      store.ID `json:"id" doc:"Also sent as the webhook-id header, the same for every attempt"`
	Webhook store.ID `json:"webhook"`
	// Event is the delivered event; test deliveries have none.
	Event     store.ID      `json:"event,omitempty"`
	EventType string        `json:"eventType" doc:"The type of the event delivered"`
	State     DeliveryState `json:"state" enum:"pending,succeeded,failed"`
	// Test deliveries are sent once and never tried again.
	Test         bool       `json:"test,omitempty"`
	RedeliveryOf store.ID   `json:"redeliveryOf,omitempty" doc:"The delivery this one sends again"`
	AttemptCount int        `json:"attemptCount" doc:"How many times it was sent"`
	NextAttempt  *time.Time `json:"nextAttempt,omitempty" doc:"When it is sent again, while it is pending"`
	LastStatus   int        `json:"lastStatus,omitempty" doc:"The HTTP status of the last answer"`
	LastError    string     `json:"lastError,omitempty" doc:"Why the last attempt failed"`
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	// Payload is the body sent. Lists leave it out.
	Payload json.RawMessage `json:"payload,omitempty" doc:"The body sent, the event as JSON"`
	// Attempts is the log of every attempt. Lists leave it out.
	Attempts []Attempt `json:"attempts,omitempty"`
}

// Attempt is one try at sending a delivery.
type Attempt struct {
	Number   int       `json:"number"`
	Time     time.Time `json:"time"`
	Status   int       `json:"status,omitempty" doc:"The HTTP status of the answer, when there was one"`
	Error    string    `json:"error,omitempty" doc:"Why the attempt failed"`
	Duration int64     `json:"durationMs" doc:"How long the receiver took, in milliseconds"`
	// Response is the start of what the receiver answered.
	Response string `json:"response,omitempty" doc:"The first kilobyte of the answer"`
}

// Schedule is how long a failed delivery waits before each new attempt:
// thirty seconds, then two minutes, ten, thirty, one hour, three, six and
// twelve. After the last, a delivery has failed.
var Schedule = []time.Duration{
	30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
}

// Options configure a Dispatcher.
type Options struct {
	Store   store.Store
	Log     *events.Log
	Secrets secrets.Reader
	// Client sends deliveries; nil means a client that waits ten seconds
	// for an answer and doesn't follow redirects.
	Client *http.Client
	// Schedule replaces the package's Schedule, for tests.
	Schedule []time.Duration
	// Workers is how many deliveries are sent at once; the default is four.
	Workers int
	// Version goes in the User-Agent header.
	Version string
	// Retention is how long finished deliveries are kept; the default is
	// thirty days, and at most MaxDeliveries of them.
	Retention     time.Duration
	MaxDeliveries int
	Logger        *slog.Logger
	Now           func() time.Time
}

// Dispatcher turns events into deliveries and sends them.
type Dispatcher struct {
	opts    Options
	storage storage
	wake    chan struct{}
}

// maxResponse is how much of an answer an attempt keeps.
const maxResponse = 1024

func newDispatcher(s storage, opts Options) *Dispatcher {
	if opts.Client == nil {
		opts.Client = &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if len(opts.Schedule) == 0 {
		opts.Schedule = Schedule
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Retention <= 0 {
		opts.Retention = 30 * 24 * time.Hour
	}
	if opts.MaxDeliveries <= 0 {
		opts.MaxDeliveries = 100_000
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	return &Dispatcher{opts: opts, storage: s, wake: make(chan struct{}, 1)}
}

// NewMemory returns a dispatcher that keeps its deliveries in memory.
func NewMemory(opts Options) *Dispatcher { return newDispatcher(newMemoryStorage(), opts) }

func (d *Dispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run delivers events until ctx is done. It starts where it left off: a
// dispatcher that never ran before starts with the events published from
// now on.
func (d *Dispatcher) Run(ctx context.Context) {
	sub := d.opts.Log.Subscribe(events.Filter{}, 256)
	defer sub.Close()
	if _, ok, err := d.storage.cursor(ctx); err == nil && !ok {
		last, err := d.opts.Log.LastSeq(ctx)
		if err == nil {
			err = d.storage.enqueue(ctx, nil, last)
		}
		if err != nil {
			d.opts.Logger.Error("starting the webhook dispatcher", "error", err)
		}
	}
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		if err := d.dispatch(ctx); err != nil && ctx.Err() == nil {
			d.opts.Logger.Warn("turning events into webhook deliveries", "error", err)
		}
		wait := d.sendDue(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-sub.C:
			// Events published together are dispatched together.
			for drained := false; !drained; {
				select {
				case <-sub.C:
				default:
					drained = true
				}
			}
		case <-d.wake:
		case <-prune.C:
			if err := d.storage.prune(ctx, d.opts.Now().Add(-d.opts.Retention), d.opts.MaxDeliveries); err != nil {
				d.opts.Logger.Warn("dropping old webhook deliveries", "error", err)
			}
		case <-timer.C:
		}
		timer.Stop()
	}
}

// dispatch turns the events published since the last call into deliveries
// for the webhooks that want them.
func (d *Dispatcher) dispatch(ctx context.Context) error {
	for {
		cursor, _, err := d.storage.cursor(ctx)
		if err != nil {
			return err
		}
		batch, err := d.opts.Log.EventsAfter(ctx, cursor, 200)
		if err != nil || len(batch) == 0 {
			return err
		}
		hooks, err := d.webhooks(ctx)
		if err != nil {
			return err
		}
		var out []pending
		for _, e := range batch {
			for _, w := range hooks {
				if !Wants(w, e) {
					continue
				}
				payload, err := json.Marshal(PayloadOf(e))
				if err != nil {
					return err
				}
				now := d.opts.Now().UTC()
				out = append(out, pending{
					delivery: Delivery{
						ID: store.NewID(DeliveryPrefix), Webhook: w.ID, Event: e.ID, EventType: e.Type,
						State: DeliveryPending, NextAttempt: &now, CreatedAt: now,
					},
					payload: payload,
				})
			}
		}
		if err := d.storage.enqueue(ctx, out, batch[len(batch)-1].Seq); err != nil {
			return err
		}
	}
}

// pending is a new delivery with its body.
type pending struct {
	delivery Delivery
	payload  json.RawMessage
}

// webhooks lists every webhook.
func (d *Dispatcher) webhooks(ctx context.Context) ([]*store.Object[Spec, Status], error) {
	var out []*store.Object[Spec, Status]
	q := store.Query{Limit: store.MaxLimit}
	for {
		page, next, err := Kind.List(ctx, d.opts.Store, q)
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

// sendDue sends the deliveries whose time has come and returns how long to
// wait for the next one.
func (d *Dispatcher) sendDue(ctx context.Context) time.Duration {
	now := d.opts.Now()
	due, err := d.storage.due(ctx, now, 100)
	if err != nil {
		d.opts.Logger.Warn("reading webhook deliveries", "error", err)
		return 10 * time.Second
	}
	sem := make(chan struct{}, d.opts.Workers)
	var wg sync.WaitGroup
	for _, dl := range due {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d.attempt(ctx, dl)
		}()
	}
	wg.Wait()
	if len(due) == 100 {
		return 0
	}
	next, ok, err := d.storage.nextDue(ctx)
	if err != nil || !ok {
		return time.Hour
	}
	return max(next.Sub(d.opts.Now()), 0)
}

// attempt sends a delivery once, records the attempt and decides what comes
// next.
func (d *Dispatcher) attempt(ctx context.Context, dl *Delivery) *Delivery {
	w, err := Kind.Get(ctx, d.opts.Store, dl.Webhook)
	now := d.opts.Now().UTC()
	var a Attempt
	var retryAfter time.Duration
	switch {
	case err != nil || w.Deleted():
		a = Attempt{Time: now, Error: "the webhook was deleted"}
		dl.State = DeliveryFailed
	default:
		a, retryAfter = d.send(ctx, w, dl)
	}
	dl.AttemptCount++
	a.Number = dl.AttemptCount
	dl.LastStatus, dl.LastError = a.Status, a.Error
	dl.NextAttempt = nil
	switch {
	case dl.State == DeliveryFailed:
	case a.Error == "":
		dl.State = DeliverySucceeded
	case dl.Test || a.Status == http.StatusGone || dl.AttemptCount > len(d.opts.Schedule):
		// A test is a one-off, and 410 Gone asks for nothing more.
		dl.State = DeliveryFailed
	default:
		next := now.Add(max(d.opts.Schedule[dl.AttemptCount-1], retryAfter))
		dl.NextAttempt = &next
	}
	if dl.State != DeliveryPending {
		dl.FinishedAt = &now
	}
	if err := d.storage.record(ctx, dl, a); err != nil {
		d.opts.Logger.Warn("recording a webhook delivery", "delivery", dl.ID, "error", err)
	}
	if w != nil && !w.Deleted() {
		d.noteResult(ctx, w.ID, a)
	}
	dl.Attempts = append(dl.Attempts, a)
	return dl
}

// send makes one HTTP request for a delivery. retryAfter is how long the
// receiver asked to wait, when it said.
func (d *Dispatcher) send(ctx context.Context, w *store.Object[Spec, Status], dl *Delivery) (a Attempt, retryAfter time.Duration) {
	start := d.opts.Now()
	a.Time = start.UTC()
	fail := func(format string, args ...any) (Attempt, time.Duration) {
		a.Error = fmt.Sprintf(format, args...)
		return a, retryAfter
	}
	if d.opts.Secrets == nil {
		return fail("there is no secret store to read the signing secret from")
	}
	secret, err := d.opts.Secrets.Get(ctx, secrets.Ref(w.Spec.Secret))
	if err != nil {
		return fail("the signing secret can't be read: %s", i18n.From(err).Message)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Spec.URL, bytes.NewReader(dl.Payload))
	if err != nil {
		return fail("the URL can't be used: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Racksail/"+d.opts.Version)
	req.Header.Set("webhook-id", string(dl.ID))
	req.Header.Set("webhook-timestamp", strconv.FormatInt(start.Unix(), 10))
	req.Header.Set("webhook-signature", Sign(secret, dl.ID, start, dl.Payload))
	req.Header.Set("Racksail-Event", dl.EventType)
	resp, err := d.opts.Client.Do(req)
	a.Duration = d.opts.Now().Sub(start).Milliseconds()
	if err != nil {
		var urlErr interface{ Timeout() bool }
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return fail("the receiver didn't answer in time")
		}
		return fail("the request failed: %v", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	a.Status, a.Response = resp.StatusCode, strings.ToValidUTF8(string(body), "")
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return a, 0
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fail("the receiver answered %d and sent it to %s; use that URL instead", resp.StatusCode, resp.Header.Get("Location"))
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		retryAfter = min(time.Duration(s)*time.Second, 12*time.Hour)
	}
	return fail("the receiver answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
}

// noteResult records the outcome of an attempt in the webhook's status.
// Several failures in a row make it degraded until a delivery succeeds.
func (d *Dispatcher) noteResult(ctx context.Context, id store.ID, a Attempt) {
	err := d.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:webhooks"}, func(tx store.Tx) error {
		w, err := Kind.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		st := w.Status
		when := a.Time
		st.LastDelivery, st.LastResponse = &when, a.Status
		if a.Error == "" {
			st.ConsecutiveFailures = 0
		} else {
			st.ConsecutiveFailures++
		}
		health(&st, w.Generation, d.opts.Now())
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, id, raw)
	})
	if err != nil && !store.ErrNotFound.Is(err) {
		d.opts.Logger.Warn("recording how a webhook answered", "webhook", id, "error", err)
	}
}

// health sets the phase and the Ready condition from how deliveries went,
// once the webhook's settings are in place.
func health(st *Status, generation int64, now time.Time) {
	if st.ObservedGeneration < generation {
		return
	}
	if st.ConsecutiveFailures >= failingAfter {
		message := fmt.Sprintf("The last %d attempts to send events failed.", st.ConsecutiveFailures)
		st.Phase, st.Message, st.Code = store.PhaseDegraded, message, ""
		st.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionFalse, Reason: "DeliveriesFailing", Message: message, ObservedGeneration: generation}, now)
		return
	}
	st.Phase, st.Message, st.Code = store.PhaseReady, "", ""
	st.SetCondition(store.Condition{Type: reconcile.ConditionReady, Status: store.ConditionTrue, Reason: "Delivering", ObservedGeneration: generation}, now)
}

// Test sends a webhook a ping event at once and returns how it went. Test
// deliveries are kept in the log but never tried again.
func (d *Dispatcher) Test(ctx context.Context, webhook store.ID) (*Delivery, error) {
	if _, err := Kind.Get(ctx, d.opts.Store, webhook); err != nil {
		return nil, err
	}
	now := d.opts.Now().UTC()
	id := store.NewID(DeliveryPrefix)
	payload, err := json.Marshal(Payload{
		ID: id, Type: PingType, Time: now, Severity: events.SeverityInfo, Scope: store.ServerScope(),
		Message: "This is a test delivery from Racksail. If it arrived, the webhook works.",
	})
	if err != nil {
		return nil, err
	}
	dl := &Delivery{ID: id, Webhook: webhook, EventType: PingType, State: DeliveryPending, Test: true, CreatedAt: now, Payload: payload}
	if err := d.storage.insert(ctx, dl); err != nil {
		return nil, err
	}
	return d.attempt(ctx, dl), nil
}

// Redeliver sends the event of an earlier delivery again, as a new delivery
// that is tried again on the usual schedule if this first attempt fails.
func (d *Dispatcher) Redeliver(ctx context.Context, webhook, delivery store.ID) (*Delivery, error) {
	if _, err := Kind.Get(ctx, d.opts.Store, webhook); err != nil {
		return nil, err
	}
	old, err := d.storage.get(ctx, delivery)
	if err != nil || old.Webhook != webhook {
		return nil, ErrDeliveryNotFound.New("delivery", string(delivery))
	}
	now := d.opts.Now().UTC()
	dl := &Delivery{
		ID: store.NewID(DeliveryPrefix), Webhook: webhook, Event: old.Event, EventType: old.EventType, State: DeliveryPending,
		Test: old.Test, RedeliveryOf: old.ID, CreatedAt: now, Payload: old.Payload,
	}
	if err := d.storage.insert(ctx, dl); err != nil {
		return nil, err
	}
	out := d.attempt(ctx, dl)
	d.signal()
	return out, nil
}

// Deliveries lists a webhook's deliveries, newest first, without their
// bodies and attempts. state may be empty.
func (d *Dispatcher) Deliveries(ctx context.Context, webhook store.ID, state DeliveryState, cursor string, limit int) ([]*Delivery, string, error) {
	if limit <= 0 || limit > store.MaxLimit {
		limit = store.DefaultLimit
	}
	if cursor != "" {
		if _, err := store.DecodeCursor(cursor); err != nil {
			return nil, "", err
		}
	}
	return d.storage.list(ctx, webhook, state, cursor, limit)
}

// Delivery returns one delivery of a webhook with its body and every
// attempt.
func (d *Dispatcher) Delivery(ctx context.Context, webhook, id store.ID) (*Delivery, error) {
	dl, err := d.storage.get(ctx, id)
	if err != nil || dl.Webhook != webhook {
		return nil, ErrDeliveryNotFound.New("delivery", string(id))
	}
	return dl, nil
}
