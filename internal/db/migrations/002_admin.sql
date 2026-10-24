-- Admin (M2a): authentication, sessions, two-factor credentials, audit log
-- and revisions. Times in the auth tables are Unix seconds (UTC); they are
-- never shown in WordPress formats.

CREATE TABLE user_meta (
	user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	key     TEXT NOT NULL,
	value   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, key)
);

-- Sessions are identified by a random 256-bit token; only its SHA-256 is
-- stored, so a database leak does not leak usable session cookies.
-- state: 'mfa' (password ok, second factor pending), 'enroll' (password ok,
-- no second factor set up yet: only enrollment allowed), 'full'.
CREATE TABLE sessions (
	id_hash    BLOB PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	state      TEXT NOT NULL,
	created    INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	expires    INTEGER NOT NULL, -- absolute timeout
	idle_secs  INTEGER NOT NULL, -- idle timeout
	remember   INTEGER NOT NULL DEFAULT 0,
	reauth_at  INTEGER NOT NULL DEFAULT 0,
	attempts   INTEGER NOT NULL DEFAULT 0, -- failed second-factor attempts
	ip         TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;
CREATE INDEX sessions_user ON sessions (user_id);

-- TOTP secret, encrypted with a key derived from the site secret.
CREATE TABLE totp (
	user_id    INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
	secret_enc BLOB NOT NULL,
	last_step  INTEGER NOT NULL DEFAULT 0, -- replay protection
	created    INTEGER NOT NULL
);

CREATE TABLE webauthn_credentials (
	id         BLOB PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name       TEXT NOT NULL DEFAULT '',
	credential TEXT NOT NULL, -- JSON of webauthn.Credential
	created    INTEGER NOT NULL,
	last_used  INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
CREATE INDEX webauthn_credentials_user ON webauthn_credentials (user_id);

CREATE TABLE recovery_codes (
	user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	code_hash BLOB NOT NULL,
	used_at   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, code_hash)
);

-- Failed logins per normalized login name (existing or not) for the
-- progressive account lockout.
CREATE TABLE login_failures (
	key          TEXT PRIMARY KEY,
	count        INTEGER NOT NULL,
	locked_until INTEGER NOT NULL DEFAULT 0,
	last         INTEGER NOT NULL
);

CREATE TABLE audit_log (
	id          INTEGER PRIMARY KEY,
	time        INTEGER NOT NULL,
	user_id     INTEGER NOT NULL DEFAULT 0,
	ip          TEXT NOT NULL DEFAULT '',
	action      TEXT NOT NULL,
	object_type TEXT NOT NULL DEFAULT '',
	object_id   INTEGER NOT NULL DEFAULT 0,
	detail      TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_time ON audit_log (time DESC);

-- Post revisions and per-user autosaves.
CREATE TABLE revisions (
	id             INTEGER PRIMARY KEY,
	post_id        INTEGER NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	author_id      INTEGER NOT NULL DEFAULT 0,
	kind           TEXT NOT NULL, -- revision, autosave
	title          TEXT NOT NULL DEFAULT '',
	content        TEXT NOT NULL DEFAULT '',
	excerpt        TEXT NOT NULL DEFAULT '',
	content_format TEXT NOT NULL DEFAULT 'html',
	created_gmt    TEXT NOT NULL
);
CREATE INDEX revisions_post ON revisions (post_id, kind, created_gmt DESC);

-- Status before a comment was trashed or marked as spam, restored by
-- "Restore" / "Not Spam" (WordPress keeps it in comment meta).
ALTER TABLE comments ADD COLUMN prev_status TEXT NOT NULL DEFAULT '';
