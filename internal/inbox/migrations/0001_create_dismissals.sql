-- What people dismissed from their attention inbox. A dismissal hides an
-- item while the item is at the version dismissed, and a snooze only until
-- its time; rows stay until they are pruned.
CREATE TABLE inbox_dismissals (
	person  TEXT NOT NULL,
	item    TEXT NOT NULL,
	version TEXT NOT NULL,
	-- Times are UTC, written with a fixed width so they sort as text.
	at      TEXT NOT NULL,
	until   TEXT,
	PRIMARY KEY (person, item)
) STRICT, WITHOUT ROWID;

CREATE INDEX inbox_dismissals_by_until ON inbox_dismissals (until) WHERE until IS NOT NULL;
CREATE INDEX inbox_dismissals_by_time ON inbox_dismissals (at);
