package db

import (
	"context"
	"strings"
)

// IndexPost (re)indexes a post for search. text must be plain text.
func IndexPost(ctx context.Context, q Querier, id int64, title, text string) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM posts_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO posts_fts (rowid, title, body) VALUES (?, ?, ?)`, id, title, text)
	return err
}

// UnindexPost removes a post from the search index.
func UnindexPost(ctx context.Context, q Querier, id int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM posts_fts WHERE rowid = ?`, id)
	return err
}

// Search returns ids of published posts and pages matching the terms of
// query, title matches first, then newest first, and the total count.
// Every term must match (prefix match), like WordPress' AND search.
func Search(ctx context.Context, q Querier, query string, limit, offset int) ([]int64, int, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, 0, nil
	}
	const from = ` FROM posts_fts f JOIN posts p ON p.id = f.rowid
		WHERE posts_fts MATCH ? AND p.status = 'publish' AND p.type IN ('post', 'page')`
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*)`+from, match).Scan(&total); err != nil {
		return nil, 0, err
	}
	ids, err := queryIDs(ctx, q, `SELECT p.id`+from+`
		ORDER BY (SELECT COUNT(*) FROM posts_fts t WHERE t.rowid = p.id AND t.title MATCH ?) DESC, p.date_gmt DESC
		LIMIT ? OFFSET ?`, match, match, limit, offset)
	return ids, total, err
}

// ftsQuery turns user input into an FTS5 query: each word becomes a quoted
// prefix term, so FTS syntax characters in the input are harmless.
func ftsQuery(s string) string {
	var terms []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, `"'.,;:!?()[]{}`)
		w = strings.ReplaceAll(w, `"`, "")
		if w == "" {
			continue
		}
		terms = append(terms, `"`+w+`"*`)
	}
	return strings.Join(terms, " ")
}
