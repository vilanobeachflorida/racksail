package reconcile

import (
	"container/heap"
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// ResyncInterval is how often the panel reconciles everything. The resync
// puts back what drifted on the machine and carries out changes whose apply
// job never ran.
const ResyncInterval = 15 * time.Minute

// Run works the reconcile queue until ctx is done. It reconciles at once the
// resources whose latest generation hasn't been carried out, then every
// resource; afterwards it retries failed reconciles with a growing wait,
// reconciles what Enqueue and ReportDrift add, and reconciles everything
// again every interval. The panel runs it in the background from the moment
// it starts.
func (r *Runner) Run(ctx context.Context, interval time.Duration) {
	r.queue.setRunning(true)
	defer r.queue.setRunning(false)
	var wg sync.WaitGroup
	for range r.workers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.work(ctx)
		}()
	}
	r.sweep(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

// sweep queues every resource, unfinished ones first.
func (r *Runner) sweep(ctx context.Context) {
	now := r.now()
	for _, q := range []store.Query{{Unreconciled: true}, {IncludeDeleted: true}} {
		reason := "resync"
		if q.Unreconciled {
			reason = "unfinished"
		}
		err := r.eachResource(ctx, q, func(id store.ID) { r.queue.push(id, now, reason) })
		if err != nil && ctx.Err() == nil {
			r.log().Warn("the resync couldn't list every resource; it tries again at the next pass", "error", err)
		}
	}
}

// work reconciles queued resources as they come due.
func (r *Runner) work(ctx context.Context) {
	for {
		item := r.queue.next(ctx, r.now)
		if item == nil {
			return
		}
		if err := r.reconcile(ctx, item.id, nil, true); err != nil && ctx.Err() == nil {
			r.log().Debug("a queued reconcile failed", "resource", item.id, "reason", item.reason, "error", err)
		}
	}
}

// Enqueue asks for a resource to be reconciled as soon as there is room,
// for example because something it depends on changed. It does nothing
// until Run is going.
func (r *Runner) Enqueue(id store.ID, reason string) {
	r.queue.push(id, r.now(), reason)
}

// DriftReport is what the agent saw change on the machine, for one
// resource.
type DriftReport struct {
	Resource store.ID
	// Changes say what differs, such as a file that was edited by hand.
	Changes []string
}

// ReportDrift records that the machine no longer matches a resource: its
// Drifted condition turns true and <kind>.drifted is published. The resource
// is then reconciled as soon as there is room, which puts it back.
func (r *Runner) ReportDrift(ctx context.Context, report DriftReport) error {
	unlock := r.locks.lock(report.Resource)
	res, err := r.Store.Get(ctx, report.Resource)
	if err != nil {
		unlock()
		return err
	}
	if res.Deleted() {
		unlock()
		return nil
	}
	message := "The server no longer matches these settings."
	if len(report.Changes) > 0 {
		message = "Changed on the server: " + strings.Join(report.Changes, ", ") + "."
	}
	raw, err := WithCommonStatus(res.Status, func(st *store.Status) {
		st.SetCondition(store.Condition{
			Type: ConditionDrifted, Status: store.ConditionTrue, Reason: "ReportedByAgent",
			Message: message, ObservedGeneration: res.Generation,
		}, r.now())
	})
	if err == nil {
		err = r.writeStatus(ctx, res.ID, raw)
	}
	unlock()
	if err != nil {
		return err
	}
	r.publish(ctx, res, events.Event{Type: res.Kind + ".drifted", Severity: events.SeverityWarning, Message: message},
		events.Drift{Source: "agent", Changes: report.Changes})
	r.Enqueue(res.ID, "drift")
	return nil
}

// settle records how a reconcile ended. Failures are retried with a growing
// wait while Run is going, and the first in a row of a reconcile the queue
// started is published, since no change set reports it.
func (r *Runner) settle(ctx context.Context, res *store.Resource, err error, background bool) {
	if err == nil {
		r.queue.succeeded(res.ID)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrNoReconciler) || store.ErrNotFound.Is(err) {
		return
	}
	failures := r.queue.failed(res.ID)
	if failures <= r.maxRetries() {
		r.queue.retry(res.ID, r.now().Add(r.backoff(failures)))
	} else if failures == r.maxRetries()+1 {
		r.log().Warn("a resource keeps failing; it is tried again at the next change, drift report or resync",
			"resource", res.ID, "kind", res.Kind, "failures", failures)
	}
	if background && failures == 1 {
		r.publish(ctx, res, events.Event{Type: res.Kind + ".failed", Severity: events.SeverityError, Message: i18n.From(err).Message}, nil)
	}
}

func (r *Runner) maxRetries() int {
	if r.MaxRetries > 0 {
		return r.MaxRetries
	}
	return 8
}

// backoff is the wait before retrying after the given number of failures in
// a row, with a little jitter so resources that failed together don't all
// retry at the same moment.
func (r *Runner) backoff(failures int) time.Duration {
	base, most := r.Backoff, r.MaxBackoff
	if base <= 0 {
		base = 5 * time.Second
	}
	if most <= 0 {
		most = 10 * time.Minute
	}
	d := base
	for i := 1; i < failures && d < most; i++ {
		d *= 2
	}
	d = min(d, most)
	return d + time.Duration(rand.Int64N(int64(d)/10+1))
}

// Failures returns how many reconciles of a resource failed in a row.
func (r *Runner) Failures(id store.ID) int {
	r.queue.mu.Lock()
	defer r.queue.mu.Unlock()
	return r.queue.failures[id]
}

// workQueue holds the resources waiting to be reconciled, each once, in the
// order they come due.
type workQueue struct {
	mu       sync.Mutex
	items    map[store.ID]*queued
	order    queuedHeap
	failures map[store.ID]int
	seq      int64
	running  bool
	wake     chan struct{}
}

type queued struct {
	id     store.ID
	due    time.Time
	reason string
	seq    int64
	index  int
}

func (q *workQueue) init() {
	if q.items == nil {
		q.items = map[store.ID]*queued{}
		q.failures = map[store.ID]int{}
		q.wake = make(chan struct{}, 1)
	}
}

func (q *workQueue) setRunning(running bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.init()
	q.running = running
}

// push queues a resource for due, or moves it up when it is already queued
// for later. Nothing is queued while the queue isn't running.
func (q *workQueue) push(id store.ID, due time.Time, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.init()
	if !q.running {
		return
	}
	if it, ok := q.items[id]; ok {
		if due.Before(it.due) {
			it.due, it.reason = due, reason
			heap.Fix(&q.order, it.index)
		}
	} else {
		q.seq++
		it := &queued{id: id, due: due, reason: reason, seq: q.seq}
		q.items[id] = it
		heap.Push(&q.order, it)
	}
	q.signal()
}

// retry queues a resource again after a failure.
func (q *workQueue) retry(id store.ID, due time.Time) { q.push(id, due, "retry") }

func (q *workQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// next waits for a queued resource to come due and takes it off the queue.
// It returns nil when ctx is done.
func (q *workQueue) next(ctx context.Context, now func() time.Time) *queued {
	for {
		q.mu.Lock()
		q.init()
		wait := time.Hour
		var item *queued
		if len(q.order) > 0 {
			if top := q.order[0]; !top.due.After(now()) {
				item = heap.Pop(&q.order).(*queued)
				delete(q.items, item.id)
				// Another worker may take the next one.
				if len(q.order) > 0 && !q.order[0].due.After(now()) {
					q.signal()
				}
			} else {
				wait = top.due.Sub(now())
			}
		}
		wake := q.wake
		q.mu.Unlock()
		if item != nil {
			return item
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// failed counts a failure in a row and returns how many there are now.
func (q *workQueue) failed(id store.ID) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.init()
	q.failures[id]++
	return q.failures[id]
}

func (q *workQueue) succeeded(id store.ID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.failures, id)
}

// forget drops a resource that no longer exists.
func (q *workQueue) forget(id store.ID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.failures, id)
	if it, ok := q.items[id]; ok {
		heap.Remove(&q.order, it.index)
		delete(q.items, id)
	}
}

// queuedHeap orders queued resources by when they come due, then by when
// they were queued.
type queuedHeap []*queued

func (h queuedHeap) Len() int { return len(h) }

func (h queuedHeap) Less(i, j int) bool {
	if !h[i].due.Equal(h[j].due) {
		return h[i].due.Before(h[j].due)
	}
	return h[i].seq < h[j].seq
}

func (h queuedHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}

func (h *queuedHeap) Push(x any) {
	it := x.(*queued)
	it.index = len(*h)
	*h = append(*h, it)
}

func (h *queuedHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	*h = old[:len(old)-1]
	it.index = -1
	return it
}
