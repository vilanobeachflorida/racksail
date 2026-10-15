package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// State is where a job is in its life.
type State string

// Job states.
const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

// Done reports whether the job has finished, one way or another.
func (s State) Done() bool { return s == StateSucceeded || s == StateFailed || s == StateCanceled }

// Error codes.
var (
	ErrUnknownType  = i18n.Define("jobs.unknown_type", http.StatusUnprocessableEntity, "There is no job type called {type}.")
	ErrNotRetryable = i18n.Define("jobs.not_retryable", http.StatusConflict, "Only failed or canceled jobs can be retried.")
)

// Progress is how far a job has come.
type Progress struct {
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Message string `json:"message,omitempty"`
}

// Failure describes why a job failed.
type Failure struct {
	Code    i18n.Code `json:"code"`
	Message string    `json:"message"`
}

// Job is one run of background work.
type Job struct {
	ID         store.ID        `json:"id"`
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	State      State           `json:"state" enum:"queued,running,succeeded,failed,canceled"`
	Progress   Progress        `json:"progress"`
	Actor      *auth.Actor     `json:"actor,omitempty"`
	ChangeSet  store.ID        `json:"changeSet,omitempty"`
	Resource   store.ID        `json:"resource,omitempty"`
	Attempt    int             `json:"attempt"`
	Error      *Failure        `json:"error,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Args       json.RawMessage `json:"-"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
}

// LogLine is one line of a job's log.
type LogLine struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// Reporter lets a handler report progress and write to the job's log.
type Reporter interface {
	Progress(done, total int64, message string)
	Log(level slog.Level, message string, args ...any)
}

// Handler does a job's work. Its result is stored as JSON.
type Handler func(ctx context.Context, job *Job, args json.RawMessage, r Reporter) (any, error)

// Type is a kind of job.
type Type struct {
	// Name is <module>.<name>, such as changeset.apply or backups.run.
	Name string
	// Timeout bounds one attempt; zero means one hour.
	Timeout time.Duration
	Run     Handler
}

var typeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_.]*$`)

var types = struct {
	sync.RWMutex
	m    map[string]Type
	errs []error
}{m: map[string]Type{}}

// RegisterType adds a job type. It is meant for init functions; problems
// are reported by Err.
func RegisterType(t Type) {
	types.Lock()
	defer types.Unlock()
	switch {
	case !typeNamePattern.MatchString(t.Name):
		types.errs = append(types.errs, fmt.Errorf("jobs: type name %q must be <module>.<name>", t.Name))
	case t.Run == nil:
		types.errs = append(types.errs, fmt.Errorf("jobs: type %s has no handler", t.Name))
	case types.m[t.Name].Run != nil:
		types.errs = append(types.errs, fmt.Errorf("jobs: type %s is registered twice", t.Name))
	default:
		types.m[t.Name] = t
	}
}

// Err reports problems found while job types were registered.
func Err() error {
	types.RLock()
	defer types.RUnlock()
	return errors.Join(types.errs...)
}

func lookupType(name string) (Type, bool) {
	types.RLock()
	defer types.RUnlock()
	t, ok := types.m[name]
	return t, ok
}

// Options describe a job when it is queued.
type Options struct {
	Title     string
	Actor     *auth.Actor
	ChangeSet store.ID
	Resource  store.ID
}

// Filter selects jobs.
type Filter struct {
	State     State
	ChangeSet store.ID
	Resource  store.ID
	Limit     int
}

// Update is a change to a job seen by watchers: a new state or progress, or
// a log line.
type Update struct {
	Job *Job     `json:"job,omitempty"`
	Log *LogLine `json:"log,omitempty"`
}

// Queue runs jobs.
type Queue interface {
	// Handle registers a job type on this queue only, for components that
	// need their own dependencies, such as the change pipeline. It must be
	// called before jobs of the type are queued.
	Handle(t Type) error
	Enqueue(ctx context.Context, typ string, args any, opts Options) (*Job, error)
	Get(ctx context.Context, id store.ID) (*Job, error)
	List(ctx context.Context, f Filter) ([]*Job, error)
	Cancel(ctx context.Context, id store.ID) error
	// Retry queues a failed or canceled job again with the same arguments.
	Retry(ctx context.Context, id store.ID) (*Job, error)
	Logs(ctx context.Context, id store.ID) ([]LogLine, error)
	// Watch streams updates until the job finishes or stop is called.
	Watch(id store.ID) (updates <-chan Update, stop func())
	// Wait blocks until the job finishes.
	Wait(ctx context.Context, id store.ID) (*Job, error)
}

// Retention bounds the finished jobs a Memory queue keeps, with their logs.
// Jobs live in the queue's memory, so Get, Logs and Retry reach only the ones
// kept here.
type Retention struct {
	// Finished is how long succeeded, failed and canceled jobs stay
	// available. The default is seven days, as long as the change pipeline
	// keeps finished change sets, so a change set's job can be looked up.
	Finished time.Duration
	// MaxFinished caps the number of finished jobs kept. The default is 2000,
	// twice the change sets the pipeline keeps, because other work runs as
	// jobs too.
	MaxFinished int
}

func (r Retention) withDefaults() Retention {
	if r.Finished <= 0 {
		r.Finished = 7 * 24 * time.Hour
	}
	if r.MaxFinished <= 0 {
		r.MaxFinished = 2000
	}
	return r
}

// MemoryOptions configure a Memory queue.
type MemoryOptions struct {
	// Workers is how many jobs run at once; the default is four.
	Workers   int
	Retention Retention
	Log       *slog.Logger
	Now       func() time.Time
}

// Memory is an in-memory Queue that runs up to a fixed number of jobs at
// once and keeps finished jobs within its retention.
type Memory struct {
	log       *slog.Logger
	now       func() time.Time
	sem       chan struct{}
	base      context.Context
	retention Retention

	mu       sync.Mutex
	jobs     map[store.ID]*entry
	watchers map[store.ID]map[chan Update]bool
	handlers map[string]Type
}

type entry struct {
	job    Job
	logs   []LogLine
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMemory returns an in-memory queue. Jobs stop when ctx is done.
func NewMemory(ctx context.Context, opts MemoryOptions) *Memory {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Memory{
		log: opts.Log, now: opts.Now, sem: make(chan struct{}, opts.Workers), base: ctx, retention: opts.Retention.withDefaults(),
		jobs: map[store.ID]*entry{}, watchers: map[store.ID]map[chan Update]bool{}, handlers: map[string]Type{},
	}
}

// Handle implements Queue.
func (m *Memory) Handle(t Type) error {
	if !typeNamePattern.MatchString(t.Name) || t.Run == nil {
		return fmt.Errorf("jobs: type %q needs a <module>.<name> name and a handler", t.Name)
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
func (m *Memory) typeFor(name string) (Type, bool) {
	m.mu.Lock()
	t, ok := m.handlers[name]
	m.mu.Unlock()
	if ok {
		return t, true
	}
	return lookupType(name)
}

// Enqueue implements Queue.
func (m *Memory) Enqueue(ctx context.Context, typ string, args any, opts Options) (*Job, error) {
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
	e := &entry{job: Job{
		ID: store.NewID(store.PrefixJob), Type: typ, Title: title, State: StateQueued, Actor: opts.Actor,
		ChangeSet: opts.ChangeSet, Resource: opts.Resource, Attempt: 1, Args: raw, CreatedAt: m.now().UTC(),
	}, done: make(chan struct{})}
	m.mu.Lock()
	m.prune(e.job.CreatedAt)
	m.jobs[e.job.ID] = e
	snapshot := e.job
	m.mu.Unlock()
	m.start(e)
	return &snapshot, nil
}

// prune drops finished jobs and their logs once they are older than the
// retention or over its cap, oldest first. Queued and running jobs are
// always kept. m.mu must be held.
func (m *Memory) prune(now time.Time) {
	var done []*entry
	for _, e := range m.jobs {
		if e.job.State.Done() && e.job.FinishedAt != nil {
			done = append(done, e)
		}
	}
	// IDs sort by creation time.
	sort.Slice(done, func(i, j int) bool { return done[i].job.ID < done[j].job.ID })
	var kept []*entry
	for _, e := range done {
		if now.Sub(*e.job.FinishedAt) > m.retention.Finished {
			delete(m.jobs, e.job.ID)
		} else {
			kept = append(kept, e)
		}
	}
	for i := 0; i < len(kept)-m.retention.MaxFinished; i++ {
		delete(m.jobs, kept[i].job.ID)
	}
}

func (m *Memory) start(e *entry) {
	ctx, cancel := context.WithCancel(m.base)
	m.mu.Lock()
	e.cancel = cancel
	m.mu.Unlock()
	go m.run(ctx, e)
}

func (m *Memory) run(ctx context.Context, e *entry) {
	defer close(e.done)
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		m.finish(e, nil, ctx.Err())
		return
	}
	typ, _ := m.typeFor(e.job.Type)
	timeout := typ.Timeout
	if timeout == 0 {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	m.mu.Lock()
	now := m.now().UTC()
	e.job.State = StateRunning
	e.job.StartedAt = &now
	job := e.job
	m.mu.Unlock()
	m.notify(e)

	result, err := m.safeRun(ctx, typ, &job, e)
	m.finish(e, result, err)
}

// safeRun turns a panicking handler into a failed job instead of a crashed
// panel.
func (m *Memory) safeRun(ctx context.Context, typ Type, job *Job, e *entry) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			m.log.Error("job handler panicked", "job", job.ID, "type", job.Type, "panic", r)
			err = fmt.Errorf("job handler panicked: %v", r)
		}
	}()
	return typ.Run(ctx, job, job.Args, &reporter{m: m, e: e})
}

func (m *Memory) finish(e *entry, result any, err error) {
	m.mu.Lock()
	now := m.now().UTC()
	e.job.FinishedAt = &now
	switch {
	case err == nil:
		e.job.State = StateSucceeded
		if result != nil {
			if raw, mErr := json.Marshal(result); mErr == nil {
				e.job.Result = raw
			}
		}
	case errors.Is(err, context.Canceled):
		e.job.State = StateCanceled
		f := i18n.ErrCanceled.New()
		e.job.Error = &Failure{Code: f.Code, Message: f.Message}
	default:
		e.job.State = StateFailed
		f := i18n.From(err)
		msg := f.Message
		if f.Code == i18n.ErrInternal.Code {
			// Internal errors are logged in full; the job shows the summary.
			m.log.Error("job failed", "job", e.job.ID, "type", e.job.Type, "error", err)
		}
		e.job.Error = &Failure{Code: f.Code, Message: msg}
	}
	m.mu.Unlock()
	m.notify(e)
	m.closeWatchers(e.job.ID)
}

type reporter struct {
	m *Memory
	e *entry
}

func (r *reporter) Progress(done, total int64, message string) {
	r.m.mu.Lock()
	r.e.job.Progress = Progress{Done: done, Total: total, Message: message}
	r.m.mu.Unlock()
	r.m.notify(r.e)
}

func (r *reporter) Log(level slog.Level, message string, args ...any) {
	line := LogLine{Time: r.m.now().UTC(), Level: level.String(), Message: message}
	if len(args) > 0 {
		line.Attrs = map[string]any{}
		for i := 0; i+1 < len(args); i += 2 {
			line.Attrs[fmt.Sprint(args[i])] = args[i+1]
		}
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	r.e.logs = append(r.e.logs, line)
	r.m.send(r.e.job.ID, Update{Log: &line})
}

// send delivers an update to watchers without blocking; slow watchers can
// reread the job and its logs. The caller holds m.mu, which also guards
// closing the channels.
func (m *Memory) send(id store.ID, u Update) {
	for w := range m.watchers[id] {
		select {
		case w <- u:
		default:
		}
	}
}

func (m *Memory) notify(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := e.job
	m.send(job.ID, Update{Job: &job})
}

func (m *Memory) closeWatchers(id store.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for w := range m.watchers[id] {
		close(w)
	}
	delete(m.watchers, id)
}

func (m *Memory) entry(id store.ID) (*entry, error) {
	e, ok := m.jobs[id]
	if !ok {
		return nil, i18n.ErrNotFound.New("what", "Job "+string(id))
	}
	return e, nil
}

// Get implements Queue.
func (m *Memory) Get(ctx context.Context, id store.ID) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	job := e.job
	return &job, nil
}

// List implements Queue, newest first.
func (m *Memory) List(ctx context.Context, f Filter) ([]*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	for _, e := range m.jobs {
		j := e.job
		if (f.State != "" && j.State != f.State) || (f.ChangeSet != "" && j.ChangeSet != f.ChangeSet) || (f.Resource != "" && j.Resource != f.Resource) {
			continue
		}
		out = append(out, &j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	limit := f.Limit
	if limit <= 0 || limit > store.MaxLimit {
		limit = store.DefaultLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Cancel implements Queue.
func (m *Memory) Cancel(ctx context.Context, id store.ID) error {
	m.mu.Lock()
	e, err := m.entry(id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	cancel := e.cancel
	done := e.job.State.Done()
	m.mu.Unlock()
	if !done && cancel != nil {
		cancel()
	}
	return nil
}

// Retry implements Queue.
func (m *Memory) Retry(ctx context.Context, id store.ID) (*Job, error) {
	m.mu.Lock()
	old, err := m.entry(id)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if old.job.State != StateFailed && old.job.State != StateCanceled {
		m.mu.Unlock()
		return nil, ErrNotRetryable.New()
	}
	prev := old.job
	m.mu.Unlock()
	job, err := m.Enqueue(ctx, prev.Type, json.RawMessage(prev.Args), Options{Title: prev.Title, Actor: prev.Actor, ChangeSet: prev.ChangeSet, Resource: prev.Resource})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.jobs[job.ID].job.Attempt = prev.Attempt + 1
	job.Attempt = prev.Attempt + 1
	m.mu.Unlock()
	return job, nil
}

// Logs implements Queue.
func (m *Memory) Logs(ctx context.Context, id store.ID) ([]LogLine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := m.entry(id)
	if err != nil {
		return nil, err
	}
	return append([]LogLine(nil), e.logs...), nil
}

// Watch implements Queue. The channel closes when the job finishes.
func (m *Memory) Watch(id store.ID) (<-chan Update, func()) {
	ch := make(chan Update, 64)
	m.mu.Lock()
	e, ok := m.jobs[id]
	if !ok || e.job.State.Done() {
		m.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	if m.watchers[id] == nil {
		m.watchers[id] = map[chan Update]bool{}
	}
	m.watchers[id][ch] = true
	m.mu.Unlock()
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

// Wait implements Queue.
func (m *Memory) Wait(ctx context.Context, id store.ID) (*Job, error) {
	m.mu.Lock()
	e, err := m.entry(id)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// The entry, not the map, so a job dropped by the retention in the
	// meantime is still reported.
	m.mu.Lock()
	defer m.mu.Unlock()
	job := e.job
	return &job, nil
}
