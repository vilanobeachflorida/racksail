package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"
)

// atomicWorld is a store and a table of another part of the panel, both in
// one database.
type atomicWorld struct {
	db    *DB
	store *SQLite
	kind  *KindOf[testSiteSpec, testSiteStatus]
}

func newAtomicWorld(t *testing.T) *atomicWorld {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	reg := NewKindRegistry()
	k := testSiteKind()
	if err := reg.Register(k); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLite(ctx, db, reg)
	if err != nil {
		t.Fatal(err)
	}
	notes := fstest.MapFS{"0001_create_notes.sql": {Data: []byte("CREATE TABLE notes (text TEXT NOT NULL) STRICT;")}}
	if err := db.Migrate(ctx, "test.notes", notes); err != nil {
		t.Fatal(err)
	}
	return &atomicWorld{db: db, store: s, kind: k}
}

func (w *atomicWorld) note(ctx context.Context, text string) error {
	_, err := w.db.Exec(ctx, `INSERT INTO notes (text) VALUES (?)`, text)
	return err
}

func (w *atomicWorld) notes(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.db.Reader().QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (w *atomicWorld) sites(t *testing.T) int {
	t.Helper()
	page, err := w.store.List(context.Background(), Query{Kind: "test.site"})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

func (w *atomicWorld) createSite(ctx context.Context, domain string) error {
	r, err := w.kind.Encode(w.kind.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{domain}}))
	if err != nil {
		return err
	}
	return w.store.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, r) })
}

// Writes of different parts made in Atomic commit together, and what waits
// for the commit, such as telling watchers, happens after it.
func TestAtomicCommitsTogether(t *testing.T) {
	w := newAtomicWorld(t)
	watcher := w.store.Watch(WatchFilter{}, 8)
	defer watcher.Close()
	var after []string
	err := w.db.Atomic(context.Background(), func(ctx context.Context) error {
		if err := w.createSite(ctx, "one.test"); err != nil {
			return err
		}
		if err := w.note(ctx, "made one.test"); err != nil {
			return err
		}
		w.db.AfterCommit(ctx, func() { after = append(after, "committed") })
		// Nothing is visible outside the transaction yet.
		if n := w.sites(t); n != 0 {
			t.Errorf("a reader sees %d sites before the commit", n)
		}
		if n := w.notes(t); n != 0 {
			t.Errorf("a reader sees %d notes before the commit", n)
		}
		select {
		case n := <-watcher.C:
			t.Errorf("a watcher heard of %s before the commit", n.ID)
		default:
		}
		if len(after) != 0 {
			t.Error("what waits for the commit ran before it")
		}
		// Atomic inside Atomic is the same transaction.
		return w.db.Atomic(ctx, func(ctx context.Context) error { return w.note(ctx, "nested") })
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.sites(t) != 1 || w.notes(t) != 2 {
		t.Errorf("after the commit: %d sites, %d notes", w.sites(t), w.notes(t))
	}
	if len(after) != 1 {
		t.Errorf("after the commit ran %v", after)
	}
	select {
	case n := <-watcher.C:
		if n.Op != NoticeCreated {
			t.Errorf("notice = %+v", n)
		}
	default:
		t.Error("watchers hear about the writes once they are committed")
	}
}

// When Atomic's function fails, nothing it wrote stays, and nothing that
// waited for the commit runs.
func TestAtomicRollsBackTogether(t *testing.T) {
	w := newAtomicWorld(t)
	watcher := w.store.Watch(WatchFilter{}, 8)
	defer watcher.Close()
	boom := errors.New("boom")
	ran := false
	err := w.db.Atomic(context.Background(), func(ctx context.Context) error {
		if err := w.createSite(ctx, "two.test"); err != nil {
			return err
		}
		if err := w.note(ctx, "made two.test"); err != nil {
			return err
		}
		w.db.AfterCommit(ctx, func() { ran = true })
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Atomic = %v", err)
	}
	if w.sites(t) != 0 || w.notes(t) != 0 || ran {
		t.Errorf("after a rollback: %d sites, %d notes, after commit ran: %v", w.sites(t), w.notes(t), ran)
	}
	select {
	case n := <-watcher.C:
		t.Errorf("a watcher heard of %s, which was rolled back", n.ID)
	default:
	}
	// The domain is free again.
	if err := w.createSite(context.Background(), "two.test"); err != nil {
		t.Error(err)
	}
}

// A write that fails inside Atomic is undone on its own; the others go on
// and commit.
func TestAtomicUndoesAFailedWriteAlone(t *testing.T) {
	w := newAtomicWorld(t)
	failed := errors.New("failed")
	err := w.db.Atomic(context.Background(), func(ctx context.Context) error {
		if err := w.note(ctx, "kept"); err != nil {
			return err
		}
		err := w.db.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notes (text) VALUES ('undone')`); err != nil {
				return err
			}
			return failed
		})
		if !errors.Is(err, failed) {
			t.Errorf("the failed write = %v", err)
		}
		// The store refuses a duplicate domain the same way.
		if err := w.createSite(ctx, "dup.test"); err != nil {
			return err
		}
		if err := w.createSite(ctx, "dup.test"); err == nil {
			t.Error("a second site with the same domain")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	if err := w.db.Reader().QueryRow(`SELECT group_concat(text) FROM notes`).Scan(&text); err != nil || text != "kept" {
		t.Errorf("notes = %q, %v", text, err)
	}
	if w.sites(t) != 1 {
		t.Errorf("sites = %d", w.sites(t))
	}
}

func TestAtomicKeepsToOneDatabase(t *testing.T) {
	w := newAtomicWorld(t)
	other := openTestDB(t)
	ran := false
	err := other.Atomic(context.Background(), func(ctx context.Context) error {
		// A write to another database doesn't join this transaction, and
		// commits on its own.
		if err := w.note(ctx, "elsewhere"); err != nil {
			return err
		}
		w.db.AfterCommit(ctx, func() { ran = true })
		if !ran {
			t.Error("waiting for another database's commit")
		}
		return w.db.Atomic(ctx, func(ctx context.Context) error { return nil })
	})
	if !errors.Is(err, ErrOtherTransaction) {
		t.Errorf("Atomic within another database's = %v", err)
	}
	if w.notes(t) != 1 {
		t.Errorf("notes = %d", w.notes(t))
	}
}
