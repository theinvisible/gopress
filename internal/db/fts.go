package db

import (
	"context"
	"strings"
)

// searchStopwords are WordPress' default search stopwords (en_US).
var searchStopwords = map[string]bool{
	"about": true, "an": true, "are": true, "as": true, "at": true, "be": true, "by": true, "com": true,
	"for": true, "from": true, "how": true, "in": true, "is": true, "it": true, "of": true, "on": true,
	"or": true, "that": true, "the": true, "this": true, "to": true, "was": true, "what": true,
	"when": true, "where": true, "who": true, "will": true, "with": true, "www": true,
}

// SearchTerms splits a query like WP_Query::parse_search(): quoted
// phrases stay together, stopwords and single letters are dropped; if
// nothing remains (or more than 9 terms) the whole query is one term.
func SearchTerms(q string) []string {
	q = strings.NewReplacer("\r", "", "\n", "").Replace(q)
	var raw []string
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '"':
			end := strings.IndexByte(q[i+1:], '"')
			if end < 0 {
				raw = append(raw, q[i:])
				i = len(q)
			} else {
				raw = append(raw, q[i:i+end+2])
				i += end + 2
			}
		case c == '\t' || c == ' ' || c == ',' || c == '+':
			i++
		default:
			j := i
			for j < len(q) && !strings.ContainsRune("\t \",+", rune(q[j])) {
				j++
			}
			raw = append(raw, q[i:j])
			i = j
		}
	}
	var terms []string
	for _, t := range raw {
		if strings.HasPrefix(t, `"`) {
			t = strings.Trim(t, `"`)
		} else {
			t = strings.Trim(t, "\"'\n\r ")
		}
		if t == "" || len(t) == 1 && (t[0] >= 'a' && t[0] <= 'z' || t[0] >= 'A' && t[0] <= 'Z' || t[0] == '-') {
			continue
		}
		if searchStopwords[strings.ToLower(t)] {
			continue
		}
		terms = append(terms, t)
	}
	if len(terms) == 0 || len(terms) > 9 {
		return []string{q}
	}
	return terms
}

func escLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// Search runs a WordPress-compatible search (LIKE on title, excerpt and
// content of published posts, pages and attachments) and returns one page
// of ids in WordPress' relevance order plus the total count.
func Search(ctx context.Context, q Querier, query string, limit, offset int) ([]int64, int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, 0, nil
	}
	terms := SearchTerms(query)
	var where []string
	var args []any
	var titleLikes []string
	var titleArgs []any
	for _, t := range terms {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			like := "%" + escLike(t[1:]) + "%"
			where = append(where, `(p.title NOT LIKE ? ESCAPE '\' AND p.excerpt NOT LIKE ? ESCAPE '\' AND p.content NOT LIKE ? ESCAPE '\')`)
			args = append(args, like, like, like)
			continue
		}
		like := "%" + escLike(t) + "%"
		where = append(where, `(p.title LIKE ? ESCAPE '\' OR p.excerpt LIKE ? ESCAPE '\' OR p.content LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
		titleLikes = append(titleLikes, `p.title LIKE ? ESCAPE '\'`)
		titleArgs = append(titleArgs, like)
	}
	from := ` FROM posts p LEFT JOIN posts parent ON parent.id = p.parent_id
		WHERE ` + strings.Join(where, " AND ") + ` AND p.password = ''
		AND ((p.type IN ('post', 'page') AND p.status = 'publish')
		  OR (p.type = 'attachment' AND p.status = 'inherit' AND (p.parent_id = 0 OR parent.status = 'publish')))`
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Relevance (WP_Query::parse_search_order()).
	var order string
	var orderArgs []any
	switch {
	case len(terms) > 1:
		var cases []string
		if !strings.HasPrefix(query, "-") && !strings.Contains(query, " -") {
			sentence := "%" + escLike(query) + "%"
			cases = append(cases, `WHEN p.title LIKE ? ESCAPE '\' THEN 1`)
			orderArgs = append(orderArgs, sentence)
			if len(titleLikes) < 7 && len(titleLikes) > 0 {
				cases = append(cases, "WHEN "+strings.Join(titleLikes, " AND ")+" THEN 2")
				orderArgs = append(orderArgs, titleArgs...)
				if len(titleLikes) > 1 {
					cases = append(cases, "WHEN "+strings.Join(titleLikes, " OR ")+" THEN 3")
					orderArgs = append(orderArgs, titleArgs...)
				}
			}
			cases = append(cases, `WHEN p.excerpt LIKE ? ESCAPE '\' THEN 4`, `WHEN p.content LIKE ? ESCAPE '\' THEN 5`)
			orderArgs = append(orderArgs, sentence, sentence)
		} else if len(titleLikes) < 7 && len(titleLikes) > 0 {
			cases = append(cases, "WHEN "+strings.Join(titleLikes, " AND ")+" THEN 2")
			orderArgs = append(orderArgs, titleArgs...)
		}
		order = "(CASE " + strings.Join(cases, " ") + " ELSE 6 END), "
	case len(titleLikes) == 1:
		order = titleLikes[0] + " DESC, "
		orderArgs = append(orderArgs, titleArgs[0])
	}
	all := append(append(append([]any{}, args...), orderArgs...), limit, offset)
	ids, err := queryIDs(ctx, q, `SELECT p.id`+from+` ORDER BY `+order+`p.date_gmt DESC, p.id DESC LIMIT ? OFFSET ?`, all...)
	return ids, total, err
}
