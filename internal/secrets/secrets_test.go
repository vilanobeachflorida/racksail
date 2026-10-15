package secrets

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

func newStore(t *testing.T) *Memory {
	t.Helper()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	return NewMemory(box)
}

func TestStore(t *testing.T) {
	ctx := context.Background()
	m := newStore(t)
	site := store.NewID("site")
	ref := NewRef(site, "db-password")
	if err := m.Put(ctx, ref, []byte("s3cret!")); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(ctx, ref)
	if err != nil || string(got) != "s3cret!" {
		t.Errorf("get = %q, %v", got, err)
	}
	if bytes.Contains(m.data[ref], []byte("s3cret!")) {
		t.Error("values must be encrypted at rest")
	}
	if refs, _ := m.List(ctx, site); len(refs) != 1 || refs[0] != ref {
		t.Errorf("list = %v", refs)
	}
	if err := m.Put(ctx, "not-a-ref", []byte("x")); !errors.Is(err, ErrBadRef) {
		t.Errorf("bad ref: %v", err)
	}
	if err := m.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestValuesAreBoundToTheirRef(t *testing.T) {
	m := newStore(t)
	a, b := NewRef(store.NewID("site"), "a"), NewRef(store.NewID("site"), "b")
	sealed, err := m.box.Encrypt(a, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.box.Decrypt(b, sealed); err == nil {
		t.Error("a value moved to another ref must not decrypt")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := m.box.Decrypt(a, sealed); err == nil {
		t.Error("a tampered value must not decrypt")
	}
	if _, err := NewBox([]byte("short")); err == nil {
		t.Error("short keys must be refused")
	}
}

func TestRedact(t *testing.T) {
	got := Redact("connecting with password hunter22 and token hunter22-extra", "hunter22", "hunter22-extra", "x")
	if got != "connecting with password "+Placeholder+" and token "+Placeholder {
		t.Errorf("Redact = %q", got)
	}
}
