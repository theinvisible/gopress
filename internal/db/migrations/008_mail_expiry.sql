-- Messages with one-time links expire with the link: they are neither
-- sent nor kept afterwards (the queue would otherwise hold live tokens).
ALTER TABLE mail_queue ADD COLUMN expires INTEGER NOT NULL DEFAULT 0;
