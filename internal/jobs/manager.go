package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Manager is the Queue. It keeps jobs, their progress and their logs in a
// storage, so a queue kept in the panel database carries on after a restart:
// jobs that were waiting run, and jobs that were running are run again when
// their type says that is safe, or reported as interrupted.
type Manager struct {
	storage   storage
	log       *slog.Logger
	now       func() time.Time
	base      context.Context
	stop      context.CancelFunc
	workers   int
	retention Retention
	wake      chan struct{}
	startOnce sync.Once
	// work counts the dispatcher and the jobs running here, which Stop
	// waits for.
	work sync.WaitGroup
	// retrying takes retries one at a time, so the work of a job is queued
	// again once however many ask for it at the same moment.
	retrying sync.Mutex

	mu       sync.Mutex
	started  bool
	handlers map[string]Type
	running  map[store.ID]*running
	// dropped counts the cancels of queued jobs under way; the dispatcher
	// doesn't start a job while one is.
	dropped map[store.ID]int
	// ending counts the ends being recorded, which Wait waits for, so a
	// job's type has heard how it ended before Wait returns it.
	ending   map[store.ID]int
	waiters  map[store.ID]*waiter
	watchers map[store.ID]map[chan Update]bool
	counts   Stats
}

// running is a job this process is running.
type running struct {
	job *Job
	// cancel ends the job's context with the reason, which the handler
	// learns from Stopped.
	cancel context.CancelCauseFunc
	// canceled is set when someone canceled the job, as opposed to the
	// panel stopping under it.
	canceled bool
	// started is set once storage says the job runs. Until then Get and
	// List read the job from storage, since the start may still fail.
	started bool
	// logMu takes the job's log lines one at a time: each is numbered,
	// stored and sent before the next.
	logMu  sync.Mutex
	logSeq int64
	saved  time.Time
}

// waiter is what the calls to Wait for one job wait on.
type waiter struct {
	done chan struct{}
	// n counts the calls waiting on it; the last one to leave removes it.
	n int
}

// watchBuffer is how many updates a watcher holds before it counts as
// fallen behind.
const watchBuffer = 64

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	// Workers is how many jobs run at once; the default is four.
	Workers   int
	Retention Retention
	Log       *slog.Logger
	Now       func() time.Time
}

// MemoryOptions configure a queue kept in memory.
type MemoryOptions = ManagerOptions

func newManager(ctx context.Context, s storage, opts ManagerOptions) *Manager {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	base, stop := context.WithCancel(ctx)
	return &Manager{
		storage: s, log: opts.Log, now: opts.Now, base: base, stop: stop, workers: opts.Workers, retention: opts.Retention.withDefaults(),
		wake: make(chan struct{}, 1), handlers: map[string]Type{}, running: map[store.ID]*running{}, dropped: map[store.ID]int{},
		ending: map[store.ID]int{}, waiters: map[store.ID]*waiter{}, watchers: map[store.ID]map[chan Update]bool{},
	}
}

// NewMemory returns a queue kept in memory, for tests and tools. Jobs stop
// when ctx is done, or when Stop is called.
func NewMemory(ctx context.Context, opts ManagerOptions) *Manager {
	return newManager(ctx, newMemoryStorage(), opts)
}

// Stop stops the queue: no more jobs start, and the running ones see their
// context end with jobs.interrupted, after which they're left for the next
// start to pick up. It waits for them and the dispatcher to return, or for
// ctx to be done, so whoever owns the database can close it afterwards.
func (m *Manager) Stop(ctx context.Context) error {
	m.stop()
	done := make(chan struct{})
	go func() {
		m.work.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jobs: waiting for the running jobs to stop: %w", ctx.Err())
	}
}

// Stats describes the queue for the panel's metrics.
func (m *Manager) Stats(ctx context.Context) (Stats, error) {
	now := m.now()
	ready, waiting, oldest, err := m.storage.backlog(ctx, now)
	if err != nil {
		return Stats{}, err
	}
	m.mu.Lock()
	st := m.counts
	for _, r := range m.running {
		if r.started {
			st.Running++
		}
	}
	m.mu.Unlock()
	st.Queued, st.Waiting = ready, waiting
	if !oldest.IsZero() && now.After(oldest) {
		st.OldestQueued = now.Sub(oldest)
	}
	return st, nil
}

// Check is the queue's health check: it fails when the queue isn't running
// or its storage can't be read.
func (m *Manager) Check(ctx context.Context) error {
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started || m.base.Err() != nil {
		return i18n.ErrUnavailable.New("what", "The job queue")
	}
	_, _, _, err := m.storage.backlog(ctx, m.now())
	return err
}

var _ Queue = (*Manager)(nil)

// Handle implements Queue.
func (m *Manager) Handle(t Type) error {
	if err := t.check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.handlers[t.Name]; ok {
		return fmt.Errorf("jobs: type %s is handled twice", t.Name)
	}
	m.handlers[t.Name] = t
	return nil
}

// typeFor finds a job type, preferring the queue's own handlers.
func (m *Manager) typeFor(name string) (Type, bool) {
	m.mu.Lock()
	t, ok := m.handlers[name]
	m.mu.Unlock()
	if ok {
		return t, true
	}
	return lookupType(name)
}

// Start picks up the jobs a previous run of the panel left behind and starts
// running jobs. Call it once every component has registered its job types;
// queuing a job starts the queue too.
func (m *Manager) Start() {
	m.startOnce.Do(func() {
		if m.base.Err() != nil {
			return
		}
		if err := m.recover(); err != nil {
			m.log.Error("picking up the jobs left from before the panel restarted", "error", err)
		}
		m.mu.Lock()
		m.started = true
		m.mu.Unlock()
		m.work.Add(1)
		go func() {
			defer m.work.Done()
			m.dispatch()
		}()
	})
}

// recover deals with jobs a previous run left running.
func (m *Manager) recover() error {
	ctx := context.WithoutCancel(m.base)
	stale, err := m.storage.interrupted(ctx)
	if err != nil {
		return err
	}
	for _, job := range stale {
		typ, ok := m.typeFor(job.Type)
		if ok && typ.Resume {
			job.State, job.StartedAt = StateQueued, nil
			if _, err := m.storage.transition(ctx, job, StateRunning); err != nil {
				return err
			}
			m.appendLog(ctx, job.ID, slog.LevelWarn, "The panel restarted while this job ran, so it runs again from the start.", nil)
			continue
		}
		now := m.now().UTC()
		f := ErrInterrupted.New()
		job.State, job.FinishedAt, job.Error = StateFailed, &now, &Failure{Code: f.Code, Message: f.Message}
		m.end(job, StateRunning)
	}
	return nil
}

// ended tells the job's type how the job ended, once that is recorded.
func (m *Manager) ended(job *Job) {
	typ, ok := m.typeFor(job.Type)
	if !ok || typ.Ended == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("a job type's end handler panicked", "job", job.ID, "type", job.Type, "panic", p)
		}
	}()
	typ.Ended(context.WithoutCancel(m.base), job.clone())
}

// appendLog adds a line to the log of a job that isn't running here.
func (m *Manager) appendLog(ctx context.Context, id store.ID, level slog.Level, message string, attrs map[string]any) {
	seq, err := m.storage.lastLogSeq(ctx, id)
	if err == nil {
		err = m.storage.appendLog(ctx, id, &LogLine{Seq: seq + 1, Time: m.now().UTC(), Level: level.String(), Message: message, Attrs: attrs})
	}
	if err != nil {
		m.log.Warn("writing to a job's log", "job", id, "error", err)
	}
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// dispatch starts queued jobs as workers free up and their time comes.
func (m *Manager) dispatch() {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		wait := m.fill()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-m.base.Done():
			return
		case <-m.wake:
		case <-timer.C:
		}
	}
}

// fill starts runnable jobs while workers are free, and returns how long to
// wait before looking again when nothing else wakes the dispatcher.
func (m *Manager) fill() time.Duration {
	ctx := context.WithoutCancel(m.base)
	m.mu.Lock()
	free := m.workers - len(m.running)
	m.mu.Unlock()
	now := m.now().UTC()
	if free > 0 {
		ready, err := m.storage.runnable(ctx, now, free)
		if err != nil {
			m.log.Error("reading the job queue", "error", err)
			return 5 * time.Second
		}
		for _, job := range ready {
			m.start(job)
		}
	}
	next, ok, err := m.storage.nextRunAfter(ctx, now)
	if err != nil || !ok {
		return time.Minute
	}
	return max(next.Sub(now), 10*time.Millisecond)
}

// start runs a queued job in the background. The dispatcher read the job a
// moment ago; it only starts if it is still queued then.
func (m *Manager) start(job *Job) {
	ctx := context.WithoutCancel(m.base)
	typ, ok := m.typeFor(job.Type)
	if !ok {
		now := m.now().UTC()
		f := ErrUnknownType.New("type", job.Type)
		job.State, job.FinishedAt, job.Error = StateFailed, &now, &Failure{Code: f.Code, Message: f.Message}
		m.end(job, StateQueued)
		return
	}
	seq, err := m.storage.lastLogSeq(ctx, job.ID)
	if err != nil {
		m.log.Warn("reading a job's log", "job", job.ID, "error", err)
	}
	now := m.now().UTC()
	job.State, job.StartedAt = StateRunning, &now
	// The context's cause tells the handler why it ended: Cancel says
	// core.canceled, the timer core.timeout, and the panel stopping nothing,
	// which Stopped reports as jobs.interrupted.
	cancelable, cancel := context.WithCancelCause(m.base)
	runCtx, stopTimer := context.WithTimeoutCause(cancelable, typ.timeout(), i18n.ErrTimeout.Wrap(context.DeadlineExceeded, "what", job.Title))
	release := func() {
		stopTimer()
		cancel(nil)
	}
	r := &running{job: job.clone(), cancel: cancel, logSeq: seq, saved: now}
	// Registering the job before it is marked as running in storage lets
	// Cancel find it either way.
	m.mu.Lock()
	if m.dropped[job.ID] > 0 {
		m.mu.Unlock()
		release()
		return
	}
	m.running[job.ID] = r
	m.mu.Unlock()
	// A cancel that ended the job since the dispatcher read it wins.
	started, err := m.storage.transition(ctx, job, StateQueued)
	if err != nil || !started {
		if err != nil {
			m.log.Error("starting a job", "job", job.ID, "error", err)
		}
		m.mu.Lock()
		delete(m.running, job.ID)
		canceled := r.canceled
		// Waits look at the job again, in case one saw it in between.
		m.wakeWaiters(job.ID)
		m.mu.Unlock()
		release()
		if canceled && err != nil {
			// The job is still queued, and someone canceled it meanwhile,
			// thinking it had started.
			if queued, getErr := m.storage.get(ctx, job.ID); getErr == nil && queued.State == StateQueued {
				m.cancelQueued(queued)
			}
		}
		return
	}
	m.mu.Lock()
	r.started = true
	m.counts.Started++
	m.send(job.ID, Update{Job: job.clone()})
	m.mu.Unlock()
	m.work.Add(1)
	go func() {
		defer m.work.Done()
		defer release()
		// A job that was canceled once it showed as running still runs its
		// handler, which learns from its context that it was asked to stop.
		result, err := m.safeRun(runCtx, typ, job.clone(), &reporter{m: m, r: r, id: job.ID})
		m.finish(job, r, result, err)
		m.signal()
	}()
}

// safeRun turns a panicking handler into a failed job instead of a crashed
// panel.
func (m *Manager) safeRun(ctx context.Context, typ Type, job *Job, r Reporter) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("job handler panicked", "job", job.ID, "type", job.Type, "panic", p)
			err = fmt.Errorf("job handler panicked: %v", p)
		}
	}()
	return typ.Run(ctx, job, job.Args, r)
}

// finish records how a job this process ran ended, and queues an automatic
// retry when its type wants one. A job the stopping panel cut short is left
// as it is, for the next start to pick up.
func (m *Manager) finish(job *Job, r *running, result any, err error) {
	ctx := context.WithoutCancel(m.base)
	m.mu.Lock()
	canceled := r.canceled
	job.Progress = r.job.Progress
	delete(m.running, job.ID)
	if !canceled && m.base.Err() != nil {
		m.wakeWaiters(job.ID)
		m.closeWatchers(job.ID)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	now := m.now().UTC()
	job.FinishedAt = &now
	var retry bool
	switch {
	case canceled:
		job.State = StateCanceled
		f := i18n.ErrCanceled.New()
		job.Error = &Failure{Code: f.Code, Message: f.Message}
	case err == nil:
		job.State = StateSucceeded
		if result != nil {
			if raw, mErr := json.Marshal(result); mErr == nil {
				job.Result = raw
			}
		}
	default:
		job.State = StateFailed
		if errors.Is(err, context.DeadlineExceeded) && !i18n.ErrTimeout.Is(err) {
			err = i18n.ErrTimeout.Wrap(err, "what", job.Title)
		}
		f := i18n.From(err)
		if f.Code == i18n.ErrInternal.Code {
			// Internal errors are logged in full; the job shows the summary.
			m.log.Error("job failed", "job", job.ID, "type", job.Type, "error", err)
		}
		job.Error = &Failure{Code: f.Code, Message: f.Message}
		typ, ok := m.typeFor(job.Type)
		retry = ok && job.Attempt <= typ.Retries && (typ.Retryable == nil || typ.Retryable(ctx, job.clone()) == nil)
	}
	if !m.end(job, StateRunning) || !retry {
		return
	}
	typ, _ := m.typeFor(job.Type)
	m.retrying.Lock()
	_, err = m.queueRetry(ctx, job, now.Add(typ.backoff(job.Attempt)))
	m.retrying.Unlock()
	switch {
	case ErrAlreadyRetried.Is(err):
		// Someone retried it by hand already.
	case err != nil:
		m.log.Error("queueing a retry", "job", job.ID, "error", err)
	}
}

// end records that a job ended, as job says, if it is still in the state
// from. Then its watchers get the final update, its type hears how it
// ended, and the calls waiting for it return. It reports whether it
// recorded the end: a job something else ended first, as when two cancels
// race, is left as that one recorded it.
func (m *Manager) end(job *Job, from State) bool {
	ctx := context.WithoutCancel(m.base)
	m.mu.Lock()
	m.ending[job.ID]++
	m.mu.Unlock()
	recorded, err := m.storage.transition(ctx, job, from)
	if err != nil {
		// The job did end here, so it is told all the same.
		m.log.Error("recording how a job ended", "job", job.ID, "error", err)
	}
	ended := recorded || err != nil
	if ended {
		m.mu.Lock()
		switch job.State {
		case StateSucceeded:
			m.counts.Succeeded++
		case StateFailed:
			m.counts.Failed++
		case StateCanceled:
			m.counts.Canceled++
		}
		m.send(job.ID, Update{Job: job.clone()})
		m.closeWatchers(job.ID)
		m.mu.Unlock()
		m.ended(job)
	}
	m.mu.Lock()
	if m.ending[job.ID]--; m.ending[job.ID] <= 0 {
		delete(m.ending, job.ID)
	}
	// Waiters check again either way: one that saw the job end may be
	// waiting for this to finish.
	m.wakeWaiters(job.ID)
	m.mu.Unlock()
	return recorded
}

// wakeWaiters releases the calls waiting for a job, which then look at it
// again. The caller holds m.mu.
func (m *Manager) wakeWaiters(id store.ID) {
	if w, ok := m.waiters[id]; ok {
		close(w.done)
		delete(m.waiters, id)
	}
}

// closeWatchers ends the updates of a job's watchers. The caller holds m.mu.
func (m *Manager) closeWatchers(id store.ID) {
	for w := range m.watchers[id] {
		close(w)
	}
	delete(m.watchers, id)
}

// Enqueue implements Queue.
func (m *Manager) Enqueue(ctx context.Context, typ string, args any, opts Options) (*Job, error) {
	if _, ok := m.typeFor(typ); !ok {
		return nil, ErrUnknownType.New("type", typ)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encoding job arguments: %w", err)
	}
	title := opts.Title
	if title == "" {
		title = typ
	}
	scope := opts.Scope
	if scope.Level == "" {
		scope = store.ServerScope()
	}
	job := &Job{
		ID: store.NewID(store.PrefixJob), Type: typ, Title: title, State: StateQueued, Actor: opts.Actor.Clone(), Scope: scope,
		ChangeSet: opts.ChangeSet, Resource: opts.Resource, Attempt: 1, Args: raw, CreatedAt: m.now().UTC(),
	}
	if !opts.RunAfter.IsZero() {
		t := opts.RunAfter.UTC()
		job.RunAfter = &t
	}
	return m.insert(ctx, job)
}

// insert stores a new queued job, after dropping what has outlived the
// retention, and wakes the dispatcher once the job is committed.
func (m *Manager) insert(ctx context.Context, job *Job) (*Job, error) {
	ctx = context.WithoutCancel(ctx)
	if err := m.storage.prune(ctx, m.retention, job.CreatedAt); err != nil {
		m.log.Warn("dropping old jobs", "error", err)
	}
	if err := m.storage.insert(ctx, job); err != nil {
		return nil, fmt.Errorf("jobs: queueing %s: %w", job.Type, err)
	}
	m.storage.afterCommit(ctx, func() {
		m.Start()
		m.signal()
	})
	return job.clone(), nil
}

// queueRetry queues the work of a finished job again as a new job, unless
// that was done already. The caller holds m.retrying.
func (m *Manager) queueRetry(ctx context.Context, prev *Job, after time.Time) (*Job, error) {
	next, err := m.storage.retryOf(ctx, prev.ID)
	if err != nil {
		return nil, err
	}
	if next != nil {
		return nil, ErrAlreadyRetried.New("job", string(next.ID))
	}
	job := &Job{
		ID: store.NewID(store.PrefixJob), Type: prev.Type, Title: prev.Title, State: StateQueued, Actor: prev.Actor.Clone(), Scope: prev.Scope,
		ChangeSet: prev.ChangeSet, Resource: prev.Resource, Attempt: prev.Attempt + 1, RetryOf: prev.ID, Args: prev.Args, CreatedAt: m.now().UTC(),
	}
	if !after.IsZero() {
		t := after.UTC()
		job.RunAfter = &t
	}
	return m.insert(ctx, job)
}

// Get implements Queue. A running job shows its latest progress.
func (m *Manager) Get(ctx context.Context, id store.ID) (*Job, error) {
	m.mu.Lock()
	if r, ok := m.running[id]; ok && r.started {
		job := r.job.clone()
		m.mu.Unlock()
		return job, nil
	}
	m.mu.Unlock()
	return m.storage.get(ctx, id)
}

// List implements Queue, newest first. Running jobs show their latest
// progress.
func (m *Manager) List(ctx context.Context, f Filter) ([]*Job, string, error) {
	if f.Cursor != "" {
		if _, err := store.DecodeCursor(f.Cursor); err != nil {
			return nil, "", err
		}
	}
	list, next, err := m.storage.list(ctx, f)
	if err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, job := range list {
		if r, ok := m.running[job.ID]; ok && r.started {
			list[i] = r.job.clone()
		}
	}
	return list, next, nil
}

// Cancel implements Queue. A queued job is canceled at once; a running one
// is asked to stop and ends as canceled.
func (m *Manager) Cancel(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	if r, ok := m.running[id]; ok {
		r.canceled = true
		r.cancel(i18n.ErrCanceled.New())
		m.mu.Unlock()
		return nil
	}
	// The dispatcher won't start a job being canceled here.
	m.dropped[id]++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.dropped[id]--; m.dropped[id] <= 0 {
			delete(m.dropped, id)
		}
		m.mu.Unlock()
	}()
	job, err := m.storage.get(ctx, id)
	if err != nil {
		return err
	}
	if job.State == StateQueued {
		m.cancelQueued(job)
	}
	return nil
}

// cancelQueued ends a queued job as canceled without running it, unless it
// ended since it was read.
func (m *Manager) cancelQueued(job *Job) {
	now := m.now().UTC()
	f := i18n.ErrCanceled.New()
	job.State, job.FinishedAt, job.Error = StateCanceled, &now, &Failure{Code: f.Code, Message: f.Message}
	m.end(job, StateQueued)
}

// Retry implements Queue.
func (m *Manager) Retry(ctx context.Context, id store.ID, opts RetryOptions) (*Job, error) {
	var key string
	if opts.IdempotencyKey != "" {
		key = "retry\x00" + opts.Actor.String() + "\x00" + opts.IdempotencyKey
	}
	m.retrying.Lock()
	defer m.retrying.Unlock()
	if key != "" {
		earlier, err := m.storage.keyJob(ctx, key, m.now().UTC())
		if err != nil {
			return nil, err
		}
		if earlier != "" {
			if job, err := m.Get(ctx, earlier); err == nil {
				return job, nil
			}
		}
	}
	prev, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if prev.State != StateFailed && prev.State != StateCanceled {
		return nil, ErrNotRetryable.New()
	}
	if typ, ok := m.typeFor(prev.Type); ok && typ.Retryable != nil {
		if err := typ.Retryable(ctx, prev.clone()); err != nil {
			return nil, err
		}
	}
	job, err := m.queueRetry(ctx, prev, time.Time{})
	if err != nil {
		return nil, err
	}
	if key != "" {
		if err := m.storage.saveKey(context.WithoutCancel(ctx), key, job.ID, m.now().UTC()); err != nil {
			m.log.Warn("recording an idempotency key", "error", err)
		}
	}
	return job, nil
}

// Logs implements Queue.
func (m *Manager) Logs(ctx context.Context, id store.ID) ([]LogLine, error) {
	return m.LogsAfter(ctx, id, 0, 0)
}

// LogsAfter returns up to limit lines of a job's log that come after the
// line numbered seq; zero means every line.
func (m *Manager) LogsAfter(ctx context.Context, id store.ID, seq int64, limit int) ([]LogLine, error) {
	if _, err := m.Get(ctx, id); err != nil {
		return nil, err
	}
	return m.storage.logs(ctx, id, seq, limit)
}

// Watch implements Queue. The channel closes when the job finishes.
func (m *Manager) Watch(ctx context.Context, id store.ID) (<-chan Update, func()) {
	ch := make(chan Update, watchBuffer)
	job, err := m.Get(ctx, id)
	m.mu.Lock()
	if err != nil || job.State.Done() {
		m.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	if m.watchers[id] == nil {
		m.watchers[id] = map[chan Update]bool{}
	}
	m.watchers[id][ch] = true
	m.mu.Unlock()
	// The job may have finished between reading it and registering.
	if again, err := m.storage.get(ctx, id); err == nil && again.State.Done() {
		m.mu.Lock()
		m.closeWatchers(id)
		m.mu.Unlock()
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.watchers[id][ch] {
				delete(m.watchers[id], ch)
				if len(m.watchers[id]) == 0 {
					delete(m.watchers, id)
				}
				close(ch)
			}
		})
	}
	return ch, stop
}

// send delivers an update to the job's watchers without blocking. When a
// watcher has no room left, what it holds is replaced with one update that
// says it missed some and carries the job as it is now; the log lines it
// missed are in the job's log. The caller holds m.mu, which also guards
// sending to and closing the channels, so the room made is still there.
func (m *Manager) send(id store.ID, u Update) {
	for w := range m.watchers[id] {
		select {
		case w <- u:
			continue
		default:
		}
		for drained := false; !drained; {
			select {
			case <-w:
			default:
				drained = true
			}
		}
		missed := Update{Job: u.Job, Missed: true}
		if missed.Job == nil {
			if r, ok := m.running[id]; ok {
				missed.Job = r.job.clone()
			}
		}
		select {
		case w <- missed:
		default:
		}
	}
}

// Wait implements Queue.
func (m *Manager) Wait(ctx context.Context, id store.ID) (*Job, error) {
	for {
		// Registering before reading the job means an end recorded in
		// between still wakes this call.
		w := m.addWaiter(id)
		job, err := m.Get(ctx, id)
		if err != nil {
			m.removeWaiter(id, w)
			return nil, err
		}
		m.mu.Lock()
		ending := m.ending[id] > 0
		m.mu.Unlock()
		if job.State.Done() && !ending {
			m.removeWaiter(id, w)
			return job, nil
		}
		select {
		case <-w.done:
			m.removeWaiter(id, w)
		case <-ctx.Done():
			m.removeWaiter(id, w)
			return nil, ctx.Err()
		}
	}
}

func (m *Manager) addWaiter(id store.ID) *waiter {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.waiters[id]
	if w == nil {
		w = &waiter{done: make(chan struct{})}
		m.waiters[id] = w
	}
	w.n++
	return w
}

// removeWaiter takes one call off a waiter, and the waiter off the job once
// nobody waits on it.
func (m *Manager) removeWaiter(id store.ID, w *waiter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w.n--; w.n == 0 && m.waiters[id] == w {
		delete(m.waiters, id)
	}
}

type reporter struct {
	m  *Manager
	r  *running
	id store.ID
}

// progressEvery limits how often progress is written to storage; watchers
// see every report.
const progressEvery = 250 * time.Millisecond

func (r *reporter) Progress(done, total int64, message string) {
	m := r.m
	now := m.now()
	m.mu.Lock()
	r.r.job.Progress = Progress{Done: done, Total: total, Message: message}
	job := r.r.job.clone()
	save := now.Sub(r.r.saved) >= progressEvery || (total > 0 && done >= total)
	if save {
		r.r.saved = now
	}
	m.send(r.id, Update{Job: job.clone()})
	m.mu.Unlock()
	if save {
		if err := m.storage.saveProgress(context.WithoutCancel(m.base), r.id, job.Progress); err != nil {
			m.log.Warn("saving a job's progress", "job", r.id, "error", err)
		}
	}
}

func (r *reporter) Log(level slog.Level, message string, args ...any) {
	m := r.m
	var attrs map[string]any
	if len(args) > 0 {
		attrs = map[string]any{}
		for i := 0; i+1 < len(args); i += 2 {
			attrs[fmt.Sprint(args[i])] = args[i+1]
		}
		attrs = normalizeAttrs(attrs)
	}
	// One line at a time: a line is never stored before the lines numbered
	// below it, which followers reading back what they missed rely on.
	r.r.logMu.Lock()
	defer r.r.logMu.Unlock()
	m.mu.Lock()
	r.r.logSeq++
	line := LogLine{Seq: r.r.logSeq, Time: m.now().UTC(), Level: level.String(), Message: message, Attrs: attrs}
	m.mu.Unlock()
	if err := m.storage.appendLog(context.WithoutCancel(m.base), r.id, &line); err != nil {
		m.log.Warn("writing to a job's log", "job", r.id, "error", err)
	}
	m.mu.Lock()
	m.send(r.id, Update{Log: &line})
	m.mu.Unlock()
}

// normalizeAttrs gives log attributes the shape they have after being
// stored as JSON, whichever storage keeps them: errors become their text
// and numbers become float64.
func normalizeAttrs(attrs map[string]any) map[string]any {
	for k, v := range attrs {
		switch x := v.(type) {
		case error:
			attrs[k] = x.Error()
		case fmt.Stringer:
			attrs[k] = x.String()
		}
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return attrs
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return attrs
	}
	return out
}
