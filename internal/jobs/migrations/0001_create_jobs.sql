-- The job queue and its history. Queued jobs wait here across restarts.
CREATE TABLE jobs (
	id          TEXT PRIMARY KEY,
	type        TEXT NOT NULL,
	title       TEXT NOT NULL,
	state       TEXT NOT NULL,
	progress    TEXT NOT NULL DEFAULT '{}',
	actor       TEXT,
	actor_id    TEXT NOT NULL DEFAULT '',
	actor_user  TEXT NOT NULL DEFAULT '',
	scope_level TEXT NOT NULL,
	scope_id    TEXT NOT NULL DEFAULT '',
	change_set  TEXT NOT NULL DEFAULT '',
	resource    TEXT NOT NULL DEFAULT '',
	attempt     INTEGER NOT NULL,
	retry_of    TEXT NOT NULL DEFAULT '',
	error       TEXT,
	result      TEXT,
	args        TEXT NOT NULL,
	run_after   TEXT,
	created_at  TEXT NOT NULL,
	started_at  TEXT,
	finished_at TEXT
) STRICT;

CREATE INDEX jobs_by_state ON jobs (state, id);
CREATE INDEX jobs_waiting ON jobs (run_after, id) WHERE state = 'queued';
CREATE INDEX jobs_by_change_set ON jobs (change_set, id) WHERE change_set <> '';
CREATE INDEX jobs_by_resource ON jobs (resource, id) WHERE resource <> '';
CREATE INDEX jobs_by_finished ON jobs (finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE job_logs (
	job_id  TEXT NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	time    TEXT NOT NULL,
	level   TEXT NOT NULL,
	message TEXT NOT NULL,
	attrs   TEXT,
	PRIMARY KEY (job_id, seq)
) STRICT, WITHOUT ROWID;
