-- Where the dispatcher is in the event timeline.
CREATE TABLE webhook_cursor (
	id  INTEGER PRIMARY KEY CHECK (id = 1),
	seq INTEGER NOT NULL
) STRICT;

-- Every delivery, pending or finished, with the body it sends.
CREATE TABLE webhook_deliveries (
	id            TEXT PRIMARY KEY,
	webhook       TEXT NOT NULL,
	event         TEXT NOT NULL DEFAULT '',
	event_type    TEXT NOT NULL,
	state         TEXT NOT NULL,
	test          INTEGER NOT NULL DEFAULT 0,
	redelivery_of TEXT NOT NULL DEFAULT '',
	attempt_count INTEGER NOT NULL DEFAULT 0,
	next_attempt  TEXT,
	last_status   INTEGER NOT NULL DEFAULT 0,
	last_error    TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	finished_at   TEXT,
	payload       TEXT NOT NULL
) STRICT;

CREATE INDEX webhook_deliveries_by_webhook ON webhook_deliveries (webhook, id);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (next_attempt) WHERE state = 'pending';
CREATE INDEX webhook_deliveries_finished ON webhook_deliveries (created_at) WHERE state <> 'pending';

-- The log of every attempt.
CREATE TABLE webhook_attempts (
	delivery TEXT NOT NULL REFERENCES webhook_deliveries (id) ON DELETE CASCADE,
	number   INTEGER NOT NULL,
	time     TEXT NOT NULL,
	status   INTEGER NOT NULL DEFAULT 0,
	error    TEXT NOT NULL DEFAULT '',
	duration INTEGER NOT NULL DEFAULT 0,
	response TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (delivery, number)
) STRICT, WITHOUT ROWID;
