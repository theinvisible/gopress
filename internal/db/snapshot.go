package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// SchemaVersion is the newest migration this build knows.
func SchemaVersion() int {
	files, _ := fs.Glob(migrations, "migrations/*.sql")
	latest := 0
	for _, f := range files {
		v, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(f, "migrations/"), "_", 2)[0])
		if err == nil && v > latest {
			latest = v
		}
	}
	return latest
}

// Snapshot writes a consistent copy of the database at src to dst (which
// must not exist) with VACUUM INTO. It works while a server is using the
// database: the copy is one read transaction and changes nothing in src.
func Snapshot(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	// VACUUM INTO accepts an empty file: creating it first gives the copy
	// mode 0600 from the start and refuses an existing target.
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("snapshot target: %w", err)
	}
	f.Close()
	c, err := sql.Open("sqlite", dsn(src, []string{"busy_timeout(10000)"}))
	if err != nil {
		os.Remove(dst)
		return err
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	if _, err := c.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		os.Remove(dst)
		return fmt.Errorf("snapshot: %w", err)
	}
	return nil
}

// Check runs SQLite's integrity check on the database file at path without
// changing it and returns its schema version.
func Check(ctx context.Context, path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	c, err := sql.Open("sqlite", dsn(path, []string{"busy_timeout(10000)", "query_only(1)"}, "mode=ro"))
	if err != nil {
		return 0, err
	}
	defer c.Close()
	rows, err := c.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return 0, fmt.Errorf("integrity check: %w", err)
	}
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return 0, err
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(problems) > 0 {
		if len(problems) > 5 {
			problems = append(problems[:5], "…")
		}
		return 0, fmt.Errorf("integrity check failed: %s", strings.Join(problems, "; "))
	}
	var version int
	if err := c.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("not a GoPress database: %w", err)
	}
	return version, nil
}
