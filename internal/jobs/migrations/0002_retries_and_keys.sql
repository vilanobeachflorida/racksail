-- A job is retried once; its retry is found by the job it retries.
CREATE INDEX jobs_by_retry_of ON jobs (retry_of, id) WHERE retry_of <> '';

-- Idempotency keys of requests that queue jobs, such as retries, and the
-- job each one queued.
CREATE TABLE job_idempotency_keys (
	key  TEXT PRIMARY KEY,
	job  TEXT NOT NULL,
	time TEXT NOT NULL
) STRICT, WITHOUT ROWID;
