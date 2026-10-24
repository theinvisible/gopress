package db

import (
	"context"
	"encoding/json"
	"fmt"
)

// InsertMedia stores the file details of attachment post m.PostID.
func InsertMedia(ctx context.Context, q Querier, m *Media) error {
	sizes, err := json.Marshal(m.Sizes)
	if err != nil {
		return err
	}
	if m.Sizes == nil {
		sizes = []byte("{}")
	}
	_, err = q.ExecContext(ctx, `INSERT INTO media (post_id, file, mime, width, height, filesize, alt, sizes, original)
		VALUES (?,?,?,?,?,?,?,?,?)`, m.PostID, m.File, m.Mime, m.Width, m.Height, m.Filesize, m.Alt, string(sizes), m.Original)
	if err != nil {
		return fmt.Errorf("insert media %d: %w", m.PostID, err)
	}
	return nil
}

// MediaByIDs returns the media rows of the given attachment ids.
func MediaByIDs(ctx context.Context, q Querier, ids []int64) (map[int64]*Media, error) {
	out := make(map[int64]*Media, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT post_id, file, mime, width, height, filesize, alt, sizes, original
		FROM media WHERE post_id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Media
		var sizes string
		if err := rows.Scan(&m.PostID, &m.File, &m.Mime, &m.Width, &m.Height, &m.Filesize, &m.Alt, &sizes, &m.Original); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sizes), &m.Sizes); err != nil {
			return nil, fmt.Errorf("media %d sizes: %w", m.PostID, err)
		}
		out[m.PostID] = &m
	}
	return out, rows.Err()
}

// GetMedia returns the media row of one attachment, or nil.
func GetMedia(ctx context.Context, q Querier, id int64) (*Media, error) {
	m, err := MediaByIDs(ctx, q, []int64{id})
	if err != nil {
		return nil, err
	}
	return m[id], nil
}

// SetMediaAlt changes the alternative text of an image.
func SetMediaAlt(ctx context.Context, q Querier, postID int64, alt string) error {
	_, err := q.ExecContext(ctx, `UPDATE media SET alt = ? WHERE post_id = ?`, alt, postID)
	return err
}

// SetMediaSizes replaces the generated sizes of an image.
func SetMediaSizes(ctx context.Context, q Querier, postID int64, sizes map[string]MediaSize) error {
	b, err := json.Marshal(sizes)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE media SET sizes = ? WHERE post_id = ?`, string(b), postID)
	return err
}

// AllMediaIDs returns the ids of all attachments with a media row.
func AllMediaIDs(ctx context.Context, q Querier) ([]int64, error) {
	return queryIDs(ctx, q, `SELECT post_id FROM media ORDER BY post_id`)
}

// MediaFileTaken reports whether a media row uses file.
func MediaFileTaken(ctx context.Context, q Querier, file string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE file = ?`, file).Scan(&n)
	return n > 0, err
}
