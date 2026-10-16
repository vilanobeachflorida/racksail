package store

import (
	"slices"
	"sync"
	"sync/atomic"
)

// NoticeOp says what a committed write did to a resource.
type NoticeOp string

// What writes do.
const (
	NoticeCreated NoticeOp = "created"
	NoticeUpdated NoticeOp = "updated"
	// NoticeStatus is a status write: the reconciler reported what it saw.
	NoticeStatus  NoticeOp = "status"
	NoticeDeleted NoticeOp = "deleted"
	NoticePurged  NoticeOp = "purged"
)

// Notice is one committed write to a resource, as watchers see it. It names
// the resource; watchers read it again when they need more.
type Notice struct {
	Op         NoticeOp `json:"op" enum:"created,updated,status,deleted,purged" doc:"What the write did"`
	ID         ID       `json:"id" doc:"The resource written"`
	Kind       string   `json:"kind" doc:"The resource's kind"`
	Scope      Scope    `json:"scope" doc:"Who the resource belongs to"`
	Generation int64    `json:"generation" doc:"The generation of its settings after the write"`
	Revision   int64    `json:"revision" doc:"Its revision after the write"`
	// Phase is the phase the resource's status reports after the write.
	Phase     Phase `json:"phase" enum:"pending,applying,ready,degraded,failed,deleting" doc:"The phase its status reports after the write"`
	ChangeSet ID    `json:"changeSet,omitempty" doc:"The change set that made the write, if one did"`
}

// WatchFilter selects the notices a watcher receives.
type WatchFilter struct {
	// Kinds limits the notices to these kinds when it isn't empty.
	Kinds []string
	// Scopes limits the notices to resources of these owners when it isn't
	// empty.
	Scopes []Scope
	// IDs limits the notices to these resources when it isn't empty.
	IDs []ID
}

// Match reports whether a notice passes the filter.
func (f WatchFilter) Match(n Notice) bool {
	return (len(f.Kinds) == 0 || slices.Contains(f.Kinds, n.Kind)) &&
		(len(f.Scopes) == 0 || slices.Contains(f.Scopes, n.Scope)) &&
		(len(f.IDs) == 0 || slices.Contains(f.IDs, n.ID))
}

// Watcher receives the notices of committed writes until it is closed.
type Watcher struct {
	C       <-chan Notice
	ch      chan Notice
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

// Dropped returns how many notices didn't fit in the watcher's buffer.
func (w *Watcher) Dropped() int64 { return w.dropped.Load() }

// hub hands notices to watchers. Sending never blocks, and happens under the
// lock Close takes, so a watcher is never closed while a notice is being
// sent to it.
type hub struct {
	mu       sync.Mutex
	watchers map[*Watcher]bool
}

func (h *hub) watch(f WatchFilter, buffer int) *Watcher {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Notice, buffer)
	w := &Watcher{C: ch, ch: ch, filter: f, hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watchers == nil {
		h.watchers = map[*Watcher]bool{}
	}
	h.watchers[w] = true
	return w
}

func (h *hub) publish(notices []Notice) {
	if len(notices) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range notices {
		for w := range h.watchers {
			if !w.filter.Match(n) {
				continue
			}
			select {
			case w.ch <- n:
			default:
				w.dropped.Add(1)
			}
		}
	}
}

// noticeOf describes a write to r.
func noticeOf(op NoticeOp, r *Resource, cs ID) Notice {
	return Notice{
		Op: op, ID: r.ID, Kind: r.Kind, Scope: r.Scope, Generation: r.Generation, Revision: r.Revision,
		Phase: PhaseOf(r), ChangeSet: cs,
	}
}
