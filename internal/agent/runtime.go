package agent

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Runtime is the agent's state that outlives single operations: the audit
// trail, what operations wrote on whose behalf so drift can be reported,
// network changes waiting to be confirmed, and account IDs handed out but
// not used yet. What must survive a restart is kept in the agent's state
// directory through the backend, so the simulated server keeps it too.
type Runtime struct {
	backend *system.Backend
	audit   Auditor
	log     *slog.Logger
	now     func() time.Time
	dir     string

	// driftEvery is how often Start checks for drift.
	driftEvery time.Duration

	mu       sync.Mutex
	reserved map[int]time.Time
	ledger   map[string]ledgerEntry
	loaded   bool
	rollback *time.Timer
	started  bool

	events *eventHub
}

// RuntimeOptions configure a Runtime.
type RuntimeOptions struct {
	// Audit keeps the audit trail; records stay in memory when it's nil.
	Audit Auditor
	Log   *slog.Logger
	// Clock returns the current time; time.Now by default.
	Clock func() time.Time
	// StateDir is where the agent keeps its state on the machine;
	// system.AgentStateDir by default.
	StateDir string
	// DriftInterval is how often Start checks for drift; a minute by
	// default.
	DriftInterval time.Duration
}

// NewRuntime returns the runtime for a backend.
func NewRuntime(backend *system.Backend, opts RuntimeOptions) *Runtime {
	if opts.Audit == nil {
		opts.Audit = NewMemoryAudit(0)
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.StateDir == "" {
		opts.StateDir = system.AgentStateDir
	}
	if opts.DriftInterval <= 0 {
		opts.DriftInterval = time.Minute
	}
	return &Runtime{
		backend: backend, audit: opts.Audit, log: opts.Log, now: opts.Clock, dir: opts.StateDir, driftEvery: opts.DriftInterval,
		reserved: map[int]time.Time{}, ledger: map[string]ledgerEntry{}, events: newEventHub(),
	}
}

// Audit returns the audit trail.
func (rt *Runtime) Audit() Auditor { return rt.audit }

// Start recovers what an earlier run left: a network change that wasn't
// confirmed in time is rolled back now, one still waiting gets its timer
// back. Then it checks for drift periodically until ctx is done, publishing
// what it finds. Starting again does nothing.
func (rt *Runtime) Start(ctx context.Context) error {
	rt.mu.Lock()
	if rt.started {
		rt.mu.Unlock()
		return nil
	}
	rt.started = true
	rt.mu.Unlock()
	if err := rt.recoverNetwork(ctx); err != nil {
		rt.log.Error("recovering a network change", "error", err)
	}
	go func() {
		t := time.NewTicker(rt.driftEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				drift, err := rt.CheckDrift(ctx)
				if err != nil {
					rt.log.Warn("checking for drift", "error", err)
					continue
				}
				for _, d := range drift {
					rt.events.publish(Event{Type: EventDrift, Time: rt.now().UTC(), Drift: &d})
				}
			}
		}
	}()
	return nil
}

// Subscribe returns events as they happen: drift found by the periodic
// check and network changes rolled back. Call cancel when done.
func (rt *Runtime) Subscribe() (<-chan Event, func()) { return rt.events.subscribe() }

// statePath is a file in the agent's state directory.
func (rt *Runtime) statePath(name string) string { return path.Join(rt.dir, name) }

// writeState keeps data in the state directory, creating it.
func (rt *Runtime) writeState(ctx context.Context, name string, data []byte) error {
	f := rt.backend.Files
	var missing []string
	for d := rt.dir; d != "/"; d = path.Dir(d) {
		if _, err := f.Stat(ctx, d); err == nil {
			break
		} else if !errors.Is(err, system.ErrNotFound) {
			return err
		}
		missing = append([]string{d}, missing...)
	}
	for _, d := range missing {
		perm := fs.FileMode(0o755)
		if d == rt.dir {
			perm = 0o700
		}
		if err := f.Mkdir(ctx, d, perm, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
			return err
		}
	}
	return f.WriteFile(ctx, rt.statePath(name), data, 0o600, system.Owner{})
}

// readState reads a state file, returning nil when there is none.
func (rt *Runtime) readState(ctx context.Context, name string) ([]byte, error) {
	data, err := rt.backend.Files.ReadFile(ctx, rt.statePath(name), 64<<20)
	if errors.Is(err, system.ErrNotFound) {
		return nil, nil
	}
	return data, err
}

func (rt *Runtime) removeState(ctx context.Context, name string) error {
	err := rt.backend.Files.Remove(ctx, rt.statePath(name))
	if errors.Is(err, system.ErrNotFound) {
		return nil
	}
	return err
}

// reserve hands out an account ID until it is used or the reservation
// lapses, so two allocations in a row never get the same ID.
func (rt *Runtime) reserve(ctx context.Context, exclude []int) (int, error) {
	users, err := rt.backend.Users.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	groups, err := rt.backend.Users.ListGroups(ctx)
	if err != nil {
		return 0, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := rt.now()
	held := append([]int(nil), exclude...)
	for id, until := range rt.reserved {
		if now.After(until) {
			delete(rt.reserved, id)
			continue
		}
		held = append(held, id)
	}
	id, err := system.AllocateAccountID(users, groups, held)
	if err != nil {
		return 0, err
	}
	rt.reserved[id] = now.Add(10 * time.Minute)
	return id, nil
}

// release drops a reservation once the ID is in use.
func (rt *Runtime) release(id int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.reserved, id)
}

// Ledger kinds.
const (
	ledgerFile   = "file"
	ledgerUnit   = "unit"
	ledgerDropIn = "dropin"
)

// ledgerEntry is something an operation put on the machine for a resource.
type ledgerEntry struct {
	Owner    store.ID  `json:"owner"`
	Kind     string    `json:"kind"`
	SHA256   string    `json:"sha256"`
	Recorded time.Time `json:"recorded"`
}

const ledgerFileName = "ledger.json"

func (rt *Runtime) loadLedger(ctx context.Context) error {
	if rt.loaded {
		return nil
	}
	data, err := rt.readState(ctx, ledgerFileName)
	if err != nil {
		return err
	}
	if data != nil {
		if err := json.Unmarshal(data, &rt.ledger); err != nil {
			rt.log.Warn("the agent's ledger can't be read; starting a new one", "error", err)
			rt.ledger = map[string]ledgerEntry{}
		}
	}
	rt.loaded = true
	return nil
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// remember records that target now holds content written for owner, so a
// later change by hand is reported as drift.
func (rt *Runtime) remember(ctx context.Context, kind, target string, owner store.ID, content []byte) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err := rt.loadLedger(ctx); err != nil {
		return err
	}
	e := ledgerEntry{Owner: owner, Kind: kind, SHA256: sum(content), Recorded: rt.now().UTC()}
	if old, ok := rt.ledger[target]; ok && old.Owner == e.Owner && old.Kind == e.Kind && old.SHA256 == e.SHA256 {
		return nil
	}
	rt.ledger[target] = e
	return rt.saveLedger(ctx)
}

// forget drops what an operation removed from the ledger.
func (rt *Runtime) forget(ctx context.Context, targets ...string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err := rt.loadLedger(ctx); err != nil {
		return err
	}
	changed := false
	for _, t := range targets {
		for key := range rt.ledger {
			if key == t || strings.HasPrefix(key, strings.TrimSuffix(t, "/")+"/") {
				delete(rt.ledger, key)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return rt.saveLedger(ctx)
}

func (rt *Runtime) saveLedger(ctx context.Context) error {
	data, err := json.Marshal(rt.ledger)
	if err != nil {
		return err
	}
	return rt.writeState(ctx, ledgerFileName, data)
}

// Drift is something on the machine that no longer matches what Racksail
// put there.
type Drift struct {
	Owner store.ID `json:"owner,omitempty" doc:"The resource it was written for"`
	// Kind is file, unit or dropin.
	Kind   string `json:"kind" enum:"file,unit,dropin"`
	Target string `json:"target" doc:"The path or unit"`
	// Reason is changed, missing or failed.
	Reason string `json:"reason" enum:"changed,missing,failed"`
}

// CheckDrift compares the machine with the ledger and reports what was
// changed or removed by hand, and Racksail's units that failed.
func (rt *Runtime) CheckDrift(ctx context.Context) ([]Drift, error) {
	rt.mu.Lock()
	if err := rt.loadLedger(ctx); err != nil {
		rt.mu.Unlock()
		return nil, err
	}
	entries := make(map[string]ledgerEntry, len(rt.ledger))
	for k, v := range rt.ledger {
		entries[k] = v
	}
	rt.mu.Unlock()

	out := []Drift{}
	for target, e := range entries {
		var content []byte
		var err error
		switch e.Kind {
		case ledgerUnit:
			content, err = rt.backend.Units.ReadUnit(ctx, target)
		default:
			content, err = rt.backend.Files.ReadFile(ctx, target, maxFileContent)
		}
		switch {
		case errors.Is(err, system.ErrNotFound):
			out = append(out, Drift{Owner: e.Owner, Kind: e.Kind, Target: target, Reason: "missing"})
		case err != nil:
			return nil, err
		case sum(content) != e.SHA256:
			out = append(out, Drift{Owner: e.Owner, Kind: e.Kind, Target: target, Reason: "changed"})
		}
	}
	units, err := rt.backend.Units.List(ctx, "racksail-*")
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		if u.Failed() {
			owner := store.ID("")
			if e, ok := entries[u.Name]; ok {
				owner = e.Owner
			}
			out = append(out, Drift{Owner: owner, Kind: ledgerUnit, Target: u.Name, Reason: "failed"})
		}
	}
	sortDrift(out)
	return out, nil
}

func sortDrift(d []Drift) {
	slices.SortFunc(d, func(a, b Drift) int {
		return cmp.Or(cmp.Compare(a.Target, b.Target), cmp.Compare(a.Reason, b.Reason))
	})
}

// Event types.
const (
	EventDrift             = "drift"
	EventNetworkRolledBack = "network.rolled_back"
	EventNetworkConfirmed  = "network.confirmed"
)

// Event is something the agent tells the panel about without being asked.
type Event struct {
	Type    string    `json:"type"`
	Time    time.Time `json:"time"`
	Drift   *Drift    `json:"drift,omitempty"`
	Message string    `json:"message,omitempty"`
}

type eventHub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func newEventHub() *eventHub { return &eventHub{subs: map[chan Event]struct{}{}} }

func (h *eventHub) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
}

// publish sends to every subscriber that keeps up; a subscriber whose
// buffer is full misses the event rather than holding the agent up.
func (h *eventHub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func newToken() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// errNoRuntime is returned by operations that need state when they're run
// without a runtime, which only happens in misconfigured tests.
var errNoRuntime = fmt.Errorf("this operation needs the agent's runtime")
