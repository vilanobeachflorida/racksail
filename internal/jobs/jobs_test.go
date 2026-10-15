package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var attempts atomic.Int32

// gate holds test.gated jobs until it is closed.
var gate = make(chan struct{})

func init() {
	RegisterType(Type{Name: "test.count", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		var n int
		if err := json.Unmarshal(args, &n); err != nil {
			return nil, err
		}
		for i := 1; i <= n; i++ {
			r.Progress(int64(i), int64(n), "counting")
			r.Log(slog.LevelInfo, "counted", "i", i)
		}
		return map[string]int{"counted": n}, nil
	}})
	RegisterType(Type{Name: "test.flaky", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		if attempts.Add(1) == 1 {
			return nil, i18n.ErrUnavailable.New("what", "The mirror")
		}
		return "ok", nil
	}})
	RegisterType(Type{Name: "test.stubborn", Retries: 2, Backoff: 20 * time.Millisecond, Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		if job.Attempt < 3 {
			return nil, i18n.ErrUnavailable.New("what", "The mirror")
		}
		return job.Attempt, nil
	}})
	RegisterType(Type{Name: "test.block", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	RegisterType(Type{Name: "test.slow", Timeout: 20 * time.Millisecond, Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	RegisterType(Type{Name: "test.panic", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		panic("boom")
	}})
	RegisterType(Type{Name: "test.resumable", Resume: true, Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		r.Log(slog.LevelInfo, "working")
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return "resumed", nil
	}})
	RegisterType(Type{Name: "test.fragile", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	RegisterType(Type{
		Name: "test.watched", Timeout: time.Second,
		Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
			var wait bool
			if err := json.Unmarshal(args, &wait); err != nil {
				return nil, err
			}
			if !wait {
				return "done", nil
			}
			r.Log(slog.LevelInfo, "waiting")
			<-ctx.Done()
			why := Stopped(ctx)
			ended.stopped(job.ID, why)
			return nil, why
		},
		Ended: func(ctx context.Context, job *Job) { ended.add(job) },
		Retryable: func(ctx context.Context, job *Job) error {
			if job.Title == "final" {
				return i18n.ErrConflict.New("what", job.Title)
			}
			return nil
		},
	})
}

// ended records what test.watched jobs said when they stopped and how they
// ended.
var ended = &endings{why: map[store.ID]error{}, jobs: map[store.ID]*Job{}}

type endings struct {
	mu   sync.Mutex
	why  map[store.ID]error
	jobs map[store.ID]*Job
}

func (e *endings) stopped(id store.ID, why error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.why[id] = why
}

func (e *endings) add(job *Job) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jobs[job.ID] = job
}

// get returns how a job ended and why its handler said it stopped.
func (e *endings) get(id store.ID) (*Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.jobs[id], e.why[id]
}

// forEachQueue runs a test against a queue kept in memory and one kept in
// the panel database.
func forEachQueue(t *testing.T, opts ManagerOptions, test func(t *testing.T, q *Manager)) {
	t.Run("memory", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		test(t, NewMemory(ctx, opts))
	})
	t.Run("sqlite", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		q, err := NewSQL(ctx, openTestDB(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		// The queue stops before its database closes.
		t.Cleanup(func() { q.Stop(context.Background()) })
		test(t, q)
	})
}

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.OpenDB(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func wait(t *testing.T, q Queue, id store.ID) *Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := q.Wait(ctx, id)
	if err != nil {
		t.Fatalf("waiting for %s: %v", id, err)
	}
	return job
}

func TestRegistry(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRunToCompletion(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 2}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		acct := store.AccountScope(store.NewID("acct"))
		actor := &auth.Actor{Type: auth.ActorUser, ID: store.NewID("user"), Source: auth.SourceUI}
		job, err := q.Enqueue(ctx, "test.count", 3, Options{Title: "Count to three", Actor: actor, Scope: acct})
		if err != nil {
			t.Fatal(err)
		}
		if job.ID.Prefix() != "job" || job.State != StateQueued || job.Scope != acct {
			t.Errorf("queued job = %+v", job)
		}
		done := wait(t, q, job.ID)
		if done.State != StateSucceeded || done.Progress.Done != 3 || string(done.Result) != `{"counted":3}` || done.FinishedAt == nil || done.StartedAt == nil {
			t.Errorf("finished job = %+v", done)
		}
		if done.Actor == nil || done.Actor.ID != actor.ID {
			t.Errorf("the actor is kept: %+v", done.Actor)
		}
		logs, _ := q.Logs(ctx, job.ID)
		if len(logs) != 3 || fmt.Sprint(logs[2].Attrs["i"]) != "3" || logs[2].Seq != 3 || logs[0].Level != "INFO" {
			t.Errorf("logs = %+v", logs)
		}
		if rest, _ := q.LogsAfter(ctx, job.ID, 1, 1); len(rest) != 1 || rest[0].Seq != 2 {
			t.Errorf("logs after the first = %+v", rest)
		}
		if _, err := q.Enqueue(ctx, "test.missing", nil, Options{}); !errors.Is(err, ErrUnknownType) {
			t.Errorf("unknown type: %v", err)
		}
		if _, err := q.Get(ctx, store.NewID("job")); !errors.Is(err, i18n.ErrNotFound) {
			t.Errorf("a missing job: %v", err)
		}
		if _, err := q.Logs(ctx, store.NewID("job")); !errors.Is(err, i18n.ErrNotFound) {
			t.Errorf("the logs of a missing job: %v", err)
		}
	})
}

func TestFailureAndRetry(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		attempts.Store(0)
		job, _ := q.Enqueue(ctx, "test.flaky", nil, Options{})
		failed := wait(t, q, job.ID)
		if failed.State != StateFailed || failed.Error == nil || failed.Error.Code != i18n.ErrUnavailable.Code {
			t.Fatalf("failed job = %+v", failed)
		}
		retry, err := q.Retry(ctx, job.ID, RetryOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ok := wait(t, q, retry.ID)
		if ok.State != StateSucceeded || ok.Attempt != 2 || ok.RetryOf != job.ID {
			t.Errorf("retried job = %+v", ok)
		}
		if _, err := q.Retry(ctx, retry.ID, RetryOptions{}); !errors.Is(err, ErrNotRetryable) {
			t.Errorf("retrying a succeeded job: %v", err)
		}
	})
}

func TestAutomaticRetries(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		job, err := q.Enqueue(ctx, "test.stubborn", nil, Options{Title: "Download"})
		if err != nil {
			t.Fatal(err)
		}
		if first := wait(t, q, job.ID); first.State != StateFailed {
			t.Fatalf("first attempt = %+v", first)
		}
		// Each retry is a job of its own, linked to the one before.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			list, _, err := q.List(ctx, Filter{Type: "test.stubborn"})
			if err != nil {
				t.Fatal(err)
			}
			if len(list) == 3 && list[0].State == StateSucceeded {
				if list[0].Attempt != 3 || list[0].RetryOf != list[1].ID || list[1].RetryOf != job.ID || list[1].RunAfter == nil {
					t.Errorf("attempts = %+v %+v", list[0], list[1])
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		list, _, _ := q.List(ctx, Filter{Type: "test.stubborn"})
		t.Fatalf("the retries didn't finish: %+v", list)
	})
}

func TestCancelAndWatch(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		job, _ := q.Enqueue(ctx, "test.block", nil, Options{})
		updates, stop := q.Watch(ctx, job.ID)
		defer stop()
		deadline := time.After(2 * time.Second)
		for running := false; !running; {
			select {
			case u := <-updates:
				running = u.Job != nil && u.Job.State == StateRunning
			case <-deadline:
				t.Fatal("the job never started")
			}
		}
		// The only worker is busy, so this one waits, and is canceled
		// before it ever runs.
		queued, _ := q.Enqueue(ctx, "test.count", 1, Options{})
		if err := q.Cancel(ctx, queued.ID); err != nil {
			t.Fatal(err)
		}
		if got := wait(t, q, queued.ID); got.State != StateCanceled || got.StartedAt != nil {
			t.Errorf("canceled while queued = %+v", got)
		}
		if err := q.Cancel(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		canceled := wait(t, q, job.ID)
		if canceled.State != StateCanceled || canceled.Error.Code != i18n.ErrCanceled.Code {
			t.Errorf("canceled job = %+v", canceled)
		}
		for range updates {
		}
		list, _, _ := q.List(ctx, Filter{State: StateCanceled})
		if len(list) != 2 {
			t.Errorf("canceled jobs = %d", len(list))
		}
		if err := q.Cancel(ctx, job.ID); err != nil {
			t.Errorf("canceling a finished job changes nothing: %v", err)
		}
		closed, _ := q.Watch(ctx, job.ID)
		if _, open := <-closed; open {
			t.Error("watching a finished job ends at once")
		}
	})
}

// started waits until a job is running.
func started(t *testing.T, q *Manager, id store.ID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		j, _ := q.Get(context.Background(), id)
		if j != nil && j.State == StateRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never started", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A handler learns why its context ended, and the job's type learns how
// every job ended, even one whose handler never ran.
func TestHowJobsEnd(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		done, _ := q.Enqueue(ctx, "test.watched", false, Options{})
		wait(t, q, done.ID)
		if job, _ := ended.get(done.ID); job == nil || job.State != StateSucceeded {
			t.Errorf("a job that succeeded: %+v", job)
		}

		running, _ := q.Enqueue(ctx, "test.watched", true, Options{})
		started(t, q, running.ID)
		// The only worker is busy, so this one is canceled before it runs.
		queued, _ := q.Enqueue(ctx, "test.watched", true, Options{})
		if err := q.Cancel(ctx, queued.ID); err != nil {
			t.Fatal(err)
		}
		if job, why := ended.get(queued.ID); why != nil || job == nil || job.State != StateCanceled || job.Error.Code != i18n.ErrCanceled.Code {
			t.Errorf("a job canceled while it waited: %v, %+v", why, job)
		}
		if err := q.Cancel(ctx, running.ID); err != nil {
			t.Fatal(err)
		}
		wait(t, q, running.ID)
		if job, why := ended.get(running.ID); !errors.Is(why, i18n.ErrCanceled) || job == nil || job.State != StateCanceled {
			t.Errorf("a job canceled while it ran: %v, %+v", why, job)
		}

		slow, _ := q.Enqueue(ctx, "test.watched", true, Options{Title: "The import"})
		got := wait(t, q, slow.ID)
		if job, why := ended.get(slow.ID); !errors.Is(why, i18n.ErrTimeout) || !errors.Is(why, context.DeadlineExceeded) || job == nil || got.Error.Code != i18n.ErrTimeout.Code {
			t.Errorf("a job out of time: %v, %+v", why, got)
		}

		// Retrying asks the type first.
		final, _ := q.Enqueue(ctx, "test.watched", true, Options{Title: "final"})
		started(t, q, final.ID)
		q.Cancel(ctx, final.ID)
		wait(t, q, final.ID)
		if _, err := q.Retry(ctx, final.ID, RetryOptions{}); !errors.Is(err, i18n.ErrConflict) {
			t.Errorf("a retry the type refuses: %v", err)
		}
		if again, err := q.Retry(ctx, running.ID, RetryOptions{}); err != nil || again.RetryOf != running.ID {
			t.Errorf("a retry the type allows: %+v, %v", again, err)
		} else {
			started(t, q, again.ID)
			q.Cancel(ctx, again.ID)
			wait(t, q, again.ID)
		}
	})
}

// Stopping the queue stops its jobs and waits for them, so the database
// can be closed after it. They're left for the next start to pick up.
func TestStop(t *testing.T) {
	db := openTestDB(t)
	q, err := NewSQL(context.Background(), db, ManagerOptions{Workers: 2, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Check(ctx); err == nil {
		t.Error("a queue that hasn't started isn't healthy")
	}
	job, _ := q.Enqueue(ctx, "test.watched", true, Options{})
	started(t, q, job.ID)
	if err := q.Check(ctx); err != nil {
		t.Errorf("a running queue is healthy: %v", err)
	}
	stats, err := q.Stats(ctx)
	if err != nil || stats.Running != 1 || stats.Started < 1 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := q.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	// The handler has returned by now, so it said why it stopped.
	if _, why := ended.get(job.ID); !errors.Is(why, ErrInterrupted) {
		t.Errorf("a job the panel stopped under learns it was interrupted: %v", why)
	}
	if got, _ := q.Get(ctx, job.ID); got.State != StateRunning {
		t.Errorf("it is left for the next start: %+v", got)
	}
	if err := q.Check(ctx); err == nil {
		t.Error("a stopped queue isn't healthy")
	}
	waiting, _ := q.Enqueue(ctx, "test.count", 1, Options{})
	time.Sleep(20 * time.Millisecond)
	if got, _ := q.Get(ctx, waiting.ID); got.State != StateQueued {
		t.Errorf("a stopped queue runs nothing: %+v", got)
	}

	// The next start reports it, and its type hears about that.
	q2, err := NewSQL(ctx, db, ManagerOptions{Workers: 2, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q2.Stop(context.Background()) })
	q2.Start()
	if got, _ := ended.get(job.ID); got == nil || got.State != StateFailed || got.Error.Code != ErrInterrupted.Code {
		t.Errorf("an interrupted job's type learns how it ended: %+v", got)
	}
	wait(t, q2, waiting.ID)
}

func TestStats(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		blocking, _ := q.Enqueue(ctx, "test.watched", true, Options{})
		started(t, q, blocking.ID)
		q.Enqueue(ctx, "test.count", 1, Options{})
		q.Enqueue(ctx, "test.count", 1, Options{RunAfter: time.Now().Add(time.Hour)})
		time.Sleep(20 * time.Millisecond)
		st, err := q.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.Running != 1 || st.Queued != 1 || st.Waiting != 1 || st.OldestQueued <= 0 {
			t.Errorf("stats = %+v", st)
		}
		q.Cancel(ctx, blocking.ID)
		wait(t, q, blocking.ID)
		if st, _ := q.Stats(ctx); st.Canceled != 1 {
			t.Errorf("after a cancel = %+v", st)
		}
	})
}

func TestTimeouts(t *testing.T) {
	forEachQueue(t, ManagerOptions{}, func(t *testing.T, q *Manager) {
		job, _ := q.Enqueue(context.Background(), "test.slow", nil, Options{Title: "The import"})
		got := wait(t, q, job.ID)
		if got.State != StateFailed || got.Error.Code != i18n.ErrTimeout.Code {
			t.Errorf("a job that runs out of time = %+v", got)
		}
	})
}

func TestPanickingHandler(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 1, Log: slog.New(slog.DiscardHandler)}, func(t *testing.T, q *Manager) {
		job, _ := q.Enqueue(context.Background(), "test.panic", nil, Options{})
		done := wait(t, q, job.ID)
		if done.State != StateFailed || done.Error.Code != i18n.ErrInternal.Code {
			t.Errorf("a panic should fail the job: %+v", done)
		}
	})
}

func TestRunAfter(t *testing.T) {
	forEachQueue(t, ManagerOptions{}, func(t *testing.T, q *Manager) {
		start := time.Now()
		job, _ := q.Enqueue(context.Background(), "test.count", 1, Options{RunAfter: start.Add(100 * time.Millisecond)})
		done := wait(t, q, job.ID)
		if done.State != StateSucceeded || done.StartedAt.Before(start.Add(100*time.Millisecond)) {
			t.Errorf("a delayed job = %+v", done)
		}
	})
}

func TestListFilters(t *testing.T) {
	forEachQueue(t, ManagerOptions{Workers: 4}, func(t *testing.T, q *Manager) {
		ctx := context.Background()
		acct := store.AccountScope(store.NewID("acct"))
		user := store.NewID("user")
		cs, site := store.NewID("chg"), store.NewID("site")
		a, _ := q.Enqueue(ctx, "test.count", 1, Options{Scope: acct, ChangeSet: cs, Actor: &auth.Actor{Type: auth.ActorToken, ID: store.NewID("tok"), User: user}})
		b, _ := q.Enqueue(ctx, "test.count", 1, Options{Resource: site})
		c, _ := q.Enqueue(ctx, "test.flaky", nil, Options{})
		for _, j := range []*Job{a, b, c} {
			wait(t, q, j.ID)
		}
		ids := func(f Filter) []store.ID {
			t.Helper()
			list, _, err := q.List(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			var out []store.ID
			for _, j := range list {
				out = append(out, j.ID)
			}
			return out
		}
		same := func(got []store.ID, want ...store.ID) bool { return fmt.Sprint(got) == fmt.Sprint(want) }
		if got := ids(Filter{}); !same(got, c.ID, b.ID, a.ID) {
			t.Errorf("newest first = %v", got)
		}
		if got := ids(Filter{Type: "test.count"}); !same(got, b.ID, a.ID) {
			t.Errorf("by type = %v", got)
		}
		if got := ids(Filter{ChangeSet: cs}); !same(got, a.ID) {
			t.Errorf("by change set = %v", got)
		}
		if got := ids(Filter{Resource: site}); !same(got, b.ID) {
			t.Errorf("by resource = %v", got)
		}
		if got := ids(Filter{Scopes: []store.Scope{acct}}); !same(got, a.ID) {
			t.Errorf("by scope = %v", got)
		}
		if got := ids(Filter{Actor: user}); !same(got, a.ID) {
			t.Errorf("by the person behind the token = %v", got)
		}
		// Someone sees the jobs of the owners they may read and their own,
		// and pages hold only those.
		d, _ := q.Enqueue(ctx, "test.count", 1, Options{Actor: &auth.Actor{Type: auth.ActorUser, ID: user}})
		wait(t, q, d.ID)
		if got := ids(Filter{Visible: &Visibility{Person: user}}); !same(got, d.ID, a.ID) {
			t.Errorf("their own jobs = %v", got)
		}
		if got := ids(Filter{Visible: &Visibility{Scopes: []store.Scope{store.ServerScope()}}, Limit: 2}); !same(got, d.ID, c.ID) {
			t.Errorf("the jobs of an owner they may read = %v", got)
		}
		if got := ids(Filter{Visible: &Visibility{Scopes: []store.Scope{acct}, Person: store.NewID("user")}}); !same(got, a.ID) {
			t.Errorf("someone else = %v", got)
		}
		if got := ids(Filter{Visible: &Visibility{}}); len(got) != 0 {
			t.Errorf("nobody = %v", got)
		}
		first, next, _ := q.List(ctx, Filter{Limit: 3})
		rest, last, _ := q.List(ctx, Filter{Limit: 3, Cursor: next})
		if len(first) != 3 || len(rest) != 1 || last != "" || rest[0].ID != a.ID {
			t.Errorf("pages = %v / %v", first, rest)
		}
		if _, _, err := q.List(ctx, Filter{Cursor: "?"}); !errors.Is(err, i18n.ErrBadRequest) {
			t.Errorf("a bad cursor: %v", err)
		}
	})
}

func TestRetention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	for name, q := range map[string]*Manager{
		"memory": NewMemory(ctx, ManagerOptions{Workers: 2, Retention: Retention{Finished: time.Hour, MaxFinished: 2}, Now: now}),
		"sqlite": func() *Manager {
			q, err := NewSQL(ctx, openTestDB(t), ManagerOptions{Workers: 2, Retention: Retention{Finished: time.Hour, MaxFinished: 2}, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			return q
		}(),
	} {
		mu.Lock()
		clock = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
		mu.Unlock()
		run := func() *Job {
			job, err := q.Enqueue(ctx, "test.count", 1, Options{})
			if err != nil {
				t.Fatal(err)
			}
			wait(t, q, job.ID)
			return job
		}
		first, second, third := run(), run(), run()
		// Queuing the third job kept the two finished before it; the next
		// one drops the oldest of the three, with its logs.
		run()
		if _, err := q.Get(ctx, first.ID); !errors.Is(err, i18n.ErrNotFound) {
			t.Errorf("%s: finished jobs over the cap are dropped: %v", name, err)
		}
		if _, err := q.Logs(ctx, first.ID); !errors.Is(err, i18n.ErrNotFound) {
			t.Errorf("%s: their logs go with them: %v", name, err)
		}
		for _, j := range []*Job{second, third} {
			if _, err := q.Get(ctx, j.ID); err != nil {
				t.Errorf("%s: %s is within the cap: %v", name, j.ID, err)
			}
		}

		blocked, _ := q.Enqueue(ctx, "test.block", nil, Options{})
		mu.Lock()
		clock = clock.Add(2 * time.Hour)
		mu.Unlock()
		last := run()
		list, _, _ := q.List(ctx, Filter{})
		if len(list) != 2 || list[0].ID != last.ID || list[1].ID != blocked.ID {
			t.Errorf("%s: old finished jobs are dropped and running ones kept: %+v", name, list)
		}
		if err := q.Cancel(ctx, blocked.ID); err != nil {
			t.Fatal(err)
		}
		if done := wait(t, q, blocked.ID); done.State != StateCanceled {
			t.Errorf("%s: canceled = %+v", name, done)
		}
	}
}

// A queue kept in the database carries on after the panel restarts: what
// waited runs, what may run again does, and the rest is reported.
func TestRestart(t *testing.T) {
	gate = make(chan struct{})
	db := openTestDB(t)
	ctx, stop := context.WithCancel(context.Background())
	q, err := NewSQL(ctx, db, ManagerOptions{Workers: 2, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	resumable, _ := q.Enqueue(ctx, "test.resumable", nil, Options{})
	fragile, _ := q.Enqueue(ctx, "test.fragile", nil, Options{})
	waiting, _ := q.Enqueue(ctx, "test.count", 2, Options{RunAfter: time.Now().Add(time.Hour)})
	for _, id := range []store.ID{resumable.ID, fragile.ID} {
		deadline := time.Now().Add(2 * time.Second)
		for {
			j, _ := q.Get(ctx, id)
			if j.State == StateRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never started", id)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// The panel stops while two jobs run and one waits.
	stop()
	if err := q.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel := context.WithCancel(context.Background())
	defer cancel()
	q2, err := NewSQL(ctx2, db, ManagerOptions{Workers: 2, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q2.Stop(context.Background()) })
	q2.Start()
	close(gate)
	if got := wait(t, q2, resumable.ID); got.State != StateSucceeded || string(got.Result) != `"resumed"` {
		t.Errorf("a job that may run again does: %+v", got)
	}
	logs, _ := q2.Logs(ctx2, resumable.ID)
	if len(logs) != 3 || logs[1].Level != "WARN" || logs[2].Seq != 3 {
		t.Errorf("its log says what happened: %+v", logs)
	}
	if got := wait(t, q2, fragile.ID); got.State != StateFailed || got.Error.Code != ErrInterrupted.Code {
		t.Errorf("a job that may not run again is reported: %+v", got)
	}
	if got, _ := q2.Get(ctx2, waiting.ID); got.State != StateQueued || got.RunAfter == nil {
		t.Errorf("a waiting job keeps waiting: %+v", got)
	}
}
