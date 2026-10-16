package account

import (
	"context"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Ownership tells the authorizer which reseller each account belongs to,
// from the accounts' own specs. A module that keeps resellers may know more,
// and is asked first when there is one.
type Ownership struct {
	store store.Reader
	next  auth.Ownership
	now   func() time.Time

	mu sync.Mutex
	// expected are accounts being created for a reseller, until they're in
	// the store, so the change pipeline lets the reseller create them.
	expected map[store.ID]expectation
}

type expectation struct {
	reseller store.ID
	until    time.Time
}

var (
	_ auth.Ownership     = (*Ownership)(nil)
	_ auth.AccountLister = (*Ownership)(nil)
)

// NewOwnership returns the ownership of the accounts in st. next, when not
// nil, is what a module that keeps resellers provides.
func NewOwnership(st store.Reader, next auth.Ownership) *Ownership {
	return &Ownership{store: st, next: next, now: time.Now, expected: map[store.ID]expectation{}}
}

// ResellerOf implements auth.Ownership.
func (o *Ownership) ResellerOf(ctx context.Context, account store.ID) (store.ID, error) {
	if o.next != nil {
		r, err := o.next.ResellerOf(ctx, account)
		if err != nil || r != "" {
			return r, err
		}
	}
	obj, err := Kind.Get(ctx, o.store, account)
	switch {
	case store.ErrNotFound.Is(err):
		if r, ok := o.expectation(account); ok {
			return r, nil
		}
		return "", nil
	case err != nil:
		return "", err
	}
	return obj.Spec.Reseller, nil
}

// AccountsOf implements auth.AccountLister.
func (o *Ownership) AccountsOf(ctx context.Context, reseller store.ID) ([]store.ID, error) {
	seen := map[store.ID]bool{}
	var out []store.ID
	if lister, ok := o.next.(auth.AccountLister); ok {
		ids, err := lister.AccountsOf(ctx, reseller)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			seen[id] = true
			out = append(out, id)
		}
	}
	q := store.Query{Kind: Kind.Name(), Index: map[string]string{"reseller": string(reseller)}, Limit: store.MaxLimit}
	for {
		page, err := o.store.List(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Items {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r.ID)
			}
		}
		if page.Next == "" {
			return out, nil
		}
		q.Cursor = page.Next
	}
}

// Expect says an account with this ID is about to be created for reseller,
// after whoever creates it was found allowed to, so the change pipeline's
// check of the new account sees whose it is. It holds until the account is
// in the store or ttl passes.
func (o *Ownership) Expect(account, reseller store.ID, ttl time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	for id, e := range o.expected {
		if now.After(e.until) {
			delete(o.expected, id)
		}
	}
	o.expected[account] = expectation{reseller: reseller, until: now.Add(ttl)}
}

func (o *Ownership) expectation(account store.ID) (store.ID, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	e, ok := o.expected[account]
	if !ok || o.now().After(e.until) {
		return "", false
	}
	return e.reseller, true
}
