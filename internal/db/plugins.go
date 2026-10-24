package db

import (
	"context"
	"database/sql"
	"errors"
)

// Plugin is the stored state of an installed plugin.
type Plugin struct {
	Name     string
	Version  string
	SHA256   string
	Active   bool
	Approved string // approved permissions (canonical JSON)
	Migrated bool
	Updated  int64
}

// Plugins returns the state of all plugins.
func Plugins(ctx context.Context, q Querier) (map[string]*Plugin, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, version, sha256, active, approved, migrated, updated FROM plugins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Plugin{}
	for rows.Next() {
		p := &Plugin{}
		if err := rows.Scan(&p.Name, &p.Version, &p.SHA256, &p.Active, &p.Approved, &p.Migrated, &p.Updated); err != nil {
			return nil, err
		}
		out[p.Name] = p
	}
	return out, rows.Err()
}

// SavePlugin inserts or replaces the state of a plugin.
func SavePlugin(ctx context.Context, q Querier, p *Plugin) error {
	_, err := q.ExecContext(ctx, `INSERT INTO plugins (name, version, sha256, active, approved, migrated, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET version = excluded.version, sha256 = excluded.sha256, active = excluded.active,
			approved = excluded.approved, migrated = excluded.migrated, updated = excluded.updated`,
		p.Name, p.Version, p.SHA256, p.Active, p.Approved, p.Migrated, p.Updated)
	return err
}

// DeletePlugin removes a plugin's state, and its data if data is true.
func DeletePlugin(ctx context.Context, q Querier, name string, data bool) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM plugins WHERE name = ?`, name); err != nil {
		return err
	}
	if data {
		_, err := q.ExecContext(ctx, `DELETE FROM plugin_kv WHERE plugin = ?`, name)
		return err
	}
	return nil
}

// ErrKVQuota is returned when a plugin's data would exceed its quota.
var ErrKVQuota = errors.New("plugin data quota exceeded")

// KVGet returns a value and whether it exists.
func KVGet(ctx context.Context, q Querier, plugin, coll, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM plugin_kv WHERE plugin = ? AND collection = ? AND key = ?`,
		plugin, coll, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// KVSet stores a value unless the plugin's data would grow beyond quota
// bytes (run it in a transaction).
func KVSet(ctx context.Context, q Querier, plugin, coll, key, value string, now, quota int64) error {
	var used, old int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(collection) + length(key) + length(value)), 0) FROM plugin_kv WHERE plugin = ?`,
		plugin).Scan(&used); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(collection) + length(key) + length(value)), 0) FROM plugin_kv
		WHERE plugin = ? AND collection = ? AND key = ?`, plugin, coll, key).Scan(&old); err != nil {
		return err
	}
	if used-old+int64(len(coll)+len(key)+len(value)) > quota {
		return ErrKVQuota
	}
	_, err := q.ExecContext(ctx, `INSERT INTO plugin_kv (plugin, collection, key, value, updated) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (plugin, collection, key) DO UPDATE SET value = excluded.value, updated = excluded.updated`,
		plugin, coll, key, value, now)
	return err
}

// KVDelete removes a value.
func KVDelete(ctx context.Context, q Querier, plugin, coll, key string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM plugin_kv WHERE plugin = ? AND collection = ? AND key = ?`, plugin, coll, key)
	return err
}

// KVEntry is a key and its value.
type KVEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// KVList returns up to limit entries of a collection with keys after
// after, in key order.
func KVList(ctx context.Context, q Querier, plugin, coll, after string, limit int) ([]KVEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM plugin_kv WHERE plugin = ? AND collection = ? AND key > ?
		ORDER BY key LIMIT ?`, plugin, coll, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KVEntry
	for rows.Next() {
		var e KVEntry
		if err := rows.Scan(&e.Key, &e.Value); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// KVCount returns the number of entries in a collection.
func KVCount(ctx context.Context, q Querier, plugin, coll string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_kv WHERE plugin = ? AND collection = ?`, plugin, coll).Scan(&n)
	return n, err
}
