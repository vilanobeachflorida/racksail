package events

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func denied(t *testing.T, l *Log, q Query) []AuditRecord {
	t.Helper()
	q.Outcome, q.Limit = OutcomeDenied, 100
	list, _, err := l.QueryAudit(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A refusal the same as one recorded within the hour counts on that record;
// one that differs in anything the record says gets its own.
func TestRepeatedRefusalsCountOnOneRecord(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	forEachLog(t, Options{Now: func() time.Time { return now }}, func(t *testing.T, l *Log) {
		ctx := context.Background()
		user := auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceAPI, IP: "203.0.113.7"}
		site := store.NewID("site")
		refusal := AuditRecord{Actor: user, Action: "web.site.delete", Kind: "web.site", Subject: site, Outcome: OutcomeDenied, Reason: "forbidden"}
		record := func(r AuditRecord, at time.Duration) {
			t.Helper()
			r.Time = now.Add(at)
			if err := l.Record(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 5 {
			record(refusal, time.Duration(i)*time.Minute)
		}
		got := denied(t, l, Query{})
		if len(got) != 1 {
			t.Fatalf("five refusals the same make %d records, want one", len(got))
		}
		if r := got[0]; r.Repeats != 4 || r.LastTime == nil || !r.LastTime.Equal(now.Add(4*time.Minute)) || !r.Time.Equal(now) {
			t.Errorf("record = %+v, want the first time and four repeats up to the last", r)
		}

		other, elsewhere, someoneElse, why := refusal, refusal, refusal, refusal
		other.Subject = store.NewID("site")
		elsewhere.Actor.IP = "198.51.100.2"
		someoneElse.Actor.ID = store.NewID("user")
		why.Reason = "the site is locked"
		for _, r := range []AuditRecord{other, elsewhere, someoneElse, why} {
			record(r, 5*time.Minute)
		}
		if got := denied(t, l, Query{}); len(got) != 5 {
			t.Errorf("%d records of refusals that differ, want one each", len(got))
		}
		for range 2 {
			record(AuditRecord{Actor: user, Action: "web.site.update", Subject: site, Outcome: OutcomeSuccess}, 0)
		}
		if all, _, _ := l.QueryAudit(ctx, Query{Outcome: OutcomeSuccess}); len(all) != 2 {
			t.Errorf("%d records of two changes", len(all))
		}

		// Once the hour is over the refusal is recorded again.
		record(refusal, time.Hour)
		got = denied(t, l, Query{Subject: site, Actor: user.ID})
		if len(got) != 4 || got[0].Repeats != 0 || !got[0].Time.Equal(now.Add(time.Hour)) {
			t.Errorf("after the hour = %+v", got)
		}
	})
}

// The different refusals of one actor beyond the cap all count on one
// record, so a caller trying everything adds a bounded number of records.
func TestRefusalsBeyondTheCapCountTogether(t *testing.T) {
	forEachLog(t, Options{Denials: Denials{PerActor: 3}}, func(t *testing.T, l *Log) {
		ctx := context.Background()
		prober := auth.Actor{Type: auth.ActorToken, ID: store.NewID("tok"), Source: auth.SourceAPI}
		for range 200 {
			if err := l.Record(ctx, AuditRecord{Actor: prober, Action: "web.site.read", Subject: store.NewID("site"), Outcome: OutcomeDenied}); err != nil {
				t.Fatal(err)
			}
		}
		got := denied(t, l, Query{Actor: prober.ID})
		if len(got) != 4 {
			t.Fatalf("200 refusals make %d records, want 3 and one for the rest", len(got))
		}
		if r := got[0]; r.Action != RefusalsAction || r.Repeats != 196 || r.Outcome != OutcomeDenied || r.Scope != store.ServerScope() || r.Actor.ID != prober.ID || len(r.Details) == 0 {
			t.Errorf("the record of the rest = %+v", r)
		}
		// Someone else's refusals are recorded as ever.
		guesser := auth.Actor{Type: auth.ActorUser, Name: "admin", Source: auth.SourceUI, IP: "192.0.2.1"}
		if err := l.Record(ctx, AuditRecord{Actor: guesser, Action: "auth.sign_in", Outcome: OutcomeDenied}); err != nil {
			t.Fatal(err)
		}
		if got := denied(t, l, Query{}); len(got) != 5 || got[0].Action != "auth.sign_in" {
			t.Errorf("another caller's refusal = %+v", got[0])
		}
	})
}

// A repeat of a refusal whose record is gone, pruned or rolled back with
// the transaction that wrote it, is recorded again.
func TestRepeatsOfARecordThatIsGone(t *testing.T) {
	forEachLog(t, Options{Retention: Retention{MaxAudit: 2}}, func(t *testing.T, l *Log) {
		ctx := context.Background()
		refusal := AuditRecord{Actor: auth.Actor{Type: auth.ActorUser, ID: store.NewID("user")}, Action: "web.site.read", Subject: store.NewID("site"), Outcome: OutcomeDenied}
		if err := l.Record(ctx, refusal); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := l.Record(ctx, AuditRecord{Action: "web.site.update", Outcome: OutcomeSuccess}); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.Prune(ctx); err != nil {
			t.Fatal(err)
		}
		if got := denied(t, l, Query{}); len(got) != 0 {
			t.Fatalf("pruning kept %+v", got)
		}
		if err := l.Record(ctx, refusal); err != nil {
			t.Fatal(err)
		}
		if got := denied(t, l, Query{}); len(got) != 1 || got[0].Repeats != 0 {
			t.Errorf("after pruning = %+v", got)
		}
	})

	ctx := context.Background()
	db, err := store.OpenDB(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l, err := NewSQL(ctx, db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	refusal := AuditRecord{Actor: auth.Actor{Type: auth.ActorUser, ID: store.NewID("user")}, Action: "web.site.read", Subject: store.NewID("site"), Outcome: OutcomeDenied}
	undone := errors.New("undone")
	if err := db.Atomic(ctx, func(ctx context.Context) error {
		if err := l.Record(ctx, refusal); err != nil {
			return err
		}
		return undone
	}); !errors.Is(err, undone) {
		t.Fatal(err)
	}
	if err := l.Record(ctx, refusal); err != nil {
		t.Fatal(err)
	}
	if got := denied(t, l, Query{}); len(got) != 1 || got[0].Repeats != 0 {
		t.Errorf("after a rollback = %+v", got)
	}
}

// Refusals that come at once still make one record each, with every one of
// them counted.
func TestConcurrentRefusalsCountOnce(t *testing.T) {
	forEachLog(t, Options{Denials: Denials{PerActor: 2}}, func(t *testing.T, l *Log) {
		ctx := context.Background()
		actor := auth.Actor{Type: auth.ActorUser, ID: store.NewID("user")}
		site := store.NewID("site")
		var wg sync.WaitGroup
		for i := range 40 {
			wg.Go(func() {
				r := AuditRecord{Actor: actor, Action: "web.site.read", Subject: site, Outcome: OutcomeDenied}
				if i%2 == 1 {
					// Different from each other, so most go past the cap.
					r.Subject = store.NewID("site")
				}
				if err := l.Record(ctx, r); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		all := denied(t, l, Query{})
		total := 0
		for _, r := range all {
			total += 1 + r.Repeats
		}
		if len(all) > 3 || total != 40 {
			t.Errorf("%d records count %d refusals, want at most 3 counting 40", len(all), total)
		}
	})
}
