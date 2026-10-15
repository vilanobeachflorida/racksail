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
	workers   int
	retention Retention
	wake      chan struct{}
	startOnce sync.Once

	mu       sync.Mutex
	handlers map[string]Type
	running  map[store.ID]*running
	// dropped are queued jobs being canceled, which must not start.
	dropped  map[store.ID]bool
	waiters  map[store.ID]chan struct{}
	watchers map[store.ID]map[chan Update]bool
}

// running is a job this process is running.
type running struct {
	job    *Job
	cancel context.CancelFunc
	// canceled is set when someone canceled the job, as opposed to the
	// panel stopping under it.
	canceled bool
	logSeq   int64
	saved    time.Time
}

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
	return &Manager{
		storage: s, log: opts.Log, now: opts.Now, base: ctx, workers: opts.Workers, retention: opts.Retention.withDefaults(),
		wake: make(chan struct{}, 1), handlers: map[string]Type{}, running: map[store.ID]*running{}, dropped: map[store.ID]bool{},
		waiters: map[store.ID]chan struct{}{}, watchers: map[store.ID]map[chan Update]bool{},
	}
}

// NewMemory returns a queue kept in memory, for tests and tools. Jobs stop
// when ctx is done.
func NewMemory(ctx context.Context, opts ManagerOptions) *Manager {
	return newManager(ctx, newMemoryStorage(), opts)
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
		if err := m.recover(); err != nil {
			m.log.Error("picking up the jobs left from before the panel restarted", "error", err)
		}
		go m.dispatch()
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
		now := m.now().UTC()
		if ok && typ.Resume {
			job.State, job.StartedAt = StateQueued, nil
			if err := m.storage.update(ctx, job); err != nil {
				return err
			}
			m.appendLog(ctx, job.ID, slog.LevelWarn, "The panel restarted while this job ran, so it runs again from the start.", nil)
			continue
		}
		f := ErrInterrupted.New()
		job.State, job.FinishedAt, job.Error = StateFailed, &now, &Failure{Code: f.Code, Message: f.Message}
		if err := m.storage.update(ctx, job); err != nil {
			return err
		}
	}
	return nil
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

// start runs a queued job in the background.
func (m *Manager) start(job *Job) {
	ctx := context.WithoutCancel(m.base)
	typ, ok := m.typeFor(job.Type)
	if !ok {
		m.finish(job, nil, nil, ErrUnknownType.New("type", job.Type))
		return
	}
	seq, err := m.storage.lastLogSeq(ctx, job.ID)
	if err != nil {
		m.log.Warn("reading a job's log", "job", job.ID, "error", err)
	}
	now := m.now().UTC()
	job.State, job.StartedAt = StateRunning, &now
	runCtx, cancel := context.WithTimeout(m.base, typ.timeout())
	r := &running{job: job.clone(), cancel: cancel, logSeq: seq, saved: now}
	// Registering the job before it is marked as running in storage lets
	// Cancel find it either way.
	m.mu.Lock()
	if m.dropped[job.ID] {
		m.mu.Unlock()
		cancel()
		return
	}
	m.running[job.ID] = r
	m.mu.Unlock()
	if err := m.storage.update(ctx, job); err != nil {
		m.log.Error("starting a job", "job", job.ID, "error", err)
		m.mu.Lock()
		delete(m.running, job.ID)
		m.mu.Unlock()
		cancel()
		return
	}
	m.mu.Lock()
	m.send(job.ID, Update{Job: job.clone()})
	m.mu.Unlock()
	go func() {
		defer cancel()
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

// finish records how a job ended and tells everyone waiting for it. A job
// the stopping panel cut short is left as it is, for the next start to pick
// up.
func (m *Manager) finish(job *Job, r *running, result any, err error) {
	ctx := context.WithoutCancel(m.base)
	m.mu.Lock()
	canceled := r != nil && r.canceled
	if r != nil {
		job.Progress = r.job.Progress
		delete(m.running, job.ID)
	}
	m.mu.Unlock()
	if !canceled && m.base.Err() != nil {
		m.closeWaiters(job.ID)
		return
	}
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
		if errors.Is(err, context.DeadlineExceeded) {
			err = i18n.ErrTimeout.Wrap(err, "what", job.Title)
		}
		f := i18n.From(err)
		if f.Code == i18n.ErrInternal.Code {
			// Internal errors are logged in full; the job shows the summary.
			m.log.Error("job failed", "job", job.ID, "type", job.Type, "error", err)
		}
		job.Error = &Failure{Code: f.Code, Message: f.Message}
		typ, ok := m.typeFor(job.Type)
		retry = ok && job.Attempt <= typ.Retries
	}
	if err := m.storage.update(ctx, job); err != nil {
		m.log.Error("recording how a job ended", "job", job.ID, "error", err)
	}
	m.mu.Lock()
	m.send(job.ID, Update{Job: job.clone()})
	m.mu.Unlock()
	m.closeWaiters(job.ID)
	if retry {
		typ, _ := m.typeFor(job.Type)
		after := now.Add(typ.backoff(job.Attempt))
		if _, err := m.requeue(ctx, job, after); err != nil {
			m.log.Error("queueing a retry", "job", job.ID, "error", err)
		}
	}
}

// closeWaiters releases Wait and closes the watchers of a finished job.
func (m *Manager) closeWaiters(id store.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.waiters[id]; ok {
		close(ch)
		delete(m.waiters, id)
	}
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
// retention, and wakes the dispatcher.
func (m *Manager) insert(ctx context.Context, job *Job) (*Job, error) {
	ctx = context.WithoutCancel(ctx)
	if err := m.storage.prune(ctx, m.retention, job.CreatedAt); err != nil {
		m.log.Warn("dropping old jobs", "error", err)
	}
	if err := m.storage.insert(ctx, job); err != nil {
		return nil, fmt.Errorf("jobs: queueing %s: %w", job.Type, err)
	}
	m.Start()
	m.signal()
	return job.clone(), nil
}

// requeue queues the work of a finished job again as a new job.
func (m *Manager) requeue(ctx context.Context, prev *Job, after time.Time) (*Job, error) {
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

// Get implements Queue.
func (m *Manager) Get(ctx context.Context, id store.ID) (*Job, error) {
	m.mu.Lock()
	if r, ok := m.running[id]; ok {
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
		if r, ok := m.running[job.ID]; ok {
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
		r.cancel()
		m.mu.Unlock()
		return nil
	}
	// The dispatcher won't start a job being canceled here.
	m.dropped[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.dropped, id)
		m.mu.Unlock()
	}()
	job, err := m.storage.get(ctx, id)
	if err != nil {
		return err
	}
	if job.State == StateQueued {
		m.finish(job, &running{job: job.clone(), canceled: true}, nil, context.Canceled)
	}
	return nil
}

// Retry implements Queue.
func (m *Manager) Retry(ctx context.Context, id store.ID) (*Job, error) {
	prev, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if prev.State != StateFailed && prev.State != StateCanceled {
		return nil, ErrNotRetryable.New()
	}
	return m.requeue(ctx, prev, time.Time{})
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
func (m *Manager) Watch(id store.ID) (<-chan Update, func()) {
	ch := make(chan Update, 64)
	job, err := m.Get(context.Background(), id)
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
	if again, err := m.storage.get(context.Background(), id); err == nil && again.State.Done() {
		m.closeWaiters(id)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.watchers[id][ch] {
				delete(m.watchers[id], ch)
				close(ch)
			}
		})
	}
	return ch, stop
}

// send delivers an update to watchers without blocking; slow watchers can
// reread the job and its logs. The caller holds m.mu, which also guards
// closing the channels.
func (m *Manager) send(id store.ID, u Update) {
	for w := range m.watchers[id] {
		select {
		case w <- u:
		default:
		}
	}
}

// Wait implements Queue.
func (m *Manager) Wait(ctx context.Context, id store.ID) (*Job, error) {
	for {
		m.mu.Lock()
		ch, ok := m.waiters[id]
		if !ok {
			ch = make(chan struct{})
			m.waiters[id] = ch
		}
		m.mu.Unlock()
		job, err := m.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if job.State.Done() {
			return job, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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
		if err := m.storage.update(context.WithoutCancel(m.base), job); err != nil {
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
