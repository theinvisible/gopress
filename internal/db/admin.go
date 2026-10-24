package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// AdminPostQuery selects posts for the admin list table (edit.php).
type AdminPostQuery struct {
	Type       string
	Status     string // "" = all except trash (and auto-draft)
	Search     string // title or content contains
	Month      string // "YYYYMM" of the local date
	CategoryID int64
	TagID      int64
	AuthorID   int64
	VisibleTo  int64  // only published posts and those of this user (access tokens)
	NoPassword bool   // leave out password protected posts (plugins)
	OrderBy    string // "date" (default), "title", "comments", "menu_order"
	Ascending  bool
	Limit      int
	Offset     int
}

func (aq AdminPostQuery) where() (string, []any) {
	conds := []string{"type = ?"}
	args := []any{aq.Type}
	if aq.Status == "" {
		conds = append(conds, "status NOT IN ('trash', 'auto-draft', 'inherit')")
	} else {
		conds = append(conds, "status = ?")
		args = append(args, aq.Status)
	}
	if s := strings.TrimSpace(aq.Search); s != "" {
		like := "%" + escapeLike(s) + "%"
		if aq.VisibleTo > 0 { // the text of others' password protected posts is not searchable
			conds = append(conds, `(title LIKE ? ESCAPE '\' OR ((password = '' OR author_id = ?) AND (content LIKE ? ESCAPE '\' OR excerpt LIKE ? ESCAPE '\')))`)
			args = append(args, like, aq.VisibleTo, like, like)
		} else {
			conds = append(conds, `(title LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\' OR excerpt LIKE ? ESCAPE '\')`)
			args = append(args, like, like, like)
		}
	}
	if len(aq.Month) == 6 {
		conds = append(conds, "substr(date_local, 1, 4) = ? AND substr(date_local, 6, 2) = ?")
		args = append(args, aq.Month[:4], aq.Month[4:])
	}
	if aq.CategoryID > 0 {
		conds = append(conds, "id IN (SELECT post_id FROM post_terms WHERE term_id = ?)")
		args = append(args, aq.CategoryID)
	}
	if aq.TagID > 0 {
		conds = append(conds, "id IN (SELECT post_id FROM post_terms WHERE term_id = ?)")
		args = append(args, aq.TagID)
	}
	if aq.AuthorID > 0 {
		conds = append(conds, "author_id = ?")
		args = append(args, aq.AuthorID)
	}
	if aq.NoPassword {
		conds = append(conds, "password = ''")
	}
	if aq.VisibleTo > 0 {
		conds = append(conds, "(status = 'publish' OR author_id = ?)")
		args = append(args, aq.VisibleTo)
	}
	return strings.Join(conds, " AND "), args
}

// escapeLike escapes LIKE wildcards. NUL bytes are dropped: SQLite would
// end the pattern there.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`, "\x00", "").Replace(s)
}

// AdminPosts returns one page of posts for the list table and the total.
// Limit <= 0 returns all.
func AdminPosts(ctx context.Context, q Querier, aq AdminPostQuery) ([]Post, int, error) {
	where, args := aq.where()
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM posts WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	dir := "DESC"
	if aq.Ascending {
		dir = "ASC"
	}
	var order string
	switch aq.OrderBy {
	case "title":
		order = "title COLLATE NOCASE " + dir + ", id " + dir
	case "comments":
		order = "comment_count " + dir + ", date_gmt DESC"
	case "menu_order":
		order = "menu_order " + dir + ", title COLLATE NOCASE " + dir + ", id"
	default:
		order = "date_gmt " + dir + ", id " + dir
	}
	query := `SELECT ` + postColumns + ` FROM posts WHERE ` + where + ` ORDER BY ` + order
	if aq.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, aq.Limit, aq.Offset)
	}
	posts, err := queryPosts(ctx, q, query, args...)
	return posts, total, err
}

// PostStatusCounts counts posts of a type per status; authorID > 0 limits
// to one author.
func PostStatusCounts(ctx context.Context, q Querier, typ string, authorID int64) (map[string]int, error) {
	query := `SELECT status, COUNT(*) FROM posts WHERE type = ?`
	args := []any{typ}
	if authorID > 0 {
		query += ` AND author_id = ?`
		args = append(args, authorID)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// PostMonths lists the months (YYYYMM, newest first) that have posts of typ
// in any non-trash status, for the list table month filter.
func PostMonths(ctx context.Context, q Querier, typ string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT substr(date_local, 1, 4) || substr(date_local, 6, 2)
		FROM posts WHERE type = ? AND status NOT IN ('trash', 'auto-draft') AND date_local != '0000-00-00 00:00:00'
		ORDER BY 1 DESC`, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AllPages returns all pages except trashed ones, ordered for the
// hierarchical page list.
func AllPages(ctx context.Context, q Querier) ([]Post, error) {
	return queryPosts(ctx, q, `SELECT `+postColumns+` FROM posts
		WHERE type = 'page' AND status NOT IN ('trash', 'auto-draft') ORDER BY menu_order, title COLLATE NOCASE, id`)
}

// UpdatePost stores all columns of p.
func UpdatePost(ctx context.Context, q Querier, p *Post) error {
	_, err := q.ExecContext(ctx, `UPDATE posts SET type = ?, status = ?, slug = ?, parent_id = ?, author_id = ?,
		title = ?, content = ?, excerpt = ?, date_local = ?, date_gmt = ?, modified_local = ?, modified_gmt = ?,
		comment_status = ?, ping_status = ?, password = ?, menu_order = ?, template = ?, mime_type = ?,
		featured_media_id = ?, comment_count = ?, guid = ?, content_format = ? WHERE id = ?`,
		p.Type, p.Status, p.Slug, p.ParentID, p.AuthorID, p.Title, p.Content, p.Excerpt,
		p.Date, p.DateGMT, p.Modified, p.ModifiedGMT, p.CommentStatus, p.PingStatus,
		p.Password, p.MenuOrder, p.Template, p.MimeType, p.FeaturedMediaID, p.CommentCount, p.GUID,
		contentFormat(p.ContentFormat), p.ID)
	return err
}

// DeletePost removes a post; meta, terms, comments, old slugs and
// revisions go with it (ON DELETE CASCADE). Child pages and attachments are
// moved to the post's parent like wp_delete_post does.
func DeletePost(ctx context.Context, q Querier, id int64) error {
	var parent int64
	if err := q.QueryRowContext(ctx, `SELECT parent_id FROM posts WHERE id = ?`, id).Scan(&parent); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `UPDATE posts SET parent_id = ? WHERE parent_id = ?`, parent, id); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `DELETE FROM posts WHERE id = ?`, id)
	return err
}

// SetPostMeta sets the single value of key (replacing all existing values).
func SetPostMeta(ctx context.Context, q Querier, postID int64, key, value string) error {
	if err := DeletePostMeta(ctx, q, postID, key); err != nil {
		return err
	}
	return AddPostMeta(ctx, q, postID, key, value)
}

// DeletePostMeta removes all values of key.
func DeletePostMeta(ctx context.Context, q Querier, postID int64, key string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM post_meta WHERE post_id = ? AND key = ?`, postID, key)
	return err
}

// AllPostMeta returns all meta rows of a post in insertion order.
func AllPostMeta(ctx context.Context, q Querier, postID int64) ([][2]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM post_meta WHERE post_id = ? ORDER BY rowid`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var kv [2]string
		if err := rows.Scan(&kv[0], &kv[1]); err != nil {
			return nil, err
		}
		out = append(out, kv)
	}
	return out, rows.Err()
}

// SetPostTerms replaces the terms of one taxonomy on a post.
func SetPostTerms(ctx context.Context, q Querier, postID int64, taxonomy string, termIDs []int64) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM post_terms WHERE post_id = ?
		AND term_id IN (SELECT id FROM terms WHERE taxonomy = ?)`, postID, taxonomy); err != nil {
		return err
	}
	for i, id := range termIDs {
		if err := AddPostTerm(ctx, q, postID, id, i); err != nil {
			return err
		}
	}
	return nil
}

// SlugTaken reports whether another post (not id) of a type group uses slug
// among the given parent (parent < 0: any parent). Attachments share the
// namespace with posts/pages like in WordPress.
func SlugTaken(ctx context.Context, q Querier, slug string, types []string, parent, id int64) (bool, error) {
	query := `SELECT COUNT(*) FROM posts WHERE slug = ? AND id != ? AND type IN (` + placeholders(len(types)) + `)`
	args := []any{slug, id}
	for _, t := range types {
		args = append(args, t)
	}
	if parent >= 0 {
		query += ` AND parent_id = ?`
		args = append(args, parent)
	}
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n > 0, err
}

// DueFuturePosts returns the ids of scheduled posts whose date has come.
func DueFuturePosts(ctx context.Context, q Querier, nowGMT Time) ([]int64, error) {
	return queryIDs(ctx, q, `SELECT id FROM posts WHERE status = 'future' AND date_gmt <= ? ORDER BY date_gmt`, nowGMT)
}

// Revision is a stored revision or autosave of a post.
type Revision struct {
	ID            int64
	PostID        int64
	AuthorID      int64
	Kind          string // revision, autosave
	Title         string
	Content       string
	Excerpt       string
	ContentFormat string
	CreatedGMT    Time
}

const revisionColumns = `id, post_id, author_id, kind, title, content, excerpt, content_format, created_gmt`

func queryRevisions(ctx context.Context, q Querier, query string, args ...any) ([]Revision, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.ID, &r.PostID, &r.AuthorID, &r.Kind, &r.Title, &r.Content, &r.Excerpt,
			&r.ContentFormat, &r.CreatedGMT); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRevision stores r.
func InsertRevision(ctx context.Context, q Querier, r *Revision) error {
	res, err := q.ExecContext(ctx, `INSERT INTO revisions (post_id, author_id, kind, title, content, excerpt, content_format, created_gmt)
		VALUES (?,?,?,?,?,?,?,?)`, r.PostID, r.AuthorID, r.Kind, r.Title, r.Content, r.Excerpt, contentFormat(r.ContentFormat), r.CreatedGMT)
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// Revisions returns the revisions (not autosaves) of a post, newest first.
func Revisions(ctx context.Context, q Querier, postID int64) ([]Revision, error) {
	return queryRevisions(ctx, q, `SELECT `+revisionColumns+` FROM revisions
		WHERE post_id = ? AND kind = 'revision' ORDER BY created_gmt DESC, id DESC`, postID)
}

// GetRevision returns one revision or autosave, or sql.ErrNoRows.
func GetRevision(ctx context.Context, q Querier, id int64) (*Revision, error) {
	rs, err := queryRevisions(ctx, q, `SELECT `+revisionColumns+` FROM revisions WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &rs[0], nil
}

// Autosave returns the autosave of a post by a user, or nil.
func Autosave(ctx context.Context, q Querier, postID, userID int64) (*Revision, error) {
	rs, err := queryRevisions(ctx, q, `SELECT `+revisionColumns+` FROM revisions
		WHERE post_id = ? AND author_id = ? AND kind = 'autosave' ORDER BY id DESC LIMIT 1`, postID, userID)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

// SetAutosave replaces the autosave of r.PostID by r.AuthorID with r.
func SetAutosave(ctx context.Context, q Querier, r *Revision) error {
	if err := DeleteAutosave(ctx, q, r.PostID, r.AuthorID); err != nil {
		return err
	}
	r.Kind = "autosave"
	return InsertRevision(ctx, q, r)
}

// DeleteAutosave removes a user's autosave of a post.
func DeleteAutosave(ctx context.Context, q Querier, postID, userID int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM revisions WHERE post_id = ? AND author_id = ? AND kind = 'autosave'`, postID, userID)
	return err
}

// PruneRevisions keeps the newest keep revisions of a post.
func PruneRevisions(ctx context.Context, q Querier, postID int64, keep int) error {
	_, err := q.ExecContext(ctx, `DELETE FROM revisions WHERE post_id = ? AND kind = 'revision' AND id NOT IN (
		SELECT id FROM revisions WHERE post_id = ? AND kind = 'revision' ORDER BY created_gmt DESC, id DESC LIMIT ?)`,
		postID, postID, keep)
	return err
}

// AdminCommentQuery selects comments for the moderation list.
type AdminCommentQuery struct {
	Status string // "" = approved + pending (WordPress "All")
	Search string
	PostID int64
	UserID int64 // "Mine"
	Limit  int
	Offset int
}

func (cq AdminCommentQuery) where() (string, []any) {
	var conds []string
	var args []any
	if cq.Status == "" {
		conds = append(conds, "status IN ('approved', 'pending')")
	} else {
		conds = append(conds, "status = ?")
		args = append(args, cq.Status)
	}
	if s := strings.TrimSpace(cq.Search); s != "" {
		like := "%" + escapeLike(s) + "%"
		conds = append(conds, `(author LIKE ? ESCAPE '\' OR author_email LIKE ? ESCAPE '\' OR author_url LIKE ? ESCAPE '\'
			OR author_ip LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like, like)
	}
	if cq.PostID > 0 {
		conds = append(conds, "post_id = ?")
		args = append(args, cq.PostID)
	}
	if cq.UserID > 0 {
		conds = append(conds, "user_id = ?")
		args = append(args, cq.UserID)
	}
	return strings.Join(conds, " AND "), args
}

// AdminComments returns one page of comments, newest first, and the total.
func AdminComments(ctx context.Context, q Querier, cq AdminCommentQuery) ([]Comment, int, error) {
	where, args := cq.where()
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT ` + commentColumns + ` FROM comments WHERE ` + where + ` ORDER BY date_gmt DESC, id DESC`
	if cq.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, cq.Limit, cq.Offset)
	}
	cs, err := queryComments(ctx, q, query, args...)
	return cs, total, err
}

// CommentStatusCounts counts comments per status (optionally of one post).
func CommentStatusCounts(ctx context.Context, q Querier, postID int64) (map[string]int, error) {
	query := `SELECT status, COUNT(*) FROM comments`
	var args []any
	if postID > 0 {
		query += ` WHERE post_id = ?`
		args = append(args, postID)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// GetComment returns one comment, or sql.ErrNoRows.
func GetComment(ctx context.Context, q Querier, id int64) (*Comment, error) {
	cs, err := queryComments(ctx, q, `SELECT `+commentColumns+` FROM comments WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &cs[0], nil
}

// UpdateComment stores the editable fields of c.
func UpdateComment(ctx context.Context, q Querier, c *Comment) error {
	_, err := q.ExecContext(ctx, `UPDATE comments SET status = ?, author = ?, author_email = ?, author_url = ?,
		date_local = ?, date_gmt = ?, content = ? WHERE id = ?`,
		c.Status, c.Author, c.AuthorEmail, c.AuthorURL, c.Date, c.DateGMT, c.Content, c.ID)
	return err
}

// DeleteComment removes a comment; its replies move up to its parent like
// wp_delete_comment does.
func DeleteComment(ctx context.Context, q Querier, id int64) error {
	var parent int64
	err := q.QueryRowContext(ctx, `SELECT parent_id FROM comments WHERE id = ?`, id).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `UPDATE comments SET parent_id = ? WHERE parent_id = ?`, parent, id); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM comments WHERE id = ?`, id)
	return err
}

// SetCommentStatus changes a comment's status. Moving to trash or spam
// remembers the previous status; restoring (status "") returns to it.
func SetCommentStatus(ctx context.Context, q Querier, id int64, status string) error {
	var err error
	switch status {
	case CommentTrash, CommentSpam:
		_, err = q.ExecContext(ctx, `UPDATE comments SET prev_status = CASE WHEN status IN ('trash', 'spam') THEN prev_status ELSE status END,
			status = ? WHERE id = ?`, status, id)
	case "":
		_, err = q.ExecContext(ctx, `UPDATE comments SET status = CASE WHEN prev_status IN ('approved', 'pending') THEN prev_status ELSE 'pending' END,
			prev_status = '' WHERE id = ?`, id)
	default:
		_, err = q.ExecContext(ctx, `UPDATE comments SET status = ?, prev_status = '' WHERE id = ?`, status, id)
	}
	return err
}

// CommentIDsWithStatus returns the ids of all comments with status.
func CommentIDsWithStatus(ctx context.Context, q Querier, status string) ([]int64, error) {
	return queryIDs(ctx, q, `SELECT id FROM comments WHERE status = ?`, status)
}

// PendingCommentCounts counts pending comments per post.
func PendingCommentCounts(ctx context.Context, q Querier, postIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(postIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(postIDs))
	for i, id := range postIDs {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT post_id, COUNT(*) FROM comments WHERE status = 'pending'
		AND post_id IN (`+placeholders(len(postIDs))+`) GROUP BY post_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// MediaQuery selects attachments for the media picker.
type MediaQuery struct {
	Search     string
	ImagesOnly bool
	Type       string // "image", "audio", "video", "document" (other), "" = all
	Month      string // "YYYYMM" of the local upload date
	Unattached bool
	// VisibleTo limits the list to the user's own files and those attached
	// to published posts (access tokens).
	VisibleTo int64
	Limit     int
	Offset    int
}

// AdminMedia returns attachments (newest first) with their file details
// and the total count.
func AdminMedia(ctx context.Context, q Querier, mq MediaQuery) ([]Post, map[int64]*Media, int, error) {
	conds := []string{"type = 'attachment'"}
	var args []any
	if s := strings.TrimSpace(mq.Search); s != "" {
		like := "%" + escapeLike(s) + "%"
		conds = append(conds, `(title LIKE ? ESCAPE '\' OR excerpt LIKE ? ESCAPE '\' OR id IN (SELECT post_id FROM media WHERE file LIKE ? ESCAPE '\' OR alt LIKE ? ESCAPE '\'))`)
		args = append(args, like, like, like, like)
	}
	if mq.ImagesOnly || mq.Type == "image" {
		conds = append(conds, "mime_type LIKE 'image/%'")
	}
	switch mq.Type {
	case "audio":
		conds = append(conds, "mime_type LIKE 'audio/%'")
	case "video":
		conds = append(conds, "mime_type LIKE 'video/%'")
	case "document":
		conds = append(conds, "mime_type NOT LIKE 'image/%' AND mime_type NOT LIKE 'audio/%' AND mime_type NOT LIKE 'video/%'")
	}
	if len(mq.Month) == 6 {
		conds = append(conds, "substr(date_local, 1, 4) = ? AND substr(date_local, 6, 2) = ?")
		args = append(args, mq.Month[:4], mq.Month[4:])
	}
	if mq.Unattached {
		conds = append(conds, "parent_id = 0")
	}
	if mq.VisibleTo > 0 {
		conds = append(conds, "(author_id = ? OR parent_id IN (SELECT id FROM posts WHERE status = 'publish'))")
		args = append(args, mq.VisibleTo)
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM posts WHERE `+where, args...).Scan(&total); err != nil {
		return nil, nil, 0, err
	}
	posts, err := queryPosts(ctx, q, `SELECT `+postColumns+` FROM posts WHERE `+where+
		` ORDER BY date_gmt DESC, id DESC LIMIT ? OFFSET ?`, append(args, mq.Limit, mq.Offset)...)
	if err != nil {
		return nil, nil, 0, err
	}
	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	media, err := MediaByIDs(ctx, q, ids)
	return posts, media, total, err
}
