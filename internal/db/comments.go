package db

import (
	"context"
	"fmt"
)

const commentColumns = `id, post_id, parent_id, type, status, author, author_email, author_url,
	author_ip, agent, user_id, date_local, date_gmt, content`

func queryComments(ctx context.Context, q Querier, query string, args ...any) ([]Comment, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.PostID, &c.ParentID, &c.Type, &c.Status, &c.Author, &c.AuthorEmail,
			&c.AuthorURL, &c.AuthorIP, &c.Agent, &c.UserID, &c.Date, &c.DateGMT, &c.Content); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertComment inserts c, keeping a non-zero c.ID.
func InsertComment(ctx context.Context, q Querier, c *Comment) error {
	var id any
	if c.ID != 0 {
		id = c.ID
	}
	res, err := q.ExecContext(ctx, `INSERT INTO comments (`+commentColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, c.PostID, c.ParentID, c.Type, c.Status, c.Author, c.AuthorEmail, c.AuthorURL,
		c.AuthorIP, c.Agent, c.UserID, c.Date, c.DateGMT, c.Content)
	if err != nil {
		return fmt.Errorf("insert comment %d: %w", c.ID, err)
	}
	if c.ID == 0 {
		c.ID, err = res.LastInsertId()
	}
	return err
}

// PostComments returns the approved comments of a post plus, if
// pendingEmail is set, the pending ones written with that email (the
// commenter's own "awaiting moderation" view). Ordered oldest first.
func PostComments(ctx context.Context, q Querier, postID int64, pendingEmail string) ([]Comment, error) {
	if pendingEmail == "" {
		return queryComments(ctx, q, `SELECT `+commentColumns+` FROM comments
			WHERE post_id = ? AND status = 'approved' ORDER BY date_gmt, id`, postID)
	}
	return queryComments(ctx, q, `SELECT `+commentColumns+` FROM comments
		WHERE post_id = ? AND (status = 'approved' OR (status = 'pending' AND author_email = ?))
		ORDER BY date_gmt, id`, postID, pendingEmail)
}

// RecentComments returns the newest approved comments on published content.
func RecentComments(ctx context.Context, q Querier, limit int) ([]Comment, error) {
	return queryComments(ctx, q, `SELECT `+commentColumns+` FROM comments c
		WHERE status = 'approved' AND EXISTS (SELECT 1 FROM posts p WHERE p.id = c.post_id AND p.status = 'publish')
		ORDER BY date_gmt DESC, id DESC LIMIT ?`, limit)
}

// HasApprovedComment reports whether email has an approved comment, which
// WordPress uses for "comment author must have a previously approved comment".
func HasApprovedComment(ctx context.Context, q Querier, author, email string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE status = 'approved' AND author = ? AND author_email = ?`,
		author, email).Scan(&n)
	return n > 0, err
}
