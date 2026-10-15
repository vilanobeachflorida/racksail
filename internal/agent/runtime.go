package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"path"
	"strings"
	"sync"
	"time"

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
	// reservedLoaded says whether the reservations were read from the
	// state directory.
	reservedLoaded bool
	ledger         map[string]ledgerEntry
	loaded         bool
	// verified is when each file in the ledger was last compared in full.
	verified map[string]time.Time
	rollback *time.Timer
	started  bool

	events   *eventHub
	refusals refusalLog

	// driftMu guards drift, what the last periodic check found.
	driftMu sync.Mutex
	drift   map[string]Drift
}

// refusalEvery is how often a refused caller from one address is recorded;
// the refusals in between are counted into the next record, so a flood of
// them can't push the real records out of the trail.
const refusalEvery = 10 * time.Second

type refusalLog struct {
	mu   sync.Mutex
	seen map[string]refusalCount
}

type refusalCount struct {
	last    time.Time
	skipped int
}

// refuse records a caller the agent turned away before any operation ran,
// during the TLS handshake or because what it holds may not call the agent.
func (rt *Runtime) refuse(op, caller, remote string, reason error) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	now := rt.now()
	rt.refusals.mu.Lock()
	if rt.refusals.seen == nil || len(rt.refusals.seen) >= 4096 {
		rt.refusals.seen = map[string]refusalCount{}
	}
	c := rt.refusals.seen[host]
	if !c.last.IsZero() && now.Sub(c.last) < refusalEvery {
		c.skipped++
		rt.refusals.seen[host] = c
		rt.refusals.mu.Unlock()
		return
	}
	rt.refusals.seen[host] = refusalCount{last: now}
	rt.refusals.mu.Unlock()
	msg := reason.Error()
	if c.skipped > 0 {
		msg += fmt.Sprintf(" (%d more refused from %s since the last record)", c.skipped, host)
	}
	r := AuditRecord{Time: now.UTC(), Op: op, Caller: caller, Remote: remote, Code: string(ErrNotAllowed.Code), Error: msg}
	if err := rt.audit.Record(r); err != nil {
		rt.log.Error("writing the audit record", "op", op, "error", err)
	}
	rt.log.Warn("refused a caller", "op", op, "caller", caller, "remote", remote, "error", reason)
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
		reserved: map[int]time.Time{}, ledger: map[string]ledgerEntry{}, verified: map[string]time.Time{}, events: newEventHub(),
		drift: map[string]Drift{},
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
				rt.publishDrift(drift)
			}
		}
	}()
	return nil
}

// Subscribe returns events as they happen: drift found by the periodic
// check, once per change, and network changes confirmed or rolled back.
// Call cancel when done.
func (rt *Runtime) Subscribe() (<-chan Event, func()) { return rt.events.subscribe() }

// Events calls fn with the drift the last check found, then with every
// event as it happens, until ctx is done.
func (rt *Runtime) Events(ctx context.Context, fn func(Event)) error {
	events, cancel := rt.Subscribe()
	defer cancel()
	for _, d := range rt.CurrentDrift() {
		fn(Event{Type: EventDrift, Time: rt.now().UTC(), Drift: &d})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-events:
			if !ok {
				return nil
			}
			fn(e)
		}
	}
}

// statePath is a file in the agent's state directory.
func (rt *Runtime) statePath(name string) string { return path.Join(rt.dir, name) }

// writeState keeps data in the state directory, creating the directories
// it needs: the state directory and those in it only root can enter.
func (rt *Runtime) writeState(ctx context.Context, name string, data []byte) error {
	f := rt.backend.Files
	target := rt.statePath(name)
	var missing []string
	for d := path.Dir(target); d != "/"; d = path.Dir(d) {
		if _, err := f.Stat(ctx, d); err == nil {
			break
		} else if !errors.Is(err, system.ErrNotFound) {
			return err
		}
		missing = append([]string{d}, missing...)
	}
	for _, d := range missing {
		perm := fs.FileMode(0o755)
		if d == rt.dir || strings.HasPrefix(d, rt.dir+"/") {
			perm = 0o700
		}
		if err := f.Mkdir(ctx, d, perm, system.Owner{}); err != nil && !errors.Is(err, system.ErrExists) {
			return err
		}
	}
	return f.WriteFile(ctx, target, data, 0o600, system.Owner{})
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

// accountIDsName keeps the account IDs handed out but not used yet.
const accountIDsName = "account-ids.json"

// reservationTime is how long an allocated account ID is held for the
// account that will use it.
const reservationTime = 10 * time.Minute

// loadReserved reads the reservations an earlier run left, once. The caller
// holds rt.mu.
func (rt *Runtime) loadReserved(ctx context.Context) error {
	if rt.reservedLoaded {
		return nil
	}
	data, err := rt.readState(ctx, accountIDsName)
	if err != nil {
		return err
	}
	if data != nil {
		saved := map[int]time.Time{}
		if err := json.Unmarshal(data, &saved); err != nil {
			rt.log.Warn("the agent's account ID reservations can't be read; starting over", "error", err)
		}
		for id, until := range saved {
			rt.reserved[id] = until
		}
	}
	rt.reservedLoaded = true
	return nil
}

// saveReserved keeps the reservations. The caller holds rt.mu.
func (rt *Runtime) saveReserved(ctx context.Context) error {
	data, err := json.Marshal(rt.reserved)
	if err != nil {
		return err
	}
	return rt.writeState(ctx, accountIDsName, data)
}

// reserve hands out an account ID until it is used or the reservation
// lapses, so two allocations in a row never get the same ID, even when the
// agent restarts in between.
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
	if err := rt.loadReserved(ctx); err != nil {
		return 0, err
	}
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
	rt.reserved[id] = now.Add(reservationTime).UTC()
	if err := rt.saveReserved(ctx); err != nil {
		delete(rt.reserved, id)
		return 0, err
	}
	return id, nil
}

// release drops a reservation once the ID is in use.
func (rt *Runtime) release(ctx context.Context, id int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err := rt.loadReserved(ctx); err != nil {
		rt.log.Warn("reading the account ID reservations", "error", err)
		return
	}
	if _, ok := rt.reserved[id]; !ok {
		return
	}
	delete(rt.reserved, id)
	if err := rt.saveReserved(ctx); err != nil {
		rt.log.Warn("keeping the account ID reservations", "error", err)
	}
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

// Snapshot files the agent saved, so a restore only ever reads one of them,
// as it left it.

const snapshotFilesName = "snapshot-files.json"

// savedSnapshot describes a file SaveSnapshot wrote.
type savedSnapshot struct {
	Snapshot string    `json:"snapshot"`
	SHA256   string    `json:"sha256"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modTime"`
}

func (rt *Runtime) loadSnapshotFiles(ctx context.Context) (map[string]savedSnapshot, error) {
	data, err := rt.readState(ctx, snapshotFilesName)
	if err != nil {
		return nil, err
	}
	files := map[string]savedSnapshot{}
	if data != nil {
		if err := json.Unmarshal(data, &files); err != nil {
			return nil, fmt.Errorf("reading the saved snapshot files: %w", err)
		}
	}
	return files, nil
}

// rememberSnapshot records a file SaveSnapshot wrote.
func (rt *Runtime) rememberSnapshot(ctx context.Context, p string, s savedSnapshot) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	files, err := rt.loadSnapshotFiles(ctx)
	if err != nil {
		return err
	}
	files[p] = s
	data, err := json.Marshal(files)
	if err != nil {
		return err
	}
	return rt.writeState(ctx, snapshotFilesName, data)
}

// savedSnapshot returns the record of a file SaveSnapshot wrote, as long as
// the file still has the size and modification time it was saved with.
func (rt *Runtime) savedSnapshot(ctx context.Context, p string, info system.FileInfo) (savedSnapshot, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	files, err := rt.loadSnapshotFiles(ctx)
	if err != nil {
		return savedSnapshot{}, err
	}
	s, ok := files[p]
	switch {
	case !ok:
		return s, fmt.Errorf("%s wasn't saved by SaveSnapshot", p)
	case s.Size != info.Size || !s.ModTime.Equal(info.ModTime):
		return s, fmt.Errorf("%s changed since SaveSnapshot wrote it", p)
	}
	return s, nil
}
