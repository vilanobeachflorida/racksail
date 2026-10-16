package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
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

// Valid reports whether s is a known state.
func (s State) Valid() bool { return s == StateQueued || s == StateRunning || s.Done() }

// Error codes.
var (
	ErrUnknownType    = i18n.Define("jobs.unknown_type", http.StatusUnprocessableEntity, "There is no job type called {type}.")
	ErrNotRetryable   = i18n.Define("jobs.not_retryable", http.StatusConflict, "Only failed or canceled jobs can be retried.")
	ErrAlreadyRetried = i18n.Define("jobs.already_retried", http.StatusConflict, "This job was retried already, as {job}. If that one failed too, retry it instead.")
	ErrInterrupted    = i18n.Define("jobs.interrupted", http.StatusServiceUnavailable, "The panel stopped while this job was running. Retry it to run it again.")
)

// IdempotencyWindow is how long a retry's Idempotency-Key keeps answering
// with the job the first request queued, as long as that job is kept.
const IdempotencyWindow = 24 * time.Hour

// Progress is how far a job has come.
type Progress struct {
	Done    int64  `json:"done" doc:"Units of work done"`
	Total   int64  `json:"total" doc:"Units of work in all; zero when unknown"`
	Message string `json:"message,omitempty" doc:"What the job is doing now"`
}

// Failure describes why a job failed.
type Failure struct {
	Code    i18n.Code `json:"code"`
	Message string    `json:"message"`
}

// Job is one run of background work.
type Job struct {
	ID       store.ID    `json:"id"`
	Type     string      `json:"type" doc:"What kind of work it is, such as changeset.apply"`
	Title    string      `json:"title"`
	State    State       `json:"state" enum:"queued,running,succeeded,failed,canceled"`
	Progress Progress    `json:"progress"`
	Actor    *auth.Actor `json:"actor,omitempty"`
	// Scope is the owner the work belongs to, which decides who may see it.
	Scope     store.Scope `json:"scope"`
	ChangeSet store.ID    `json:"changeSet,omitempty"`
	Resource  store.ID    `json:"resource,omitempty"`
	// Attempt counts the runs of the same work: a retry is a new job with
	// the next attempt number and RetryOf set.
	Attempt int             `json:"attempt"`
	RetryOf store.ID        `json:"retryOf,omitempty" doc:"The job this one retries"`
	Error   *Failure        `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Args    json.RawMessage `json:"-"`
	// RunAfter delays a queued job, as for an automatic retry.
	RunAfter   *time.Time `json:"runAfter,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func (j *Job) clone() *Job {
	c := *j
	c.Actor = j.Actor.Clone()
	c.Error = cloneFailure(j.Error)
	c.Result = append(json.RawMessage(nil), j.Result...)
	c.Args = append(json.RawMessage(nil), j.Args...)
	c.RunAfter = cloneTime(j.RunAfter)
	c.StartedAt = cloneTime(j.StartedAt)
	c.FinishedAt = cloneTime(j.FinishedAt)
	return &c
}

func cloneFailure(f *Failure) *Failure {
	if f == nil {
		return nil
	}
	c := *f
	return &c
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// LogLine is one line of a job's log.
type LogLine struct {
	// Seq numbers the lines of a job from 1, so a stream can resume after
	// the last line it showed.
	Seq     int64          `json:"seq"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level" enum:"DEBUG,INFO,WARN,ERROR"`
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
	// Retries is how many times a failed job is tried again on its own,
	// waiting Backoff before the first retry and twice as long before each
	// one after.
	Retries int
	// Backoff is the wait before the first automatic retry; zero means 30
	// seconds.
	Backoff time.Duration
	// Resume marks work that is safe to run again from the start, which
	// jobs of this type then do when the panel restarts while they run.
	// Others are reported as interrupted.
	Resume bool
	Run    Handler
	// Ended, when set, is told about every job of the type once its end is
	// recorded, however it ended: also when its handler never ran, as for a
	// job canceled while it waited or one the panel stopped under that
	// doesn't resume. It is told once per job, before Wait returns the job.
	// Work that keeps records of its own uses it to keep them in line with
	// the job.
	Ended func(ctx context.Context, job *Job)
	// Retryable, when set, is asked before a failed or canceled job of the
	// type is retried, by Retry or on its own; an error refuses the retry
	// and says why.
	Retryable func(ctx context.Context, job *Job) error
}

// Stopped says why a job's context ended: core.canceled when someone
// canceled the job, core.timeout when it ran out of time, and
// jobs.interrupted when the panel is stopping, in which case the job runs
// again or is reported as interrupted once the panel is back. It returns nil
// while the context hasn't ended. Handlers use it to tell how they stopped.
func Stopped(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if i18n.ErrCanceled.Is(cause) || i18n.ErrTimeout.Is(cause) {
		return cause
	}
	return ErrInterrupted.Wrap(cause)
}

func (t Type) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return time.Hour
}

func (t Type) backoff(attempt int) time.Duration {
	base := t.Backoff
	if base <= 0 {
		base = 30 * time.Second
	}
	d := base
	for i := 1; i < attempt && d < 24*time.Hour; i++ {
		d *= 2
	}
	return d
}

var typeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_.]*$`)

func (t Type) check() error {
	switch {
	case !typeNamePattern.MatchString(t.Name):
		return fmt.Errorf("jobs: type name %q must be <module>.<name>", t.Name)
	case t.Run == nil:
		return fmt.Errorf("jobs: type %s has no handler", t.Name)
	case t.Retries < 0 || t.Timeout < 0 || t.Backoff < 0:
		return fmt.Errorf("jobs: type %s has a negative retry count, timeout or backoff", t.Name)
	}
	return nil
}

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
	if err := t.check(); err != nil {
		types.errs = append(types.errs, err)
		return
	}
	if types.m[t.Name].Run != nil {
		types.errs = append(types.errs, fmt.Errorf("jobs: type %s is registered twice", t.Name))
		return
	}
	types.m[t.Name] = t
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
	Title string
	Actor *auth.Actor
	// Scope is the owner the work belongs to; the server's when empty.
	Scope     store.Scope
	ChangeSet store.ID
	Resource  store.ID
	// RunAfter delays the job until then.
	RunAfter time.Time
}

// Filter selects jobs.
type Filter struct {
	State     State
	Type      string
	ChangeSet store.ID
	Resource  store.ID
	// Scopes keeps the jobs of any of these owners when it isn't empty.
	Scopes []store.Scope
	// Actor keeps the jobs this user, token or automation started, or that
	// were started for this user.
	Actor store.ID
	// Visible, when set, keeps only the jobs someone may see.
	Visible *Visibility
	Cursor  string
	Limit   int
}

// Visibility is what someone may see of the queue: every job of the owners
// in Scopes, and the jobs Person started or that were started for them,
// whatever their owner.
type Visibility struct {
	Scopes []store.Scope
	// Person is a user, or the token or automation acting when it acts for
	// nobody; a job is theirs when its actor is, or acts for, them.
	Person store.ID
}

// Of returns the person behind an actor, as Visibility.Person names them:
// the user it acts for, or itself.
func Of(a *auth.Actor) store.ID {
	if a == nil {
		return ""
	}
	if a.User != "" {
		return a.User
	}
	return a.ID
}

func (v *Visibility) shows(j *Job) bool {
	return slices.Contains(v.Scopes, j.Scope) || (v.Person != "" && Of(j.Actor) == v.Person)
}

func (f Filter) limit() int {
	if f.Limit <= 0 || f.Limit > store.MaxLimit {
		return store.DefaultLimit
	}
	return f.Limit
}

// Update is a change to a job seen by watchers: a new state or progress, or
// a log line.
type Update struct {
	Job *Job     `json:"job,omitempty"`
	Log *LogLine `json:"log,omitempty"`
	// Missed says the watcher fell behind and updates before this one were
	// dropped. Job, when set, is the job as it was then; the log lines the
	// watcher missed are in the job's log, where LogsAfter reads them back.
	Missed bool `json:"missed,omitempty"`
}

// RetryOptions describe a retry someone asks for.
type RetryOptions struct {
	// Actor is who asks; an idempotency key is theirs alone.
	Actor *auth.Actor
	// IdempotencyKey, when set, makes asking again with the same key within
	// IdempotencyWindow answer with the job the first request queued.
	IdempotencyKey string
	// DryRun checks that the job can be retried, as a retry would, and
	// returns it, queuing nothing.
	DryRun bool
}

// Queue runs jobs.
type Queue interface {
	// Handle registers a job type on this queue only, for components that
	// need their own dependencies, such as the change pipeline. It must be
	// called before jobs of the type are queued.
	Handle(t Type) error
	// Enqueue queues a job. Within a transaction of the panel database
	// (store.DB.Atomic), the job is queued with the transaction's other
	// writes and starts once it commits.
	Enqueue(ctx context.Context, typ string, args any, opts Options) (*Job, error)
	Get(ctx context.Context, id store.ID) (*Job, error)
	// List returns jobs matching f, newest first, and the cursor of the
	// next page.
	List(ctx context.Context, f Filter) ([]*Job, string, error)
	// Cancel asks a running job to stop, and cancels a queued one before it
	// starts, and reports whether it did either. Canceling a job that
	// finished changes nothing and reports false.
	Cancel(ctx context.Context, id store.ID) (bool, error)
	// Retry queues a failed or canceled job again with the same arguments.
	// A job is retried once: asking again gets jobs.already_retried, naming
	// the retry, unless the request carries the first one's idempotency key.
	Retry(ctx context.Context, id store.ID, opts RetryOptions) (*Job, error)
	// Retried returns the job that a retry by the actor with the idempotency
	// key queued, within IdempotencyWindow, or nil when there was none. The
	// API asks it before it reads the job a repeated request is about.
	Retried(ctx context.Context, actor *auth.Actor, key string) (*Job, error)
	Logs(ctx context.Context, id store.ID) ([]LogLine, error)
	// LogsAfter returns up to limit lines of a job's log after the line
	// numbered seq; zero means every line.
	LogsAfter(ctx context.Context, id store.ID, seq int64, limit int) ([]LogLine, error)
	// Watch streams updates until the job finishes or stop is called. ctx
	// bounds reading the job to start with. Log lines arrive in order; a
	// watcher that falls behind gets an update with Missed set instead of
	// the ones it had no room for, and reads the lines it missed from the
	// log.
	Watch(ctx context.Context, id store.ID) (updates <-chan Update, stop func())
	// Wait blocks until the job finishes and its type has been told how it
	// ended.
	Wait(ctx context.Context, id store.ID) (*Job, error)
}

// Stats describe a queue for the panel's metrics.
type Stats struct {
	// Queued jobs wait for a worker, Waiting ones for a later time, such as
	// automatic retries, and Running ones are running here.
	Queued, Waiting, Running int
	// OldestQueued is how long the job that has waited longest for a worker
	// has waited.
	OldestQueued time.Duration
	// The counts, since the process started, of the jobs it started and of
	// the ends it recorded.
	Started, Succeeded, Failed, Canceled int64
}

// Retention bounds the finished jobs a queue keeps, with their logs, so
// Get, Logs and Retry reach only the ones kept here.
type Retention struct {
	// Finished is how long succeeded, failed and canceled jobs stay
	// available. The default is thirty days, as long as the change pipeline
	// keeps finished change sets, so a change set's job can be looked up.
	Finished time.Duration
	// MaxFinished caps the number of finished jobs kept. The default is
	// 20,000.
	MaxFinished int
}

func (r Retention) withDefaults() Retention {
	if r.Finished <= 0 {
		r.Finished = 30 * 24 * time.Hour
	}
	if r.MaxFinished <= 0 {
		r.MaxFinished = 20_000
	}
	return r
}
