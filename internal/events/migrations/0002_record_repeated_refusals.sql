-- A refusal that happens again soon after it was recorded counts on that
-- record instead of getting one of its own: how many times it did, and when
-- it last did.
ALTER TABLE audit ADD COLUMN repeats INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audit ADD COLUMN last_time TEXT;
