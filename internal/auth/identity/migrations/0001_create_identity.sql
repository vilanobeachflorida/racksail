-- Users who sign in to the panel. Their grants are kept as JSON with the
-- user and, for finding who has access where, once more in identity_grants.
CREATE TABLE identity_users (
	id                   TEXT PRIMARY KEY,
	username             TEXT NOT NULL UNIQUE,
	name                 TEXT NOT NULL DEFAULT '',
	email                TEXT NOT NULL DEFAULT '',
	home_level           TEXT NOT NULL,
	home_id              TEXT NOT NULL DEFAULT '',
	grants               TEXT NOT NULL DEFAULT '[]',
	disabled             INTEGER NOT NULL DEFAULT 0,
	locale               TEXT NOT NULL DEFAULT '',
	allow_from           TEXT NOT NULL DEFAULT '[]',
	preferences          TEXT,
	alert_channel        TEXT NOT NULL DEFAULT '',
	password_hash        TEXT NOT NULL DEFAULT '',
	password_changed_at  TEXT NOT NULL DEFAULT '',
	must_change_password INTEGER NOT NULL DEFAULT 0,
	totp                 INTEGER NOT NULL DEFAULT 0,
	totp_step            INTEGER NOT NULL DEFAULT 0,
	failed_sign_ins      INTEGER NOT NULL DEFAULT 0,
	locked_until         TEXT NOT NULL DEFAULT '',
	last_sign_in_at      TEXT NOT NULL DEFAULT '',
	last_sign_in_ip      TEXT NOT NULL DEFAULT '',
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL,
	revision             INTEGER NOT NULL DEFAULT 1
) STRICT;

CREATE INDEX identity_users_by_home ON identity_users (home_level, home_id, username);

CREATE TABLE identity_grants (
	user_id     TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	role        TEXT NOT NULL,
	scope_level TEXT NOT NULL,
	scope_id    TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX identity_grants_by_scope ON identity_grants (scope_level, scope_id);
CREATE INDEX identity_grants_by_role ON identity_grants (role);
CREATE INDEX identity_grants_by_user ON identity_grants (user_id);

-- Recovery codes are kept hashed; using one removes it.
CREATE TABLE identity_recovery_codes (
	user_id TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	hash    TEXT NOT NULL,
	PRIMARY KEY (user_id, hash)
) STRICT;

CREATE TABLE identity_passkeys (
	id            TEXT PRIMARY KEY,
	user_id       TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	credential_id TEXT NOT NULL UNIQUE,
	credential    TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	last_used_at  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX identity_passkeys_by_user ON identity_passkeys (user_id);

-- Sessions are found by the hash of their cookie, so the database holds
-- nothing that signs anyone in.
CREATE TABLE identity_sessions (
	id            TEXT PRIMARY KEY,
	user_id       TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	hash          TEXT NOT NULL UNIQUE,
	created_at    TEXT NOT NULL,
	last_seen_at  TEXT NOT NULL,
	expires_at    TEXT NOT NULL,
	ip            TEXT NOT NULL DEFAULT '',
	user_agent    TEXT NOT NULL DEFAULT '',
	method        TEXT NOT NULL,
	second_factor TEXT NOT NULL DEFAULT '',
	stepped_up_at TEXT NOT NULL DEFAULT '',
	impersonator  TEXT NOT NULL DEFAULT '',
	parent        TEXT NOT NULL DEFAULT '',
	persistent    INTEGER NOT NULL DEFAULT 0,
	pending       TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX identity_sessions_by_user ON identity_sessions (user_id);
CREATE INDEX identity_sessions_by_expiry ON identity_sessions (expires_at);

CREATE TABLE identity_devices (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	hash         TEXT NOT NULL UNIQUE,
	name         TEXT NOT NULL DEFAULT '',
	ip           TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL,
	expires_at   TEXT NOT NULL
) STRICT;

CREATE INDEX identity_devices_by_user ON identity_devices (user_id);

CREATE TABLE identity_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	hash         TEXT NOT NULL UNIQUE,
	hint         TEXT NOT NULL DEFAULT '',
	permissions  TEXT NOT NULL DEFAULT '[]',
	accounts     TEXT NOT NULL DEFAULT '[]',
	allow_from   TEXT NOT NULL DEFAULT '[]',
	expires_at   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT '',
	last_used_ip TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX identity_tokens_by_user ON identity_tokens (user_id);

CREATE TABLE identity_ssh_keys (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	type        TEXT NOT NULL,
	bits        INTEGER NOT NULL DEFAULT 0,
	fingerprint TEXT NOT NULL,
	public_key  TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	UNIQUE (user_id, fingerprint)
) STRICT;

CREATE TABLE identity_invites (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	hash       TEXT NOT NULL UNIQUE,
	purpose    TEXT NOT NULL,
	created_by TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
) STRICT;

CREATE INDEX identity_invites_by_user ON identity_invites (user_id);

-- Identities at outside providers, such as an LDAP directory, linked to the
-- users they sign in as.
CREATE TABLE identity_links (
	user_id   TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	provider  TEXT NOT NULL,
	subject   TEXT NOT NULL,
	username  TEXT NOT NULL DEFAULT '',
	linked_at TEXT NOT NULL,
	PRIMARY KEY (provider, subject),
	UNIQUE (user_id, provider)
) STRICT;

CREATE TABLE identity_mfa (
	user_id    TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
	method     TEXT NOT NULL,
	enabled_at TEXT NOT NULL,
	PRIMARY KEY (user_id, method)
) STRICT;

-- The one-time code that creates the first admin, while there is none.
CREATE TABLE identity_setup (
	id   INTEGER PRIMARY KEY CHECK (id = 1),
	hash TEXT NOT NULL
) STRICT;
