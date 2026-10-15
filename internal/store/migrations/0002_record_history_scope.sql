-- Each spec version records who the resource belonged to, so a resource
-- that comes back under its old ID can be checked against the one it was.
ALTER TABLE resource_history ADD COLUMN scope_level TEXT NOT NULL DEFAULT '';
ALTER TABLE resource_history ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';

-- Versions of resources that still exist get their owner now; those of
-- resources purged before this migration stay unknown.
UPDATE resource_history SET
	scope_level = (SELECT r.scope_level FROM resources r WHERE r.id = resource_history.resource_id),
	scope_id = (SELECT r.scope_id FROM resources r WHERE r.id = resource_history.resource_id)
WHERE resource_id IN (SELECT id FROM resources);
