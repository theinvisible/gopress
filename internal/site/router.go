package site

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// permalinkMatcher matches request paths against the permalink structure.
type permalinkMatcher struct {
	re     *regexp.Regexp
	tokens []string
}

var permalinkTokens = map[string]string{
	"%year%": `([0-9]{4})`, "%monthnum%": `([0-9]{1,2})`, "%day%": `([0-9]{1,2})`,
	"%hour%": `([0-9]{1,2})`, "%minute%": `([0-9]{1,2})`, "%second%": `([0-9]{1,2})`,
	"%postname%": `([^/]+)`, "%post_id%": `([0-9]+)`, "%category%": `(.+?)`, "%author%": `([^/]+)`,
}

var rePermalinkToken = regexp.MustCompile(`%[a-z_]+%`)

func newPermalinkMatcher(structure string) *permalinkMatcher {
	m := &permalinkMatcher{}
	structure = "/" + strings.Trim(structure, "/") + "/"
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range rePermalinkToken.FindAllStringIndex(structure, -1) {
		b.WriteString(regexp.QuoteMeta(structure[last:loc[0]]))
		tok := structure[loc[0]:loc[1]]
		if re, ok := permalinkTokens[tok]; ok {
			b.WriteString(re)
			m.tokens = append(m.tokens, tok)
		} else {
			b.WriteString(regexp.QuoteMeta(tok))
		}
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(structure[last:]))
	b.WriteString("$")
	m.re = regexp.MustCompile(b.String())
	return m
}

// match returns the token values if path (with leading and trailing slash)
// matches the structure.
func (m *permalinkMatcher) match(path string) map[string]string {
	sub := m.re.FindStringSubmatch(path)
	if sub == nil {
		return nil
	}
	out := make(map[string]string, len(m.tokens))
	for i, tok := range m.tokens {
		out[tok] = sub[i+1]
	}
	return out
}

// route is a parsed request.
type route struct {
	segs   []string // path segments without paging/feed suffixes
	paged  int      // /page/N/
	cpage  int      // /comment-page-N/
	feed   string   // "" or feed type ("feed", "rss2", "atom", ...)
	query  url.Values
	origin string // original path
}

func parseRoute(r *http.Request) *route {
	rt := &route{query: r.URL.Query(), origin: r.URL.Path, paged: 1}
	segs := strings.FieldsFunc(r.URL.Path, func(c rune) bool { return c == '/' })
	// Suffixes, outermost first: feed, page/N, comment-page-N.
	if n := len(segs); n >= 1 && segs[n-1] == "feed" {
		rt.feed, segs = "feed", segs[:n-1]
	} else if n >= 2 && segs[n-2] == "feed" && isFeedType(segs[n-1]) {
		rt.feed, segs = segs[n-1], segs[:n-2]
	}
	if n := len(segs); n >= 2 && segs[n-2] == "page" {
		if p, err := strconv.Atoi(segs[n-1]); err == nil && p > 0 {
			rt.paged, segs = p, segs[:n-2]
		}
	}
	if n := len(segs); n >= 1 && strings.HasPrefix(segs[n-1], "comment-page-") {
		if p, err := strconv.Atoi(strings.TrimPrefix(segs[n-1], "comment-page-")); err == nil && p > 0 {
			rt.cpage, segs = p, segs[:n-1]
		}
	}
	rt.segs = segs
	return rt
}

func isFeedType(s string) bool {
	switch s {
	case "feed", "rss2", "rss", "rdf", "atom":
		return true
	}
	return false
}

// handle routes a GET request to a response.
func (s *Site) handle(st *state, r *http.Request) *response {
	rq := newRequest(s, st, r)
	p := r.URL.Path

	switch p {
	case "/robots.txt":
		return rq.robots()
	case "/sitemap.xml", "/sitemap.xml.gz", "/wp-sitemap.xml", "/sitemap_index.xml":
		return rq.sitemapIndex(p)
	case "/wp-login.php", "/wp-admin", "/wp-signup.php", "/wp-register.php":
		return rq.notFound()
	}
	if strings.HasPrefix(p, "/wp-sitemap-") && strings.HasSuffix(p, ".xml") {
		return rq.sitemap(strings.TrimSuffix(strings.TrimPrefix(p, "/wp-sitemap-"), ".xml"))
	}
	if strings.HasPrefix(p, "/wp-admin/") || strings.HasPrefix(p, "/wp-includes/") || strings.HasPrefix(p, "/wp-content/") {
		return rq.notFound()
	}

	// Paths need a trailing slash (user_trailingslashit), except files.
	if !strings.HasSuffix(p, "/") {
		last := p[strings.LastIndexByte(p, '/')+1:]
		if strings.Contains(last, ".") {
			return rq.notFound()
		}
		target := p + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		return redirect(st.links.URL(target))
	}

	rt := parseRoute(r)
	if len(rt.segs) == 0 {
		if resp := rq.queryRoute(rt); resp != nil {
			return resp
		}
	}
	return rq.pathRoute(rt)
}

// queryRoute handles WordPress' plain query URLs on the home path.
func (rq *request) queryRoute(rt *route) *response {
	q := rt.query
	l := rq.st.links
	id := func(k string) int64 {
		n, _ := strconv.ParseInt(q.Get(k), 10, 64)
		return n
	}
	if q.Has("s") {
		paged := rt.paged
		if n, err := strconv.Atoi(q.Get("paged")); err == nil && n > 1 {
			paged = n
		}
		if rt.feed != "" || q.Get("feed") != "" {
			return rq.feed(feedSearch, nil, q.Get("s"))
		}
		return rq.search(q.Get("s"), paged)
	}
	if f := q.Get("feed"); f != "" {
		switch f {
		case "comments-rss2", "comments-atom":
			return redirect(l.URL("/comments/feed/"))
		default:
			return redirect(l.URL("/feed/"))
		}
	}
	for _, k := range []string{"p", "page_id", "attachment_id"} {
		if q.Has(k) {
			p := rq.st.ix.posts[id(k)]
			if p == nil || !rq.st.ix.isPublic(p) {
				return rq.notFound()
			}
			return redirect(l.PostURL(p))
		}
	}
	if q.Has("cat") {
		if t := rq.st.ix.terms[id("cat")]; t != nil && t.Taxonomy == db.TaxCategory {
			return redirect(l.TermURL(t))
		}
		return rq.notFound()
	}
	if q.Has("tag") {
		if t := rq.st.ix.tagBySlug[q.Get("tag")]; t != nil {
			return redirect(l.TermURL(t))
		}
		return rq.notFound()
	}
	if q.Has("author") {
		if u := rq.st.ix.users[id("author")]; u != nil {
			return redirect(l.AuthorURL(u))
		}
		return rq.notFound()
	}
	if m := q.Get("m"); len(m) >= 4 {
		y, _ := strconv.Atoi(m[:4])
		mo, d := 0, 0
		if len(m) >= 6 {
			mo, _ = strconv.Atoi(m[4:6])
		}
		if len(m) >= 8 {
			d, _ = strconv.Atoi(m[6:8])
		}
		return redirect(l.DateURL(y, mo, d))
	}
	if n, err := strconv.Atoi(q.Get("paged")); err == nil && n > 1 {
		return redirect(Paged(l.Home(), n))
	}
	return nil
}

// pathRoute resolves pretty permalinks.
func (rq *request) pathRoute(rt *route) *response {
	st := rq.st
	segs := rt.segs
	if rt.feed != "" && rt.feed != "feed" && rt.feed != "rss2" {
		// Atom/RDF/RSS 0.92 are served as RSS2 at the canonical feed URL.
		return redirect(FeedURL(st.links.URL("/" + strings.Join(segs, "/") + "/")))
	}
	if len(segs) == 0 {
		if rt.feed != "" {
			return rq.feed(feedPosts, nil, "")
		}
		return rq.home(rt.paged)
	}

	switch {
	case len(segs) == 1 && segs[0] == "comments" && rt.feed != "":
		return rq.feed(feedComments, nil, "")
	case segs[0] == st.s.CategoryBase && len(segs) > 1:
		t := st.ix.catByPath[strings.Join(segs[1:], "/")]
		if t == nil {
			// WordPress also accepts the last segment alone.
			for _, c := range st.ix.cats {
				if c.Slug == segs[len(segs)-1] {
					return redirect(rq.withSuffix(st.links.TermURL(c), rt))
				}
			}
			return rq.notFound()
		}
		if rt.feed != "" {
			return rq.feed(feedTerm, t, "")
		}
		return rq.termArchive(t, rt.paged)
	case segs[0] == st.s.TagBase && len(segs) == 2:
		t := st.ix.tagBySlug[segs[1]]
		if t == nil {
			return rq.notFound()
		}
		if rt.feed != "" {
			return rq.feed(feedTerm, t, "")
		}
		return rq.termArchive(t, rt.paged)
	case segs[0] == "author" && len(segs) == 2:
		u := st.ix.userByNice[segs[1]]
		if u == nil {
			return rq.notFound()
		}
		if rt.feed != "" {
			return rq.feed(feedAuthor, u, "")
		}
		return rq.authorArchive(u, rt.paged)
	case segs[0] == "search" && len(segs) == 2:
		if rt.feed != "" {
			return rq.feed(feedSearch, nil, segs[1])
		}
		return rq.search(segs[1], rt.paged)
	}

	path := "/" + strings.Join(segs, "/") + "/"
	// Single post, possibly followed by an attachment slug.
	if resp := rq.matchPost(path, rt); resp != nil {
		return resp
	}
	if len(segs) > 1 {
		parentPath := "/" + strings.Join(segs[:len(segs)-1], "/") + "/"
		if parent := rq.resolveSingular(parentPath); parent != nil {
			if att := rq.childAttachment(parent, segs[len(segs)-1]); att != nil {
				return rq.singular(att, rt)
			}
		}
		if len(segs) > 2 && segs[len(segs)-2] == "attachment" {
			grand := "/" + strings.Join(segs[:len(segs)-2], "/") + "/"
			if parent := rq.resolveSingular(grand); parent != nil {
				if att := rq.childAttachment(parent, segs[len(segs)-1]); att != nil {
					return rq.singular(att, rt)
				}
			}
		}
	}
	// Date archives.
	if y, m, d, ok := dateSegments(segs); ok {
		if rt.feed != "" {
			return rq.feed(feedPosts, nil, "")
		}
		return rq.dateArchive(y, m, d, rt.paged)
	}
	// Pages by hierarchical path.
	if page := st.ix.pagesByPath[strings.Join(segs, "/")]; page != nil {
		if rt.feed != "" {
			return rq.feed(feedPostComments, page, "")
		}
		return rq.singular(page, rt)
	}
	// Unattached attachments at the top level.
	if len(segs) == 1 {
		for _, a := range st.ix.attachBySlug[segs[0]] {
			if a.ParentID == 0 {
				return rq.singular(a, rt)
			}
		}
	}
	return rq.guess404(segs)
}

// resolveSingular finds the post or page at path (no suffixes).
func (rq *request) resolveSingular(path string) *db.Post {
	if vals := rq.st.perma.match(path); vals != nil {
		if p := rq.findPost(vals); p != nil {
			return p
		}
	}
	return rq.st.ix.pagesByPath[strings.Trim(path, "/")]
}

// matchPost handles a single post permalink.
func (rq *request) matchPost(path string, rt *route) *response {
	vals := rq.st.perma.match(path)
	if vals == nil {
		return nil
	}
	p := rq.findPost(vals)
	if p == nil {
		return nil
	}
	if rt.feed != "" {
		return rq.feed(feedPostComments, p, "")
	}
	return rq.singular(p, rt)
}

// findPost returns the published post matching the permalink values.
func (rq *request) findPost(vals map[string]string) *db.Post {
	ix := rq.st.ix
	var candidates []*db.Post
	if id, ok := vals["%post_id%"]; ok {
		n, _ := strconv.ParseInt(id, 10, 64)
		if p := ix.posts[n]; p != nil && p.Type == db.TypePost && p.Status == db.StatusPublish {
			candidates = append(candidates, p)
		}
	} else if name, ok := vals["%postname%"]; ok {
		candidates = ix.postsBySlug[name]
	}
	for _, p := range candidates {
		d := p.Date.Time
		if v, ok := vals["%year%"]; ok && atoi(v) != d.Year() {
			continue
		}
		if v, ok := vals["%monthnum%"]; ok && atoi(v) != int(d.Month()) {
			continue
		}
		if v, ok := vals["%day%"]; ok && atoi(v) != d.Day() {
			continue
		}
		if v, ok := vals["%postname%"]; ok && v != p.Slug {
			continue
		}
		return p
	}
	return nil
}

func (rq *request) childAttachment(parent *db.Post, slug string) *db.Post {
	for _, a := range rq.st.ix.attachBySlug[slug] {
		if a.ParentID == parent.ID {
			return a
		}
	}
	return nil
}

// withSuffix re-applies paging/feed suffixes of rt to a canonical URL.
func (rq *request) withSuffix(base string, rt *route) string {
	if rt.feed != "" {
		return FeedURL(base)
	}
	return Paged(base, rt.paged)
}

// guess404 redirects unknown URLs whose last segment is a current or
// former slug of exactly one published post or page; otherwise 404.
func (rq *request) guess404(segs []string) *response {
	ix := rq.st.ix
	slug := segs[len(segs)-1]
	var found []*db.Post
	found = append(found, ix.postsBySlug[slug]...)
	for _, p := range ix.pages {
		if p.Slug == slug {
			found = append(found, p)
		}
	}
	if len(found) == 1 {
		return redirect(rq.st.links.PostURL(found[0]))
	}
	if ids, err := db.SlugRedirects(rq.ctx, rq.site.db.R, slug); err == nil && len(ids) > 0 {
		if p := ix.posts[ids[0]]; p != nil && ix.isPublic(p) {
			return redirect(rq.st.links.PostURL(p))
		}
	}
	return rq.notFound()
}

func dateSegments(segs []string) (y, m, d int, ok bool) {
	if len(segs) == 0 || len(segs) > 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, len(segs))
	for i, s := range segs {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	if len(segs[0]) != 4 {
		return 0, 0, 0, false
	}
	y = nums[0]
	if len(nums) > 1 {
		m = nums[1]
		if m < 1 || m > 12 {
			return 0, 0, 0, false
		}
	}
	if len(nums) > 2 {
		d = nums[2]
		if d < 1 || d > 31 {
			return 0, 0, 0, false
		}
	}
	return y, m, d, true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
