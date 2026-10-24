package site

import (
	"context"
	"sort"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// contentIndex is an in-memory map of all routable content (ids, slugs,
// parents, terms, users). It is small (one row per post) and rebuilt
// whenever content changes; routing and URL building use it instead of
// querying the database.
type contentIndex struct {
	posts        map[int64]*db.Post // all posts, pages, attachments (content not loaded)
	postsBySlug  map[string][]*db.Post
	pagesByPath  map[string]*db.Post // "parent/child"
	attachBySlug map[string][]*db.Post
	pages        []*db.Post // published pages in menu order

	terms     map[int64]*db.Term
	catByPath map[string]*db.Term // "parent/child"
	tagBySlug map[string]*db.Term
	cats      []*db.Term // name order
	tags      []*db.Term
	// oldTermSlugs maps "taxonomy/old-slug" to the renamed term.
	oldTermSlugs map[string]*db.Term

	users       map[int64]*db.User
	userByNice  map[string]*db.User
	multiAuthor bool
}

func buildIndex(ctx context.Context, q db.Querier) (*contentIndex, error) {
	ix := &contentIndex{
		posts: map[int64]*db.Post{}, postsBySlug: map[string][]*db.Post{}, pagesByPath: map[string]*db.Post{},
		attachBySlug: map[string][]*db.Post{}, terms: map[int64]*db.Term{}, catByPath: map[string]*db.Term{},
		tagBySlug: map[string]*db.Term{}, users: map[int64]*db.User{}, userByNice: map[string]*db.User{},
	}
	rows, err := q.QueryContext(ctx, `SELECT id, type, status, slug, parent_id, author_id, title, date_local, date_gmt,
		modified_gmt, comment_status, password, menu_order, template, mime_type, comment_count, guid
		FROM posts WHERE status IN ('publish', 'inherit', 'private', 'future')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		p := &db.Post{}
		if err := rows.Scan(&p.ID, &p.Type, &p.Status, &p.Slug, &p.ParentID, &p.AuthorID, &p.Title, &p.Date, &p.DateGMT,
			&p.ModifiedGMT, &p.CommentStatus, &p.Password, &p.MenuOrder, &p.Template, &p.MimeType, &p.CommentCount, &p.GUID); err != nil {
			rows.Close()
			return nil, err
		}
		ix.posts[p.ID] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	authors := map[int64]bool{}
	for _, p := range ix.posts {
		switch p.Type {
		case db.TypePost:
			if p.Status == db.StatusPublish {
				ix.postsBySlug[p.Slug] = append(ix.postsBySlug[p.Slug], p)
				authors[p.AuthorID] = true
			}
		case db.TypePage:
			if p.Status == db.StatusPublish {
				ix.pagesByPath[ix.pagePath(p)] = p
				ix.pages = append(ix.pages, p)
			}
		case db.TypeAttachment:
			ix.attachBySlug[p.Slug] = append(ix.attachBySlug[p.Slug], p)
		}
	}
	ix.multiAuthor = len(authors) > 1
	sort.Slice(ix.pages, func(i, j int) bool {
		a, b := ix.pages[i], ix.pages[j]
		if a.MenuOrder != b.MenuOrder {
			return a.MenuOrder < b.MenuOrder
		}
		if lessText(a.Title, b.Title) != lessText(b.Title, a.Title) {
			return lessText(a.Title, b.Title)
		}
		return a.ID < b.ID
	})

	for _, tax := range []string{db.TaxCategory, db.TaxTag} {
		terms, err := db.Terms(ctx, q, tax)
		if err != nil {
			return nil, err
		}
		for i := range terms {
			t := &terms[i]
			ix.terms[t.ID] = t
			if tax == db.TaxCategory {
				ix.cats = append(ix.cats, t)
			} else {
				ix.tags = append(ix.tags, t)
				ix.tagBySlug[t.Slug] = t
			}
		}
	}
	byName := func(list []*db.Term) {
		sort.SliceStable(list, func(i, j int) bool { return lessText(list[i].Name, list[j].Name) })
	}
	byName(ix.cats)
	byName(ix.tags)
	for _, t := range ix.cats {
		ix.catByPath[ix.termPath(t)] = t
	}
	ix.oldTermSlugs = map[string]*db.Term{}
	for _, tax := range []string{db.TaxCategory, db.TaxTag} {
		old, err := db.TermSlugRedirects(ctx, q, tax)
		if err != nil {
			return nil, err
		}
		for slug, id := range old {
			if t := ix.terms[id]; t != nil {
				ix.oldTermSlugs[tax+"/"+slug] = t
			}
		}
	}

	users, err := db.Users(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range users {
		u := &users[i]
		ix.users[u.ID] = u
		ix.userByNice[u.Nicename] = u
	}
	return ix, nil
}

// pagePath returns the hierarchical path of a page ("parent/child").
func (ix *contentIndex) pagePath(p *db.Post) string {
	parts := []string{p.Slug}
	seen := map[int64]bool{p.ID: true}
	for parent := ix.posts[p.ParentID]; parent != nil && parent.Type == db.TypePage && !seen[parent.ID]; parent = ix.posts[parent.ParentID] {
		seen[parent.ID] = true
		parts = append([]string{parent.Slug}, parts...)
	}
	return strings.Join(parts, "/")
}

// termPath returns the hierarchical slug path of a category.
func (ix *contentIndex) termPath(t *db.Term) string {
	parts := []string{t.Slug}
	seen := map[int64]bool{t.ID: true}
	for parent := ix.terms[t.ParentID]; parent != nil && !seen[parent.ID]; parent = ix.terms[parent.ParentID] {
		seen[parent.ID] = true
		parts = append([]string{parent.Slug}, parts...)
	}
	return strings.Join(parts, "/")
}

// termAndChildren returns the ids of t and all its descendants.
func (ix *contentIndex) termAndChildren(t *db.Term) []int64 {
	ids := []int64{t.ID}
	for i := 0; i < len(ids); i++ {
		for _, c := range ix.cats {
			if c.ParentID == ids[i] {
				ids = append(ids, c.ID)
			}
		}
	}
	return ids
}

// childPages returns the published child pages of parent in menu order.
func (ix *contentIndex) childPages(parent int64) []*db.Post {
	var out []*db.Post
	for _, p := range ix.pages {
		if p.ParentID == parent {
			out = append(out, p)
		}
	}
	return out
}

// isPublic reports whether a post is publicly viewable.
func (ix *contentIndex) isPublic(p *db.Post) bool {
	if p == nil {
		return false
	}
	switch p.Status {
	case db.StatusPublish:
		return true
	case db.StatusInherit:
		parent := ix.posts[p.ParentID]
		return p.ParentID == 0 || parent != nil && ix.isPublic(parent)
	}
	return false
}
