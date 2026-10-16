-- The hash of each running add-on's token for the panel's API.
CREATE TABLE addon_tokens (
	instance   TEXT PRIMARY KEY,
	hash       BLOB NOT NULL UNIQUE,
	created_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;
