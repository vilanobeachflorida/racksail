package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// tally counts, per job, how often its handler ran and how often its type
// heard it end.
type tally struct {
	mu   sync.Mutex
	runs map[store.ID]int
	ends map[store.ID][]State
}

func newTally() *tally {
	return &tally{runs: map[store.ID]int{}, ends: map[store.ID][]State{}}
}

func (t *tally) ran(id store.ID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.runs[id]++
}

func (t *tally) ended(job *Job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ends[job.ID] = append(t.ends[job.ID], job.State)
}

var raced = newTally()

func init() {
	RegisterType(Type{
		Name: "test.raced",
		Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
			raced.ran(job.ID)
			return nil, nil
		},
		Ended: func(ctx context.Context, job *Job) { raced.ended(job) },
	})
	// test.chatty reports progress and writes a line, as many times as its
	// argument says.
	RegisterType(Type{Name: "test.chatty", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		var n int
		if err := json.Unmarshal(args, &n); err != nil {
			return nil, err
		}
		for i := 1; i <= n; i++ {
			r.Progress(int64(i), int64(n), "talking")
			r.Log(slog.LevelInfo, fmt.Sprintf("line %d", i))
			r.Progress(int64(i), int64(n), "still talking")
		}
		return n, nil
	}})
	// test.crowded writes its log from several goroutines at once.
	RegisterType(Type{Name: "test.crowded", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 50 {
					r.Log(slog.LevelInfo, fmt.Sprintf("writer %d line %d", g, i))
				}
			})
		}
		wg.Wait()
		return nil, nil
	}})
	// test.later fails, and is retried on its own an hour later.
	RegisterType(Type{Name: "test.later", Retries: 1, Backoff: time.Hour, Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		return nil, errors.New("not yet")
	}})
}

// A cancel racing the dispatcher either keeps a job from starting or stops
// it; the job never both ends as canceled and runs, and its type hears how
// it ended once.
func TestCancelRacesStart(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 4, Log: slog.New(slog.DiscardHandler)}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		const n = 400
		ids := make([]store.ID, 0, n)
		for range n {
			job, err := q.Enqueue(ctx, "test.raced", nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Cancel(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, job.ID)
		}
		for _, id := range ids {
			job := wait(t, q, id)
			raced.mu.Lock()
			runs, ends := raced.runs[id], slices.Clone(raced.ends[id])
			raced.mu.Unlock()
			switch {
			case len(ends) != 1:
				t.Errorf("%s ended %d times: %v", id, len(ends), ends)
			case ends[0] != job.State:
				t.Errorf("%s ended as %s and is %s", id, ends[0], job.State)
			case runs > 1:
				t.Errorf("%s ran %d times", id, runs)
			case job.State == StateCanceled && runs != 0 && job.StartedAt == nil:
				t.Errorf("%s was canceled before it started, but its handler ran", id)
			case job.State == StateSucceeded && runs != 1:
				t.Errorf("%s succeeded after %d runs", id, runs)
			}
		}
	})
}

// pausedStorage holds the dispatcher after it read the queue the first
// time, and a start of one job before storage says whether it may.
type pausedStorage struct {
	*memoryStorage
	job               store.ID
	read, resume      chan struct{}
	starting, proceed chan struct{}
	once              sync.Once
}

func (p *pausedStorage) runnable(ctx context.Context, now time.Time, limit int) ([]*Job, error) {
	jobs, err := p.memoryStorage.runnable(ctx, now, limit)
	if len(jobs) > 0 {
		p.once.Do(func() {
			close(p.read)
			<-p.resume
		})
	}
	return jobs, err
}

func (p *pausedStorage) transition(ctx context.Context, job *Job, from State) (bool, error) {
	if job.ID == p.job && from == StateQueued && job.State == StateRunning {
		close(p.starting)
		<-p.proceed
	}
	return p.memoryStorage.transition(ctx, job, from)
}

// A job canceled after the dispatcher read it, but before it started, never
// shows as running, and waiting for it returns as soon as it is canceled.
func TestStartAfterACancelStaysHidden(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &pausedStorage{memoryStorage: newMemoryStorage(), read: make(chan struct{}), resume: make(chan struct{}),
		starting: make(chan struct{}), proceed: make(chan struct{})}
	q := newManager(ctx, p, ManagerOptions{Workers: 1})
	job, err := q.Enqueue(ctx, "test.raced", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.job = job.ID
	<-p.read
	if err := q.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	close(p.resume)
	<-p.starting
	if got, _ := q.Get(ctx, job.ID); got.State != StateCanceled {
		t.Errorf("while the dispatcher tries to start it, the job is %s", got.State)
	}
	waited := make(chan *Job, 1)
	go func() {
		got, _ := q.Wait(ctx, job.ID)
		waited <- got
	}()
	close(p.proceed)
	select {
	case got := <-waited:
		if got == nil || got.State != StateCanceled {
			t.Errorf("waited for %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait didn't return")
	}
	raced.mu.Lock()
	defer raced.mu.Unlock()
	if raced.runs[job.ID] != 0 || len(raced.ends[job.ID]) != 1 {
		t.Errorf("runs = %d, ends = %v", raced.runs[job.ID], raced.ends[job.ID])
	}
}

// A wait for a job canceled while it was queued returns, even when the
// dispatcher read the job just before and tries to start it meanwhile.
func TestWaitAfterCancelReturns(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 4, Log: slog.New(slog.DiscardHandler)}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		for range 200 {
			job, err := q.Enqueue(ctx, "test.watched", true, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Cancel(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			if got := wait(t, q, job.ID); got.State != StateCanceled {
				t.Fatalf("canceled job = %+v", got)
			}
		}
	})
}

// Waiting leaves nothing behind, however the wait ends.
func TestWaitLeavesNothingBehind(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 2}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		var ids []store.ID
		for range 50 {
			job, _ := q.Enqueue(ctx, "test.count", 2, Options{})
			ids = append(ids, job.ID)
		}
		var wg sync.WaitGroup
		for _, id := range ids {
			// Several waits for one job, while it runs and after it ended.
			for range 3 {
				wg.Go(func() { wait(t, q, id) })
			}
		}
		wg.Wait()
		for _, id := range ids[:10] {
			wait(t, q, id)
		}
		if _, err := q.Wait(ctx, store.NewID("job")); err == nil {
			t.Error("waiting for a job that doesn't exist")
		}
		blocked, _ := q.Enqueue(ctx, "test.block", nil, Options{})
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if _, err := q.Wait(short, blocked.ID); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("a wait that ran out of time: %v", err)
		}
		updates, stop := q.Watch(ctx, blocked.ID)
		stop()
		if _, open := <-updates; open {
			t.Error("stopping a watch closes its channel")
		}
		q.Cancel(ctx, blocked.ID)
		wait(t, q, blocked.ID)
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.waiters) != 0 || len(q.watchers) != 0 || len(q.ending) != 0 || len(q.dropped) != 0 {
			t.Errorf("left behind: %d waiters, %d watchers, %d ending, %d dropped", len(q.waiters), len(q.watchers), len(q.ending), len(q.dropped))
		}
	})
}

// follow reads a job's updates the way a stream does, slowly, and returns
// the numbers of the log lines it got, from the updates or read back from
// the log when it fell behind.
func follow(t *testing.T, q *Manager, id store.ID, updates <-chan Update, pause time.Duration) (lines []int64, missed int) {
	t.Helper()
	ctx := context.Background()
	var after int64
	readBack := func() {
		rest, err := q.LogsAfter(ctx, id, after, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range rest {
			lines = append(lines, l.Seq)
			after = l.Seq
		}
	}
	readBack()
	for u := range updates {
		time.Sleep(pause)
		if u.Missed {
			missed++
			readBack()
		}
		if u.Log != nil && u.Log.Seq > after {
			if u.Log.Seq != after+1 {
				t.Errorf("line %d came after %d", u.Log.Seq, after)
			}
			lines = append(lines, u.Log.Seq)
			after = u.Log.Seq
		}
	}
	readBack()
	return lines, missed
}

// A watcher that falls behind is told so, and nothing of the log is lost to
// it: it reads back what it missed.
func TestWatchersThatFallBehind(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		const n = 300
		gate, _ := q.Enqueue(ctx, "test.block", nil, Options{})
		started(t, q, gate.ID)
		job, _ := q.Enqueue(ctx, "test.chatty", n, Options{})
		updates, stop := q.Watch(ctx, job.ID)
		defer stop()
		q.Cancel(ctx, gate.ID)
		lines, missed := follow(t, q, job.ID, updates, 200*time.Microsecond)
		if missed == 0 {
			t.Error("a slow watcher is told it fell behind")
		}
		if len(lines) != n {
			t.Fatalf("got %d lines of %d", len(lines), n)
		}
		for i, seq := range lines {
			if seq != int64(i+1) {
				t.Fatalf("line %d is %d", i+1, seq)
			}
		}
		if done := wait(t, q, job.ID); done.State != StateSucceeded || done.Progress.Done != n {
			t.Errorf("the job = %+v", done)
		}
	})
}

// Lines written from several goroutines are numbered in the order they are
// stored, without gaps, and watchers get them in that order.
func TestLogLinesStayInOrder(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		gate, _ := q.Enqueue(ctx, "test.block", nil, Options{})
		started(t, q, gate.ID)
		job, _ := q.Enqueue(ctx, "test.crowded", nil, Options{})
		updates, stop := q.Watch(ctx, job.ID)
		defer stop()
		q.Cancel(ctx, gate.ID)
		lines, _ := follow(t, q, job.ID, updates, 0)
		if len(lines) != 400 {
			t.Errorf("got %d lines", len(lines))
		}
		logs, _ := q.Logs(ctx, job.ID)
		for i, l := range logs {
			if l.Seq != int64(i+1) {
				t.Fatalf("stored line %d is numbered %d", i+1, l.Seq)
			}
		}
	})
}

// A failed job is retried once, however many ask at the same moment; a
// repeat of a request with its idempotency key gets the job it queued.
func TestRetriesAreTakenOnce(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 2}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		// test.flaky fails the first time it runs after this.
		attempts.Store(0)
		failed, _ := q.Enqueue(ctx, "test.flaky", nil, Options{})
		if got := wait(t, q, failed.ID); got.State != StateFailed {
			t.Fatalf("the first attempt = %+v", got)
		}
		alice := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI}
		var mu sync.Mutex
		queued := map[store.ID]int{}
		refused := 0
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				job, err := q.Retry(ctx, failed.ID, RetryOptions{Actor: alice})
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					queued[job.ID]++
				case errors.Is(err, ErrAlreadyRetried):
					refused++
				default:
					t.Errorf("retry: %v", err)
				}
			})
		}
		wg.Wait()
		if len(queued) != 1 || refused != 19 {
			t.Fatalf("queued %v, refused %d", queued, refused)
		}

		// With a key, the same request answers the same way.
		again, _ := q.Enqueue(ctx, "test.watched", true, Options{})
		started(t, q, again.ID)
		q.Cancel(ctx, again.ID)
		wait(t, q, again.ID)
		keyed := RetryOptions{Actor: alice, IdempotencyKey: "retry-once"}
		first, err := q.Retry(ctx, again.ID, keyed)
		if err != nil {
			t.Fatal(err)
		}
		if repeat, err := q.Retry(ctx, again.ID, keyed); err != nil || repeat.ID != first.ID {
			t.Errorf("a repeat with the key = %+v, %v", repeat, err)
		}
		bob := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI}
		if _, err := q.Retry(ctx, again.ID, RetryOptions{Actor: bob, IdempotencyKey: "retry-once"}); !errors.Is(err, ErrAlreadyRetried) {
			t.Errorf("someone else's request with the same key: %v", err)
		}
		q.Cancel(ctx, first.ID)
		wait(t, q, first.ID)

		// An automatic retry counts as the retry.
		later, _ := q.Enqueue(ctx, "test.later", nil, Options{})
		wait(t, q, later.ID)
		var auto *Job
		deadline := time.Now().Add(2 * time.Second)
		for auto == nil && time.Now().Before(deadline) {
			list, _, _ := q.List(ctx, Filter{Type: "test.later", State: StateQueued})
			if len(list) == 1 {
				auto = list[0]
			}
			time.Sleep(5 * time.Millisecond)
		}
		if auto == nil || auto.RetryOf != later.ID || auto.RunAfter == nil {
			t.Fatalf("the automatic retry = %+v", auto)
		}
		if _, err := q.Retry(ctx, later.ID, RetryOptions{Actor: alice}); !errors.Is(err, ErrAlreadyRetried) {
			t.Errorf("retrying a job already retried on its own: %v", err)
		}
		q.Cancel(ctx, auto.ID)
	})
}

// A job queued within a transaction of the panel database starts once the
// transaction commits, and never when it rolls back.
func TestJobsQueuedInATransaction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openTestDB(t)
	q, err := NewSQL(ctx, db, ManagerOptions{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Stop(context.Background()) })
	q.Start()
	var kept *Job
	err = db.Atomic(ctx, func(ctx context.Context) error {
		job, err := q.Enqueue(ctx, "test.count", 1, Options{})
		if err != nil {
			return err
		}
		kept = job
		time.Sleep(20 * time.Millisecond)
		if _, err := q.storage.get(context.Background(), job.ID); err == nil {
			t.Error("the job is visible before the commit")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, q, kept.ID); got.State != StateSucceeded {
		t.Errorf("the committed job = %+v", got)
	}
	boom := errors.New("boom")
	var dropped *Job
	err = db.Atomic(ctx, func(ctx context.Context) error {
		dropped, _ = q.Enqueue(ctx, "test.count", 1, Options{})
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := q.Get(ctx, dropped.ID); err == nil {
		t.Error("a job queued in a transaction that rolled back exists")
	}
}
