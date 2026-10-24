package db

import (
	"context"
	"encoding/json"
	"fmt"
)

// InsertUser inserts u, keeping a non-zero u.ID.
func InsertUser(ctx context.Context, q Querier, u *User) error {
	var id any
	if u.ID != 0 {
		id = u.ID
	}
	res, err := q.ExecContext(ctx, `INSERT INTO users (id, login, nicename, email, url, display_name, description, pass_hash, role, registered_gmt)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, u.Login, u.Nicename, u.Email, u.URL, u.DisplayName, u.Description, u.PassHash, u.Role, u.RegisteredGMT)
	if err != nil {
		return fmt.Errorf("insert user %q: %w", u.Login, err)
	}
	if u.ID == 0 {
		u.ID, err = res.LastInsertId()
	}
	return err
}

const userColumns = `id, login, nicename, email, url, display_name, description, pass_hash, role, registered_gmt`

func queryUsers(ctx context.Context, q Querier, query string, args ...any) ([]User, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Login, &u.Nicename, &u.Email, &u.URL, &u.DisplayName,
			&u.Description, &u.PassHash, &u.Role, &u.RegisteredGMT); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Users returns all users ordered by id.
func Users(ctx context.Context, q Querier) ([]User, error) {
	return queryUsers(ctx, q, `SELECT `+userColumns+` FROM users ORDER BY id`)
}

// InsertWidget inserts a widget instance.
func InsertWidget(ctx context.Context, q Querier, w *Widget) error {
	settings, err := json.Marshal(w.Settings)
	if err != nil {
		return err
	}
	if w.Settings == nil {
		settings = []byte("{}")
	}
	_, err = q.ExecContext(ctx, `INSERT INTO widgets (id, sidebar, position, type, settings) VALUES (?,?,?,?,?)`,
		w.ID, w.Sidebar, w.Position, w.Type, string(settings))
	return err
}

// Widgets returns the widgets of a sidebar in display order.
func Widgets(ctx context.Context, q Querier, sidebar string) ([]Widget, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, sidebar, position, type, settings FROM widgets
		WHERE sidebar = ? ORDER BY position`, sidebar)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Widget
	for rows.Next() {
		var w Widget
		var settings string
		if err := rows.Scan(&w.ID, &w.Sidebar, &w.Position, &w.Type, &settings); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(settings), &w.Settings); err != nil {
			return nil, fmt.Errorf("widget %s settings: %w", w.ID, err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Stash stores imported data owned by a plugin for later migration.
func Stash(ctx context.Context, q Querier, source, kind, key string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO import_stash (source, kind, key, data) VALUES (?,?,?,?)
		ON CONFLICT (source, kind, key) DO UPDATE SET data = excluded.data`, source, kind, key, string(raw))
	return err
}

// StashEntries returns the stashed entries of source/kind, keyed by key.
func StashEntries(ctx context.Context, q Querier, source, kind string) (map[string]json.RawMessage, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, data FROM import_stash WHERE source = ? AND kind = ?`, source, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, d string
		if err := rows.Scan(&k, &d); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(d)
	}
	return out, rows.Err()
}
