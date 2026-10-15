package jobs

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// storage keeps jobs and their logs for a Manager, which is the only one
// writing to it.
type storage interface {
	insert(ctx context.Context, job *Job) error
	// update replaces everything about a job but its logs.
	update(ctx context.Context, job *Job) error
	get(ctx context.Context, id store.ID) (*Job, error)
	list(ctx context.Context, f Filter) ([]*Job, string, error)
	// runnable returns up to limit queued jobs whose time has come, oldest
	// first.
	runnable(ctx context.Context, now time.Time, limit int) ([]*Job, error)
	// nextRunAfter returns the earliest time a queued job waits for that is
	// after now.
	nextRunAfter(ctx context.Context, now time.Time) (time.Time, bool, error)
	// interrupted returns the jobs marked as running, which a panel that
	// just started isn't running.
	interrupted(ctx context.Context) ([]*Job, error)
	// backlog counts the queued jobs whose time has come and those waiting
	// for a later time, and returns when the job that has waited longest
	// for a worker could first run.
	backlog(ctx context.Context, now time.Time) (ready, waiting int, oldest time.Time, err error)
	appendLog(ctx context.Context, id store.ID, line *LogLine) error
	lastLogSeq(ctx context.Context, id store.ID) (int64, error)
	logs(ctx context.Context, id store.ID, after int64, limit int) ([]LogLine, error)
	// prune drops finished jobs, with their logs, that have outlived the
	// retention.
	prune(ctx context.Context, r Retention, now time.Time) error
}

func notFound(id store.ID) error { return i18n.ErrNotFound.New("what", "Job "+string(id)) }

// matches reports whether a job passes a filter, apart from the cursor.
func (f Filter) matches(j *Job) bool {
	switch {
	case f.State != "" && j.State != f.State:
		return false
	case f.Type != "" && j.Type != f.Type:
		return false
	case f.ChangeSet != "" && j.ChangeSet != f.ChangeSet:
		return false
	case f.Resource != "" && j.Resource != f.Resource:
		return false
	case len(f.Scopes) > 0 && !slices.Contains(f.Scopes, j.Scope):
		return false
	case f.Actor != "" && (j.Actor == nil || (j.Actor.ID != f.Actor && j.Actor.User != f.Actor)):
		return false
	case f.Visible != nil && !f.Visible.shows(j):
		return false
	}
	return true
}

// memoryStorage keeps jobs in maps.
type memoryStorage struct {
	mu    sync.RWMutex
	jobs  map[store.ID]*Job
	lines map[store.ID][]LogLine
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{jobs: map[store.ID]*Job{}, lines: map[store.ID][]LogLine{}}
}

func (m *memoryStorage) insert(ctx context.Context, job *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[job.ID] = job.clone()
	return nil
}

func (m *memoryStorage) update(ctx context.Context, job *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[job.ID]; !ok {
		return notFound(job.ID)
	}
	m.jobs[job.ID] = job.clone()
	return nil
}

func (m *memoryStorage) get(ctx context.Context, id store.ID) (*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, notFound(id)
	}
	return j.clone(), nil
}

func (m *memoryStorage) list(ctx context.Context, f Filter) ([]*Job, string, error) {
	var before store.ID
	if f.Cursor != "" {
		id, err := store.DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		before = id
	}
	m.mu.RLock()
	var out []*Job
	for _, j := range m.jobs {
		if (before == "" || j.ID < before) && f.matches(j) {
			out = append(out, j.clone())
		}
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit := f.limit(); len(out) > limit {
		out = out[:limit]
		return out, store.EncodeCursor(out[limit-1].ID), nil
	}
	return out, "", nil
}

func (m *memoryStorage) runnable(ctx context.Context, now time.Time, limit int) ([]*Job, error) {
	m.mu.RLock()
	var out []*Job
	for _, j := range m.jobs {
		if j.State == StateQueued && (j.RunAfter == nil || !j.RunAfter.After(now)) {
			out = append(out, j.clone())
		}
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memoryStorage) nextRunAfter(ctx context.Context, now time.Time) (time.Time, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var next time.Time
	for _, j := range m.jobs {
		if j.State == StateQueued && j.RunAfter != nil && j.RunAfter.After(now) && (next.IsZero() || j.RunAfter.Before(next)) {
			next = *j.RunAfter
		}
	}
	return next, !next.IsZero(), nil
}

func (m *memoryStorage) interrupted(ctx context.Context) ([]*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Job
	for _, j := range m.jobs {
		if j.State == StateRunning {
			out = append(out, j.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memoryStorage) backlog(ctx context.Context, now time.Time) (int, int, time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ready, waiting int
	var oldest time.Time
	for _, j := range m.jobs {
		if j.State != StateQueued {
			continue
		}
		if j.RunAfter != nil && j.RunAfter.After(now) {
			waiting++
			continue
		}
		ready++
		since := j.CreatedAt
		if j.RunAfter != nil {
			since = *j.RunAfter
		}
		if oldest.IsZero() || since.Before(oldest) {
			oldest = since
		}
	}
	return ready, waiting, oldest, nil
}

func (m *memoryStorage) appendLog(ctx context.Context, id store.ID, line *LogLine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[id]; !ok {
		return notFound(id)
	}
	m.lines[id] = append(m.lines[id], *line)
	return nil
}

func (m *memoryStorage) lastLogSeq(ctx context.Context, id store.ID) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lines := m.lines[id]
	if len(lines) == 0 {
		return 0, nil
	}
	return lines[len(lines)-1].Seq, nil
}

func (m *memoryStorage) logs(ctx context.Context, id store.ID, after int64, limit int) ([]LogLine, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.jobs[id]; !ok {
		return nil, notFound(id)
	}
	var out []LogLine
	for _, l := range m.lines[id] {
		if l.Seq > after {
			out = append(out, l)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (m *memoryStorage) prune(ctx context.Context, r Retention, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var finished []*Job
	for _, j := range m.jobs {
		if j.State.Done() && j.FinishedAt != nil {
			finished = append(finished, j)
		}
	}
	// IDs sort by creation time.
	sort.Slice(finished, func(i, j int) bool { return finished[i].ID < finished[j].ID })
	var kept []*Job
	for _, j := range finished {
		if now.Sub(*j.FinishedAt) > r.Finished {
			delete(m.jobs, j.ID)
			delete(m.lines, j.ID)
		} else {
			kept = append(kept, j)
		}
	}
	for i := 0; i < len(kept)-r.MaxFinished; i++ {
		delete(m.jobs, kept[i].ID)
		delete(m.lines, kept[i].ID)
	}
	return nil
}
