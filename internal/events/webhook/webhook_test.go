package webhook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/secrets"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func TestSigning(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"type":"web.site.created"}`)
	sig := Sign([]byte("plain secret"), "dlv_01", now, body)
	if !strings.HasPrefix(sig, "v1,") {
		t.Fatalf("signature = %s", sig)
	}
	if !Verify([]byte("plain secret"), "dlv_01", "1700000000", sig, body, now.Add(time.Minute), 5*time.Minute) {
		t.Error("a signature checks out")
	}
	for name, ok := range map[string]bool{
		"other body":   Verify([]byte("plain secret"), "dlv_01", "1700000000", sig, []byte(`{}`), now, time.Minute),
		"other id":     Verify([]byte("plain secret"), "dlv_02", "1700000000", sig, body, now, time.Minute),
		"other secret": Verify([]byte("wrong"), "dlv_01", "1700000000", sig, body, now, time.Minute),
		"too old":      Verify([]byte("plain secret"), "dlv_01", "1700000000", sig, body, now.Add(time.Hour), time.Minute),
		"bad stamp":    Verify([]byte("plain secret"), "dlv_01", "soon", sig, body, now, time.Minute),
	} {
		if ok {
			t.Errorf("%s: a signature that shouldn't check out did", name)
		}
	}
	// A secret in the Standard Webhooks format is decoded first, as their
	// libraries do.
	key := []byte("0123456789abcdef0123456789abcdef")
	whsec := "whsec_" + base64.StdEncoding.EncodeToString(key)
	if Sign([]byte(whsec), "dlv_01", now, body) != Sign(key, "dlv_01", now, body) {
		t.Error("whsec_ secrets are decoded")
	}
	if !Verify([]byte(whsec), "dlv_01", "1700000000", "v1,other "+Sign(key, "dlv_01", now, body), body, now, time.Minute) {
		t.Error("any of several listed signatures may match")
	}
}

func TestWants(t *testing.T) {
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	acct := store.AccountScope(store.NewID("acct"))
	w := &store.Object[Spec, Status]{Meta: store.Meta{CreatedAt: created}, Spec: Spec{Events: []string{"web.site.*", "*.failed"}}}
	event := func(typ string, sev events.Severity, scope store.Scope, at time.Time) events.Event {
		return events.Event{Type: typ, Severity: sev, Scope: scope, Time: at}
	}
	later := created.Add(time.Hour)
	for name, tc := range map[string]struct {
		mutate func(*store.Object[Spec, Status])
		event  events.Event
		want   bool
	}{
		"matching type":     {nil, event("web.site.created", events.SeverityInfo, acct, later), true},
		"another pattern":   {nil, event("dns.zone.failed", events.SeverityError, acct, later), true},
		"other type":        {nil, event("dns.zone.created", events.SeverityInfo, acct, later), false},
		"before it existed": {nil, event("web.site.created", events.SeverityInfo, acct, created.Add(-time.Second)), false},
		"disabled": {func(w *store.Object[Spec, Status]) { w.Spec.Disabled = true },
			event("web.site.created", events.SeverityInfo, acct, later), false},
		"too quiet": {func(w *store.Object[Spec, Status]) { w.Spec.MinSeverity = events.SeverityWarning },
			event("web.site.created", events.SeverityInfo, acct, later), false},
		"urgent enough": {func(w *store.Object[Spec, Status]) { w.Spec.MinSeverity = events.SeverityWarning },
			event("web.site.failed", events.SeverityError, acct, later), true},
		"its owner": {func(w *store.Object[Spec, Status]) { w.Spec.Owner = &acct },
			event("web.site.created", events.SeverityInfo, acct, later), true},
		"another owner": {func(w *store.Object[Spec, Status]) { w.Spec.Owner = &acct },
			event("web.site.created", events.SeverityInfo, store.ServerScope(), later), false},
	} {
		c := *w
		if tc.mutate != nil {
			tc.mutate(&c)
		}
		if got := Wants(&c, tc.event); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// receiver is a webhook receiver that checks signatures and answers with
// the statuses it is given, then 204.
type receiver struct {
	srv    *httptest.Server
	secret []byte

	mu       sync.Mutex
	answers  []int
	received []Payload
	headers  []http.Header
	bad      int
}

func newReceiver(t *testing.T, secret string, answers ...int) *receiver {
	r := &receiver{secret: []byte(secret), answers: answers}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		if !Verify(r.secret, store.ID(req.Header.Get("webhook-id")), req.Header.Get("webhook-timestamp"), req.Header.Get("webhook-signature"), body, time.Now(), 5*time.Minute) {
			r.bad++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var p Payload
		if err := json.Unmarshal(body, &p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.received = append(r.received, p)
		r.headers = append(r.headers, req.Header.Clone())
		status := http.StatusOK
		if len(r.answers) > 0 {
			status, r.answers = r.answers[0], r.answers[1:]
		}
		w.WriteHeader(status)
		io.WriteString(w, "thanks")
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.received {
		out = append(out, p.Type)
	}
	return out
}

// world is a panel with webhooks: a store, the event log, the change
// pipeline and a dispatcher.
type world struct {
	store      store.Store
	log        *events.Log
	engine     *changeset.Engine
	dispatcher *Dispatcher
	vault      *secrets.Memory
	admin      *auth.Actor
	jobs       *jobs.Manager
	runner     *reconcile.Runner
}

func newWorld(t *testing.T, ctx context.Context, db *store.DB, vault *secrets.Memory, schedule ...time.Duration) *world {
	t.Helper()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(Kind); err != nil {
		t.Fatal(err)
	}
	var st store.Store = store.NewMemory(kinds)
	log := events.NewMemory(1000)
	q := jobs.NewMemory(ctx, jobs.ManagerOptions{})
	var storage changeset.Storage
	opts := Options{Secrets: vault, Schedule: schedule, Version: "test", Logger: slog.New(slog.DiscardHandler)}
	var d *Dispatcher
	if db != nil {
		var err error
		if st, err = store.NewSQLite(ctx, db, kinds); err != nil {
			t.Fatal(err)
		}
		if log, err = events.NewSQL(ctx, db, events.Options{}); err != nil {
			t.Fatal(err)
		}
		if q, err = jobs.NewSQL(ctx, db, jobs.ManagerOptions{}); err != nil {
			t.Fatal(err)
		}
		if storage, err = changeset.NewSQLStorage(ctx, db); err != nil {
			t.Fatal(err)
		}
		opts.Store, opts.Log = st, log
		if d, err = NewSQL(ctx, db, opts); err != nil {
			t.Fatal(err)
		}
	} else {
		opts.Store, opts.Log = st, log
		d = NewMemory(opts)
	}
	recs := reconcile.NewRegistry()
	if err := recs.Register(Reconciler(d)); err != nil {
		t.Fatal(err)
	}
	runner := &reconcile.Runner{Store: st, Registry: recs, Secrets: vault, Events: log}
	engine, err := changeset.NewEngine(changeset.Options{
		Store: st, Kinds: kinds, Runner: runner, Jobs: q, Events: log, Audit: log, Secrets: vault, Storage: storage,
		Authorizer: auth.NewRoleAuthorizer(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	return &world{store: st, log: log, engine: engine, dispatcher: d, vault: vault, jobs: q, runner: runner,
		admin: &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI, Grants: []auth.Grant{{Role: auth.RoleAdmin, Scope: store.ServerScope()}}}}
}

func newVault() *secrets.Memory {
	key, _ := secrets.NewKey()
	box, _ := secrets.NewBox(key)
	return secrets.NewMemory(box)
}

func (w *world) submit(t *testing.T, ch changeset.Change) *changeset.ChangeSet {
	t.Helper()
	ctx := context.Background()
	cs, err := w.engine.Submit(ctx, changeset.Request{Actor: w.admin, Changes: []changeset.Change{ch}})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := w.jobs.Wait(wctx, cs.Job); err != nil {
		t.Fatal(err)
	}
	final, err := w.engine.Get(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func (w *world) createWebhook(t *testing.T, url, secret string, patterns ...string) store.ID {
	t.Helper()
	spec, _ := json.Marshal(Spec{URL: url, Events: patterns})
	cs := w.submit(t, changeset.Change{Op: changeset.OpCreate, Kind: Kind.Name(), Scope: store.ServerScope(), Spec: spec, Secrets: map[string]string{"secret": secret}})
	if cs.State != changeset.StateApplied {
		t.Fatalf("creating the webhook: %+v", cs)
	}
	return cs.Changes[0].ID
}

func (w *world) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.dispatcher.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
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

func forEachWorld(t *testing.T, test func(t *testing.T, newWorld func(schedule ...time.Duration) *world)) {
	t.Run("memory", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		test(t, func(schedule ...time.Duration) *world { return newWorld(t, ctx, nil, newVault(), schedule...) })
	})
	t.Run("sqlite", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		test(t, func(schedule ...time.Duration) *world { return newWorld(t, ctx, db, newVault(), schedule...) })
	})
}

func TestDeliveringEvents(t *testing.T) {
	forEachWorld(t, func(t *testing.T, newWorld func(...time.Duration) *world) {
		ctx := context.Background()
		w := newWorld()
		recv := newReceiver(t, "s3cret")
		w.run(t)
		id := w.createWebhook(t, recv.srv.URL, "s3cret", "web.site.*")
		site := store.NewID("site")
		for _, e := range []events.Event{
			{Type: "web.site.created", Subject: site, Kind: "web.site", Message: "Site created", Actor: &auth.Actor{Type: auth.ActorUser, ID: w.admin.ID, IP: "203.0.113.7", Source: auth.SourceCLI}},
			{Type: "dns.zone.created"},
			{Type: "web.site.deployed", Subject: site, Data: json.RawMessage(`{"release":"r2"}`)},
		} {
			if err := w.log.Publish(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, "delivered both site events", func() bool { return len(recv.types()) == 2 })
		if got := recv.types(); !slices.Equal(got, []string{"web.site.created", "web.site.deployed"}) && !slices.Equal(got, []string{"web.site.deployed", "web.site.created"}) {
			t.Errorf("delivered = %v", got)
		}
		recv.mu.Lock()
		first := recv.received[slices.IndexFunc(recv.received, func(p Payload) bool { return p.Type == "web.site.created" })]
		header := recv.headers[0]
		recv.mu.Unlock()
		if first.Subject != site || first.Actor == nil || first.Actor.ID != w.admin.ID || first.Actor.Source != "cli" || first.ID.Prefix() != events.EventPrefix {
			t.Errorf("payload = %+v", first)
		}
		if raw, _ := json.Marshal(first); strings.Contains(string(raw), "203.0.113.7") {
			t.Error("addresses don't leave the server")
		}
		if header.Get("User-Agent") != "Racksail/test" || header.Get("Content-Type") != "application/json" || !strings.HasPrefix(header.Get("Racksail-Event"), "web.site.") {
			t.Errorf("headers = %v", header)
		}
		var list []*Delivery
		waitFor(t, "recorded both deliveries", func() bool {
			list, _, _ = w.dispatcher.Deliveries(ctx, id, DeliverySucceeded, "", 10)
			return len(list) == 2
		})
		if list[0].Payload != nil || list[0].AttemptCount != 1 || list[0].LastStatus != http.StatusOK {
			t.Errorf("listed delivery = %+v", list[0])
		}
		one, err := w.dispatcher.Delivery(ctx, id, list[0].ID)
		if err != nil || len(one.Attempts) != 1 || one.Attempts[0].Response != "thanks" || len(one.Payload) == 0 {
			t.Errorf("delivery = %+v, %v", one, err)
		}
		waitFor(t, "noted the delivery on the webhook", func() bool {
			got, _ := Kind.Get(ctx, w.store, id)
			return got.Status.LastDelivery != nil && got.Status.LastResponse == http.StatusOK && got.Status.Phase == store.PhaseReady
		})
		if recv.bad != 0 {
			t.Errorf("%d deliveries had bad signatures", recv.bad)
		}
	})
}

func TestRetriesAndGivingUp(t *testing.T) {
	forEachWorld(t, func(t *testing.T, newWorld func(...time.Duration) *world) {
		ctx := context.Background()
		w := newWorld(10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
		w.run(t)
		flaky := newReceiver(t, "k", http.StatusInternalServerError, http.StatusServiceUnavailable)
		down := newReceiver(t, "k", 500, 500, 500, 500, 500, 500, 500)
		gone := newReceiver(t, "k", http.StatusGone)
		flakyID := w.createWebhook(t, flaky.srv.URL, "k", "test.thing.*")
		downID := w.createWebhook(t, down.srv.URL, "k", "test.thing.*")
		goneID := w.createWebhook(t, gone.srv.URL, "k", "test.thing.*")
		if err := w.log.Publish(ctx, events.Event{Type: "test.thing.happened"}); err != nil {
			t.Fatal(err)
		}
		finished := func(id store.ID, state DeliveryState) *Delivery {
			t.Helper()
			var list []*Delivery
			waitFor(t, "finished the delivery to "+string(id), func() bool {
				list, _, _ = w.dispatcher.Deliveries(ctx, id, state, "", 10)
				return len(list) == 1
			})
			d, _ := w.dispatcher.Delivery(ctx, id, list[0].ID)
			return d
		}
		if d := finished(flakyID, DeliverySucceeded); d.AttemptCount != 3 || d.Attempts[0].Status != 500 || d.Attempts[2].Status != 200 {
			t.Errorf("a receiver that recovers gets the event in the end: %+v", d)
		}
		if d := finished(downID, DeliveryFailed); d.AttemptCount != 6 || d.FinishedAt == nil || !strings.Contains(d.LastError, "500") {
			t.Errorf("after the schedule runs out a delivery has failed: %+v", d)
		}
		if d := finished(goneID, DeliveryFailed); d.AttemptCount != 1 {
			t.Errorf("410 Gone stops the attempts: %+v", d)
		}
		waitFor(t, "marked the failing webhook degraded", func() bool {
			got, _ := Kind.Get(ctx, w.store, downID)
			return got.Status.Phase == store.PhaseDegraded && got.Status.ConsecutiveFailures == 6 && !got.Status.IsTrue(reconcile.ConditionReady)
		})
		// A resync doesn't hide that it is failing.
		if err := w.runner.Reconcile(ctx, downID, nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := Kind.Get(ctx, w.store, downID); got.Status.Phase != store.PhaseDegraded {
			t.Errorf("after a reconcile = %+v", got.Status)
		}
		if got, _ := Kind.Get(ctx, w.store, flakyID); got.Status.ConsecutiveFailures != 0 || got.Status.Phase != store.PhaseReady {
			t.Errorf("a success clears the failures: %+v", got.Status)
		}
	})
}

func TestTestAndRedeliverActions(t *testing.T) {
	forEachWorld(t, func(t *testing.T, newWorld func(...time.Duration) *world) {
		ctx := context.Background()
		w := newWorld()
		recv := newReceiver(t, "whsec_"+base64.StdEncoding.EncodeToString([]byte("a key of my own")))
		w.run(t)
		id := w.createWebhook(t, recv.srv.URL, "whsec_"+base64.StdEncoding.EncodeToString([]byte("a key of my own")), "*")
		cs := w.submit(t, changeset.Change{Op: changeset.OpAction, Kind: Kind.Name(), ID: id, Action: "test"})
		var out Delivery
		if err := json.Unmarshal(cs.Changes[0].Output, &out); err != nil || out.State != DeliverySucceeded || !out.Test || out.EventType != PingType {
			t.Fatalf("test output = %s, %v", cs.Changes[0].Output, err)
		}
		waitFor(t, "received the ping", func() bool { return slices.Contains(recv.types(), PingType) })

		redo := w.submit(t, changeset.Change{Op: changeset.OpAction, Kind: Kind.Name(), ID: id, Action: "redeliver", Input: json.RawMessage(`{"delivery":"` + string(out.ID) + `"}`)})
		var again Delivery
		if err := json.Unmarshal(redo.Changes[0].Output, &again); err != nil || again.RedeliveryOf != out.ID || again.State != DeliverySucceeded {
			t.Fatalf("redelivery = %s, %v", redo.Changes[0].Output, err)
		}
		if _, err := w.engine.Submit(ctx, changeset.Request{Actor: w.admin, Changes: []changeset.Change{{
			Op: changeset.OpAction, Kind: Kind.Name(), ID: id, Action: "redeliver", Input: json.RawMessage(`{"delivery":"nope"}`),
		}}}); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("a malformed delivery ID: %v", err)
		}
		missing, err := w.engine.Submit(ctx, changeset.Request{Actor: w.admin, Changes: []changeset.Change{{
			Op: changeset.OpAction, Kind: Kind.Name(), ID: id, Action: "redeliver", Input: json.RawMessage(`{"delivery":"` + string(store.NewID(DeliveryPrefix)) + `"}`),
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if final, _ := w.jobs.Wait(ctx, missing.Job); final.State != jobs.StateFailed || final.Error.Code != ErrDeliveryNotFound.Code {
			t.Errorf("a delivery that doesn't exist: %+v", final)
		}
	})
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, ctx, nil, newVault())
	submit := func(spec Spec, secret string) error {
		raw, _ := json.Marshal(spec)
		ch := changeset.Change{Op: changeset.OpCreate, Kind: Kind.Name(), Scope: store.ServerScope(), Spec: raw}
		if secret != "" {
			ch.Secrets = map[string]string{"secret": secret}
		}
		_, err := w.engine.Submit(ctx, changeset.Request{Actor: w.admin, DryRun: true, Changes: []changeset.Change{ch}})
		return err
	}
	acct := store.AccountScope(store.NewID("acct"))
	server := store.ServerScope()
	for name, tc := range map[string]struct {
		spec   Spec
		secret string
		path   string
	}{
		"no secret":        {Spec{URL: "https://example.test/hook", Events: []string{"*"}}, "", "secrets.secret"},
		"ftp":              {Spec{URL: "ftp://example.test/hook", Events: []string{"*"}}, "k", "spec.url"},
		"no host":          {Spec{URL: "https:///hook", Events: []string{"*"}}, "k", "spec.url"},
		"credentials":      {Spec{URL: "https://me:pw@example.test/hook", Events: []string{"*"}}, "k", "spec.url"},
		"bad pattern":      {Spec{URL: "https://example.test/hook", Events: []string{"Web/Site"}}, "k", "spec.events[0]"},
		"no events":        {Spec{URL: "https://example.test/hook", Events: []string{}}, "k", "spec.events"},
		"server as owner":  {Spec{URL: "https://example.test/hook", Events: []string{"*"}, Owner: &server}, "k", "spec.owner"},
		"unknown severity": {Spec{URL: "https://example.test/hook", Events: []string{"*"}, MinSeverity: "loud"}, "k", "spec.minSeverity"},
	} {
		err := submit(tc.spec, tc.secret)
		e := i18n.From(err)
		if err == nil || e.Code != i18n.ErrInvalid.Code || len(e.Fields) == 0 || e.Fields[0].Path != tc.path {
			t.Errorf("%s: %v (%+v)", name, err, e)
		}
	}
	if err := submit(Spec{URL: "https://example.test/hook", Events: []string{"web.site.*"}, Owner: &acct, MinSeverity: events.SeverityError}, "k"); err != nil {
		t.Errorf("a valid webhook: %v", err)
	}
}

// The dispatcher keeps its place in the panel database: after a restart it
// delivers what was published while it was away, and nothing twice.
func TestDispatcherResumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault := newVault()
	recv := newReceiver(t, "k")
	first := newWorld(t, ctx, db, vault)
	firstRun, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		first.dispatcher.Run(firstRun)
	}()
	first.createWebhook(t, recv.srv.URL, "k", "test.*")
	if err := first.log.Publish(ctx, events.Event{Type: "test.thing.first"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delivered the first event", func() bool { return len(recv.types()) == 1 })
	stop()
	<-done
	if err := first.log.Publish(ctx, events.Event{Type: "test.thing.second"}); err != nil {
		t.Fatal(err)
	}

	second := newWorld(t, ctx, db, vault)
	second.run(t)
	waitFor(t, "delivered what was published while it was away", func() bool { return len(recv.types()) == 2 })
	time.Sleep(50 * time.Millisecond)
	if got := recv.types(); !slices.Equal(got, []string{"test.thing.first", "test.thing.second"}) {
		t.Errorf("delivered = %v", got)
	}
}
