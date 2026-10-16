package inbox

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Category says what an item is about.
type Category string

// Categories, as the inbox groups and marks its items.
const (
	// CategoryJob is work that failed, such as applying a change.
	CategoryJob Category = "job"
	// CategoryResource is something Racksail manages that isn't working.
	CategoryResource Category = "resource"
	// CategoryAlert is something that happened and needs a look.
	CategoryAlert Category = "alert"
	// CategoryUpdate is software waiting to be installed, or a restart that
	// would finish installing it.
	CategoryUpdate Category = "update"
	// CategoryCertificate is a certificate that expires soon, expired, or
	// couldn't be obtained.
	CategoryCertificate Category = "certificate"
	// CategorySuggestion is a module that would help.
	CategorySuggestion Category = "suggestion"
)

// ActionType says what an action does. The UI knows each one: what its
// button says and which screen or request it leads to.
type ActionType string

// Actions items offer.
const (
	ActionOpenResource  ActionType = "open_resource"
	ActionOpenJob       ActionType = "open_job"
	ActionOpenChangeSet ActionType = "open_changeset"
	ActionRetryJob      ActionType = "retry_job"
	ActionUndoChangeSet ActionType = "undo_changeset"
	// ActionOpenEvents shows the timeline of the item's events.
	ActionOpenEvents ActionType = "open_events"
	// ActionDocs opens a page of the documentation, named by its path
	// there, such as install/updates.
	ActionDocs ActionType = "docs"
)

// Action is something to do about an item, offered as a button.
type Action struct {
	Type ActionType `json:"type"`
	// Title is the button's label in English; the UI has its own for each
	// type.
	Title string `json:"title"`
	// Target is what the action works on.
	Target string `json:"target,omitempty"`
}

// Dismissal is someone's decision not to see an item for now.
type Dismissal struct {
	// Version is the item's version when it was dismissed.
	Version string    `json:"-"`
	At      time.Time `json:"at"`
	// Until is when a snoozed item comes back; without it the item comes
	// back once it changes.
	Until *time.Time `json:"until,omitempty"`
}

// active reports whether the dismissal still hides an item at version.
func (d Dismissal) active(version string, now time.Time) bool {
	return d.Version == version && (d.Until == nil || d.Until.After(now))
}

// Access says who may see an item. The API checks it, for every item,
// before it shows any.
type Access struct {
	// Resource is set for items about a resource: those who may read it may
	// see the item.
	Resource *store.Meta
	// Job is set for items about a job: whoever started it may see the
	// item, and those who hold core.job.read where it belongs.
	Job *jobs.Job
	// Permission on Scope is what it takes to see any other item.
	Permission auth.Permission
	Scope      store.Scope
}

// Item is one thing that needs attention.
type Item struct {
	// ID names the item while it lasts: the same problem has the same ID
	// every time the inbox is read.
	ID       string          `json:"id"`
	Category Category        `json:"category"`
	Severity events.Severity `json:"severity"`
	// Code names the message the UI shows, inbox.<code> in its catalogs,
	// filled in from Params; Title and Message are the English text.
	Code    string            `json:"code"`
	Params  map[string]string `json:"params,omitempty"`
	Title   string            `json:"title"`
	Message string            `json:"message,omitempty"`
	Time    time.Time         `json:"time"`
	// Count is how often an alert happened in the time the inbox looks at.
	Count     int         `json:"count,omitempty"`
	Subject   store.ID    `json:"subject,omitempty"`
	Kind      string      `json:"kind,omitempty"`
	Scope     store.Scope `json:"scope"`
	Job       store.ID    `json:"job,omitempty"`
	ChangeSet store.ID    `json:"changeSet,omitempty"`
	// Event is the latest event of an alert, which the UI shows in the
	// person's language as the timeline does.
	Event   *events.Event `json:"event,omitempty"`
	Actions []Action      `json:"actions"`
	// Source is what found the item: core, or a module's id.
	Source string `json:"source"`
	// Version changes when the item does, so a dismissal ends once there is
	// something new to see.
	Version   string     `json:"version"`
	Dismissal *Dismissal `json:"dismissal,omitempty"`
	Access    Access     `json:"-"`
}

// Query says whose inbox is read.
type Query struct {
	Actor *auth.Actor
	// All says the actor's grants reach every owner; otherwise Scopes are
	// the owners they reach. Sources narrow what they read to those, and
	// the API checks every item again.
	All    bool
	Scopes []store.Scope
	Now    time.Time
}

// Source finds items.
type Source interface {
	Items(ctx context.Context, q Query) ([]Item, error)
}

// SourceFunc is a function that finds items.
type SourceFunc func(ctx context.Context, q Query) ([]Item, error)

// Items implements Source.
func (f SourceFunc) Items(ctx context.Context, q Query) ([]Item, error) { return f(ctx, q) }

// Registration adds a source to the inbox.
type Registration struct {
	// Name says which source it is in the problems the API reports, such
	// as "updates".
	Name   string
	Source Source
	// Owner is what the source's items say found them: core, or a module's
	// id.
	Owner string
	// Keep is for sources that find the same for everyone and take a
	// while: the service keeps what they found for this long and checks
	// again in the background after that. Their Query has no actor, and
	// they are checked when the service starts, so the first person to look
	// doesn't wait. Zero reads the source for every request.
	Keep time.Duration
	// Active reports whether the source applies now, such as a module's
	// while the module is on; nil means always.
	Active func() bool
}

// SourceProblem says why a source couldn't be read.
type SourceProblem struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Result is what someone's inbox holds.
type Result struct {
	Items []Item
	// Dismissed counts the items left out because the person dismissed
	// them.
	Dismissed int
	// Unavailable lists the sources that couldn't be read.
	Unavailable []SourceProblem
	// Checking lists the sources being checked for the first time, whose
	// items come later.
	Checking []string
}

// Options configure a Service.
type Options struct {
	Sources    []Registration
	Dismissals Dismissals
	Log        *slog.Logger
	Now        func() time.Time
	// Wait bounds how long a request waits for a source the service is
	// checking for the first time; a second when zero.
	Wait time.Duration
	// CheckTimeout bounds each check of a source the service keeps; a
	// minute when zero.
	CheckTimeout time.Duration
}

// shared is what the service keeps of a source that finds the same for
// everyone.
type shared struct {
	reg Registration

	mu      sync.Mutex
	items   []Item
	err     error
	checked time.Time
	// running is closed when the check under way ends; nil without one.
	running chan struct{}
}

// Service puts together what needs someone's attention.
type Service struct {
	opts    Options
	base    context.Context
	sources []*shared
	// changes tells the inbox streams that what sources found changed.
	changes broadcaster
	work    sync.WaitGroup
}

// New returns a service. ctx bounds the checks it runs in the background.
func New(ctx context.Context, opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait <= 0 {
		opts.Wait = time.Second
	}
	if opts.CheckTimeout <= 0 {
		opts.CheckTimeout = time.Minute
	}
	if opts.Dismissals == nil {
		opts.Dismissals = NewMemory()
	}
	s := &Service{opts: opts, base: ctx}
	for _, reg := range opts.Sources {
		if reg.Owner == "" {
			reg.Owner = "core"
		}
		s.sources = append(s.sources, &shared{reg: reg})
	}
	return s
}

// Person names whose dismissals they are: the user an actor acts for, or
// the actor itself, so a person's tokens and sessions share them.
func Person(a *auth.Actor) string {
	if id := jobs.Of(a); id != "" {
		return string(id)
	}
	if a == nil {
		return ""
	}
	return string(a.Type) + ":" + a.Name
}

func (sh *shared) active() bool { return sh.reg.Active == nil || sh.reg.Active() }

// List returns the items of someone's inbox: those visible lets through,
// most urgent first, without the ones they dismissed unless withDismissed.
// A source that can't be read is reported in Unavailable, and the others
// still show.
func (s *Service) List(ctx context.Context, q Query, visible func(Item) bool, withDismissed bool) (Result, error) {
	if q.Now.IsZero() {
		q.Now = s.opts.Now()
	}
	var res Result
	seen := map[string]bool{}
	for _, src := range s.sources {
		if !src.active() {
			continue
		}
		var items []Item
		var err error
		if src.reg.Keep > 0 {
			var known bool
			if items, err, known = s.kept(ctx, src); !known {
				res.Checking = append(res.Checking, src.reg.Name)
				continue
			}
		} else {
			items, err = src.reg.Source.Items(ctx, q)
		}
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			s.opts.Log.Warn("reading the inbox", "source", src.reg.Name, "error", err)
			e := i18n.From(err)
			res.Unavailable = append(res.Unavailable, SourceProblem{Source: src.reg.Name, Code: string(e.Code), Message: e.Message})
			continue
		}
		for _, it := range items {
			if seen[it.ID] || (visible != nil && !visible(it)) {
				continue
			}
			seen[it.ID] = true
			if it.Source == "" {
				it.Source = src.reg.Owner
			}
			if it.Actions == nil {
				it.Actions = []Action{}
			}
			res.Items = append(res.Items, it)
		}
	}
	dismissals, err := s.opts.Dismissals.Of(ctx, Person(q.Actor))
	if err != nil {
		return Result{}, err
	}
	kept := res.Items[:0]
	for _, it := range res.Items {
		if d, ok := dismissals[it.ID]; ok && d.active(it.Version, q.Now) {
			res.Dismissed++
			if !withDismissed {
				continue
			}
			it.Dismissal = &d
		}
		kept = append(kept, it)
	}
	res.Items = kept
	sortItems(res.Items)
	if res.Items == nil {
		res.Items = []Item{}
	}
	return res, nil
}

var severityRank = map[events.Severity]int{
	events.SeverityInfo: 0, events.SeverityNotice: 1, events.SeverityWarning: 2, events.SeverityError: 3, events.SeverityCritical: 4,
}

// sortItems puts the most urgent first, then the most recent.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ra, rb := severityRank[a.Severity], severityRank[b.Severity]; ra != rb {
			return ra > rb
		}
		if !a.Time.Equal(b.Time) {
			return a.Time.After(b.Time)
		}
		return a.ID < b.ID
	})
}

// kept returns what a source the service keeps last found, starting a check
// in the background when that is old. A source never checked yet is waited
// for a little; known is false when it still hasn't answered.
func (s *Service) kept(ctx context.Context, sh *shared) (items []Item, err error, known bool) {
	sh.mu.Lock()
	if sh.checked.IsZero() || s.opts.Now().Sub(sh.checked) >= sh.reg.Keep {
		s.startCheck(sh)
	}
	running := sh.running
	if !sh.checked.IsZero() {
		items, err = sh.items, sh.err
		sh.mu.Unlock()
		return items, err, true
	}
	sh.mu.Unlock()
	if running != nil {
		timer := time.NewTimer(s.opts.Wait)
		defer timer.Stop()
		select {
		case <-running:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.checked.IsZero() {
		return nil, nil, false
	}
	return sh.items, sh.err, true
}

// startCheck checks a kept source in the background unless a check is
// under way. sh.mu is held.
func (s *Service) startCheck(sh *shared) {
	if sh.running != nil || s.base.Err() != nil {
		return
	}
	done := make(chan struct{})
	sh.running = done
	s.work.Go(func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(s.base, s.opts.CheckTimeout)
		defer cancel()
		items, err := sh.reg.Source.Items(ctx, Query{All: true, Now: s.opts.Now()})
		if err != nil && s.base.Err() != nil {
			// The service is stopping; what the source found before stays.
			sh.mu.Lock()
			sh.running = nil
			sh.mu.Unlock()
			return
		}
		if err != nil {
			s.opts.Log.Warn("checking for the inbox", "source", sh.reg.Name, "error", err)
		}
		sh.mu.Lock()
		changed := sh.checked.IsZero() || signature(sh.items) != signature(items) || (sh.err == nil) != (err == nil)
		sh.items, sh.err, sh.checked, sh.running = items, err, s.opts.Now(), nil
		sh.mu.Unlock()
		if changed {
			s.changes.notify()
		}
	})
}

// signature tells whether two lists of items show the same.
func signature(items []Item) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it.ID+"@"+it.Version)
	}
	slices.Sort(parts)
	return strings.Join(parts, "\n")
}

// Refresh checks every source the service keeps again in the background,
// as when something they depend on changed.
func (s *Service) Refresh() {
	for _, sh := range s.sources {
		if sh.reg.Keep > 0 && sh.active() {
			sh.mu.Lock()
			s.startCheck(sh)
			sh.mu.Unlock()
		}
	}
}

// Run checks the sources the service keeps when it starts and whenever what
// they found gets old, and drops dismissals nobody needs any more, until
// ctx is done. interval is how often it looks.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	s.Refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.work.Wait()
			return
		case <-ticker.C:
			for _, sh := range s.sources {
				if sh.reg.Keep <= 0 || !sh.active() {
					continue
				}
				sh.mu.Lock()
				if s.opts.Now().Sub(sh.checked) >= sh.reg.Keep {
					s.startCheck(sh)
				}
				sh.mu.Unlock()
			}
			now := s.opts.Now()
			if n, err := s.opts.Dismissals.Prune(ctx, now, now.Add(-DismissalRetention)); err != nil {
				s.opts.Log.Warn("pruning inbox dismissals", "error", err)
			} else if n > 0 {
				s.opts.Log.Debug("pruned inbox dismissals", "count", n)
			}
		}
	}
}

// DismissalRetention is how long a dismissal is kept when the item it hides
// may never come back; an item that does after that shows again.
const DismissalRetention = 180 * 24 * time.Hour

// Dismiss hides an item from someone's inbox until it changes, or until
// the given time when it isn't nil.
func (s *Service) Dismiss(ctx context.Context, person string, item Item, until *time.Time) error {
	if err := s.opts.Dismissals.Put(ctx, person, item.ID, Dismissal{Version: item.Version, At: s.opts.Now().UTC(), Until: until}); err != nil {
		return err
	}
	s.changes.notify()
	return nil
}

// Restore shows a dismissed item again. It reports whether there was a
// dismissal to take back.
func (s *Service) Restore(ctx context.Context, person, id string) (bool, error) {
	ok, err := s.opts.Dismissals.Delete(ctx, person, id)
	if ok {
		s.changes.notify()
	}
	return ok, err
}

// Changes returns a channel that receives whenever what the kept sources
// found changes, or someone dismisses or restores an item, until stop is
// called. A receiver that is busy gets one notice for several.
func (s *Service) Changes() (<-chan struct{}, func()) { return s.changes.subscribe() }

// broadcaster tells subscribers that something changed.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func (b *broadcaster) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[chan struct{}]struct{}{}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *broadcaster) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
