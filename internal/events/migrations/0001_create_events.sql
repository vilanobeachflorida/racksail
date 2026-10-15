-- The event timeline. seq is never reused, so followers of the timeline,
-- such as webhook deliveries and live streams, keep their place by it.
CREATE TABLE events (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	id          TEXT NOT NULL UNIQUE,
	type        TEXT NOT NULL,
	time        TEXT NOT NULL,
	severity    TEXT NOT NULL,
	severity_rank INTEGER NOT NULL,
	subject     TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT '',
	scope_level TEXT NOT NULL,
	scope_id    TEXT NOT NULL DEFAULT '',
	actor       TEXT,
	change_set  TEXT NOT NULL DEFAULT '',
	job         TEXT NOT NULL DEFAULT '',
	message     TEXT NOT NULL,
	data        TEXT
) STRICT;

CREATE INDEX events_by_time ON events (time);
CREATE INDEX events_by_subject ON events (subject, seq) WHERE subject <> '';
CREATE INDEX events_by_scope ON events (scope_level, scope_id, seq);
CREATE INDEX events_by_change_set ON events (change_set, seq) WHERE change_set <> '';
CREATE INDEX events_by_job ON events (job, seq) WHERE job <> '';

-- The audit log: every change and every denied attempt, with who acted and
-- for whom.
CREATE TABLE audit (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	id          TEXT NOT NULL UNIQUE,
	time        TEXT NOT NULL,
	actor       TEXT NOT NULL,
	actor_id    TEXT NOT NULL DEFAULT '',
	actor_user  TEXT NOT NULL DEFAULT '',
	action      TEXT NOT NULL,
	kind        TEXT NOT NULL DEFAULT '',
	subject     TEXT NOT NULL DEFAULT '',
	scope_level TEXT NOT NULL,
	scope_id    TEXT NOT NULL DEFAULT '',
	change_set  TEXT NOT NULL DEFAULT '',
	outcome     TEXT NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	summary     TEXT NOT NULL DEFAULT '',
	details     TEXT,
	request_id  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX audit_by_time ON audit (time);
CREATE INDEX audit_by_actor ON audit (actor_id, seq) WHERE actor_id <> '';
CREATE INDEX audit_by_user ON audit (actor_user, seq) WHERE actor_user <> '';
CREATE INDEX audit_by_subject ON audit (subject, seq) WHERE subject <> '';
CREATE INDEX audit_by_scope ON audit (scope_level, scope_id, seq);
CREATE INDEX audit_by_change_set ON audit (change_set, seq) WHERE change_set <> '';
