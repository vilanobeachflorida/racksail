package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/hooks"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// clock is a time the tests move by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
	// real follows the wall clock instead, for retries.
	real bool
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.real {
		return time.Now()
	}
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// directory is a fixed list of people.
type directory map[store.ID]*Person

func (d directory) Person(ctx context.Context, id store.ID) (*Person, error) { return d[id], nil }

// panel is a panel with notifications: a store, the timeline, the change
// pipeline and the service.
type panel struct {
	store    store.Store
	log      *events.Log
	engine   *changeset.Engine
	jobs     *jobs.Manager
	svc      *Service
	vault    *secrets.Memory
	clock    *clock
	registry *hooks.Registry
	admin    *auth.Actor
}

func newPanel(t *testing.T, opts Options) *panel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kinds := store.NewKindRegistry()
	for _, k := range []store.Kind{ChannelKind, RelayKind, PreferencesKind} {
		if err := kinds.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.NewSQLite(ctx, db, kinds)
	if err != nil {
		t.Fatal(err)
	}
	log, err := events.NewSQL(ctx, db, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	q, err := jobs.NewSQL(ctx, db, jobs.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	storage, err := changeset.NewSQLStorage(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	p := &panel{store: st, log: log, jobs: q, vault: newVault(), clock: &clock{now: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)}, registry: hooks.NewRegistry(),
		admin: &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}}
	opts.Store, opts.Log, opts.Secrets, opts.Registry, opts.Now = st, log, p.vault, p.registry, p.clock.Now
	opts.Logger = slog.New(slog.DiscardHandler)
	if opts.HTTP == nil {
		// The receivers the tests run listen on the loopback address.
		opts.HTTP = http.DefaultClient
	}
	if opts.Schedule == nil {
		opts.Schedule = []time.Duration{time.Millisecond, time.Millisecond}
	}
	if p.svc, err = NewSQL(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	recs := reconcile.NewRegistry()
	for _, r := range Reconcilers(p.svc) {
		if err := recs.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	runner := &reconcile.Runner{Store: st, Registry: recs, Secrets: p.vault, Events: log}
	for _, k := range kinds.All() {
		p.registry.DeclareKind(k)
	}
	for _, h := range p.svc.Hooks() {
		if err := p.registry.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	p.engine, err = changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: q, Events: log, Audit: log, Secrets: p.vault, Storage: storage,
		Hooks: &hooks.Runner{Registry: p.registry}, Authorizer: auth.NewRoleAuthorizer(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	return p
}

func (p *panel) submit(t *testing.T, ch changeset.Change) *changeset.ChangeSet {
	t.Helper()
	ctx := context.Background()
	cs, err := p.engine.Submit(ctx, changeset.Request{Actor: p.admin, Changes: []changeset.Change{ch}})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := p.jobs.Wait(wctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final, err := p.engine.Get(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func (p *panel) createChannel(t *testing.T, spec ChannelSpec, secretValues map[string]string) store.ID {
	t.Helper()
	raw, _ := json.Marshal(spec)
	cs := p.submit(t, changeset.Change{Op: changeset.OpCreate, Kind: ChannelKind.Name(), Scope: store.ServerScope(), Spec: raw, Secrets: secretValues})
	if cs.State != changeset.StateApplied {
		t.Fatalf("creating the channel: %s %+v", cs.State, cs.Error)
	}
	return cs.Changes[0].ID
}

func (p *panel) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.svc.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, "the service started", func() bool { return p.svc.Check(context.Background()) == nil })
}

func (p *panel) publish(t *testing.T, typ string, sev events.Severity, scope store.Scope) {
	t.Helper()
	// Events carry the real time, which channels compare with when they
	// were created; quiet hours go by the service's clock.
	if err := p.log.Publish(context.Background(), events.Event{Type: typ, Severity: sev, Scope: scope, Message: "Something about " + typ, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *panel) deliveries(t *testing.T, f DeliveryFilter) []*Notification {
	t.Helper()
	out, _, err := p.svc.Deliveries(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func titles(r *receiver) []string {
	var out []string
	for _, req := range r.requests() {
		var b WebhookBody
		json.Unmarshal(req.body, &b)
		out = append(out, b.Title)
	}
	return out
}

func TestRoutingEventsToChannels(t *testing.T) {
	p := newPanel(t, Options{})
	r := newReceiver(t, http.StatusOK, "")
	p.run(t)
	acct := store.AccountScope(store.NewID("acct"))
	id := p.createChannel(t, ChannelSpec{
		Name: "Ops", Type: "webhook", Settings: settings(WebhookSettings{URL: r.srv.URL}),
		Events: []string{"*.failed", "backups.*"}, MinSeverity: events.SeverityWarning,
	}, nil)
	p.publish(t, "web.site.failed", events.SeverityError, acct)
	p.publish(t, "web.site.created", events.SeverityInfo, acct)
	p.publish(t, "backups.run.finished", events.SeverityInfo, store.ServerScope())
	p.publish(t, "backups.run.failed", events.SeverityError, store.ServerScope())
	waitFor(t, "sent both failures", func() bool { return len(r.requests()) == 2 })
	// Deliveries go out side by side, in no particular order.
	got := titles(r)
	slices.Sort(got)
	if got[0] != "Something about backups.run.failed" || got[1] != "Something about web.site.failed" {
		t.Errorf("titles = %v", got)
	}
	waitFor(t, "recorded both", func() bool {
		ds := p.deliveries(t, DeliveryFilter{Channel: id})
		return len(ds) == 2 && ds[0].State == DeliverySent && ds[1].State == DeliverySent
	})
	// The channel's status is written after the delivery it reports on.
	var c *store.Object[ChannelSpec, ChannelStatus]
	waitFor(t, "noted the channel works", func() bool {
		var err error
		c, err = ChannelKind.Get(context.Background(), p.store, id)
		return err == nil && c.Status.LastSent != nil
	})
	if c.Status.Phase != store.PhaseReady {
		t.Errorf("status = %+v", c.Status)
	}
	d, err := p.svc.Notification(context.Background(), p.deliveries(t, DeliveryFilter{Channel: id})[0].ID)
	if err != nil || d.Message == nil || d.Message.Event == nil || d.Message.ID != string(d.ID) {
		t.Errorf("a delivery keeps its message: %+v, %v", d, err)
	}
}

func TestRetriesThenGivesUp(t *testing.T) {
	p := newPanel(t, Options{Schedule: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}})
	p.clock.real = true
	r := newReceiver(t, http.StatusServiceUnavailable, "try later")
	p.run(t)
	id := p.createChannel(t, ChannelSpec{Name: "Flaky", Type: "webhook", Settings: settings(WebhookSettings{URL: r.srv.URL}), Events: []string{"core.panel.*"}}, nil)
	p.publish(t, "core.panel.started", events.SeverityInfo, store.ServerScope())
	waitFor(t, "gave up", func() bool {
		ds := p.deliveries(t, DeliveryFilter{Channel: id})
		return len(ds) == 1 && ds[0].State == DeliveryFailed
	})
	ds := p.deliveries(t, DeliveryFilter{Channel: id})
	if ds[0].AttemptCount != 4 || !strings.Contains(ds[0].LastError, "503") {
		t.Errorf("delivery = %+v", ds[0])
	}
	if n := len(r.requests()); n != 4 {
		t.Errorf("%d attempts, want the first and three more", n)
	}
	// The channel's status is written after the delivery it reports on.
	var c *store.Object[ChannelSpec, ChannelStatus]
	waitFor(t, "noted the last failure", func() bool {
		c, _ = ChannelKind.Get(context.Background(), p.store, id)
		return c != nil && c.Status.ConsecutiveFailures == 4
	})
	if c.Status.Phase != store.PhaseDegraded {
		t.Errorf("a channel that keeps failing is degraded: %+v", c.Status)
	}
	// One that gets through puts it right.
	r.set(http.StatusOK, "")
	p.publish(t, "core.panel.started", events.SeverityInfo, store.ServerScope())
	waitFor(t, "recovered", func() bool {
		c, _ := ChannelKind.Get(context.Background(), p.store, id)
		return c.Status.Phase == store.PhaseReady && c.Status.ConsecutiveFailures == 0
	})
}

func TestQuietHoursSummary(t *testing.T) {
	p := newPanel(t, Options{})
	r := newReceiver(t, http.StatusOK, "")
	p.clock.set(time.Date(2026, 3, 2, 23, 0, 0, 0, time.UTC))
	p.run(t)
	id := p.createChannel(t, ChannelSpec{
		Name: "Night", Type: "webhook", Settings: settings(WebhookSettings{URL: r.srv.URL}), Events: []string{"web.*", "dns.*", "storage.*"},
		QuietHours: &QuietHours{Start: "22:00", End: "07:00", TimeZone: "UTC"},
	}, nil)
	p.clock.set(time.Date(2026, 3, 2, 23, 30, 0, 0, time.UTC))
	p.publish(t, "web.site.failed", events.SeverityError, store.ServerScope())
	p.publish(t, "dns.zone.failed", events.SeverityWarning, store.ServerScope())
	p.publish(t, "storage.pool.degraded", events.SeverityCritical, store.ServerScope())
	waitFor(t, "sent the critical one at once", func() bool { return len(r.requests()) == 1 })
	if got := titles(r)[0]; got != "Something about storage.pool.degraded" {
		t.Errorf("first = %q", got)
	}
	held := p.deliveries(t, DeliveryFilter{Channel: id, State: DeliveryHeld})
	if len(held) != 2 || !held[0].NextAttempt.Equal(time.Date(2026, 3, 3, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("held = %+v", held)
	}
	p.clock.set(time.Date(2026, 3, 3, 7, 0, 1, 0, time.UTC))
	p.svc.signal()
	waitFor(t, "sent the summary", func() bool { return len(r.requests()) == 2 })
	var summary WebhookBody
	json.Unmarshal(r.requests()[1].body, &summary)
	if summary.Title != "2 notifications from quiet hours" || len(summary.Digest) != 2 || summary.Severity != events.SeverityError {
		t.Errorf("summary = %+v", summary)
	}
	merged := p.deliveries(t, DeliveryFilter{Channel: id, State: DeliveryMerged})
	if len(merged) != 2 || merged[0].Summary == "" {
		t.Errorf("merged = %+v", merged)
	}
}

func TestQuietHoursThatDrop(t *testing.T) {
	p := newPanel(t, Options{})
	r := newReceiver(t, http.StatusOK, "")
	p.clock.set(time.Date(2026, 3, 2, 3, 0, 0, 0, time.UTC))
	p.run(t)
	id := p.createChannel(t, ChannelSpec{
		Name: "Night", Type: "webhook", Settings: settings(WebhookSettings{URL: r.srv.URL}), Events: []string{"web.*"},
		QuietHours: &QuietHours{Start: "22:00", End: "07:00", TimeZone: "UTC", Drop: true},
	}, nil)
	p.publish(t, "web.site.failed", events.SeverityError, store.ServerScope())
	waitFor(t, "dropped it", func() bool { return len(p.deliveries(t, DeliveryFilter{Channel: id, State: DeliveryDropped})) == 1 })
	if len(r.requests()) != 0 {
		t.Error("quiet hours that drop send nothing")
	}
}

func TestPeopleByPreference(t *testing.T) {
	mail := newMailServer(t, nil, false)
	mine, theirs := store.NewID("acct"), store.NewID("acct")
	alice := &Person{ID: store.NewID("user"), Name: "Alice", Email: "alice@example.com", Actor: &auth.Actor{
		Type: auth.ActorUser, ID: "", Grants: []auth.Grant{{Role: auth.RoleOwner, Scope: store.AccountScope(mine)}},
	}}
	alice.Actor.ID = alice.ID
	p := newPanel(t, Options{Directory: directory{alice.ID: alice}, Authorizer: auth.NewRoleAuthorizer(nil), BrandName: "Acme"})
	p.run(t)
	relay, _ := json.Marshal(RelaySpec{Host: "127.0.0.1", Port: mail.port(), Security: SecurityNone, From: "alerts@acme.example"})
	if cs := p.submit(t, changeset.Change{Op: changeset.OpCreate, Kind: RelayKind.Name(), Scope: store.ServerScope(), Spec: relay}); cs.State != changeset.StateApplied {
		t.Fatalf("relay: %+v", cs.Error)
	}
	ctx := context.Background()
	set := func(prefs NotificationPreferences) (*changeset.ChangeSet, error) {
		req, err := p.svc.PreferencesRequest(ctx, alice.ID, prefs)
		if err != nil {
			return nil, err
		}
		req.Actor = PreferencesActor(alice.Actor)
		cs, err := p.engine.Submit(ctx, req)
		if err != nil {
			return nil, err
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if _, err := p.jobs.Wait(wctx, cs.Job); err != nil {
			return nil, err
		}
		return p.engine.Get(ctx, cs.ID)
	}
	if _, err := set(NotificationPreferences{Email: true, Events: []string{"bad pattern!"}}); !errors.Is(err, i18n.ErrInvalid) || !strings.Contains(string(mustJSON(i18n.From(err))), "spec.events[0]") {
		t.Errorf("bad preferences are refused: %v", err)
	}
	if cs, err := set(NotificationPreferences{Email: true, Muted: []string{"dns.*"}}); err != nil || cs.State != changeset.StateApplied {
		t.Fatalf("setting alice's preferences: %v %+v", err, cs)
	}
	p.publish(t, "web.site.failed", events.SeverityError, store.AccountScope(theirs))
	p.publish(t, "dns.zone.failed", events.SeverityError, store.AccountScope(mine))
	p.publish(t, "web.site.created", events.SeverityInfo, store.AccountScope(mine))
	p.publish(t, "web.site.failed", events.SeverityError, store.AccountScope(mine))
	waitFor(t, "emailed alice", func() bool { return len(mail.messages()) == 1 })
	time.Sleep(50 * time.Millisecond)
	got := mail.messages()
	if len(got) != 1 || got[0].to[0] != "alice@example.com" || !strings.Contains(got[0].data, "web.site.failed") {
		t.Fatalf("alice only hears about her own failures, and not about muted DNS: %+v", got)
	}
	prefs, err := p.svc.Preferences(ctx, alice.ID)
	if err != nil || !prefs.Email || prefs.UpdatedAt == nil {
		t.Errorf("preferences = %+v, %v", prefs, err)
	}
	// A message for her alone, such as a sign-in alert.
	if _, err := p.svc.Send(ctx, Recipient{Person: alice.ID}, Message{Title: "New sign-in", Body: "From Lisbon.", Severity: events.SeverityNotice}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "sent the sign-in alert", func() bool { return len(mail.messages()) == 2 })
	if _, err := p.svc.Send(ctx, Recipient{Email: "customer@example.net"}, Message{Title: "Welcome"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "welcomed the customer", func() bool { return len(mail.messages()) == 3 })
}

func TestActionsAndProviders(t *testing.T) {
	p := newPanel(t, Options{})
	r := newReceiver(t, http.StatusOK, "")
	p.run(t)
	id := p.createChannel(t, ChannelSpec{Name: "Ops", Type: "webhook", Settings: settings(WebhookSettings{URL: r.srv.URL}), Events: []string{"*"}, Disabled: true}, map[string]string{"settings.secret": "shh"})
	cs := p.submit(t, changeset.Change{Op: changeset.OpAction, Kind: ChannelKind.Name(), ID: id, Action: "test"})
	var out ChannelTestOutput
	if err := json.Unmarshal(cs.Changes[0].Output, &out); err != nil || out.Notification == nil || out.Notification.State != DeliverySent || !out.Notification.Test {
		t.Fatalf("test output = %s, %v", cs.Changes[0].Output, err)
	}
	if h := r.requests()[0].header; h.Get("webhook-signature") == "" {
		t.Error("the secret set with the change signs the test, even on a disabled channel")
	}

	// A type nothing provides is refused when the change is planned.
	bad := ChannelSpec{Name: "Gotify", Type: "gotify", Settings: json.RawMessage(`{"room":"ops"}`), Events: []string{"core.panel.*"}}
	raw, _ := json.Marshal(bad)
	_, err := p.engine.Submit(context.Background(), changeset.Request{Actor: p.admin, Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: ChannelKind.Name(), Scope: store.ServerScope(), Spec: raw}}})
	if !strings.Contains(string(mustJSON(i18n.From(err))), "notify.unknown_channel_type") {
		t.Fatalf("unknown type: %s", mustJSON(i18n.From(err)))
	}
	// Once a module or add-on provides it, it works like any other.
	rec := &recorder{name: "gotify"}
	err = p.registry.Register(hooks.Registration{Point: hooks.ProvideNotifier, Kind: hooks.Provide, Owner: "module:home", Provider: &hooks.Provider{
		Name: "gotify", Title: "Gotify", Open: func(ctx context.Context, settings json.RawMessage) (any, error) { return rec, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	gid := p.createChannel(t, bad, nil)
	p.publish(t, "core.panel.started", events.SeverityInfo, store.ServerScope())
	waitFor(t, "sent through the provider", func() bool {
		ds := p.deliveries(t, DeliveryFilter{Channel: gid})
		return len(ds) == 1 && ds[0].State == DeliverySent
	})
	if len(rec.got) != 1 {
		t.Errorf("provider got %d messages", len(rec.got))
	}
	var names []string
	for _, typ := range p.svc.Types() {
		names = append(names, typ.Name+"/"+typ.Provider)
	}
	if !strings.Contains(strings.Join(names, ","), "gotify/module:home") {
		t.Errorf("types = %v", names)
	}

	// The relay's test says what went wrong.
	relay, _ := json.Marshal(RelaySpec{Host: "127.0.0.1", Port: 1, Security: SecurityNone, From: "alerts@acme.example"})
	if cs := p.submit(t, changeset.Change{Op: changeset.OpCreate, Kind: RelayKind.Name(), Scope: store.ServerScope(), Spec: relay}); cs.State != changeset.StateApplied {
		t.Fatalf("relay: %s", mustJSON(cs.Error))
	}
	page, _, _ := RelayKind.List(context.Background(), p.store, store.Query{})
	tested := p.submit(t, changeset.Change{Op: changeset.OpAction, Kind: RelayKind.Name(), ID: page[0].ID, Action: "test", Input: json.RawMessage(`{"to":"me@example.com"}`)})
	var res RelayTestOutput
	if err := json.Unmarshal(tested.Changes[0].Output, &res); err != nil || res.Sent || !strings.Contains(res.Error, "connecting to the relay") {
		t.Errorf("relay test = %s, %v", tested.Changes[0].Output, err)
	}
}

func TestChannelValidation(t *testing.T) {
	p := newPanel(t, Options{})
	for name, spec := range map[string]ChannelSpec{
		"bad pattern":     {Name: "x", Type: "webhook", Settings: settings(WebhookSettings{URL: "https://example.com"}), Events: []string{"Web Site"}},
		"bad settings":    {Name: "x", Type: "webhook", Settings: settings(WebhookSettings{URL: "ftp://example.com"}), Events: []string{"*"}},
		"bad quiet hours": {Name: "x", Type: "webhook", Settings: settings(WebhookSettings{URL: "https://example.com"}), Events: []string{"*"}, QuietHours: &QuietHours{Start: "22:00", End: "22:00"}},
		"server as owner": {Name: "x", Type: "webhook", Settings: settings(WebhookSettings{URL: "https://example.com"}), Events: []string{"*"}, Owner: &store.Scope{Level: store.ScopeServer}},
	} {
		raw, _ := json.Marshal(spec)
		cs, err := p.engine.Submit(context.Background(), changeset.Request{Actor: p.admin, Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: ChannelKind.Name(), Scope: store.ServerScope(), Spec: raw}}})
		if err == nil && cs.State == changeset.StateApplied {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func TestQuietHoursWindows(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	night := &QuietHours{Start: "22:00", End: "07:00", TimeZone: "Europe/Madrid"}
	for _, tc := range []struct {
		at    time.Time
		quiet bool
		until time.Time
	}{
		{time.Date(2026, 3, 2, 21, 59, 0, 0, madrid), false, time.Time{}},
		{time.Date(2026, 3, 2, 22, 0, 0, 0, madrid), true, time.Date(2026, 3, 3, 7, 0, 0, 0, madrid)},
		{time.Date(2026, 3, 3, 6, 59, 0, 0, madrid), true, time.Date(2026, 3, 3, 7, 0, 0, 0, madrid)},
		{time.Date(2026, 3, 3, 7, 0, 0, 0, madrid), false, time.Time{}},
		// The night clocks go forward still ends at seven, local time.
		{time.Date(2026, 3, 28, 23, 0, 0, 0, madrid), true, time.Date(2026, 3, 29, 7, 0, 0, 0, madrid)},
	} {
		quiet, until := night.Quiet(tc.at)
		if quiet != tc.quiet || !until.Equal(tc.until) {
			t.Errorf("%s: %v until %s, want %v until %s", tc.at, quiet, until, tc.quiet, tc.until)
		}
	}
	weekend := &QuietHours{Start: "00:00", End: "23:59", TimeZone: "UTC", Days: []string{"sat", "sun"}}
	if q, _ := weekend.Quiet(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)); !q {
		t.Error("Saturday is quiet")
	}
	if q, _ := weekend.Quiet(time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)); q {
		t.Error("Monday isn't")
	}
	// Overnight hours that start on Friday cover early Saturday, not early
	// Friday.
	friday := &QuietHours{Start: "22:00", End: "07:00", TimeZone: "UTC", Days: []string{"fri"}}
	if q, _ := friday.Quiet(time.Date(2026, 3, 7, 3, 0, 0, 0, time.UTC)); !q {
		t.Error("early Saturday follows Friday night")
	}
	if q, _ := friday.Quiet(time.Date(2026, 3, 6, 3, 0, 0, 0, time.UTC)); q {
		t.Error("early Friday follows Thursday night")
	}
	if hold, _ := night.Holds(events.SeverityCritical, time.Date(2026, 3, 2, 23, 0, 0, 0, madrid)); hold {
		t.Error("critical notifications go out during quiet hours")
	}
	strict := &QuietHours{Start: "22:00", End: "07:00", Urgent: events.SeverityError}
	if hold, _ := strict.Holds(events.SeverityError, time.Date(2026, 3, 2, 23, 0, 0, 0, time.Local)); hold {
		t.Error("the urgent threshold lets errors through")
	}
	for name, q := range map[string]QuietHours{
		"bad time": {Start: "25:00", End: "07:00"}, "same time": {Start: "07:00", End: "07:00"},
		"bad zone": {Start: "22:00", End: "07:00", TimeZone: "Mars/Olympus"}, "bad day": {Start: "22:00", End: "07:00", Days: []string{"someday"}},
	} {
		if len(q.check("q")) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPreferencesWant(t *testing.T) {
	p := NotificationPreferences{Email: true}
	for typ, want := range map[string]bool{"web.site.failed": true, "security.login.blocked": true, "web.site.created": false} {
		if got := p.Wants(events.Event{Type: typ, Severity: events.SeverityError}); got != want {
			t.Errorf("default %s: %v", typ, got)
		}
	}
	if p.Wants(events.Event{Type: "web.site.failed", Severity: events.SeverityNotice}) {
		t.Error("below warning isn't wanted by default")
	}
	if (&NotificationPreferences{}).Wants(events.Event{Type: "web.site.failed", Severity: events.SeverityCritical}) {
		t.Error("no email means nothing")
	}
}
