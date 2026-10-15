package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	q := NewMemory(ctx, 2, nil)
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
	q := NewMemory(ctx, 1, nil)
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
	q := NewMemory(ctx, 1, nil)
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
	q := NewMemory(ctx, 1, slog.New(slog.DiscardHandler))
	job, _ := q.Enqueue(ctx, "test.panic", nil, Options{})
	done, _ := q.Wait(ctx, job.ID)
	if done.State != StateFailed || done.Error.Code != i18n.ErrInternal.Code {
		t.Errorf("a panic should fail the job: %+v", done)
	}
}
