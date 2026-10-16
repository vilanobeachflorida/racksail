-- Where notifications are in the event timeline.
CREATE TABLE notify_cursor (
	id  INTEGER PRIMARY KEY CHECK (id = 1),
	seq INTEGER NOT NULL
) STRICT;

-- Every notification, waiting, held back by quiet hours or finished, with
-- the message it sends.
CREATE TABLE notify_deliveries (
	id            TEXT PRIMARY KEY,
	channel       TEXT NOT NULL DEFAULT '',
	person        TEXT NOT NULL DEFAULT '',
	address       TEXT NOT NULL DEFAULT '',
	event         TEXT NOT NULL DEFAULT '',
	event_type    TEXT NOT NULL DEFAULT '',
	title         TEXT NOT NULL,
	severity      TEXT NOT NULL,
	state         TEXT NOT NULL,
	test          INTEGER NOT NULL DEFAULT 0,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	next_attempt  TEXT,
	last_error    TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	finished_at   TEXT,
	message       TEXT NOT NULL
) STRICT;

CREATE INDEX notify_deliveries_due ON notify_deliveries (next_attempt) WHERE state IN ('pending', 'held');
CREATE INDEX notify_deliveries_by_channel ON notify_deliveries (channel, id) WHERE channel <> '';
CREATE INDEX notify_deliveries_by_person ON notify_deliveries (person, id) WHERE person <> '';
CREATE INDEX notify_deliveries_finished ON notify_deliveries (created_at) WHERE state NOT IN ('pending', 'held');

-- What each person wants to hear about.
CREATE TABLE notify_preferences (
	person      TEXT PRIMARY KEY,
	preferences TEXT NOT NULL,
	updated_at  TEXT NOT NULL
) STRICT, WITHOUT ROWID;
