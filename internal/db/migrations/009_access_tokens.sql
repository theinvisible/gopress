-- Personal access tokens for the MCP interface (AI agents). Only the
-- SHA-256 of a token is stored; prefix is the start of the token, shown
-- to tell tokens apart. scopes is a space separated list.
CREATE TABLE access_tokens (
	id        INTEGER PRIMARY KEY,
	user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	name      TEXT NOT NULL,
	prefix    TEXT NOT NULL,
	hash      BLOB NOT NULL UNIQUE,
	scopes    TEXT NOT NULL,
	created   INTEGER NOT NULL,
	expires   INTEGER NOT NULL,
	last_used INTEGER NOT NULL DEFAULT 0,
	last_ip   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX access_tokens_user ON access_tokens (user_id);
