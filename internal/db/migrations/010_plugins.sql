-- Installed plugins' state. The packages themselves are embedded in the
-- binary (bundled) or stored in data/plugins (uploaded). approved is the
-- permission set an administrator approved (canonical JSON); a package
-- asking for more stays inactive until approved again.
CREATE TABLE plugins (
	name     TEXT PRIMARY KEY,
	version  TEXT NOT NULL,
	sha256   TEXT NOT NULL,
	active   INTEGER NOT NULL DEFAULT 0,
	approved TEXT NOT NULL DEFAULT '',
	migrated INTEGER NOT NULL DEFAULT 0,
	updated  INTEGER NOT NULL
);

-- Plugin data: one namespace per plugin, values are JSON.
CREATE TABLE plugin_kv (
	plugin     TEXT NOT NULL,
	collection TEXT NOT NULL,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	updated    INTEGER NOT NULL,
	PRIMARY KEY (plugin, collection, key)
);
