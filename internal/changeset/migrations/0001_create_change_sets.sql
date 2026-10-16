-- Change sets, previews among them, kept for history, undo and applying
-- previews. The change set itself is JSON; the columns are what lists filter
-- and prune on.
CREATE TABLE change_sets (
	id         TEXT PRIMARY KEY,
	title      TEXT NOT NULL,
	state      TEXT NOT NULL,
	dry_run    INTEGER NOT NULL,
	actor_id   TEXT NOT NULL DEFAULT '',
	actor_user TEXT NOT NULL DEFAULT '',
	undo_of    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	body       TEXT NOT NULL,
	-- The request a preview came from, without secret values.
	request    TEXT
) STRICT;

CREATE INDEX change_sets_by_state ON change_sets (dry_run, state, id);
CREATE INDEX change_sets_by_age ON change_sets (dry_run, created_at);
CREATE INDEX change_sets_by_actor ON change_sets (actor_id, id) WHERE actor_id <> '';
CREATE INDEX change_sets_by_user ON change_sets (actor_user, id) WHERE actor_user <> '';
CREATE INDEX change_sets_by_undo ON change_sets (undo_of) WHERE undo_of <> '';

-- Which resources each change set touched, for a resource's history of
-- changes.
CREATE TABLE change_set_resources (
	change_set TEXT NOT NULL REFERENCES change_sets (id) ON DELETE CASCADE,
	resource   TEXT NOT NULL,
	PRIMARY KEY (change_set, resource)
) STRICT, WITHOUT ROWID;

CREATE INDEX change_set_resources_by_resource ON change_set_resources (resource, change_set);

-- Idempotency keys and the change set each produced; an empty change set
-- means the first request is still being processed.
CREATE TABLE idempotency_keys (
	key        TEXT PRIMARY KEY,
	change_set TEXT NOT NULL DEFAULT '',
	time       TEXT NOT NULL
) STRICT, WITHOUT ROWID;
