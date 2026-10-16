package changeset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Confirm is asked about the change set as planned, before anything is
// written. What it refuses leaves everything as it was: nothing written,
// the idempotency key free for a retry, a preview still a preview and a
// change set still not undone. Dry runs don't ask it.
func TestConfirmIsAskedAboutThePlan(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	errRefused := errors.New("confirm it's you first")
	var asked []*ChangeSet
	refuse := func(ctx context.Context, cs *ChangeSet) error {
		asked = append(asked, cs)
		return errRefused
	}
	page := e.createPage(pageSpec{Path: "home.html", Content: "Home"})
	if _, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{page}, DryRun: true, Confirm: refuse}); err != nil || len(asked) != 0 {
		t.Fatalf("a dry run = %v, asked %d times", err, len(asked))
	}
	_, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{page}, IdempotencyKey: "home", Confirm: refuse})
	if !errors.Is(err, errRefused) || len(asked) != 1 || asked[0].Changes[0].Op != OpCreate || asked[0].DryRun {
		t.Fatalf("a refused change = %v, asked about %+v", err, asked)
	}
	if list, _, _ := pageKind.List(ctx, e.store, store.Query{}); len(list) != 0 {
		t.Fatalf("a refused change wrote %+v", list)
	}
	// What Confirm is given is a copy, so changing it changes nothing.
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: []Change{page}, IdempotencyKey: "home", Confirm: func(ctx context.Context, cs *ChangeSet) error {
		cs.Changes[0].After = json.RawMessage(`{"path":"elsewhere.html","content":"Elsewhere"}`)
		return nil
	}})
	if err != nil {
		t.Fatalf("the retry once agreed = %v", err)
	}
	created := e.wait(t, cs)
	if got := e.file(pagesDir + "/home.html"); got != "Home" || e.file(pagesDir+"/elsewhere.html") != "" {
		t.Errorf("the page = %q", got)
	}

	// Applying a preview asks about it as planned again.
	raw, _ := json.Marshal(pageSpec{Path: "home.html", Content: "Welcome"})
	preview := e.submit(t, true, Change{Op: OpUpdate, Kind: "test.page", ID: created.Changes[0].ID, Spec: raw})
	asked = nil
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, Confirm: refuse}); !errors.Is(err, errRefused) {
		t.Fatalf("a refused preview = %v", err)
	}
	if len(asked) != 1 || !strings.Contains(string(asked[0].Changes[0].Before), "Home") || !strings.Contains(string(asked[0].Changes[0].After), "Welcome") {
		t.Errorf("asked about %+v", asked)
	}
	if still, err := e.engine.Get(ctx, preview.ID); err != nil || !still.DryRun {
		t.Fatalf("the refused preview = %+v, %v", still, err)
	}
	applied, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, applied)

	// So does an undo, by the changes it makes.
	asked = nil
	if _, err := e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: applied.ID, DryRun: true, Confirm: refuse}); err != nil || len(asked) != 0 {
		t.Fatalf("a dry run of an undo = %v, asked %d times", err, len(asked))
	}
	_, err = e.engine.Undo(ctx, UndoRequest{Actor: e.owner, ChangeSet: applied.ID, Confirm: refuse})
	if !errors.Is(err, errRefused) || len(asked) != 1 || asked[0].UndoOf != applied.ID || asked[0].Changes[0].Op != OpUpdate {
		t.Fatalf("a refused undo = %v, asked about %+v", err, asked)
	}
	if orig, _ := e.engine.Get(ctx, applied.ID); orig.State != StateApplied {
		t.Errorf("after a refused undo the change set is %s", orig.State)
	}
	if got := e.file(pagesDir + "/home.html"); got != "Welcome" {
		t.Errorf("the page = %q", got)
	}
}

// What Confirm agreed to is what gets written. A change that lands after it
// was asked has a request that names no revision planned and asked about
// again, from what landed, and makes applying a preview fail.
func TestConfirmHoldsToThePlan(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, Retention{}, nil)
	id := e.submit(t, false, e.create("test.label", labelSpec{Name: "draft"})).Changes[0].ID
	// landing has another write rename the label once Confirm agreed, the
	// first time it is asked, and records what each plan started from.
	var from []string
	landing := func() Confirm {
		landed := false
		return func(ctx context.Context, cs *ChangeSet) error {
			var before labelSpec
			if err := json.Unmarshal(cs.Changes[0].Before, &before); err != nil {
				return err
			}
			from = append(from, before.Name)
			if landed {
				return nil
			}
			landed = true
			return e.store.Write(ctx, store.WriteMeta{Actor: "test"}, func(tx store.Tx) error {
				cur, err := tx.Get(ctx, id)
				if err != nil {
					return err
				}
				cur.Spec = json.RawMessage(`{"name":"theirs"}`)
				return tx.Update(ctx, cur)
			})
		}
	}
	rename := func(name string) []Change {
		return []Change{{Op: OpUpdate, Kind: "test.label", ID: id, Spec: json.RawMessage(`{"name":"` + name + `"}`)}}
	}
	cs, err := e.engine.Submit(ctx, Request{Actor: e.owner, Changes: rename("mine"), Confirm: landing()})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, cs)
	if strings.Join(from, ",") != "draft,theirs" {
		t.Errorf("Confirm was asked about plans from %v", from)
	}
	if got, _ := labelKind.Get(ctx, e.store, id); got.Spec.Name != "mine" {
		t.Errorf("label = %+v", got.Spec)
	}

	preview := e.submit(t, true, rename("yours")...)
	from = nil
	if _, err := e.engine.Apply(ctx, ApplyRequest{Actor: e.owner, ChangeSet: preview.ID, Confirm: landing()}); !store.ErrConflict.Is(err) {
		t.Errorf("a preview whose resource changed after Confirm agreed = %v", err)
	}
	if got, _ := labelKind.Get(ctx, e.store, id); got.Spec.Name != "theirs" {
		t.Errorf("label = %+v", got.Spec)
	}
	if still, err := e.engine.Get(ctx, preview.ID); err != nil || !still.DryRun {
		t.Errorf("the preview = %+v, %v", still, err)
	}
}
