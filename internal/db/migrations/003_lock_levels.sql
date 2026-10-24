-- Escalation level of login locks: each lock doubles the next one, while
-- the attempt counter starts again after a lock has expired.
ALTER TABLE login_failures ADD COLUMN level INTEGER NOT NULL DEFAULT 0;
