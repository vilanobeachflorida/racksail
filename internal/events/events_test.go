package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

func TestPublishSubscribe(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(100)
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
		if e.ID.Prefix() != EventPrefix || e.Time.IsZero() || e.Severity != SeverityInfo {
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

func TestTimelineAndAudit(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(3)
	site := store.NewID("site")
	for i := 0; i < 5; i++ {
		if err := m.Publish(ctx, Event{Type: "web.site.deployed", Subject: site}); err != nil {
			t.Fatal(err)
		}
	}
	events, next, err := m.QueryEvents(ctx, Query{Subject: site, Limit: 2})
	if err != nil || len(events) != 2 || next == "" {
		t.Fatalf("first page = %d events, next %q, %v", len(events), next, err)
	}
	rest, next, _ := m.QueryEvents(ctx, Query{Subject: site, Limit: 2, Cursor: next})
	if len(rest) != 1 || next != "" || rest[0].ID >= events[1].ID {
		t.Errorf("second page = %+v, next %q", rest, next)
	}
	actor := auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI}
	if err := m.Record(ctx, AuditRecord{Actor: actor, Action: "web.site.delete", Subject: site, Outcome: OutcomeDenied, Reason: "forbidden"}); err != nil {
		t.Fatal(err)
	}
	records, _, _ := m.QueryAudit(ctx, Query{Types: []string{"web.site.*"}})
	if len(records) != 1 || records[0].ID.Prefix() != AuditPrefix || records[0].Outcome != OutcomeDenied {
		t.Errorf("audit = %+v", records)
	}
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
	want := []string{"web.site.created", "web.site.updated", "web.site.deleted", "web.site.failed", "web.site.deployed"}
	if len(names) != len(want) {
		t.Fatalf("types = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("types = %v, want %v", names, want)
		}
	}
}
