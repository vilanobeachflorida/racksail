-- Which owner each resource a change set touched belongs to, so a list can
-- keep the change sets someone may see without reading each one.
ALTER TABLE change_set_resources ADD COLUMN scope_level TEXT NOT NULL DEFAULT '';
ALTER TABLE change_set_resources ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';

UPDATE change_set_resources SET
	scope_level = coalesce((SELECT json_extract(c.value, '$.scope.level') FROM change_sets s, json_each(s.body, '$.changes') c
		WHERE s.id = change_set_resources.change_set AND json_extract(c.value, '$.id') = change_set_resources.resource LIMIT 1), ''),
	scope_id = coalesce((SELECT json_extract(c.value, '$.scope.id') FROM change_sets s, json_each(s.body, '$.changes') c
		WHERE s.id = change_set_resources.change_set AND json_extract(c.value, '$.id') = change_set_resources.resource LIMIT 1), '');

CREATE INDEX change_set_resources_by_scope ON change_set_resources (scope_level, scope_id);
