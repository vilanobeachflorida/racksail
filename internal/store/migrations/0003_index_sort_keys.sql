-- Lists sort by an indexed field through a key that orders numbers by their
-- value, which comparing the values as text doesn't. Values indexed before
-- this have no key yet; the store fills them in from the resources' specs
-- when it opens, and finds them through the index below, which is empty
-- once that is done.
ALTER TABLE resource_index_values ADD COLUMN sort_key TEXT NOT NULL DEFAULT '';

CREATE INDEX resource_index_values_unsorted ON resource_index_values (resource_id) WHERE sort_key = '';
