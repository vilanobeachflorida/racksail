package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

var attempts atomic.Int32

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
	RegisterType(Type{Name: "test.block", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	RegisterType(Type{Name: "test.panic", Run: func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error) {
		panic("boom")
	}})
}

func TestRegistry(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRunToCompletion(t *testing.T) {
	ctx := context.Background()
	q := NewMemory(ctx, MemoryOptions{Workers: 2})
	job, err := q.Enqueue(ctx, "test.count", 3, Options{Title: "Count to three"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID.Prefix() != "job" || job.State != StateQueued {
		t.Errorf("queued job = %+v", job)
	}
	done, err := q.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != StateSucceeded || done.Progress.Done != 3 || string(done.Result) != `{"counted":3}` || done.FinishedAt == nil {
		t.Errorf("finished job = %+v", done)
	}
	logs, _ := q.Logs(ctx, job.ID)
	if len(logs) != 3 || logs[2].Attrs["i"] != 3 {
		t.Errorf("logs = %+v", logs)
	}
	if _, err := q.Enqueue(ctx, "test.missing", nil, Options{}); !errors.Is(err, ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
}

func TestFailureAndRetry(t *testing.T) {
	ctx := context.Background()
	attempts.Store(0)
	q := NewMemory(ctx, MemoryOptions{Workers: 1})
	job, _ := q.Enqueue(ctx, "test.flaky", nil, Options{})
	failed, _ := q.Wait(ctx, job.ID)
	if failed.State != StateFailed || failed.Error == nil || failed.Error.Code != i18n.ErrUnavailable.Code {
		t.Fatalf("failed job = %+v", failed)
	}
	retry, err := q.Retry(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := q.Wait(ctx, retry.ID)
	if ok.State != StateSucceeded || ok.Attempt != 2 {
		t.Errorf("retried job = %+v", ok)
	}
	if _, err := q.Retry(ctx, retry.ID); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("retrying a succeeded job: %v", err)
	}
}

func TestCancelAndWatch(t *testing.T) {
	ctx := context.Background()
	q := NewMemory(ctx, MemoryOptions{Workers: 1})
	job, _ := q.Enqueue(ctx, "test.block", nil, Options{})
	updates, stop := q.Watch(job.ID)
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
	if err := q.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	canceled, _ := q.Wait(ctx, job.ID)
	if canceled.State != StateCanceled {
		t.Errorf("canceled job = %+v", canceled)
	}
	for range updates {
	}
	list, _ := q.List(ctx, Filter{State: StateCanceled})
	if len(list) != 1 {
		t.Errorf("canceled jobs = %d", len(list))
	}
}

func TestPanickingHandler(t *testing.T) {
	ctx := context.Background()
	q := NewMemory(ctx, MemoryOptions{Workers: 1, Log: slog.New(slog.DiscardHandler)})
	job, _ := q.Enqueue(ctx, "test.panic", nil, Options{})
	done, _ := q.Wait(ctx, job.ID)
	if done.State != StateFailed || done.Error.Code != i18n.ErrInternal.Code {
		t.Errorf("a panic should fail the job: %+v", done)
	}
}

func TestRetention(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	q := NewMemory(ctx, MemoryOptions{Workers: 2, Retention: Retention{Finished: time.Hour, MaxFinished: 2}, Now: now})
	run := func() *Job {
		job, err := q.Enqueue(ctx, "test.count", 1, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Wait(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		return job
	}
	first, second, third := run(), run(), run()
	// Queuing the third job kept the two finished before it; the next one
	// drops the oldest of the three, with its logs.
	run()
	if _, err := q.Get(ctx, first.ID); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("finished jobs over the cap are dropped: %v", err)
	}
	if _, err := q.Logs(ctx, first.ID); !errors.Is(err, i18n.ErrNotFound) {
		t.Errorf("their logs go with them: %v", err)
	}
	for _, j := range []*Job{second, third} {
		if _, err := q.Get(ctx, j.ID); err != nil {
			t.Errorf("%s is within the cap: %v", j.ID, err)
		}
	}

	blocked, _ := q.Enqueue(ctx, "test.block", nil, Options{})
	mu.Lock()
	clock = clock.Add(2 * time.Hour)
	mu.Unlock()
	last := run()
	list, _ := q.List(ctx, Filter{})
	if len(list) != 2 || list[0].ID != last.ID || list[1].ID != blocked.ID {
		t.Errorf("old finished jobs are dropped and running ones kept: %+v", list)
	}
	if err := q.Cancel(ctx, blocked.ID); err != nil {
		t.Fatal(err)
	}
	if done, err := q.Wait(ctx, blocked.ID); err != nil || done.State != StateCanceled {
		t.Errorf("canceled = %+v, %v", done, err)
	}
}
