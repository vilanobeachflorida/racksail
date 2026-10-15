package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestOverlayShowsPlannedChanges(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	acct := NewID("acct")
	kept := createSite(t, s, k, acct, "kept.test")
	changed := createSite(t, s, k, acct, "old.test")
	gone := createSite(t, s, k, acct, "gone.test")
	o := NewOverlay(s, s.kinds)

	upd := changed.Clone()
	upd.Spec = json.RawMessage(`{"domains":["new.test"],"runtime":"php"}`)
	o.Put(upd)
	del := gone.Clone()
	now := time.Now()
	del.DeletedAt = &now
	o.Put(del)
	added := &Resource{Meta: Meta{ID: NewID("tsite"), Kind: "test.site", Scope: AccountScope(acct)}, Spec: json.RawMessage(`{"domains":["added.test"],"runtime":"php"}`)}
	o.Put(added)

	if got, err := o.Get(ctx, added.ID); err != nil || got.ID != added.ID {
		t.Errorf("planned resources can be read: %v, %v", got, err)
	}
	if got, _ := o.Get(ctx, changed.ID); string(got.Spec) != string(upd.Spec) {
		t.Errorf("the planned spec wins: %s", got.Spec)
	}
	if got, _ := o.Get(ctx, gone.ID); !got.Deleted() {
		t.Error("a planned delete is visible")
	}
	if _, err := s.Get(ctx, added.ID); !errors.Is(err, ErrNotFound) {
		t.Error("nothing reaches the store")
	}

	page, err := o.List(ctx, Query{Kind: "test.site"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []ID
	for _, r := range page.Items {
		ids = append(ids, r.ID)
	}
	if len(ids) != 3 || ids[0] != kept.ID || ids[1] != changed.ID || ids[2] != added.ID {
		t.Errorf("list = %v", ids)
	}
	php, err := o.List(ctx, Query{Kind: "test.site", Index: map[string]string{"runtime": "php"}})
	if err != nil || len(php.Items) != 2 {
		t.Errorf("queries see planned specs: %v, %v", php, err)
	}
	old, _ := o.List(ctx, Query{Kind: "test.site", Index: map[string]string{"domain": "old.test"}})
	if len(old.Items) != 0 {
		t.Error("a resource whose planned spec no longer matches drops out")
	}
	withDeleted, _ := o.List(ctx, Query{Kind: "test.site", IncludeDeleted: true})
	if len(withDeleted.Items) != 4 {
		t.Errorf("deleted resources are listed on request: %d", len(withDeleted.Items))
	}
	picked, _ := o.List(ctx, Query{Kind: "test.site", IDs: []ID{added.ID, gone.ID, kept.ID}})
	if len(picked.Items) != 2 || picked.Items[0].ID != kept.ID || picked.Items[1].ID != added.ID {
		t.Errorf("a list of IDs selects stored and planned resources: %+v", picked.Items)
	}
}

func TestOverlayPages(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	acct := NewID("acct")
	for _, d := range []string{"a.test", "b.test", "c.test"} {
		createSite(t, s, k, acct, d)
	}
	o := NewOverlay(s, s.kinds)
	for _, d := range []string{"d.test", "e.test"} {
		o.Put(&Resource{Meta: Meta{ID: NewID("tsite"), Kind: "test.site", Scope: AccountScope(acct)}, Spec: json.RawMessage(`{"domains":["` + d + `"]}`)})
	}
	var all []ID
	cursor := ""
	for range 10 {
		page, err := o.List(ctx, Query{Kind: "test.site", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatalf("page of %d", len(page.Items))
		}
		for _, r := range page.Items {
			all = append(all, r.ID)
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(all) != 5 {
		t.Fatalf("paging saw %d resources", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i] <= all[i-1] {
			t.Errorf("pages are in ID order without repeats: %v", all)
		}
	}
}
