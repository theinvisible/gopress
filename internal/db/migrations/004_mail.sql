-- Outgoing mail is queued and sent by a background worker, so requests
-- never wait for (or reveal anything through) SMTP. Sent mail is deleted.
CREATE TABLE mail_queue (
	id         INTEGER PRIMARY KEY,
	recipient  TEXT NOT NULL,
	subject    TEXT NOT NULL,
	body       TEXT NOT NULL,
	created    INTEGER NOT NULL,
	next_try   INTEGER NOT NULL,
	tries      INTEGER NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT 'pending', -- pending, failed
	last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX mail_queue_due ON mail_queue (status, next_try);

-- One-time links (password reset, invitation, email confirmation). Only
-- the SHA-256 of the token is stored; bind is a fingerprint of the
-- password hash at issue time, so a password change invalidates the link.
CREATE TABLE user_tokens (
	hash    BLOB PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	purpose TEXT NOT NULL,
	expires INTEGER NOT NULL,
	bind    BLOB NOT NULL,
	data    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX user_tokens_user ON user_tokens (user_id, purpose);
