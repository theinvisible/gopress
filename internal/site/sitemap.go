package site

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// robots serves robots.txt like WordPress' do_robots().
func (rq *request) robots() *response {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	if rq.st.s.BlogPublic {
		b.WriteString("Disallow: /wp-admin/\nDisallow: /admin/\n\nSitemap: " + rq.st.links.URL("/wp-sitemap.xml") + "\n")
	} else {
		b.WriteString("Disallow: /\n")
	}
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	return &response{status: http.StatusOK, header: h, body: []byte(b.String())}
}

func xmlResponse(body string) *response {
	h := http.Header{}
	h.Set("Content-Type", "application/xml; charset=UTF-8")
	h.Set("X-Robots-Tag", "noindex, follow")
	return &response{status: http.StatusOK, header: h, body: []byte(body)}
}

// sitemapIndex serves the WordPress core sitemap index; /sitemap.xml(.gz)
// (the old Google XML Sitemaps URLs) serve the same index.
func (rq *request) sitemapIndex(path string) *response {
	if !rq.st.s.BlogPublic {
		return rq.notFound()
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<sitemapindex xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">")
	for _, name := range []string{"posts-post-1", "posts-page-1", "taxonomies-category-1", "taxonomies-post_tag-1", "users-1"} {
		fmt.Fprintf(&b, "<sitemap><loc>%s</loc></sitemap>", xmlText(rq.st.links.URL("/wp-sitemap-"+name+".xml")))
	}
	b.WriteString("</sitemapindex>\n")
	resp := xmlResponse(b.String())
	if strings.HasSuffix(path, ".gz") {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		zw.Write(resp.body)
		zw.Close()
		resp.body = gz.Bytes()
		resp.header.Set("Content-Type", "application/gzip")
	}
	return resp
}

// sitemap serves one wp-sitemap-<name>.xml page.
func (rq *request) sitemap(name string) *response {
	st := rq.st
	if !st.s.BlogPublic {
		return rq.notFound()
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">")
	url := func(loc, lastmod string) {
		b.WriteString("<url><loc>" + xmlText(loc) + "</loc>")
		if lastmod != "" {
			b.WriteString("<lastmod>" + lastmod + "</lastmod>")
		}
		b.WriteString("</url>")
	}
	switch name {
	case "posts-post-1", "posts-page-1":
		typ := strings.TrimSuffix(strings.TrimPrefix(name, "posts-"), "-1")
		if typ == db.TypePost {
			url(st.links.Home(), "")
		}
		posts, _, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{Type: typ, Limit: 2000})
		if err != nil {
			return errorResponse(err)
		}
		for i := range posts {
			p := &posts[i]
			if p.Password != "" {
				continue
			}
			url(st.links.PostURL(p), p.ModifiedGMT.UTC().Format("2006-01-02T15:04:05+00:00"))
		}
	case "taxonomies-category-1":
		for _, t := range st.ix.cats {
			if t.Count > 0 {
				url(st.links.TermURL(t), "")
			}
		}
	case "taxonomies-post_tag-1":
		for _, t := range st.ix.tags {
			if t.Count > 0 {
				url(st.links.TermURL(t), "")
			}
		}
	case "users-1":
		// Only authors with published posts (their archives are public anyway).
		authors := map[int64]bool{}
		for _, list := range st.ix.postsBySlug {
			for _, p := range list {
				authors[p.AuthorID] = true
			}
		}
		for id, u := range st.ix.users {
			if authors[id] {
				url(st.links.AuthorURL(u), "")
			}
		}
	default:
		return rq.notFound()
	}
	b.WriteString("</urlset>\n")
	return xmlResponse(b.String())
}
