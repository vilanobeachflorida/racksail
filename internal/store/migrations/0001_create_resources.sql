-- Every resource of every kind lives in one table. Specs, statuses and
-- labels are JSON; the columns lists filter and sort on are real or
-- generated columns with indexes of their own.
CREATE TABLE resources (
	id          TEXT PRIMARY KEY,
	kind        TEXT NOT NULL,
	scope_level TEXT NOT NULL,
	scope_id    TEXT NOT NULL DEFAULT '',
	server      TEXT NOT NULL DEFAULT '',
	labels      TEXT NOT NULL DEFAULT '{}',
	spec        TEXT NOT NULL,
	status      TEXT,
	generation  INTEGER NOT NULL,
	revision    INTEGER NOT NULL,
	-- Times are UTC, written with a fixed width so they sort as text.
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	deleted_at  TEXT,
	phase TEXT GENERATED ALWAYS AS (coalesce(nullif(json_extract(status, '$.phase'), ''), 'pending')) VIRTUAL,
	observed_generation INTEGER GENERATED ALWAYS AS (coalesce(json_extract(status, '$.observedGeneration'), 0)) VIRTUAL
) STRICT;

CREATE INDEX resources_by_kind ON resources (kind, id);
CREATE INDEX resources_by_scope ON resources (kind, scope_level, scope_id, id);
CREATE INDEX resources_by_server ON resources (kind, server, id) WHERE server <> '';
CREATE INDEX resources_by_phase ON resources (kind, phase, id);
CREATE INDEX resources_by_created ON resources (kind, created_at, id);
CREATE INDEX resources_by_updated ON resources (kind, updated_at, id);
-- Resources whose latest generation hasn't been carried out, or that are
-- being removed: what a reconciler still has to do.
CREATE INDEX resources_unreconciled ON resources (kind, id)
	WHERE deleted_at IS NOT NULL OR observed_generation < generation;

-- Values of the indexes kinds declare, plus the title and subtitle fields
-- searches look at, one row per value, so array fields are indexed too.
CREATE TABLE resource_index_values (
	resource_id TEXT NOT NULL REFERENCES resources (id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	name        TEXT NOT NULL,
	value       TEXT NOT NULL,
	PRIMARY KEY (resource_id, name, value)
) STRICT, WITHOUT ROWID;

CREATE INDEX resource_index_values_lookup ON resource_index_values (kind, name, value);

-- Values held in unique indexes. A deleted resource keeps its values until
-- it is purged, so a domain stays reserved while its old site is removed.
CREATE TABLE resource_unique_values (
	kind        TEXT NOT NULL,
	scope       TEXT NOT NULL,
	name        TEXT NOT NULL,
	value       TEXT NOT NULL,
	resource_id TEXT NOT NULL REFERENCES resources (id) ON DELETE CASCADE,
	PRIMARY KEY (kind, scope, name, value)
) STRICT, WITHOUT ROWID;

CREATE INDEX resource_unique_values_by_resource ON resource_unique_values (resource_id);

-- Every spec version, kept after the resource is purged, for history and
-- undo.
CREATE TABLE resource_history (
	resource_id TEXT NOT NULL,
	generation  INTEGER NOT NULL,
	kind        TEXT NOT NULL,
	spec        TEXT NOT NULL,
	labels      TEXT NOT NULL DEFAULT '{}',
	server      TEXT NOT NULL DEFAULT '',
	change_set  TEXT NOT NULL DEFAULT '',
	actor       TEXT NOT NULL DEFAULT '',
	time        TEXT NOT NULL,
	deleted     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (resource_id, generation)
) STRICT, WITHOUT ROWID;

CREATE INDEX resource_history_by_change_set ON resource_history (change_set) WHERE change_set <> '';
