package api

import (
	"context"
	"net/http"
	"reflect"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// mayUseJob reports whether the signed-in actor may use perm on a job: on a
// job they started, or with the permission on the job's owner. Lists use it
// to leave out the others, so it records nothing.
func (s *Server) mayUseJob(ctx context.Context, perm auth.Permission, job *jobs.Job) error {
	actor, err := Actor(ctx)
	if err != nil {
		return err
	}
	if sameParty(actor, job.Actor) {
		return nil
	}
	return allowed(ctx, s.authorizer(), perm, auth.Target{Scope: job.Scope})
}

// authorizeJob is mayUseJob for an operation's own decision: a refusal is
// recorded in the audit log.
func (s *Server) authorizeJob(ctx context.Context, perm auth.Permission, job *jobs.Job) error {
	err := s.mayUseJob(ctx, perm, job)
	if i18n.ErrForbidden.Is(err) {
		refused(ctx, perm, "", job.ID, job.Scope, err)
	}
	return err
}

// job reads a job the signed-in actor may see. Others are reported as not
// found, and the refusal is recorded.
func (s *Server) job(ctx context.Context, id store.ID) (*jobs.Job, error) {
	q := s.jobs()
	if q == nil {
		return nil, i18n.ErrUnavailable.New("what", "The job queue")
	}
	job, err := q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeJob(ctx, PermJobRead, job); err != nil {
		if i18n.ErrForbidden.Is(err) {
			return nil, i18n.ErrNotFound.New("what", "Job "+string(id))
		}
		return nil, err
	}
	return job, nil
}

type listJobsInput struct {
	State     string   `query:"state" enum:"queued,running,succeeded,failed,canceled" doc:"Only jobs in this state"`
	Type      string   `query:"type" doc:"Only jobs of this type, such as changeset.apply"`
	ChangeSet store.ID `query:"changeSet" doc:"Only the jobs of this change set"`
	Resource  store.ID `query:"resource" doc:"Only the jobs about this resource"`
	Cursor    string   `query:"cursor" doc:"Continues a list where the page before it ended"`
	Limit     int      `query:"limit" minimum:"0" maximum:"1000" doc:"How many to return; 50 when left out"`
}

type jobList struct {
	Body struct {
		Jobs []*jobs.Job `json:"jobs"`
		Next string      `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type jobInput struct {
	ID store.ID `path:"id" doc:"The job's ID, which starts with job_"`
}

type cancelJobInput struct {
	ID store.ID `path:"id" doc:"The job's ID, which starts with job_"`
	// A job's controls change no settings, so a dry run has no change set
	// to show: it checks the request and answers with the job as it is.
	DryRun bool `query:"dryRun" doc:"Check that you may cancel the job, and answer with it as it is, without canceling it"`
	// Canceling twice changes nothing, so the key needs no keeping; it is
	// taken for clients that send one with every change.
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"128" doc:"Any unique string. Canceling a job again changes nothing, so a repeat is safe with or without it"`
}

type retryJobInput struct {
	ID             store.ID `path:"id" doc:"The job's ID, which starts with job_"`
	DryRun         bool     `query:"dryRun" doc:"Check that the job can be retried, and answer with it, without queuing anything"`
	IdempotencyKey string   `header:"Idempotency-Key" maxLength:"128" doc:"Any unique string; a repeat of the request with the same key, within a day, answers with the job the first one queued instead of refusing"`
}

type jobOutput struct {
	Body *jobs.Job
}

type jobLogsInput struct {
	ID    store.ID `path:"id" doc:"The job's ID"`
	After int64    `query:"after" minimum:"0" doc:"Only lines after the one with this number"`
	Limit int      `query:"limit" minimum:"0" maximum:"10000" doc:"How many lines to return; all of them when left out"`
}

type jobLogsOutput struct {
	Body struct {
		Lines []jobs.LogLine `json:"lines"`
	}
}

type jobStreamInput struct {
	ID          store.ID `path:"id" doc:"The job's ID"`
	LastEventID string   `header:"Last-Event-ID" doc:"Resume after this log line, as browsers do when they reconnect"`
}

// jobVisibility narrows a list to the jobs the signed-in actor may see: nil
// when they may see every job, and otherwise the jobs of the owners they
// hold core.job.read on and those they started.
func (s *Server) jobVisibility(ctx context.Context) (*jobs.Visibility, error) {
	actor, err := Actor(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.grantReach(ctx, PermJobRead)
	if err != nil || r.all || !narrowable(s.authorizer(), actor) {
		// Without narrowing, every job is read and mayUseJob decides.
		return nil, err
	}
	return &jobs.Visibility{Scopes: r.scopes, Person: jobs.Of(actor)}, nil
}

func (s *Server) registerJobs() {
	tags := []string{"Jobs"}
	// Anyone signed in may use these on the jobs they started; core.job.read,
	// core.job.cancel and core.job.retry reach the jobs of the owners they're
	// held on. The catalog lists no permission for them, since none is needed.
	op := huma.Operation{
		OperationID: "listJobs", Method: http.MethodGet, Path: Prefix + "/jobs", Tags: tags, Security: Protected,
		Summary: "List jobs",
		Description: "Lists the background work you can see, newest first: applying changes, backups, installs. Each job reports its progress, how it ended and, when it failed, why. " +
			"You see the jobs you started, and those of the owners you hold core.job.read on.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *listJobsInput) (*jobList, error) {
		q := s.jobs()
		if q == nil {
			return nil, i18n.ErrUnavailable.New("what", "The job queue")
		}
		visible, err := s.jobVisibility(ctx)
		if err != nil {
			return nil, err
		}
		filter := jobs.Filter{State: jobs.State(in.State), Type: in.Type, ChangeSet: in.ChangeSet, Resource: in.Resource, Visible: visible}
		list, next, err := fill(in.Cursor, in.Limit, func(cursor string, limit int) ([]*jobs.Job, string, error) {
			filter.Cursor, filter.Limit = cursor, limit
			return q.List(ctx, filter)
		}, func(j *jobs.Job) bool { return s.mayUseJob(ctx, PermJobRead, j) == nil }, func(j *jobs.Job) store.ID { return j.ID })
		if err != nil {
			return nil, err
		}
		out := &jobList{}
		out.Body.Jobs = list
		out.Body.Next = next
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getJob", Method: http.MethodGet, Path: Prefix + "/jobs/{id}", Tags: tags, Security: Protected,
		Summary:     "Get a job",
		Description: "Returns a job: its state, its progress, what it returned and, when it failed, why. Any job you started, or one of an owner you hold core.job.read on.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *jobInput) (*jobOutput, error) {
		job, err := s.job(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &jobOutput{Body: job}, nil
	})

	op = huma.Operation{
		OperationID: "getJobLogs", Method: http.MethodGet, Path: Prefix + "/jobs/{id}/logs", Tags: tags, Security: Protected,
		Summary:     "Read a job's log",
		Description: "Returns the lines of a job's log, numbered from 1. Pass after to get only the lines after the ones you have.",
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *jobLogsInput) (*jobLogsOutput, error) {
		if _, err := s.job(ctx, in.ID); err != nil {
			return nil, err
		}
		lines, err := s.jobs().LogsAfter(ctx, in.ID, in.After, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &jobLogsOutput{}
		out.Body.Lines = append([]jobs.LogLine{}, lines...)
		return out, nil
	})

	op = huma.Operation{
		OperationID: "streamJob", Method: http.MethodGet, Path: Prefix + "/jobs/{id}/stream", Tags: tags, Security: Protected,
		Summary: "Follow a job",
		Description: "Streams a job as Server-Sent Events: a job event with its state and progress whenever they change, and a log event for every line of its log, " +
			"starting with the lines written so far. The stream ends after the job finishes. Log events carry their line number as their ID, so a client that reconnects with Last-Event-ID misses nothing.",
	}
	Describe(&op, Info{Risk: store.RiskRead, Stream: true})
	registerStream(s, op, map[string]any{"job": jobs.Job{}, "log": jobs.LogLine{}}, func(ctx context.Context, in *jobStreamInput) (stream, error) {
		if _, err := s.job(ctx, in.ID); err != nil {
			return nil, err
		}
		var after int64
		if in.LastEventID != "" {
			n, err := strconv.ParseInt(in.LastEventID, 10, 64)
			if err != nil || n < 0 {
				return nil, i18n.Invalid(i18n.Field("header.Last-Event-ID", i18n.ErrBadValue, "reason", "a job's stream resumes after a log line's number"))
			}
			after = n
		}
		return func(ctx context.Context, out send, _ func() error) error { return s.followJob(ctx, in.ID, after, out) }, nil
	})

	op = huma.Operation{
		OperationID: "cancelJob", Method: http.MethodPost, Path: Prefix + "/jobs/{id}/cancel", Tags: tags, Security: Protected,
		Summary: "Cancel a job",
		Description: "Asks a running job to stop, and cancels a queued one before it starts. Canceling a job that already finished, or asking a job " +
			"to stop again, changes nothing and isn't recorded in the audit log, so sending the request again, with or without the same Idempotency-Key, is safe. " +
			"What the job did before it stopped stays done; a change set whose job is canceled ends as failed, and the settings it stored are " +
			"carried out at the next resync unless it is undone. With dryRun=true it only checks that you may cancel the job and answers with the job as it is. " +
			"You can cancel the jobs you started, and those of the owners you hold core.job.cancel on.",
	}
	Describe(&op, Info{Operation: "cancel", Risk: store.RiskMedium})
	huma.Register(s.api, op, func(ctx context.Context, in *cancelJobInput) (*jobOutput, error) {
		job, err := s.job(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.authorizeJob(ctx, PermJobCancel, job); err != nil {
			return nil, err
		}
		if in.DryRun {
			return &jobOutput{Body: job}, nil
		}
		canceled, err := s.jobs().Cancel(ctx, in.ID)
		if err != nil {
			s.auditJob(ctx, PermJobCancel, job, events.OutcomeFailed, "Cancel "+job.Title, err)
			return nil, err
		}
		if canceled {
			s.auditJob(ctx, PermJobCancel, job, events.OutcomeSuccess, "Canceled "+job.Title, nil)
		}
		if job, err = s.jobs().Get(ctx, in.ID); err != nil {
			return nil, err
		}
		return &jobOutput{Body: job}, nil
	})

	op = huma.Operation{
		OperationID: "retryJob", Method: http.MethodPost, Path: Prefix + "/jobs/{id}/retry", Tags: tags, Security: Protected,
		Summary: "Retry a job",
		Description: "Runs the work of a failed or canceled job again, as a new job with the next attempt number. The answer is the new job. " +
			"A job is retried once: retrying it again answers jobs.already_retried, naming the retry, so a double click queues the work once; " +
			"a repeat with the first request's Idempotency-Key answers with the same new job, even once the job it retried is no longer kept. " +
			"Retrying a change set's job carries on from the change that didn't finish; it doesn't run an action that may already have run a second time, " +
			"and a change set that was undone is never carried out again. With dryRun=true it checks that the job can be retried and answers 200 with it, " +
			"queuing nothing. You can retry the jobs you started, and those of the owners you hold core.job.retry on.",
		DefaultStatus: http.StatusAccepted,
		Responses: map[string]*huma.Response{
			"200": jsonResponse("The job a retry would run again, for a dry run", s.schemaOf(typeOfJob, "Job")),
			"202": jsonResponse("The new job", s.schemaOf(typeOfJob, "Job")),
		},
	}
	s.withProblems(&op)
	Describe(&op, Info{Operation: "retry", Risk: store.RiskMedium})
	huma.Register(s.api, op, func(ctx context.Context, in *retryJobInput) (*retryOutput, error) {
		actor, err := Actor(ctx)
		if err != nil {
			return nil, err
		}
		q := s.jobs()
		if q == nil {
			return nil, i18n.ErrUnavailable.New("what", "The job queue")
		}
		// A repeated request gets the job the first one queued before
		// anything is read for it.
		if !in.DryRun {
			earlier, err := q.Retried(ctx, actor, in.IdempotencyKey)
			if err != nil {
				return nil, err
			}
			if earlier != nil {
				return &retryOutput{Status: http.StatusAccepted, Body: earlier}, nil
			}
		}
		job, err := s.job(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.authorizeJob(ctx, PermJobRetry, job); err != nil {
			return nil, err
		}
		if in.DryRun {
			checked, err := q.Retry(ctx, in.ID, jobs.RetryOptions{Actor: actor, DryRun: true})
			if err != nil {
				return nil, err
			}
			return &retryOutput{Status: http.StatusOK, Body: checked}, nil
		}
		next, err := q.Retry(ctx, in.ID, jobs.RetryOptions{Actor: actor, IdempotencyKey: in.IdempotencyKey})
		if err != nil {
			s.auditJob(ctx, PermJobRetry, job, events.OutcomeFailed, "Retry "+job.Title, err)
			return nil, err
		}
		s.auditJob(ctx, PermJobRetry, job, events.OutcomeSuccess, "Retried "+job.Title, nil)
		return &retryOutput{Status: http.StatusAccepted, Body: next}, nil
	})
}

var typeOfJob = reflect.TypeFor[jobs.Job]()

type retryOutput struct {
	Status int
	Body   *jobs.Job
}

// followJob streams a job's state and log until it finishes. It watches the
// job before reading it, and its first message opens the stream. Log lines
// go out in order, each once: when the watcher fell behind, or a line came
// before the one expected, the lines in between are read back from the log.
func (s *Server) followJob(ctx context.Context, id store.ID, after int64, out send) error {
	q := s.jobs()
	updates, stop := q.Watch(ctx, id)
	defer stop()
	sendLines := func() error {
		lines, err := q.LogsAfter(ctx, id, after, 0)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if err := out("log", strconv.FormatInt(l.Seq, 10), l); err != nil {
				return err
			}
			after = l.Seq
		}
		return nil
	}
	sendJob := func() (*jobs.Job, error) {
		job, err := q.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return job, out("job", "", job)
	}
	job, err := q.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.State.Done() {
		// Its log is complete, and the finished job comes last, as it
		// does for a job that finishes while followed.
		if err := sendLines(); err != nil {
			return err
		}
		return out("job", "", job)
	}
	if err := out("job", "", job); err != nil {
		return err
	}
	if err := sendLines(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case u, ok := <-updates:
			if !ok {
				// The job finished: whatever the watcher couldn't hold is
				// in the log.
				if err := sendLines(); err != nil {
					return err
				}
				_, err := sendJob()
				return err
			}
			if u.Missed || (u.Log != nil && u.Log.Seq > after+1) {
				if err := sendLines(); err != nil {
					return err
				}
			}
			if u.Log != nil && u.Log.Seq > after {
				if err := out("log", strconv.FormatInt(u.Log.Seq, 10), u.Log); err != nil {
					return err
				}
				after = u.Log.Seq
			}
			if u.Job != nil {
				if err := out("job", "", u.Job); err != nil {
					return err
				}
			}
		}
	}
}

// auditJob records what someone did to a job, or tried to: failed with
// the reason when cause isn't nil.
func (s *Server) auditJob(ctx context.Context, perm auth.Permission, job *jobs.Job, outcome events.Outcome, summary string, cause error) {
	audit := s.services().Audit
	actor, err := Actor(ctx)
	if audit == nil || err != nil {
		return
	}
	rec := events.AuditRecord{Actor: *actor, Action: string(perm), Subject: job.ID, Scope: job.Scope, ChangeSet: job.ChangeSet, Outcome: outcome, Summary: summary}
	if cause != nil {
		rec.Reason = i18n.From(cause).Message
	}
	if err := audit.Record(ctx, rec); err != nil {
		s.opts.Log.Error("writing an audit record", "action", perm, "error", err)
	}
}
