package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

func newTestStore(t *testing.T) (*Memory, *KindOf[testSiteSpec, testSiteStatus]) {
	t.Helper()
	reg := NewKindRegistry()
	k := testSiteKind()
	if err := reg.Register(k); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return NewMemory(reg, WithClock(func() time.Time { clock = clock.Add(time.Second); return clock })), k
}

func createSite(t *testing.T, s Store, k *KindOf[testSiteSpec, testSiteStatus], account ID, domains ...string) *Resource {
	t.Helper()
	r, err := k.Encode(k.New(AccountScope(account), testSiteSpec{Domains: domains}))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), WriteMeta{Actor: "user:test"}, func(tx Tx) error {
		return tx.Create(context.Background(), r)
	})
	if err != nil {
		t.Fatalf("create %v: %v", domains, err)
	}
	return r
}

func TestMemoryCreateGetUpdate(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	acct := NewID("acct")
	r := createSite(t, s, k, acct, "example.com")
	if r.ID.Prefix() != "tsite" || r.Generation != 1 || r.Revision != 1 {
		t.Fatalf("unexpected metadata %+v", r.Meta)
	}

	got, err := k.Get(ctx, s, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Domains[0] != "example.com" {
		t.Errorf("spec = %+v", got.Spec)
	}

	// A label-only change bumps the revision but not the generation.
	r.Labels = map[string]string{"env": "prod"}
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	if r.Generation != 1 || r.Revision != 2 {
		t.Errorf("after label change: generation %d revision %d", r.Generation, r.Revision)
	}

	// A spec change bumps both and is recorded in history.
	r.Spec = json.RawMessage(`{"domains":["example.com","www.example.com"]}`)
	if err := s.Write(ctx, WriteMeta{ChangeSet: NewID("chg"), Actor: "user:test"}, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	if r.Generation != 2 || r.Revision != 3 {
		t.Errorf("after spec change: generation %d revision %d", r.Generation, r.Revision)
	}
	h, err := s.History(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[1].Generation != 2 || h[1].ChangeSet == "" || h[0].Actor != "user:test" {
		t.Errorf("history = %+v", h)
	}

	// A semantically equal spec with different key order is not a change.
	r.Spec = json.RawMessage(`{ "domains" : ["example.com", "www.example.com"] }`)
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	if r.Generation != 2 {
		t.Errorf("formatting-only change bumped the generation to %d", r.Generation)
	}
}

func TestMemoryOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	r := createSite(t, s, k, NewID("acct"), "a.test")
	stale := r.Clone()

	r.Spec = json.RawMessage(`{"domains":["b.test"]}`)
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
		t.Fatal(err)
	}
	stale.Spec = json.RawMessage(`{"domains":["c.test"]}`)
	err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Update(ctx, stale) })
	if !errors.Is(err, ErrConflict) {
		t.Errorf("stale update: got %v, want conflict", err)
	}
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Delete(ctx, r.ID, stale.Revision) }); !errors.Is(err, ErrConflict) {
		t.Errorf("stale delete: got %v, want conflict", err)
	}
}

func TestMemoryStatusDoesNotConflict(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	r := createSite(t, s, k, NewID("acct"), "a.test")
	status := testSiteStatus{Status: Status{Phase: PhaseReady, ObservedGeneration: 1}, URL: "https://a.test"}
	if err := SetStatus(ctx, s, r.ID, status); err != nil {
		t.Fatal(err)
	}
	got, _ := k.Get(ctx, s, r.ID)
	if got.Status.Phase != PhaseReady || got.Status.URL != "https://a.test" || got.Revision != r.Revision {
		t.Errorf("status write: %+v", got)
	}
	common, err := (&Resource{Status: mustJSON(t, status)}).CommonStatus()
	if err != nil || common.Phase != PhaseReady {
		t.Errorf("CommonStatus = %+v, %v", common, err)
	}
	r.Spec = json.RawMessage(`{"domains":["b.test"]}`)
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Update(ctx, r) }); err != nil {
		t.Errorf("an update after a status write should not conflict: %v", err)
	}
}

func TestMemoryUniqueIndexes(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	a := createSite(t, s, k, NewID("acct"), "Example.com")

	r, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"example.COM"}}))
	err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, r) })
	if !errors.Is(err, errTestDomainTaken) {
		t.Fatalf("duplicate domain: got %v", err)
	}
	if e := i18n.From(err); e.Params["value"] != "example.com" {
		t.Errorf("conflict params = %v", e.Params)
	}

	dup, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"x.test", "X.test"}}))
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, dup) }); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("repeated value in one resource: got %v", err)
	}

	// Deleting keeps the domain reserved until the resource is purged.
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Delete(ctx, a.ID, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, r) }); err == nil {
		t.Fatal("the domain should stay reserved while the old site is being removed")
	}
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Purge(ctx, a.ID) }); err != nil {
		t.Fatal(err)
	}
	r.ID = ""
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, r) }); err != nil {
		t.Errorf("the domain should be free after the purge: %v", err)
	}
	if h, err := s.History(ctx, a.ID); err != nil || len(h) != 2 || !h[1].Deleted {
		t.Errorf("history should survive the purge: %+v, %v", h, err)
	}
}

func TestMemoryTransactionsAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	acct := NewID("acct")
	boom := errors.New("boom")
	err := s.Write(ctx, WriteMeta{}, func(tx Tx) error {
		r, _ := k.Encode(k.New(AccountScope(acct), testSiteSpec{Domains: []string{"one.test"}}))
		if err := tx.Create(ctx, r); err != nil {
			return err
		}
		if _, err := tx.Get(ctx, r.ID); err != nil {
			t.Errorf("a transaction should see its own writes: %v", err)
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
}

func TestMemoryListFiltersAndPages(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	a, b := NewID("acct"), NewID("acct")
	var ids []ID
	for i := 0; i < 5; i++ {
		ids = append(ids, createSite(t, s, k, a, "a"+string(rune('0'+i))+".test").ID)
	}
	createSite(t, s, k, b, "b.test")

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
	if len(seen) != 5 {
		t.Fatalf("paged through %d resources, want 5", len(seen))
	}
	for i := range ids {
		if seen[i] != ids[i] {
			t.Errorf("order: got %v, want %v", seen, ids)
			break
		}
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
}

func TestMemoryRejectsBadMetadata(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	server, _ := k.Encode(k.New(ServerScope(), testSiteSpec{Domains: []string{"x.test"}}))
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, server) }); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("wrong scope level: %v", err)
	}
	wrongPrefix, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"y.test"}}))
	wrongPrefix.ID = NewID("other")
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, wrongPrefix) }); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("wrong prefix: %v", err)
	}
	r := createSite(t, s, k, NewID("acct"), "z.test")
	r.Scope = AccountScope(NewID("acct"))
	if err := s.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Update(ctx, r) }); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("changing the scope should be rejected: %v", err)
	}
	if _, err := s.Get(ctx, NewID("tsite")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing resource: %v", err)
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
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
