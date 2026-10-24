package db

import (
	"context"
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
