package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// testClock returns a clock that moves one second every time it is read, so
// every write gets a time of its own.
func testClock() func() time.Time {
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { clock = clock.Add(time.Second); return clock }
}

func newTestStore(t *testing.T) (*Memory, *KindOf[testSiteSpec, testSiteStatus]) {
	t.Helper()
	reg := NewKindRegistry()
	k := testSiteKind()
	if err := reg.Register(k); err != nil {
		t.Fatal(err)
	}
	return NewMemory(reg, WithClock(testClock())), k
}

// openTestDB opens a database in a fresh temporary directory.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// forEachStore runs a test against every Store implementation, so they all
// behave the same way.
func forEachStore(t *testing.T, test func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus])) {
	t.Run("memory", func(t *testing.T) {
		s, k := newTestStore(t)
		test(t, s, k)
	})
	t.Run("sqlite", func(t *testing.T) {
		reg := NewKindRegistry()
		k := testSiteKind()
		if err := reg.Register(k); err != nil {
			t.Fatal(err)
		}
		s, err := NewSQLite(context.Background(), openTestDB(t), reg, WithClock(testClock()))
		if err != nil {
			t.Fatal(err)
		}
		test(t, s, k)
	})
}

func createSite(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus], account ID, domains ...string) *Resource {
	t.Helper()
	r, err := k.Encode(k.New(AccountScope(account), testSiteSpec{Domains: domains}))
	if err != nil {
		t.Fatal(err)
	}
	r.Status = nil
	err = s.Write(context.Background(), WriteMeta{Actor: "user:test"}, func(tx Tx) error {
		return tx.Create(context.Background(), r)
	})
	if err != nil {
		t.Fatalf("create %v: %v", domains, err)
	}
	return r
}

func write(ctx context.Context, s Store, fn func(tx Tx) error) error {
	return s.Write(ctx, WriteMeta{}, fn)
}

func TestCreateGetUpdate(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		acct := NewID("acct")
		r := createSite(t, s, k, acct, "example.com")
		if r.ID.Prefix() != "tsite" || r.Generation != 1 || r.Revision != 1 || r.CreatedAt.IsZero() {
			t.Fatalf("unexpected metadata %+v", r.Meta)
		}

		got, err := k.Get(ctx, s, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Domains[0] != "example.com" || !got.CreatedAt.Equal(r.CreatedAt) || got.Scope != AccountScope(acct) {
			t.Errorf("got %+v", got)
		}

		// A label-only change bumps the revision but not the generation.
		r.Labels = map[string]string{"env": "prod"}
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		if r.Generation != 1 || r.Revision != 2 {
			t.Errorf("after label change: generation %d revision %d", r.Generation, r.Revision)
		}
		if got, _ := s.Get(ctx, r.ID); got.Labels["env"] != "prod" {
			t.Errorf("labels = %v", got.Labels)
		}

		// A spec change bumps both and is recorded in history.
		r.Spec = json.RawMessage(`{"domains":["example.com","www.example.com"]}`)
		cs := NewID("chg")
		if err := s.Write(ctx, WriteMeta{ChangeSet: cs, Actor: "user:test"}, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		if r.Generation != 2 || r.Revision != 3 {
			t.Errorf("after spec change: generation %d revision %d", r.Generation, r.Revision)
		}
		h, err := s.History(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(h) != 2 || h[1].Generation != 2 || h[1].ChangeSet != cs || h[0].Actor != "user:test" || h[1].Labels["env"] != "prod" {
			t.Errorf("history = %+v", h)
		}

		// A semantically equal spec with different key order is not a change.
		r.Spec = json.RawMessage(`{ "domains" : ["example.com", "www.example.com"] }`)
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		if r.Generation != 2 {
			t.Errorf("formatting-only change bumped the generation to %d", r.Generation)
		}

		// Moving a resource to another server is a new generation too.
		r.Server = NewID("srv")
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		if h, _ := s.History(ctx, r.ID); r.Generation != 3 || len(h) != 3 || h[2].Server != r.Server {
			t.Errorf("placement change: generation %d, history %+v", r.Generation, h)
		}
	})
}

func TestOptimisticConcurrency(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		r := createSite(t, s, k, NewID("acct"), "a.test")
		stale := r.Clone()

		r.Spec = json.RawMessage(`{"domains":["b.test"]}`)
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		stale.Spec = json.RawMessage(`{"domains":["c.test"]}`)
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, stale) }); !errors.Is(err, ErrConflict) {
			t.Errorf("stale update: got %v, want conflict", err)
		}
		if err := write(ctx, s, func(tx Tx) error { return tx.Delete(ctx, r.ID, stale.Revision) }); !errors.Is(err, ErrConflict) {
			t.Errorf("stale delete: got %v, want conflict", err)
		}
	})
}

func TestStatusDoesNotConflict(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		r := createSite(t, s, k, NewID("acct"), "a.test")
		status := testSiteStatus{Status: Status{Phase: PhaseReady, ObservedGeneration: 1}, URL: "https://a.test"}
		if err := SetStatus(ctx, s, r.ID, status); err != nil {
			t.Fatal(err)
		}
		got, _ := k.Get(ctx, s, r.ID)
		if got.Status.Phase != PhaseReady || got.Status.URL != "https://a.test" || got.Revision != r.Revision {
			t.Errorf("status write: %+v", got)
		}
		r.Spec = json.RawMessage(`{"domains":["b.test"]}`)
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
			t.Errorf("an update after a status write should not conflict: %v", err)
		}
		if err := SetStatus(ctx, s, NewID("tsite"), status); !errors.Is(err, ErrNotFound) {
			t.Errorf("the status of a missing resource: %v", err)
		}
		if err := write(ctx, s, func(tx Tx) error { return tx.UpdateStatus(ctx, r.ID, json.RawMessage(`{"phase":`)) }); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("a status that isn't JSON: %v", err)
		}
	})
}

func TestUniqueIndexes(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		a := createSite(t, s, k, NewID("acct"), "Example.com")

		r, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"example.COM"}}))
		err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, r) })
		if !errors.Is(err, errTestDomainTaken) {
			t.Fatalf("duplicate domain: got %v", err)
		}
		if e := i18n.From(err); e.Params["value"] != "example.com" {
			t.Errorf("conflict params = %v", e.Params)
		}

		dup, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"x.test", "X.test"}}))
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, dup) }); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("repeated value in one resource: got %v", err)
		}

		// An update may keep its own values and take free ones.
		a.Spec = json.RawMessage(`{"domains":["example.com","shop.example.com"]}`)
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, a) }); err != nil {
			t.Fatal(err)
		}

		// Deleting keeps the domain reserved until the resource is purged.
		if err := write(ctx, s, func(tx Tx) error { return tx.Delete(ctx, a.ID, 0) }); err != nil {
			t.Fatal(err)
		}
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, r) }); err == nil {
			t.Fatal("the domain should stay reserved while the old site is being removed")
		}
		if err := write(ctx, s, func(tx Tx) error { return tx.Purge(ctx, a.ID) }); err != nil {
			t.Fatal(err)
		}
		r.ID = ""
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, r) }); err != nil {
			t.Errorf("the domain should be free after the purge: %v", err)
		}
		if h, err := s.History(ctx, a.ID); err != nil || len(h) != 3 || !h[2].Deleted {
			t.Errorf("history should survive the purge: %+v, %v", h, err)
		}
		if _, err := s.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a purged resource is gone: %v", err)
		}
	})
}

func TestRecreatedResourcesKeepTheirGenerations(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		r := createSite(t, s, k, NewID("acct"), "again.test")
		if err := write(ctx, s, func(tx Tx) error {
			if err := tx.Delete(ctx, r.ID, 0); err != nil {
				return err
			}
			return tx.Purge(ctx, r.ID)
		}); err != nil {
			t.Fatal(err)
		}
		back := &Resource{Meta: Meta{ID: r.ID, Kind: r.Kind, Scope: r.Scope}, Spec: r.Spec}
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, back) }); err != nil {
			t.Fatal(err)
		}
		h, _ := s.History(ctx, r.ID)
		if back.Generation != 3 || len(h) != 3 || h[2].Generation != 3 || h[2].Deleted {
			t.Errorf("generation %d, history %+v", back.Generation, h)
		}
	})
}

func TestTransactionsAreAtomic(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		acct := NewID("acct")
		boom := errors.New("boom")
		err := write(ctx, s, func(tx Tx) error {
			r, _ := k.Encode(k.New(AccountScope(acct), testSiteSpec{Domains: []string{"one.test"}}))
			if err := tx.Create(ctx, r); err != nil {
				return err
			}
			if _, err := tx.Get(ctx, r.ID); err != nil {
				t.Errorf("a transaction should see its own writes: %v", err)
			}
			if page, err := tx.List(ctx, Query{Kind: "test.site"}); err != nil || len(page.Items) != 1 {
				t.Errorf("a transaction should list its own writes: %v", err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
		page, err := s.List(ctx, Query{Kind: "test.site"})
		if err != nil || len(page.Items) != 0 {
			t.Errorf("a failed transaction left %d resources (%v)", len(page.Items), err)
		}
		// The domain from the failed transaction is free.
		createSite(t, s, k, acct, "one.test")

		var kept Tx
		if err := write(ctx, s, func(tx Tx) error { kept = tx; return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := kept.Get(ctx, NewID("tsite")); err == nil {
			t.Error("a finished transaction can't be used")
		}
	})
}

func TestListFiltersAndPages(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		a, b := NewID("acct"), NewID("acct")
		var ids []ID
		for i := 0; i < 5; i++ {
			ids = append(ids, createSite(t, s, k, a, "a"+string(rune('0'+i))+".test").ID)
		}
		other := createSite(t, s, k, b, "b.test")

		page, err := s.List(ctx, Query{Kind: "test.site", Scope: &Scope{Level: ScopeAccount, ID: a}, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		var seen []ID
		for {
			for _, r := range page.Items {
				seen = append(seen, r.ID)
			}
			if page.Next == "" {
				break
			}
			if page, err = s.List(ctx, Query{Kind: "test.site", Scope: &Scope{Level: ScopeAccount, ID: a}, Limit: 2, Cursor: page.Next}); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(seen, ids) {
			t.Errorf("order: got %v, want %v", seen, ids)
		}

		both, err := s.List(ctx, Query{Kind: "test.site", Scopes: []Scope{AccountScope(b), AccountScope(NewID("acct"))}})
		if err != nil || len(both.Items) != 1 || both.Items[0].ID != other.ID {
			t.Errorf("a list of scopes = %+v, %v", both, err)
		}

		picked, err := s.List(ctx, Query{Kind: "test.site", IDs: []ID{ids[3], ids[1], NewID("tsite")}, Limit: 1})
		if err != nil || len(picked.Items) != 1 || picked.Items[0].ID != ids[1] || picked.Next == "" {
			t.Fatalf("by IDs = %+v, %v", picked, err)
		}
		if rest, _ := s.List(ctx, Query{Kind: "test.site", IDs: []ID{ids[3], ids[1]}, Cursor: picked.Next}); len(rest.Items) != 1 || rest.Items[0].ID != ids[3] {
			t.Errorf("lists by IDs page like any other: %+v", rest)
		}

		byDomain, err := s.List(ctx, Query{Kind: "test.site", Index: map[string]string{"domain": "B.TEST"}})
		if err != nil || len(byDomain.Items) != 1 {
			t.Errorf("index filter: %d items, %v", len(byDomain.Items), err)
		}
		if _, err := s.List(ctx, Query{Kind: "test.site", Index: map[string]string{"nope": "x"}}); err == nil {
			t.Error("filtering on an undeclared index should fail")
		}
		if _, err := s.List(ctx, Query{Kind: "test.unknown"}); err == nil {
			t.Error("listing an unknown kind should fail")
		}
		if _, err := s.List(ctx, Query{Kind: "test.site", Cursor: "!!"}); !errors.Is(err, i18n.ErrBadRequest) {
			t.Errorf("a bad cursor should be a bad request, got %v", err)
		}
		if _, err := s.List(ctx, Query{Kind: "test.site", Sort: "color"}); !errors.Is(err, i18n.ErrBadRequest) {
			t.Errorf("sorting by an unknown field should be a bad request, got %v", err)
		}
		if _, err := s.List(ctx, Query{Kind: "test.site", Phases: []Phase{"sleepy"}}); !errors.Is(err, i18n.ErrBadRequest) {
			t.Errorf("an unknown phase should be a bad request, got %v", err)
		}
	})
}

func TestListLabelsPhasesAndSearch(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		acct := NewID("acct")
		shop := createSite(t, s, k, acct, "shop.example.com")
		blog := createSite(t, s, k, acct, "blog.example.com")
		docs := createSite(t, s, k, acct, "docs.other.test")
		shop.Labels = map[string]string{"env": "prod", "racksail.dev/tier": "gold"}
		blog.Labels = map[string]string{"env": "staging"}
		for _, r := range []*Resource{shop, blog} {
			if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
				t.Fatal(err)
			}
		}
		if err := SetStatus(ctx, s, shop.ID, Status{Phase: PhaseReady, ObservedGeneration: shop.Generation}); err != nil {
			t.Fatal(err)
		}
		if err := SetStatus(ctx, s, blog.ID, Status{Phase: PhaseFailed}); err != nil {
			t.Fatal(err)
		}
		list := func(q Query) []ID {
			t.Helper()
			q.Kind = "test.site"
			page, err := s.List(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			var out []ID
			for _, r := range page.Items {
				out = append(out, r.ID)
			}
			return out
		}
		if got := list(Query{Labels: map[string]string{"env": "prod", "racksail.dev/tier": "gold"}}); !slices.Equal(got, []ID{shop.ID}) {
			t.Errorf("labels = %v", got)
		}
		if got := list(Query{Labels: map[string]string{"env": "dev"}}); len(got) != 0 {
			t.Errorf("no label match = %v", got)
		}
		if got := list(Query{Phases: []Phase{PhaseFailed, PhasePending}}); !slices.Equal(got, []ID{blog.ID, docs.ID}) {
			t.Errorf("phases = %v", got)
		}
		if got := list(Query{Search: "EXAMPLE.com"}); !slices.Equal(got, []ID{shop.ID, blog.ID}) {
			t.Errorf("search by domain = %v", got)
		}
		// The end of an ID is random, unlike its start, which says when it
		// was made.
		if got := list(Query{Search: string(docs.ID)[len(docs.ID)-10:]}); !slices.Equal(got, []ID{docs.ID}) {
			t.Errorf("search by ID = %v", got)
		}
		if got := list(Query{Search: "runtime"}); len(got) != 0 {
			t.Errorf("searches look at values, not names: %v", got)
		}
		// Shop has carried out its generation; the others haven't been
		// reconciled yet.
		if got := list(Query{Unreconciled: true}); !slices.Equal(got, []ID{blog.ID, docs.ID}) {
			t.Errorf("unreconciled = %v", got)
		}
		if err := write(ctx, s, func(tx Tx) error { return tx.Delete(ctx, shop.ID, 0) }); err != nil {
			t.Fatal(err)
		}
		if got := list(Query{Unreconciled: true}); !slices.Equal(got, []ID{shop.ID, blog.ID, docs.ID}) {
			t.Errorf("unreconciled after a delete = %v", got)
		}
	})
}

func TestListSorting(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		acct := NewID("acct")
		c := createSite(t, s, k, acct, "c.test")
		a := createSite(t, s, k, acct, "a.test")
		b := createSite(t, s, k, acct, "b.test", "z.test")
		none := &Resource{Meta: Meta{Kind: "test.site", Scope: AccountScope(acct)}, Spec: json.RawMessage(`{"domains":[]}`)}
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, none) }); err != nil {
			t.Fatal(err)
		}
		// a changes last, so it is the most recently updated.
		a.Spec = json.RawMessage(`{"domains":["a.test"],"runtime":"php"}`)
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, a) }); err != nil {
			t.Fatal(err)
		}
		if err := SetStatus(ctx, s, c.ID, Status{Phase: PhaseReady}); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			sort string
			want []ID
		}{
			{"", []ID{c.ID, a.ID, b.ID, none.ID}},
			{"-id", []ID{none.ID, b.ID, a.ID, c.ID}},
			{"createdAt", []ID{c.ID, a.ID, b.ID, none.ID}},
			{"-updatedAt", []ID{a.ID, none.ID, b.ID, c.ID}},
			{"generation", []ID{c.ID, b.ID, none.ID, a.ID}},
			{"-generation", []ID{a.ID, none.ID, b.ID, c.ID}},
			{"phase", []ID{a.ID, b.ID, none.ID, c.ID}},
			// Resources without a value come first; b sorts by its lowest.
			{"domain", []ID{none.ID, a.ID, b.ID, c.ID}},
			{"-domain", []ID{c.ID, b.ID, a.ID, none.ID}},
		} {
			// Two at a time, so every cursor is used.
			var got []ID
			cursor := ""
			for range 5 {
				page, err := s.List(ctx, Query{Kind: "test.site", Sort: tc.sort, Limit: 2, Cursor: cursor})
				if err != nil {
					t.Fatalf("sort %q: %v", tc.sort, err)
				}
				for _, r := range page.Items {
					got = append(got, r.ID)
				}
				if cursor = page.Next; cursor == "" {
					break
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("sort %q = %v, want %v", tc.sort, got, tc.want)
			}
		}
		page, _ := s.List(ctx, Query{Kind: "test.site", Sort: "createdAt", Limit: 1})
		if _, err := s.List(ctx, Query{Kind: "test.site", Sort: "-createdAt", Cursor: page.Next}); !errors.Is(err, i18n.ErrBadRequest) {
			t.Errorf("a cursor only works with the sort it came from: %v", err)
		}
	})
}

func TestRejectsBadMetadata(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		server, _ := k.Encode(k.New(ServerScope(), testSiteSpec{Domains: []string{"x.test"}}))
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, server) }); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("wrong scope level: %v", err)
		}
		wrongPrefix, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"y.test"}}))
		wrongPrefix.ID = NewID("other")
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, wrongPrefix) }); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("wrong prefix: %v", err)
		}
		r := createSite(t, s, k, NewID("acct"), "z.test")
		again := r.Clone()
		if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, again) }); !errors.Is(err, ErrExists) {
			t.Errorf("an ID that is taken: %v", err)
		}
		r.Scope = AccountScope(NewID("acct"))
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, r) }); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("changing the scope should be rejected: %v", err)
		}
		if _, err := s.Get(ctx, NewID("tsite")); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing resource: %v", err)
		}
		if _, err := s.History(ctx, NewID("tsite")); !errors.Is(err, ErrNotFound) {
			t.Errorf("history of a missing resource: %v", err)
		}
		badLabels := createSite(t, s, k, NewID("acct"), "l.test")
		badLabels.Labels = map[string]string{"Bad Key": "x"}
		if err := write(ctx, s, func(tx Tx) error { return tx.Update(ctx, badLabels) }); !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("bad labels: %v", err)
		}
	})
}

func TestWatch(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus]) {
		ctx := context.Background()
		w := s.Watch(WatchFilter{Kinds: []string{"test.site"}}, 16)
		defer w.Close()
		other := s.Watch(WatchFilter{Kinds: []string{"test.other"}}, 16)
		defer other.Close()
		small := s.Watch(WatchFilter{}, 1)
		defer small.Close()

		cs := NewID("chg")
		r, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"w.test"}}))
		r.Status = nil
		if err := s.Write(ctx, WriteMeta{ChangeSet: cs}, func(tx Tx) error { return tx.Create(ctx, r) }); err != nil {
			t.Fatal(err)
		}
		if err := write(ctx, s, func(tx Tx) error { return errors.New("never committed") }); err == nil {
			t.Fatal("expected the error back")
		}
		if err := SetStatus(ctx, s, r.ID, Status{Phase: PhaseReady}); err != nil {
			t.Fatal(err)
		}
		if err := write(ctx, s, func(tx Tx) error {
			if err := tx.Delete(ctx, r.ID, 0); err != nil {
				return err
			}
			return tx.Purge(ctx, r.ID)
		}); err != nil {
			t.Fatal(err)
		}
		var ops []NoticeOp
		for range 4 {
			select {
			case c := <-w.C:
				ops = append(ops, c.Op)
				if c.ID != r.ID || c.Kind != "test.site" {
					t.Errorf("change = %+v", c)
				}
				if c.Op == NoticeCreated && (c.ChangeSet != cs || c.Generation != 1 || c.Phase != PhasePending) {
					t.Errorf("created = %+v", c)
				}
				if c.Op == NoticeStatus && c.Phase != PhaseReady {
					t.Errorf("status = %+v", c)
				}
			case <-time.After(time.Second):
				t.Fatalf("only saw %v", ops)
			}
		}
		if !slices.Equal(ops, []NoticeOp{NoticeCreated, NoticeStatus, NoticeDeleted, NoticePurged}) {
			t.Errorf("ops = %v", ops)
		}
		select {
		case c := <-other.C:
			t.Errorf("a watcher of another kind saw %+v", c)
		default:
		}
		if small.Dropped() != 3 {
			t.Errorf("a full watcher drops what doesn't fit: %d", small.Dropped())
		}
		w.Close()
		if _, open := <-w.C; open {
			t.Error("closing a watcher closes its channel")
		}
	})
}

func TestSQLitePersists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	reg := NewKindRegistry()
	k := testSiteKind()
	if err := reg.Register(k); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLite(ctx, db, reg)
	if err != nil {
		t.Fatal(err)
	}
	r := createSite(t, s, k, NewID("acct"), "kept.test")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = OpenDB(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err = NewSQLite(ctx, db, reg)
	if err != nil {
		t.Fatalf("opening the store again: %v", err)
	}
	got, err := k.Get(ctx, s, r.ID)
	if err != nil || got.Spec.Domains[0] != "kept.test" {
		t.Fatalf("after reopening: %+v, %v", got, err)
	}
	dup, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"kept.test"}}))
	if err := write(ctx, s, func(tx Tx) error { return tx.Create(ctx, dup) }); !errors.Is(err, errTestDomainTaken) {
		t.Errorf("unique values are kept too: %v", err)
	}
	var mode string
	if err := db.Reader().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal mode = %q, %v", mode, err)
	}
	if err := db.Checkpoint(ctx); err != nil {
		t.Error(err)
	}
}

func TestOpenDBRefusesRelativePaths(t *testing.T) {
	if _, err := OpenDB(context.Background(), "state.db"); err == nil {
		t.Error("a relative path should be refused")
	}
	if _, err := OpenDB(context.Background(), "/tmp/a?b.db"); err == nil {
		t.Error("a path with a question mark should be refused")
	}
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	v1 := fstest.MapFS{"0001_create_things.sql": {Data: []byte("CREATE TABLE things (id TEXT PRIMARY KEY) STRICT;")}}
	if err := db.Migrate(ctx, "test", v1); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, "test", v1); err != nil {
		t.Errorf("running the same migrations again changes nothing: %v", err)
	}
	v2 := fstest.MapFS{
		"0001_create_things.sql": v1["0001_create_things.sql"],
		"0002_add_names.sql":     {Data: []byte("ALTER TABLE things ADD COLUMN name TEXT NOT NULL DEFAULT '';\nCREATE INDEX things_by_name ON things (name);")},
	}
	if err := db.Migrate(ctx, "test", v2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO things (id, name) VALUES ('a', 'b')"); err != nil {
		t.Errorf("both migrations ran: %v", err)
	}
	if err := db.Migrate(ctx, "test", v1); err == nil {
		t.Error("a database with migrations this binary doesn't know should be refused")
	}
	edited := fstest.MapFS{
		"0001_create_things.sql": {Data: []byte("CREATE TABLE things (id TEXT PRIMARY KEY);")},
		"0002_add_names.sql":     v2["0002_add_names.sql"],
	}
	if err := db.Migrate(ctx, "test", edited); err == nil {
		t.Error("a migration that changed after it ran should be refused")
	}
	broken := fstest.MapFS{"0001_broken.sql": {Data: []byte("CREATE TABLE ok (id TEXT);\nCREATE TABLE nope (;")}}
	if err := db.Migrate(ctx, "broken", broken); err == nil {
		t.Error("a failing migration is an error")
	}
	if _, err := db.Exec(ctx, "INSERT INTO ok (id) VALUES ('x')"); err == nil {
		t.Error("a failing migration leaves nothing behind")
	}
	// Owners are independent of each other.
	if err := db.Migrate(ctx, "other", v1); err == nil {
		t.Error("two owners can't create the same table")
	}
}

func TestLoadMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_add_index.sql":    {Data: []byte("CREATE INDEX x ON y(z);")},
		"0001_create_sites.sql": {Data: []byte("CREATE TABLE sites (id TEXT);")},
		"README.md":             {Data: []byte("not a migration")},
	}
	ms, err := LoadMigrations("web", fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Name != "create_sites" || ms[1].Version != 2 || ms[0].Owner != "web" {
		t.Errorf("migrations = %+v", ms)
	}
	gap := fstest.MapFS{"0002_late.sql": {Data: []byte("SELECT 1;")}}
	if _, err := LoadMigrations("web", gap); err == nil {
		t.Error("a gap in versions should be an error")
	}
	badName := fstest.MapFS{"1_x.sql": {Data: []byte("SELECT 1;")}}
	if _, err := LoadMigrations("web", badName); err == nil {
		t.Error("a malformed name should be an error")
	}
	if ms, err := LoadMigrations("core", CoreMigrations()); err != nil || len(ms) == 0 {
		t.Errorf("the core migrations load: %v", err)
	}
}
