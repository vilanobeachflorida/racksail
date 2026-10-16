-- Hooks the circuit breaker switched off, until an admin switches them on.
CREATE TABLE hook_breakers (
	owner       TEXT NOT NULL,
	point       TEXT NOT NULL,
	failures    INTEGER NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	disabled_at TEXT NOT NULL,
	PRIMARY KEY (owner, point)
) STRICT, WITHOUT ROWID;
