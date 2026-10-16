package events

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// forEachLog runs a test against a log kept in memory and one kept in the
// panel database.
func forEachLog(t *testing.T, opts Options, test func(t *testing.T, l *Log)) {
	t.Run("memory", func(t *testing.T) {
		l := NewMemory(100)
		l.retention = opts.Retention.withDefaults()
		l.denials.limits = opts.Denials.withDefaults()
		if opts.Now != nil {
			l.now = opts.Now
		}
		test(t, l)
	})
	t.Run("sqlite", func(t *testing.T) {
		db, err := store.OpenDB(context.Background(), filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		l, err := NewSQL(context.Background(), db, opts)
		if err != nil {
			t.Fatal(err)
		}
		test(t, l)
	})
}

func TestPublishSubscribe(t *testing.T) {
	forEachLog(t, Options{}, func(t *testing.T, m *Log) {
		ctx := context.Background()
		acct := store.AccountScope(store.NewID("acct"))
		sites := m.Subscribe(Filter{Types: []string{"web.site.*"}, Scope: &acct}, 4)
		defer sites.Close()
		failures := m.Subscribe(Filter{MinSeverity: SeverityError}, 4)
		defer failures.Close()

		if err := m.Publish(ctx, Event{Type: "web.site.created", Scope: acct, Message: "Site created"}); err != nil {
			t.Fatal(err)
		}
		if err := m.Publish(ctx, Event{Type: "backups.run.failed", Severity: SeverityError, Scope: store.ServerScope()}); err != nil {
			t.Fatal(err)
		}
		if err := m.Publish(ctx, Event{Type: "web.site.created", Scope: store.ServerScope()}); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-sites.C:
			if e.ID.Prefix() != EventPrefix || e.Time.IsZero() || e.Severity != SeverityInfo || e.Seq == 0 {
				t.Errorf("event = %+v", e)
			}
		case <-time.After(time.Second):
			t.Fatal("no site event")
		}
		select {
		case e := <-sites.C:
			t.Errorf("the scope filter let through %+v", e)
		default:
		}
		if e := <-failures.C; e.Type != "backups.run.failed" {
			t.Errorf("failure subscriber got %+v", e)
		}
		if err := m.Publish(ctx, Event{Type: "bad"}); err == nil {
			t.Error("malformed event types must be refused")
		}
		if err := m.Publish(ctx, Event{Type: "web.site.created", Severity: "loud"}); err == nil {
			t.Error("unknown severities must be refused")
		}
	})
}

func TestSlowSubscribersDrop(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(100)
	sub := m.Subscribe(Filter{}, 1)
	for i := 0; i < 3; i++ {
		if err := m.Publish(ctx, Event{Type: "web.site.updated"}); err != nil {
			t.Fatal(err)
		}
	}
	if sub.Dropped() != 2 {
		t.Errorf("dropped = %d", sub.Dropped())
	}
	sub.Close()
	sub.Close()
	if err := m.Publish(ctx, Event{Type: "web.site.updated"}); err != nil {
		t.Errorf("publishing after a subscriber closed: %v", err)
	}
}

// Subscribers come and go while events are published, as SSE clients do.
// Closing a subscription must never race with a send to it.
func TestClosingWhilePublishing(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(100)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := m.Publish(ctx, Event{Type: "web.site.updated"}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	for range 20000 {
		sub := m.Subscribe(Filter{}, 1)
		sub.Close()
		// What was buffered before the close can still be read, and then
		// the channel is closed.
		for range sub.C {
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestTimelinePages(t *testing.T) {
	forEachLog(t, Options{}, func(t *testing.T, m *Log) {
		ctx := context.Background()
		site := store.NewID("site")
		for i := 0; i < 5; i++ {
			if err := m.Publish(ctx, Event{Type: "web.site.deployed", Subject: site}); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Publish(ctx, Event{Type: "web.site.deployed", Subject: store.NewID("site")}); err != nil {
			t.Fatal(err)
		}
		events, next, err := m.QueryEvents(ctx, Query{Subject: site, Limit: 2})
		if err != nil || len(events) != 2 || next == "" || events[0].Seq <= events[1].Seq {
			t.Fatalf("first page = %+v, next %q, %v", events, next, err)
		}
		var all []Event
		all = append(all, events...)
		for next != "" {
			var page []Event
			if page, next, err = m.QueryEvents(ctx, Query{Subject: site, Limit: 2, Cursor: next}); err != nil {
				t.Fatal(err)
			}
			all = append(all, page...)
		}
		if len(all) != 5 {
			t.Errorf("paged through %d events", len(all))
		}
		if err := CheckCursor("nope"); !errors.Is(err, i18n.ErrBadRequest) || CheckCursor("") != nil || CheckCursor(encodeSeq(5)) != nil {
			t.Errorf("checking cursors: %v", err)
		}
		if _, _, err := m.QueryEvents(ctx, Query{Cursor: "nope"}); !errors.Is(err, i18n.ErrBadRequest) {
			t.Errorf("a bad cursor: %v", err)
		}
	})
}

func TestTimelineFilters(t *testing.T) {
	forEachLog(t, Options{}, func(t *testing.T, m *Log) {
		ctx := context.Background()
		acct := store.AccountScope(store.NewID("acct"))
		cs, job := store.NewID("chg"), store.NewID("job")
		t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		for i, e := range []Event{
			{Type: "web.site.created", Kind: "web.site", Scope: acct, ChangeSet: cs, Job: job},
			{Type: "web.site.password_reset", Kind: "web.site", Scope: acct},
			{Type: "dns.zone.failed", Kind: "dns.zone", Severity: SeverityError},
			{Type: "core.panel.started", Severity: SeverityNotice},
		} {
			e.Time = t0.Add(time.Duration(i) * time.Hour)
			if err := m.Publish(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		types := func(q Query) []string {
			t.Helper()
			events, _, err := m.QueryEvents(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, e := range events {
				out = append(out, e.Type)
			}
			return out
		}
		for name, tc := range map[string]struct {
			q    Query
			want []string
		}{
			"all":          {Query{}, []string{"core.panel.started", "dns.zone.failed", "web.site.password_reset", "web.site.created"}},
			"glob":         {Query{Types: []string{"web.site.*"}}, []string{"web.site.password_reset", "web.site.created"}},
			"globs":        {Query{Types: []string{"*.failed", "core.*"}}, []string{"core.panel.started", "dns.zone.failed"}},
			"kind":         {Query{Kind: "dns.zone"}, []string{"dns.zone.failed"}},
			"scope":        {Query{Scope: &acct}, []string{"web.site.password_reset", "web.site.created"}},
			"scopes":       {Query{Scopes: []store.Scope{store.ServerScope()}}, []string{"core.panel.started", "dns.zone.failed"}},
			"severity":     {Query{MinSeverity: SeverityNotice}, []string{"core.panel.started", "dns.zone.failed"}},
			"change set":   {Query{ChangeSet: cs}, []string{"web.site.created"}},
			"job":          {Query{Job: job}, []string{"web.site.created"}},
			"since":        {Query{Since: t0.Add(2 * time.Hour)}, []string{"core.panel.started", "dns.zone.failed"}},
			"until":        {Query{Until: t0.Add(time.Hour)}, []string{"web.site.created"}},
			"time window":  {Query{Since: t0.Add(time.Hour), Until: t0.Add(2 * time.Hour)}, []string{"web.site.password_reset"}},
			"no such type": {Query{Types: []string{"mail.*"}}, nil},
		} {
			if got := types(tc.q); !slices.Equal(got, tc.want) {
				t.Errorf("%s: %v, want %v", name, got, tc.want)
			}
		}
	})
}

func TestAudit(t *testing.T) {
	forEachLog(t, Options{}, func(t *testing.T, m *Log) {
		ctx := context.Background()
		site := store.NewID("site")
		user := store.NewID("user")
		token := store.NewID("tok")
		acct := store.AccountScope(store.NewID("acct"))
		records := []AuditRecord{
			{Actor: auth.Actor{Type: auth.ActorUser, ID: user, Source: auth.SourceUI}, Action: "web.site.delete", Subject: site, Scope: acct, Outcome: OutcomeDenied, Reason: "forbidden"},
			{Actor: auth.Actor{Type: auth.ActorToken, ID: token, User: user, Source: auth.SourceAPI}, Action: "web.site.update", Subject: site, Scope: acct, Outcome: OutcomeSuccess, RequestID: "req_1"},
			{Actor: auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceCLI}, Action: "dns.zone.create", Outcome: OutcomeSuccess},
		}
		for _, r := range records {
			if err := m.Record(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		got, _, err := m.QueryAudit(ctx, Query{Types: []string{"web.site.*"}})
		if err != nil || len(got) != 2 || got[0].Action != "web.site.update" || got[0].ID.Prefix() != AuditPrefix || got[0].RequestID != "req_1" {
			t.Errorf("audit = %+v, %v", got, err)
		}
		// Records of the token count for the person behind it.
		if mine, _, _ := m.QueryAudit(ctx, Query{Actor: user}); len(mine) != 2 {
			t.Errorf("by actor = %+v", mine)
		}
		if byToken, _, _ := m.QueryAudit(ctx, Query{Actor: token}); len(byToken) != 1 || byToken[0].Actor.Source != auth.SourceAPI {
			t.Errorf("by token = %+v", byToken)
		}
		if denied, _, _ := m.QueryAudit(ctx, Query{Outcome: OutcomeDenied}); len(denied) != 1 || denied[0].Reason != "forbidden" {
			t.Errorf("denied = %+v", denied)
		}
		if scoped, _, _ := m.QueryAudit(ctx, Query{Scope: &acct, Subject: site}); len(scoped) != 2 {
			t.Errorf("scoped = %+v", scoped)
		}
		first, next, _ := m.QueryAudit(ctx, Query{Limit: 2})
		rest, last, _ := m.QueryAudit(ctx, Query{Limit: 2, Cursor: next})
		if len(first) != 2 || len(rest) != 1 || last != "" || rest[0].Action != "web.site.delete" {
			t.Errorf("pages = %+v / %+v", first, rest)
		}
	})
}

func TestFollowingTheTimeline(t *testing.T) {
	forEachLog(t, Options{}, func(t *testing.T, m *Log) {
		ctx := context.Background()
		if seq, err := m.LastSeq(ctx); err != nil || seq != 0 {
			t.Errorf("an empty timeline ends at 0: %d, %v", seq, err)
		}
		var ids []store.ID
		for range 5 {
			e := Event{ID: store.NewID(EventPrefix), Type: "web.site.updated"}
			if err := m.Publish(ctx, e); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, e.ID)
		}
		seq, err := m.SeqOf(ctx, ids[1])
		if err != nil {
			t.Fatal(err)
		}
		after, err := m.EventsAfter(ctx, seq, 2)
		if err != nil || len(after) != 2 || after[0].ID != ids[2] || after[1].ID != ids[3] {
			t.Errorf("after = %+v, %v", after, err)
		}
		last, _ := m.LastSeq(ctx)
		if rest, _ := m.EventsAfter(ctx, last, 10); len(rest) != 0 {
			t.Errorf("nothing after the last event: %+v", rest)
		}
		if _, err := m.SeqOf(ctx, store.NewID(EventPrefix)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an unknown event: %v", err)
		}
	})
}

func TestRetention(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	forEachLog(t, Options{Retention: Retention{Events: 24 * time.Hour, MaxEvents: 3, Audit: 48 * time.Hour, MaxAudit: 10}, Now: clock}, func(t *testing.T, m *Log) {
		ctx := context.Background()
		old := Event{Type: "web.site.updated", Time: now.Add(-25 * time.Hour)}
		if err := m.Publish(ctx, old); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			if err := m.Publish(ctx, Event{Type: "web.site.updated", Time: now.Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		for _, age := range []time.Duration{47 * time.Hour, 49 * time.Hour} {
			if err := m.Record(ctx, AuditRecord{Action: "web.site.update", Time: now.Add(-age), Outcome: OutcomeSuccess}); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Prune(ctx); err != nil {
			t.Fatal(err)
		}
		events, _, _ := m.QueryEvents(ctx, Query{Limit: 100})
		if len(events) != 3 {
			t.Errorf("kept %d events, want the newest 3", len(events))
		}
		records, _, _ := m.QueryAudit(ctx, Query{})
		if len(records) != 1 {
			t.Errorf("kept %d audit records, want the one younger than two days", len(records))
		}
	})
}

type spec struct{}

func TestKindTypes(t *testing.T) {
	k := store.Define[spec, store.Status](store.KindInfo{
		Name: "web.site", Prefix: "site", Scope: store.ScopeAccount, Title: "Site", Plural: "sites", Description: "A site.",
		Actions: []store.Action{{Name: "deploy", Summary: "Deploy a release.", Risk: store.RiskMedium}},
	})
	var names []string
	for _, typ := range KindTypes(k) {
		names = append(names, typ.Name)
		if !ValidType(typ.Name) {
			t.Errorf("%s isn't a valid type", typ.Name)
		}
	}
	want := []string{"web.site.created", "web.site.updated", "web.site.deleted", "web.site.failed", "web.site.drifted", "web.site.deployed"}
	if !slices.Equal(names, want) {
		t.Errorf("types = %v, want %v", names, want)
	}
	catalog := Catalog([]store.Kind{k})
	var catalogNames []string
	for _, typ := range catalog {
		catalogNames = append(catalogNames, typ.Name)
	}
	if !slices.IsSorted(catalogNames) || !slices.Contains(catalogNames, "core.changeset.failed") || !slices.Contains(catalogNames, "web.site.deployed") {
		t.Errorf("catalog = %v", catalogNames)
	}
	if err := Err(); err != nil {
		t.Error(err)
	}
}

func TestPatterns(t *testing.T) {
	for p, ok := range map[string]bool{"web.site.*": true, "*": true, "*.failed": true, "web.site.password_reset": true, "Web.*": false, "web/site": false, "": false, "[a]": false} {
		if ValidPattern(p) != ok {
			t.Errorf("ValidPattern(%q) = %v", p, !ok)
		}
	}
	if !MatchTypes([]string{"web.*"}, "web.site.created") || MatchTypes([]string{"dns.*"}, "web.site.created") || !MatchTypes(nil, "x.y.z") {
		t.Error("type matching")
	}
}
