package store

import (
	"slices"
	"sync"
	"sync/atomic"
)

// ChangeOp says what a committed write did to a resource.
type ChangeOp string

// What writes do.
const (
	ChangeCreated ChangeOp = "created"
	ChangeUpdated ChangeOp = "updated"
	// ChangeStatus is a status write: the reconciler reported what it saw.
	ChangeStatus  ChangeOp = "status"
	ChangeDeleted ChangeOp = "deleted"
	ChangePurged  ChangeOp = "purged"
)

// Change is one committed write to a resource, as watchers see it. It names
// the resource; watchers read it again when they need more.
type Change struct {
	Op         ChangeOp `json:"op" enum:"created,updated,status,deleted,purged"`
	ID         ID       `json:"id"`
	Kind       string   `json:"kind"`
	Scope      Scope    `json:"scope"`
	Generation int64    `json:"generation"`
	Revision   int64    `json:"revision"`
	// Phase is the phase the resource's status reports after the write.
	Phase     Phase `json:"phase" enum:"pending,applying,ready,degraded,failed,deleting"`
	ChangeSet ID    `json:"changeSet,omitempty"`
}

// WatchFilter selects the changes a watcher receives.
type WatchFilter struct {
	// Kinds limits the changes to these kinds when it isn't empty.
	Kinds []string
	// Scopes limits the changes to resources of these owners when it isn't
	// empty.
	Scopes []Scope
	// IDs limits the changes to these resources when it isn't empty.
	IDs []ID
}

// Match reports whether a change passes the filter.
func (f WatchFilter) Match(c Change) bool {
	return (len(f.Kinds) == 0 || slices.Contains(f.Kinds, c.Kind)) &&
		(len(f.Scopes) == 0 || slices.Contains(f.Scopes, c.Scope)) &&
		(len(f.IDs) == 0 || slices.Contains(f.IDs, c.ID))
}

// Watcher receives committed changes until it is closed.
type Watcher struct {
	C       <-chan Change
	ch      chan Change
	filter  WatchFilter
	hub     *hub
	once    sync.Once
	dropped atomic.Int64
}

// Close stops the watcher. C is closed once nothing can be sent to it any
// more.
func (w *Watcher) Close() {
	w.once.Do(func() {
		w.hub.mu.Lock()
		defer w.hub.mu.Unlock()
		delete(w.hub.watchers, w)
		close(w.ch)
	})
}

// Dropped returns how many changes didn't fit in the watcher's buffer.
func (w *Watcher) Dropped() int64 { return w.dropped.Load() }

// hub hands committed changes to watchers. Sending never blocks, and
// happens under the lock Close takes, so a watcher is never closed while a
// change is being sent to it.
type hub struct {
	mu       sync.Mutex
	watchers map[*Watcher]bool
}

func (h *hub) watch(f WatchFilter, buffer int) *Watcher {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Change, buffer)
	w := &Watcher{C: ch, ch: ch, filter: f, hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watchers == nil {
		h.watchers = map[*Watcher]bool{}
	}
	h.watchers[w] = true
	return w
}

func (h *hub) publish(changes []Change) {
	if len(changes) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range changes {
		for w := range h.watchers {
			if !w.filter.Match(c) {
				continue
			}
			select {
			case w.ch <- c:
			default:
				w.dropped.Add(1)
			}
		}
	}
}

// changeOf describes a write to r.
func changeOf(op ChangeOp, r *Resource, cs ID) Change {
	return Change{
		Op: op, ID: r.ID, Kind: r.Kind, Scope: r.Scope, Generation: r.Generation, Revision: r.Revision,
		Phase: PhaseOf(r), ChangeSet: cs,
	}
}
