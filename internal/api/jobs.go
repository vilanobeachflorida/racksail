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
// job they started, or with the permission on the job's owner.
func (s *Server) mayUseJob(ctx context.Context, perm auth.Permission, job *jobs.Job) error {
	actor, err := Actor(ctx)
	if err != nil {
		return err
	}
	if sameParty(actor, job.Actor) {
		return nil
	}
	return Authorize(ctx, s.authorizer(), perm, auth.Target{Scope: job.Scope})
}

// job reads a job the signed-in actor may see. Others are reported as not
// found.
func (s *Server) job(ctx context.Context, id store.ID) (*jobs.Job, error) {
	q := s.jobs()
	if q == nil {
		return nil, i18n.ErrUnavailable.New("what", "The job queue")
	}
	job, err := q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayUseJob(ctx, PermJobRead, job); err != nil {
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

func (s *Server) registerJobs() {
	tags := []string{"Jobs"}
	op := huma.Operation{
		OperationID: "listJobs", Method: http.MethodGet, Path: Prefix + "/jobs", Tags: tags, Security: Protected,
		Summary:     "List jobs",
		Description: "Lists the background work you can see, newest first: applying changes, backups, installs. Each job reports its progress, how it ended and, when it failed, why.",
	}
	Describe(&op, Info{Permission: string(PermJobRead), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *listJobsInput) (*jobList, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		q := s.jobs()
		if q == nil {
			return nil, i18n.ErrUnavailable.New("what", "The job queue")
		}
		list, next, err := q.List(ctx, jobs.Filter{
			State: jobs.State(in.State), Type: in.Type, ChangeSet: in.ChangeSet, Resource: in.Resource, Cursor: in.Cursor, Limit: in.Limit,
		})
		if err != nil {
			return nil, err
		}
		out := &jobList{}
		out.Body.Jobs = []*jobs.Job{}
		out.Body.Next = next
		for _, j := range list {
			if s.mayUseJob(ctx, PermJobRead, j) == nil {
				out.Body.Jobs = append(out.Body.Jobs, j)
			}
		}
		return out, nil
	})

	op = huma.Operation{
		OperationID: "getJob", Method: http.MethodGet, Path: Prefix + "/jobs/{id}", Tags: tags, Security: Protected,
		Summary:     "Get a job",
		Description: "Returns a job: its state, its progress, what it returned and, when it failed, why.",
	}
	Describe(&op, Info{Permission: string(PermJobRead), Risk: store.RiskRead})
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
	Describe(&op, Info{Permission: string(PermJobRead), Risk: store.RiskRead})
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
	Describe(&op, Info{Permission: string(PermJobRead), Risk: store.RiskRead, Stream: true})
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
		Summary:     "Cancel a job",
		Description: "Asks a running job to stop, and cancels a queued one before it starts. Canceling a job that already finished changes nothing. What the job did before it stopped stays done.",
	}
	Describe(&op, Info{Operation: "cancel", Permission: string(PermJobCancel), Risk: store.RiskMedium})
	huma.Register(s.api, op, func(ctx context.Context, in *jobInput) (*jobOutput, error) {
		job, err := s.job(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.mayUseJob(ctx, PermJobCancel, job); err != nil {
			return nil, err
		}
		if err := s.jobs().Cancel(ctx, in.ID); err != nil {
			return nil, err
		}
		s.auditJob(ctx, PermJobCancel, job, "Canceled "+job.Title)
		if job, err = s.jobs().Get(ctx, in.ID); err != nil {
			return nil, err
		}
		return &jobOutput{Body: job}, nil
	})

	op = huma.Operation{
		OperationID: "retryJob", Method: http.MethodPost, Path: Prefix + "/jobs/{id}/retry", Tags: tags, Security: Protected,
		Summary:     "Retry a job",
		Description: "Runs the work of a failed or canceled job again, as a new job with the next attempt number. The answer is the new job.",
		Responses:   map[string]*huma.Response{"202": jsonResponse("The new job", s.schemaOf(typeOfJob, "Job"))},
	}
	Describe(&op, Info{Operation: "retry", Permission: string(PermJobRetry), Risk: store.RiskMedium})
	huma.Register(s.api, op, func(ctx context.Context, in *jobInput) (*retryOutput, error) {
		job, err := s.job(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.mayUseJob(ctx, PermJobRetry, job); err != nil {
			return nil, err
		}
		next, err := s.jobs().Retry(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		s.auditJob(ctx, PermJobRetry, job, "Retried "+job.Title)
		return &retryOutput{Status: http.StatusAccepted, Body: next}, nil
	})
}

var typeOfJob = reflect.TypeFor[jobs.Job]()

type retryOutput struct {
	Status int
	Body   *jobs.Job
}

// followJob streams a job's state and log until it finishes. It watches the
// job before reading it, and its first message opens the stream.
func (s *Server) followJob(ctx context.Context, id store.ID, after int64, out send) error {
	q := s.jobs()
	updates, stop := q.Watch(id)
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

// auditJob records what someone did to a job.
func (s *Server) auditJob(ctx context.Context, perm auth.Permission, job *jobs.Job, summary string) {
	audit := s.services().Audit
	actor, err := Actor(ctx)
	if audit == nil || err != nil {
		return
	}
	rec := events.AuditRecord{Actor: *actor, Action: string(perm), Subject: job.ID, Scope: job.Scope, ChangeSet: job.ChangeSet, Outcome: events.OutcomeSuccess, Summary: summary}
	if err := audit.Record(ctx, rec); err != nil {
		s.opts.Log.Error("writing an audit record", "action", perm, "error", err)
	}
}
