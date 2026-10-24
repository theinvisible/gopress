package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const postColumns = `id, type, status, slug, parent_id, author_id, title, content, excerpt,
	date_local, date_gmt, modified_local, modified_gmt, comment_status, ping_status,
	password, menu_order, template, mime_type, featured_media_id, comment_count, guid`

func scanPost(sc interface{ Scan(...any) error }, p *Post) error {
	return sc.Scan(&p.ID, &p.Type, &p.Status, &p.Slug, &p.ParentID, &p.AuthorID, &p.Title,
		&p.Content, &p.Excerpt, &p.Date, &p.DateGMT, &p.Modified, &p.ModifiedGMT,
		&p.CommentStatus, &p.PingStatus, &p.Password, &p.MenuOrder, &p.Template,
		&p.MimeType, &p.FeaturedMediaID, &p.CommentCount, &p.GUID)
}

func queryPosts(ctx context.Context, q Querier, query string, args ...any) ([]Post, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var p Post
		if err := scanPost(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertPost inserts p. A non-zero p.ID is kept (used by the importer).
func InsertPost(ctx context.Context, q Querier, p *Post) error {
	var id any
	if p.ID != 0 {
		id = p.ID
	}
	res, err := q.ExecContext(ctx, `INSERT INTO posts (`+postColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, p.Type, p.Status, p.Slug, p.ParentID, p.AuthorID, p.Title, p.Content, p.Excerpt,
		p.Date, p.DateGMT, p.Modified, p.ModifiedGMT, p.CommentStatus, p.PingStatus,
		p.Password, p.MenuOrder, p.Template, p.MimeType, p.FeaturedMediaID, p.CommentCount, p.GUID)
	if err != nil {
		return fmt.Errorf("insert post %d: %w", p.ID, err)
	}
	if p.ID == 0 {
		p.ID, err = res.LastInsertId()
	}
	return err
}

// GetPost returns the post with id, or sql.ErrNoRows.
func GetPost(ctx context.Context, q Querier, id int64) (*Post, error) {
	var p Post
	err := scanPost(q.QueryRowContext(ctx, `SELECT `+postColumns+` FROM posts WHERE id = ?`, id), &p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// FindPosts returns posts of type typ with slug and parent. parentID < 0
// matches any parent.
func FindPosts(ctx context.Context, q Querier, typ, slug string, parentID int64) ([]Post, error) {
	query := `SELECT ` + postColumns + ` FROM posts WHERE type = ? AND slug = ?`
	args := []any{typ, slug}
	if parentID >= 0 {
		query += ` AND parent_id = ?`
		args = append(args, parentID)
	}
	return queryPosts(ctx, q, query+` ORDER BY id`, args...)
}

// PostQuery selects a list of posts.
type PostQuery struct {
	Type     string
	Statuses []string // default: publish
	TermIDs  []int64  // posts in any of these terms
	AuthorID int64
	IDs      []int64 // restrict to these ids
	// Date filters on the local date; zero means no filter.
	Year, Month, Day int
	Limit, Offset    int
	Ascending        bool
}

func (pq PostQuery) where() (string, []any) {
	var conds []string
	var args []any
	if pq.Type != "" {
		conds = append(conds, "type = ?")
		args = append(args, pq.Type)
	}
	statuses := pq.Statuses
	if len(statuses) == 0 {
		statuses = []string{StatusPublish}
	}
	conds = append(conds, "status IN ("+placeholders(len(statuses))+")")
	for _, s := range statuses {
		args = append(args, s)
	}
	if len(pq.TermIDs) > 0 {
		conds = append(conds, "id IN (SELECT post_id FROM post_terms WHERE term_id IN ("+placeholders(len(pq.TermIDs))+"))")
		for _, t := range pq.TermIDs {
			args = append(args, t)
		}
	}
	if len(pq.IDs) > 0 {
		conds = append(conds, "id IN ("+placeholders(len(pq.IDs))+")")
		for _, id := range pq.IDs {
			args = append(args, id)
		}
	}
	if pq.AuthorID != 0 {
		conds = append(conds, "author_id = ?")
		args = append(args, pq.AuthorID)
	}
	if pq.Year != 0 {
		conds = append(conds, "substr(date_local, 1, 4) = ?")
		args = append(args, fmt.Sprintf("%04d", pq.Year))
	}
	if pq.Month != 0 {
		conds = append(conds, "substr(date_local, 6, 2) = ?")
		args = append(args, fmt.Sprintf("%02d", pq.Month))
	}
	if pq.Day != 0 {
		conds = append(conds, "substr(date_local, 9, 2) = ?")
		args = append(args, fmt.Sprintf("%02d", pq.Day))
	}
	return strings.Join(conds, " AND "), args
}

// ListPosts returns one page of posts matching pq and the total count.
func ListPosts(ctx context.Context, q Querier, pq PostQuery) ([]Post, int, error) {
	where, args := pq.where()
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM posts WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "DESC"
	if pq.Ascending {
		order = "ASC"
	}
	query := `SELECT ` + postColumns + ` FROM posts WHERE ` + where + ` ORDER BY date_gmt ` + order + `, id ` + order
	if pq.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", pq.Limit, pq.Offset)
	}
	posts, err := queryPosts(ctx, q, query, args...)
	return posts, total, err
}

// AdjacentPost returns the published post of the same type directly before
// (previous=true) or after p by date, or nil.
func AdjacentPost(ctx context.Context, q Querier, p *Post, previous bool) (*Post, error) {
	cmp, order := ">", "ASC"
	if previous {
		cmp, order = "<", "DESC"
	}
	posts, err := queryPosts(ctx, q, `SELECT `+postColumns+` FROM posts
		WHERE type = ? AND status = 'publish' AND (date_gmt `+cmp+` ? OR (date_gmt = ? AND id `+cmp+` ?))
		ORDER BY date_gmt `+order+`, id `+order+` LIMIT 1`,
		p.Type, p.DateGMT, p.DateGMT, p.ID)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return &posts[0], nil
}

// Pages returns all published pages ordered by menu_order and title, as used
// by the page-list menu.
func Pages(ctx context.Context, q Querier) ([]Post, error) {
	return queryPosts(ctx, q, `SELECT `+postColumns+` FROM posts
		WHERE type = 'page' AND status = 'publish' ORDER BY menu_order, title COLLATE NOCASE, id`)
}

// Children returns the attachments or pages whose parent is postID.
func Children(ctx context.Context, q Querier, postID int64, typ string) ([]Post, error) {
	return queryPosts(ctx, q, `SELECT `+postColumns+` FROM posts
		WHERE parent_id = ? AND type = ? ORDER BY menu_order, id`, postID, typ)
}

// MonthCount is one entry of the monthly archive list.
type MonthCount struct {
	Year, Month, Count int
}

// MonthlyArchives lists months with published posts, newest first.
func MonthlyArchives(ctx context.Context, q Querier) ([]MonthCount, error) {
	rows, err := q.QueryContext(ctx, `SELECT CAST(substr(date_local, 1, 4) AS INTEGER), CAST(substr(date_local, 6, 2) AS INTEGER), COUNT(*)
		FROM posts WHERE type = 'post' AND status = 'publish'
		GROUP BY 1, 2 ORDER BY 1 DESC, 2 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonthCount
	for rows.Next() {
		var m MonthCount
		if err := rows.Scan(&m.Year, &m.Month, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddPostMeta adds a meta value for a post.
func AddPostMeta(ctx context.Context, q Querier, postID int64, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO post_meta (post_id, key, value) VALUES (?, ?, ?)`, postID, key, value)
	return err
}

// PostMeta returns the first meta value of key, and whether it exists.
func PostMeta(ctx context.Context, q Querier, postID int64, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM post_meta WHERE post_id = ? AND key = ? LIMIT 1`, postID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// AddSlugRedirect records a former slug of a post.
func AddSlugRedirect(ctx context.Context, q Querier, oldSlug string, postID int64) error {
	_, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO slug_redirects (old_slug, post_id) VALUES (?, ?)`, oldSlug, postID)
	return err
}

// SlugRedirects returns the ids of published posts that formerly had slug.
func SlugRedirects(ctx context.Context, q Querier, slug string) ([]int64, error) {
	return queryIDs(ctx, q, `SELECT r.post_id FROM slug_redirects r JOIN posts p ON p.id = r.post_id
		WHERE r.old_slug = ? AND p.status = 'publish' ORDER BY p.date_gmt DESC`, slug)
}

// UpdateCommentCount recomputes posts.comment_count (approved comments).
func UpdateCommentCount(ctx context.Context, q Querier, postID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE posts SET comment_count =
		(SELECT COUNT(*) FROM comments WHERE post_id = ? AND status = 'approved') WHERE id = ?`, postID, postID)
	return err
}

func queryIDs(ctx context.Context, q Querier, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
