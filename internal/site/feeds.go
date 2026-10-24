package site

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

type feedKind int

const (
	feedPosts feedKind = iota
	feedTerm
	feedAuthor
	feedSearch
	feedComments
	feedPostComments
)

const rssDate = "Mon, 02 Jan 2006 15:04:05 -0700"

// cdata wraps s in a CDATA section, splitting any "]]>" inside.
func cdata(s string) string {
	return "<![CDATA[" + strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>") + "]]>"
}

// xmlText escapes text for XML element content (esc_xml-ish; entities
// produced by wptexturize are converted to characters first).
func xmlText(s string) string {
	s = html.UnescapeString(s)
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

var reRootRelative = regexp.MustCompile(`(\s(?:href|src)=["'])/([^/])`)

// absolutize turns root-relative href/src URLs into absolute ones, for
// content leaving the site (feeds).
func absolutize(html, home string) string {
	return reRootRelative.ReplaceAllString(html, "${1}"+home+"/${2}")
}

func (rq *request) feed(kind feedKind, obj any, search string) *response {
	st := rq.st
	s := st.s
	l := st.links
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"
	xmlns:content="http://purl.org/rss/1.0/modules/content/"
	xmlns:wfw="http://wellformedweb.org/CommentAPI/"
	xmlns:dc="http://purl.org/dc/elements/1.1/"
	xmlns:atom="http://www.w3.org/2005/Atom"
	xmlns:sy="http://purl.org/rss/1.0/modules/syndication/"
	xmlns:slash="http://purl.org/rss/1.0/modules/slash/"
	>

<channel>
`)
	self := l.URL(rq.r.URL.Path)
	if rq.r.URL.RawQuery != "" {
		self += "?" + rq.r.URL.RawQuery
	}
	rq.env.IsFeed = true

	if kind == feedComments || kind == feedPostComments {
		return rq.commentFeed(&b, kind, obj, self)
	}

	title := s.Name
	link := l.Home()
	q := db.PostQuery{Type: db.TypePost, Limit: s.PostsPerRSS}
	var posts []db.Post
	var err error
	switch kind {
	case feedTerm:
		t := obj.(*db.Term)
		title = s.Name + " &#187; " + escKeepEntities(t.Name)
		link = l.TermURL(t)
		q.TermIDs = []int64{t.ID}
		if t.Taxonomy == db.TaxCategory {
			q.TermIDs = st.ix.termAndChildren(t)
		}
	case feedAuthor:
		u := obj.(*db.User)
		title = s.Name + " &#187; " + escKeepEntities(u.DisplayName)
		link = l.AuthorURL(u)
		q.AuthorID = u.ID
	case feedSearch:
		title = s.Name + " &#187; Search Results for &#8220;" + escKeepEntities(search) + "&#8221;"
		link = l.SearchURL(search)
		ids, _, err := db.Search(rq.ctx, rq.site.db.R, search, s.PostsPerRSS, 0)
		if err != nil {
			return errorResponse(err)
		}
		if len(ids) == 0 {
			ids = []int64{-1}
		}
		q = db.PostQuery{IDs: ids, Limit: s.PostsPerRSS}
	}
	if posts, _, err = db.ListPosts(rq.ctx, rq.site.db.R, q); err != nil {
		return errorResponse(err)
	}
	lastBuild := time.Time{}
	for _, p := range posts {
		if p.ModifiedGMT.After(lastBuild) {
			lastBuild = p.ModifiedGMT.Time
		}
	}
	fmt.Fprintf(&b, "\t<title>%s</title>\n\t<atom:link href=\"%s\" rel=\"self\" type=\"application/rss+xml\" />\n\t<link>%s</link>\n\t<description>%s</description>\n\t<lastBuildDate>%s</lastBuildDate>\n\t<language>%s</language>\n\t<sy:updatePeriod>\n\thourly\t</sy:updatePeriod>\n\t<sy:updateFrequency>\n\t1\t</sy:updateFrequency>\n\t<generator>https://github.com/theinvisible/gopress</generator>\n",
		xmlText(title), xmlText(self), xmlText(strings.TrimSuffix(link, "/")), xmlText(s.Description), lastBuild.UTC().Format(rssDate), s.Lang)

	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	terms, err := db.PostTerms(rq.ctx, rq.site.db.R, ids)
	if err != nil {
		return errorResponse(err)
	}
	for i := range posts {
		p := &posts[i]
		url := l.PostURL(p)
		rq.env.Post = p
		content := ""
		if p.Password == "" {
			content = absolutize(st.pipe.Content(rq.env, p.Content, p.ContentFormat, p.ID, render.ContentOptions{}), s.Home)
		}
		excerpt := ""
		if p.Password == "" {
			excerpt = render.StripTags(rq.st.pipe.Excerpt(rq.env, p.Content, p.ContentFormat))
			if strings.TrimSpace(p.Excerpt) != "" {
				excerpt = p.Excerpt
			}
		}
		author := ""
		if u := st.ix.users[p.AuthorID]; u != nil {
			author = u.DisplayName
		}
		fmt.Fprintf(&b, "\t<item>\n\t\t<title>%s</title>\n\t\t<link>%s</link>\n", xmlText(render.StripTags(rq.title(p.Title))), xmlText(url))
		if p.CommentStatus == "open" || p.CommentCount > 0 {
			fmt.Fprintf(&b, "\t\t\t\t\t<comments>%s#respond</comments>\n", xmlText(url))
		}
		fmt.Fprintf(&b, "\t\t\n\t\t<dc:creator>%s</dc:creator>\n\t\t<pubDate>%s</pubDate>\n", cdata(author), p.DateGMT.UTC().Format(rssDate))
		for _, t := range terms[p.ID] {
			fmt.Fprintf(&b, "\t\t<category>%s</category>\n", cdata(render.DecodeNumericEntities(t.Name)))
		}
		fmt.Fprintf(&b, "\t\t<guid isPermaLink=\"false\">%s</guid>\n\n\t\t\t\t\t<description>%s</description>\n", xmlText(rq.guid(p)), cdata(excerpt))
		if !s.RSSUseExcerpt {
			fmt.Fprintf(&b, "\t\t\t\t\t\t\t\t\t\t<content:encoded>%s</content:encoded>\n", cdata(content))
		}
		if p.CommentStatus == "open" || p.CommentCount > 0 {
			fmt.Fprintf(&b, "\t\t\t\t\t\n\t\t\t<wfw:commentRss>%s</wfw:commentRss>\n\t\t\t<slash:comments>%d</slash:comments>\n", xmlText(FeedURL(url)), p.CommentCount)
		}
		b.WriteString("\t\t\n\t\t\t\n\t\t\t</item>\n")
	}
	b.WriteString("\t</channel>\n</rss>\n")
	return feedResponse(b.String(), lastBuild)
}

func (rq *request) guid(p *db.Post) string {
	if p.GUID != "" {
		return p.GUID
	}
	return rq.st.s.Home + "/?p=" + strconv.FormatInt(p.ID, 10)
}

func feedResponse(body string, lastMod time.Time) *response {
	h := http.Header{}
	h.Set("Content-Type", "application/rss+xml; charset=UTF-8")
	if !lastMod.IsZero() {
		h.Set("Last-Modified", lastMod.UTC().Format(http.TimeFormat))
	}
	return &response{status: http.StatusOK, header: h, body: []byte(body)}
}

func (rq *request) commentFeed(b *strings.Builder, kind feedKind, obj any, self string) *response {
	st := rq.st
	s := st.s
	l := st.links
	var comments []db.Comment
	var err error
	title := "Comments for " + s.Name
	link := l.Home()
	var post *db.Post
	if kind == feedPostComments {
		post = obj.(*db.Post)
		if !st.ix.isPublic(post) || post.Password != "" {
			return rq.notFound()
		}
		title = "Comments on: " + render.StripTags(rq.title(post.Title))
		link = l.PostURL(post)
		all, err := db.PostComments(rq.ctx, rq.site.db.R, post.ID, "")
		if err != nil {
			return errorResponse(err)
		}
		for i := len(all) - 1; i >= 0 && len(comments) < s.PostsPerRSS; i-- {
			comments = append(comments, all[i])
		}
	} else if comments, err = db.RecentComments(rq.ctx, rq.site.db.R, s.PostsPerRSS); err != nil {
		return errorResponse(err)
	}
	lastBuild := time.Time{}
	if len(comments) > 0 {
		lastBuild = comments[0].DateGMT.Time
	}
	fmt.Fprintf(b, "\t<title>%s</title>\n\t<atom:link href=\"%s\" rel=\"self\" type=\"application/rss+xml\" />\n\t<link>%s</link>\n\t<description>%s</description>\n\t<lastBuildDate>%s</lastBuildDate>\n\t<sy:updatePeriod>\n\thourly\t</sy:updatePeriod>\n\t<sy:updateFrequency>\n\t1\t</sy:updateFrequency>\n\t<generator>https://github.com/theinvisible/gopress</generator>\n",
		xmlText(title), xmlText(self), xmlText(link), xmlText(s.Description), lastBuild.UTC().Format(rssDate))
	for i := range comments {
		c := &comments[i]
		p := st.ix.posts[c.PostID]
		if p == nil || !st.ix.isPublic(p) || p.Password != "" {
			continue
		}
		purl := l.PostURL(p)
		clink := purl
		if page, _ := rq.commentPageOf(c); s.PageComments {
			clink = CommentPageURL(purl, page) // feeds always name the page
		}
		itemTitle := "By: " + c.Author
		if kind == feedComments {
			itemTitle = "Comment on " + render.StripTags(rq.title(p.Title)) + " by " + c.Author
		}
		text := rq.commentText(c.Content)
		fmt.Fprintf(b, "\t<item>\n\t\t<title>%s</title>\n\t\t<link>%s</link>\n\t\t<dc:creator>%s</dc:creator>\n\t\t<pubDate>%s</pubDate>\n\t\t<guid isPermaLink=\"false\">%s</guid>\n\t\t\t\t\t<description>%s</description>\n\t\t<content:encoded>%s</content:encoded>\n\t\t\t\t\t\t</item>\n",
			xmlText(itemTitle), xmlText(clink+"#comment-"+strconv.FormatInt(c.ID, 10)), cdata(render.SanitizeText(c.Author)), c.DateGMT.UTC().Format(rssDate),
			xmlText(rq.guid(p)+"#comment-"+strconv.FormatInt(c.ID, 10)), cdata(render.StripTags(text)), cdata(text))
	}
	b.WriteString("\t</channel>\n</rss>\n")
	return feedResponse(b.String(), lastBuild)
}
