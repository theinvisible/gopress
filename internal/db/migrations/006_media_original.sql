-- Unscaled original of a "-scaled" image (file name in the same
-- directory). It is kept for regenerating sizes but never served.
ALTER TABLE media ADD COLUMN original TEXT NOT NULL DEFAULT '';
