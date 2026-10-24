package db

import (
	"context"
	"database/sql"
	"fmt"
)

const termColumns = `id, taxonomy, slug, name, description, parent_id, count`

func queryTerms(ctx context.Context, q Querier, query string, args ...any) ([]Term, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Term
	for rows.Next() {
		var t Term
		if err := rows.Scan(&t.ID, &t.Taxonomy, &t.Slug, &t.Name, &t.Description, &t.ParentID, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertTerm inserts t, keeping a non-zero t.ID.
func InsertTerm(ctx context.Context, q Querier, t *Term) error {
	var id any
	if t.ID != 0 {
		id = t.ID
	}
	res, err := q.ExecContext(ctx, `INSERT INTO terms (`+termColumns+`) VALUES (?,?,?,?,?,?,?)`,
		id, t.Taxonomy, t.Slug, t.Name, t.Description, t.ParentID, t.Count)
	if err != nil {
		return fmt.Errorf("insert term %d %q: %w", t.ID, t.Slug, err)
	}
	if t.ID == 0 {
		t.ID, err = res.LastInsertId()
	}
	return err
}

// AddPostTerm links a post to a term.
func AddPostTerm(ctx context.Context, q Querier, postID, termID int64, order int) error {
	_, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO post_terms (post_id, term_id, term_order) VALUES (?, ?, ?)`, postID, termID, order)
	return err
}

// Terms returns all terms of a taxonomy ordered by name.
func Terms(ctx context.Context, q Querier, taxonomy string) ([]Term, error) {
	return queryTerms(ctx, q, `SELECT `+termColumns+` FROM terms WHERE taxonomy = ? ORDER BY name COLLATE NOCASE, id`, taxonomy)
}

// TermBySlug returns the term with slug in taxonomy, or nil.
func TermBySlug(ctx context.Context, q Querier, taxonomy, slug string) (*Term, error) {
	ts, err := queryTerms(ctx, q, `SELECT `+termColumns+` FROM terms WHERE taxonomy = ? AND slug = ?`, taxonomy, slug)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

// GetTerm returns the term with id, or nil.
func GetTerm(ctx context.Context, q Querier, id int64) (*Term, error) {
	ts, err := queryTerms(ctx, q, `SELECT `+termColumns+` FROM terms WHERE id = ?`, id)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

// PostTerms returns the terms of the given posts, keyed by post id, in
// name order per taxonomy.
func PostTerms(ctx context.Context, q Querier, postIDs []int64) (map[int64][]Term, error) {
	out := make(map[int64][]Term, len(postIDs))
	if len(postIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(postIDs))
	for i, id := range postIDs {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT pt.post_id, t.id, t.taxonomy, t.slug, t.name, t.description, t.parent_id, t.count
		FROM post_terms pt JOIN terms t ON t.id = pt.term_id
		WHERE pt.post_id IN (`+placeholders(len(postIDs))+`)
		ORDER BY t.name COLLATE NOCASE, t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var t Term
		if err := rows.Scan(&pid, &t.ID, &t.Taxonomy, &t.Slug, &t.Name, &t.Description, &t.ParentID, &t.Count); err != nil {
			return nil, err
		}
		out[pid] = append(out[pid], t)
	}
	return out, rows.Err()
}

// RecountTerms recomputes terms.count as the number of published posts.
func RecountTerms(ctx context.Context, q Querier) error {
	_, err := q.ExecContext(ctx, `UPDATE terms SET count = (
		SELECT COUNT(*) FROM post_terms pt JOIN posts p ON p.id = pt.post_id
		WHERE pt.term_id = terms.id AND p.status = 'publish' AND p.type = 'post')`)
	return err
}

// AdminTermQuery selects terms for the categories/tags screens.
type AdminTermQuery struct {
	Taxonomy string
	Search   string // name, slug or description contains
	OrderBy  string // "name" (default), "slug", "count", "description"
	Desc     bool
}

// AdminTerms returns the matching terms of a taxonomy (all of them; the
// screens paginate the hierarchy themselves).
func AdminTerms(ctx context.Context, q Querier, tq AdminTermQuery) ([]Term, error) {
	where, args := `taxonomy = ?`, []any{tq.Taxonomy}
	if tq.Search != "" {
		like := "%" + escapeLike(tq.Search) + "%"
		where += ` AND (name LIKE ? ESCAPE '\' OR slug LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\')`
		args = append(args, like, like, like)
	}
	dir := "ASC"
	if tq.Desc {
		dir = "DESC"
	}
	order := "name COLLATE NOCASE " + dir
	switch tq.OrderBy {
	case "slug":
		order = "slug " + dir
	case "count":
		order = "count " + dir + ", name COLLATE NOCASE"
	case "description":
		order = "description COLLATE NOCASE " + dir
	}
	return queryTerms(ctx, q, `SELECT `+termColumns+` FROM terms WHERE `+where+` ORDER BY `+order+`, id`, args...)
}

// UpdateTerm stores name, slug, description and parent of t.
func UpdateTerm(ctx context.Context, q Querier, t *Term) error {
	_, err := q.ExecContext(ctx, `UPDATE terms SET name = ?, slug = ?, description = ?, parent_id = ? WHERE id = ?`,
		t.Name, t.Slug, t.Description, t.ParentID, t.ID)
	return err
}

// TermSlugTaken reports whether slug is used by another term of the
// taxonomy than except.
func TermSlugTaken(ctx context.Context, q Querier, taxonomy, slug string, except int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM terms WHERE taxonomy = ? AND slug = ? AND id != ?`,
		taxonomy, slug, except).Scan(&n)
	return n > 0, err
}

// AddTermSlugRedirect records an old slug of a term (replacing an older
// redirect of the same slug) and drops a redirect of its new slug.
func AddTermSlugRedirect(ctx context.Context, q Querier, taxonomy, oldSlug, newSlug string, termID int64) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM term_slug_redirects WHERE taxonomy = ? AND old_slug = ?`, taxonomy, newSlug); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO term_slug_redirects (taxonomy, old_slug, term_id) VALUES (?, ?, ?)
		ON CONFLICT (taxonomy, old_slug) DO UPDATE SET term_id = excluded.term_id`, taxonomy, oldSlug, termID)
	return err
}

// TermSlugRedirects returns old slug → term id of a taxonomy.
func TermSlugRedirects(ctx context.Context, q Querier, taxonomy string) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT old_slug, term_id FROM term_slug_redirects WHERE taxonomy = ?`, taxonomy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var s string
		var id int64
		if err := rows.Scan(&s, &id); err != nil {
			return nil, err
		}
		out[s] = id
	}
	return out, rows.Err()
}

// DeleteTerm deletes a term inside tx. Child categories move up to the
// term's parent; posts that lose their last category get fallback (the
// default category; 0 = none).
func DeleteTerm(ctx context.Context, tx *sql.Tx, t *Term, fallback int64) error {
	posts, err := queryIDs(ctx, tx, `SELECT post_id FROM post_terms WHERE term_id = ?`, t.ID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE terms SET parent_id = ? WHERE parent_id = ? AND taxonomy = ?`, t.ParentID, t.ID, t.Taxonomy); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM terms WHERE id = ?`, t.ID); err != nil {
		return err
	}
	if t.Taxonomy != TaxCategory || fallback == 0 || fallback == t.ID {
		return nil
	}
	for _, id := range posts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO post_terms (post_id, term_id, term_order)
			SELECT ?, ?, 0 WHERE NOT EXISTS (SELECT 1 FROM post_terms pt JOIN terms t ON t.id = pt.term_id
				WHERE pt.post_id = ? AND t.taxonomy = 'category')`, id, fallback, id); err != nil {
			return err
		}
	}
	return nil
}
