package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// GetOption decodes the JSON value of option name into v. It reports whether
// the option exists.
func GetOption(ctx context.Context, q Querier, name string, v any) (bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM options WHERE name = ?`, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetOption stores v as JSON under name.
func SetOption(ctx context.Context, q Querier, name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO options (name, value) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, string(raw))
	return err
}

// AllOptions returns all options as raw JSON.
func AllOptions(ctx context.Context, q Querier) (map[string]json.RawMessage, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, value FROM options`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = json.RawMessage(value)
	}
	return out, rows.Err()
}
