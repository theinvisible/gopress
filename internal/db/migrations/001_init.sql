-- Core content schema (M1). Dates are stored WordPress-style as
-- 'YYYY-MM-DD HH:MM:SS' text: *_local is the site-local wall clock,
-- *_gmt is UTC. Both are kept because permalinks and archives use the local
-- date while feeds and sorting use GMT.

CREATE TABLE users (
	id             INTEGER PRIMARY KEY,
	login          TEXT NOT NULL UNIQUE,
	nicename       TEXT NOT NULL UNIQUE,
	email          TEXT NOT NULL DEFAULT '',
	url            TEXT NOT NULL DEFAULT '',
	display_name   TEXT NOT NULL DEFAULT '',
	description    TEXT NOT NULL DEFAULT '',
	pass_hash      TEXT NOT NULL DEFAULT '',
	role           TEXT NOT NULL DEFAULT 'subscriber',
	registered_gmt TEXT NOT NULL
);

-- posts holds posts, pages and attachments (type), as in WordPress, so that
-- attachment pages, parents and slugs work the same way.
CREATE TABLE posts (
	id                INTEGER PRIMARY KEY,
	type              TEXT NOT NULL,
	status            TEXT NOT NULL,
	slug              TEXT NOT NULL DEFAULT '',
	parent_id         INTEGER NOT NULL DEFAULT 0,
	author_id         INTEGER NOT NULL DEFAULT 0,
	title             TEXT NOT NULL DEFAULT '',
	content           TEXT NOT NULL DEFAULT '',
	excerpt           TEXT NOT NULL DEFAULT '',
	date_local        TEXT NOT NULL,
	date_gmt          TEXT NOT NULL,
	modified_local    TEXT NOT NULL,
	modified_gmt      TEXT NOT NULL,
	comment_status    TEXT NOT NULL DEFAULT 'open',
	ping_status       TEXT NOT NULL DEFAULT 'closed',
	password          TEXT NOT NULL DEFAULT '',
	menu_order        INTEGER NOT NULL DEFAULT 0,
	template          TEXT NOT NULL DEFAULT '',
	mime_type         TEXT NOT NULL DEFAULT '',
	featured_media_id INTEGER NOT NULL DEFAULT 0,
	comment_count     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX posts_type_status_date ON posts (type, status, date_gmt DESC);
CREATE INDEX posts_slug ON posts (slug, type);
CREATE INDEX posts_parent ON posts (parent_id, type);
CREATE INDEX posts_author ON posts (author_id, type, status);

CREATE TABLE post_meta (
	post_id INTEGER NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	key     TEXT NOT NULL,
	value   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX post_meta_post_key ON post_meta (post_id, key);

-- Former slugs (WordPress _wp_old_slug) for 301 redirects.
CREATE TABLE slug_redirects (
	old_slug TEXT NOT NULL,
	post_id  INTEGER NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	PRIMARY KEY (old_slug, post_id)
);

CREATE TABLE terms (
	id          INTEGER PRIMARY KEY,
	taxonomy    TEXT NOT NULL,
	slug        TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	parent_id   INTEGER NOT NULL DEFAULT 0,
	count       INTEGER NOT NULL DEFAULT 0,
	UNIQUE (taxonomy, slug)
);

CREATE TABLE post_terms (
	post_id    INTEGER NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	term_id    INTEGER NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
	term_order INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (post_id, term_id)
);
CREATE INDEX post_terms_term ON post_terms (term_id);

-- File details of attachment posts. Caption and description live in
-- posts.excerpt / posts.content like in WordPress.
CREATE TABLE media (
	post_id  INTEGER PRIMARY KEY REFERENCES posts (id) ON DELETE CASCADE,
	file     TEXT NOT NULL, -- path relative to the uploads dir, e.g. 2014/04/a.jpg
	mime     TEXT NOT NULL DEFAULT '',
	width    INTEGER NOT NULL DEFAULT 0,
	height   INTEGER NOT NULL DEFAULT 0,
	filesize INTEGER NOT NULL DEFAULT 0,
	alt      TEXT NOT NULL DEFAULT '',
	sizes    TEXT NOT NULL DEFAULT '{}' -- JSON: name -> {file,width,height,mime}
);
CREATE UNIQUE INDEX media_file ON media (file);

CREATE TABLE comments (
	id           INTEGER PRIMARY KEY,
	post_id      INTEGER NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	parent_id    INTEGER NOT NULL DEFAULT 0,
	type         TEXT NOT NULL DEFAULT 'comment', -- comment, pingback, trackback
	status       TEXT NOT NULL,                   -- approved, pending, spam, trash
	author       TEXT NOT NULL DEFAULT '',
	author_email TEXT NOT NULL DEFAULT '',
	author_url   TEXT NOT NULL DEFAULT '',
	author_ip    TEXT NOT NULL DEFAULT '',
	agent        TEXT NOT NULL DEFAULT '',
	user_id      INTEGER NOT NULL DEFAULT 0,
	date_local   TEXT NOT NULL,
	date_gmt     TEXT NOT NULL,
	content      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX comments_post_status_date ON comments (post_id, status, date_gmt);
CREATE INDEX comments_status_date ON comments (status, date_gmt);

-- Site settings; value is JSON.
CREATE TABLE options (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE widgets (
	id       TEXT PRIMARY KEY, -- e.g. "search-2", stable for HTML ids
	sidebar  TEXT NOT NULL,
	position INTEGER NOT NULL,
	type     TEXT NOT NULL,
	settings TEXT NOT NULL DEFAULT '{}' -- JSON
);
CREATE INDEX widgets_sidebar ON widgets (sidebar, position);

-- WordPress data owned by plugins, kept verbatim until the plugin migrates it.
CREATE TABLE import_stash (
	source TEXT NOT NULL, -- e.g. "wordpress"
	kind   TEXT NOT NULL, -- e.g. "post:fslb_gallery", "option"
	key    TEXT NOT NULL,
	data   TEXT NOT NULL, -- JSON
	PRIMARY KEY (source, kind, key)
);

-- Full-text search over published content. Maintained by the application
-- (it indexes text with HTML stripped), rowid = posts.id.
CREATE VIRTUAL TABLE posts_fts USING fts5 (title, body, tokenize = 'unicode61 remove_diacritics 2');
