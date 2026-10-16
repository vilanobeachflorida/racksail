package inbox

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var quiet = slog.New(slog.DiscardHandler)

func ids(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// fixed is a source that finds the same items every time, and counts how
// often it was asked.
type fixed struct {
	mu    sync.Mutex
	items []Item
	err   error
	calls atomic.Int32
	// block holds each call until it is closed, when it isn't nil.
	block chan struct{}
}

func (f *fixed) Items(ctx context.Context, q Query) ([]Item, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.items), f.err
}

func (f *fixed) set(items ...Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = items
}

var (
	alice = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "alice"}
	bob   = &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Name: "bob"}
)

func TestListOrdersAndFilters(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	jobs := &fixed{items: []Item{
		{ID: "job:a", Severity: events.SeverityError, Time: now.Add(-time.Hour), Version: "1"},
		{ID: "job:b", Severity: events.SeverityError, Time: now.Add(-time.Minute), Version: "1"},
	}}
	others := &fixed{items: []Item{
		{ID: "update:x", Severity: events.SeverityNotice, Time: now, Version: "1"},
		{ID: "alert:c", Severity: events.SeverityCritical, Time: now.Add(-48 * time.Hour), Version: "1"},
		// A source repeating an item another found shows it once.
		{ID: "job:b", Severity: events.SeverityInfo, Time: now, Version: "1"},
		{ID: "hidden", Severity: events.SeverityCritical, Time: now, Version: "1"},
	}}
	s := New(t.Context(), Options{Sources: []Registration{{Name: "jobs", Source: jobs}, {Name: "others", Source: others, Owner: "web"}}, Now: func() time.Time { return now }, Log: quiet})
	res, err := s.List(t.Context(), Query{Actor: alice}, func(it Item) bool { return it.ID != "hidden" }, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(res.Items), []string{"alert:c", "job:b", "job:a", "update:x"}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if res.Items[1].Source != "core" || res.Items[3].Source != "web" || res.Items[0].Actions == nil {
		t.Errorf("sources and actions = %+v", res.Items)
	}
}

func TestDismissals(t *testing.T) {
	for name, make := range map[string]func(t *testing.T) Dismissals{
		"memory": func(*testing.T) Dismissals { return NewMemory() },
		"sql": func(t *testing.T) Dismissals {
			db, err := store.OpenDB(t.Context(), filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			d, err := NewSQL(t.Context(), db)
			if err != nil {
				t.Fatal(err)
			}
			return d
		},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			clock := now
			src := &fixed{items: []Item{
				{ID: "job:a", Severity: events.SeverityError, Time: now, Version: "1"},
				{ID: "job:b", Severity: events.SeverityError, Time: now, Version: "1"},
			}}
			s := New(t.Context(), Options{Sources: []Registration{{Name: "jobs", Source: src}}, Dismissals: make(t), Now: func() time.Time { return clock }, Log: quiet})
			list := func(who *auth.Actor, withDismissed bool) Result {
				t.Helper()
				res, err := s.List(t.Context(), Query{Actor: who}, nil, withDismissed)
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			checked, dismissed, stop := s.Changes(Person(alice))
			defer stop()
			_, others, stopOthers := s.Changes(Person(bob))
			defer stopOthers()
			if err := s.Dismiss(t.Context(), Person(alice), list(alice, false).Items[0], nil); err != nil {
				t.Fatal(err)
			}
			select {
			case <-dismissed:
			default:
				t.Error("dismissing doesn't tell the person's streams")
			}
			select {
			case <-others:
				t.Error("someone else's streams heard of the dismissal")
			case <-checked:
				t.Error("a dismissal was told as a check")
			default:
			}
			if d, ok, err := s.Dismissed(t.Context(), Person(alice), "job:a"); err != nil || !ok || d.Version != "1" {
				t.Errorf("the dismissal = %+v, %v, %v", d, ok, err)
			}
			if _, ok, _ := s.Dismissed(t.Context(), Person(bob), "job:a"); ok {
				t.Error("someone else has the dismissal")
			}
			if res := list(alice, false); !slices.Equal(ids(res.Items), []string{"job:b"}) || res.Dismissed != 1 {
				t.Errorf("after dismissing = %v, %d dismissed", ids(res.Items), res.Dismissed)
			}
			// Others still see it.
			if res := list(bob, false); len(res.Items) != 2 {
				t.Errorf("someone else sees %v", ids(res.Items))
			}
			res := list(alice, true)
			if len(res.Items) != 2 || res.Items[0].Dismissal == nil || res.Items[0].Dismissal.At.IsZero() || res.Items[1].Dismissal != nil {
				t.Errorf("with dismissed = %+v", res.Items)
			}
			// A new version of the item comes back.
			src.set(Item{ID: "job:a", Severity: events.SeverityError, Time: now, Version: "2"}, Item{ID: "job:b", Severity: events.SeverityError, Time: now, Version: "1"})
			if res := list(alice, false); len(res.Items) != 2 {
				t.Errorf("a changed item stays hidden: %v", ids(res.Items))
			}
			// A snooze ends at its time.
			until := now.Add(time.Hour)
			if err := s.Dismiss(t.Context(), Person(alice), Item{ID: "job:b", Version: "1"}, &until); err != nil {
				t.Fatal(err)
			}
			if res := list(alice, false); !slices.Equal(ids(res.Items), []string{"job:a"}) {
				t.Errorf("snoozed = %v", ids(res.Items))
			}
			clock = until
			if res := list(alice, false); len(res.Items) != 2 {
				t.Errorf("after the snooze = %v", ids(res.Items))
			}
			if ok, err := s.Restore(t.Context(), Person(alice), "job:b"); err != nil || !ok {
				t.Errorf("restore = %v, %v", ok, err)
			}
			select {
			case <-dismissed:
			default:
				t.Error("bringing an item back doesn't tell the person's streams")
			}
			if ok, _ := s.Restore(t.Context(), Person(alice), "job:b"); ok {
				t.Error("restoring twice found a dismissal")
			}
			select {
			case <-dismissed:
				t.Error("restoring nothing told the streams")
			default:
			}
			// Pruning drops snoozes that ended and old dismissals.
			d := s.opts.Dismissals
			d.Put(t.Context(), "p", "old", Dismissal{Version: "1", At: now.Add(-200 * 24 * time.Hour)})
			d.Put(t.Context(), "p", "ended", Dismissal{Version: "1", At: now, Until: &until})
			d.Put(t.Context(), "p", "kept", Dismissal{Version: "1", At: now})
			n, err := d.Prune(t.Context(), clock, clock.Add(-DismissalRetention))
			if err != nil || n != 2 {
				t.Errorf("pruned %d, %v", n, err)
			}
			left, _ := d.Of(t.Context(), "p")
			if len(left) != 1 || left["kept"].Version != "1" {
				t.Errorf("left = %+v", left)
			}
		})
	}
}

func TestPersonOfActors(t *testing.T) {
	token := &auth.Actor{Type: auth.ActorToken, ID: store.NewID("tok"), User: alice.ID}
	dev := &auth.Actor{Type: auth.ActorUser, Name: "developer"}
	if Person(token) != string(alice.ID) || Person(alice) != string(alice.ID) || Person(dev) != "user:developer" {
		t.Errorf("people = %q %q %q", Person(token), Person(alice), Person(dev))
	}
}

func TestUnavailableSources(t *testing.T) {
	broken := &fixed{err: i18n.ErrUnavailable.New("what", "The agent")}
	working := &fixed{items: []Item{{ID: "x", Version: "1"}}}
	off := &fixed{items: []Item{{ID: "off", Version: "1"}}}
	s := New(t.Context(), Options{Sources: []Registration{
		{Name: "broken", Source: broken}, {Name: "working", Source: working},
		{Name: "off", Source: off, Active: func() bool { return false }},
	}, Log: quiet})
	res, err := s.List(t.Context(), Query{Actor: alice}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(res.Items), []string{"x"}) || len(res.Unavailable) != 1 || res.Unavailable[0].Source != "broken" || res.Unavailable[0].Code != "core.unavailable" {
		t.Errorf("result = %+v", res)
	}
	// The values of the reason's message come along, for the UI to
	// translate it.
	if got := res.Unavailable[0].Params["what"]; got != "The agent" {
		t.Errorf("the problem's params = %+v", res.Unavailable[0].Params)
	}
	if off.calls.Load() != 0 {
		t.Error("a source that isn't active was read")
	}
	// A request that ends while reading says so, rather than reporting
	// every source as unavailable.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	broken.err = context.Canceled
	if _, err := s.List(ctx, Query{Actor: alice}, nil, false); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled = %v", err)
	}
}

func TestKeptSources(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := now
	tick := func(d time.Duration) {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
	}
	slow := &fixed{items: []Item{{ID: "updates:packages", Version: "1"}}, block: make(chan struct{})}
	s := New(t.Context(), Options{
		Sources: []Registration{{Name: "updates", Source: slow, Keep: time.Hour}},
		Now:     func() time.Time { mu.Lock(); defer mu.Unlock(); return clock },
		Wait:    20 * time.Millisecond, Log: quiet,
	})
	changes, _, stop := s.Changes(Person(alice))
	defer stop()
	// The first check takes longer than a request waits.
	res, err := s.List(t.Context(), Query{Actor: alice}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || !slices.Equal(res.Checking, []string{"updates"}) {
		t.Errorf("while checking = %+v", res)
	}
	close(slow.block)
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("the end of the first check wasn't told")
	}
	for range 3 {
		res, _ = s.List(t.Context(), Query{Actor: alice}, nil, false)
	}
	if !slices.Equal(ids(res.Items), []string{"updates:packages"}) || len(res.Checking) != 0 || slow.calls.Load() != 1 {
		t.Errorf("once checked = %+v after %d calls", res, slow.calls.Load())
	}
	// Once old, what was found still shows while it is checked again.
	slow.set(Item{ID: "updates:packages", Version: "2"})
	tick(2 * time.Hour)
	res, _ = s.List(t.Context(), Query{Actor: alice}, nil, false)
	if len(res.Items) != 1 || res.Items[0].Version != "1" {
		t.Errorf("while checking again = %+v", res.Items)
	}
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("a check that found something new wasn't told")
	}
	res, _ = s.List(t.Context(), Query{Actor: alice}, nil, false)
	if res.Items[0].Version != "2" || slow.calls.Load() != 2 {
		t.Errorf("after checking again = %+v, %d calls", res.Items, slow.calls.Load())
	}
	// A check that finds the same says nothing.
	s.Refresh()
	deadline := time.After(5 * time.Second)
	for slow.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("Refresh didn't check")
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(20 * time.Millisecond)
	select {
	case <-changes:
		t.Error("a check that found nothing new was told")
	default:
	}
}

func TestRunChecksAndPrunes(t *testing.T) {
	src := &fixed{items: []Item{{ID: "x", Version: "1"}}}
	d := NewMemory()
	old := time.Now().Add(-DismissalRetention - time.Hour)
	d.Put(t.Context(), "p", "old", Dismissal{Version: "1", At: old})
	ctx, cancel := context.WithCancel(t.Context())
	s := New(ctx, Options{Sources: []Registration{{Name: "kept", Source: src, Keep: time.Millisecond}}, Dismissals: d, Log: quiet})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx, 5*time.Millisecond)
	}()
	deadline := time.After(5 * time.Second)
	for {
		left, _ := d.Of(t.Context(), "p")
		if src.calls.Load() >= 2 && len(left) == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("after running: %d checks, dismissals %v", src.calls.Load(), left)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
