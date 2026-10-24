package site

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/theme"
)

// singular renders a post, page or attachment.
func (rq *request) singular(indexed *db.Post, rt *route) *response {
	if !rq.st.ix.isPublic(indexed) {
		return rq.notFound()
	}
	p, err := db.GetPost(rq.ctx, rq.site.db.R, indexed.ID)
	if err != nil {
		return errorResponse(err)
	}
	s := rq.st.s
	l := rq.st.links
	url := l.PostURL(p)

	// Canonical URL: redirect non-canonical paths (wrong case, old parent).
	if rt != nil {
		canonicalPath := strings.TrimPrefix(url, s.Home)
		if rt.cpage > 0 {
			canonicalPath = strings.TrimPrefix(CommentPageURL(url, rt.cpage), s.Home)
		}
		if rq.r.URL.Path != canonicalPath {
			target := s.Home + canonicalPath
			if rq.r.URL.RawQuery != "" {
				target += "?" + rq.r.URL.RawQuery
			}
			return redirect(target)
		}
	}

	terms, err := db.PostTerms(rq.ctx, rq.site.db.R, []int64{p.ID})
	if err != nil {
		return errorResponse(err)
	}
	cpage := 0
	if rt != nil {
		cpage = rt.cpage
	}
	comments, err := rq.comments(p, url, cpage)
	if err != nil {
		return errorResponse(err)
	}
	if comments == nil {
		return rq.notFound() // comment page out of range
	}

	opts := viewOpts{
		title:     rq.title(p.Title) + " | " + s.Name,
		canonical: url,
		shortlink: s.Home + "/?p=" + strconv.FormatInt(p.ID, 10),
		singular:  p,
		replyJS:   p.CommentStatus == "open" && s.ThreadComments,
	}
	if cpage > 0 {
		opts.canonical = CommentPageURL(url, cpage) + "#comments"
	}
	if p.CommentStatus == "open" || p.CommentCount > 0 {
		opts.feeds = []feedLink{{s.Name + " &raquo; " + esc(render.StripTags(rq.title(p.Title))) + " Comments Feed", FeedURL(url)}}
	}

	kind := "single"
	switch p.Type {
	case db.TypePost:
		opts.bodyClass = []string{"wp-singular", "post-template-default", "single", "single-post", "postid-" + strconv.FormatInt(p.ID, 10), "single-format-standard"}
	case db.TypePage:
		kind = "page"
		opts.bodyClass = []string{"wp-singular"}
		if p.Template != "" {
			tpl := strings.TrimSuffix(p.Template, ".php")
			dir, name, _ := strings.Cut(tpl, "/")
			opts.bodyClass = append(opts.bodyClass, "page-template", "page-template-"+sanitizeClass(dir), "page-template-"+sanitizeClass(name),
				"page-template-"+sanitizeClass(strings.ReplaceAll(p.Template, "/", "")))
		} else {
			opts.bodyClass = append(opts.bodyClass, "page-template-default")
		}
		opts.bodyClass = append(opts.bodyClass, "page", "page-id-"+strconv.FormatInt(p.ID, 10))
		if len(rq.st.ix.childPages(p.ID)) > 0 {
			opts.bodyClass = append(opts.bodyClass, "page-parent")
		}
		if p.ParentID != 0 {
			opts.bodyClass = append(opts.bodyClass, "page-child", "parent-pageid-"+strconv.FormatInt(p.ParentID, 10))
		}
		opts.fullWidth = p.Template == "page-templates/full-width.php"
	case db.TypeAttachment:
		kind = "attachment"
		mimeSub := p.MimeType[strings.IndexByte(p.MimeType, '/')+1:]
		opts.bodyClass = []string{"attachment", "wp-singular", "attachment-template-default", "attachmentid-" + strconv.FormatInt(p.ID, 10), "attachment-" + sanitizeClass(mimeSub)}
		opts.noSidebar = true
	}

	v := rq.baseView(opts)
	var entry *theme.Post
	if p.Type == db.TypeAttachment {
		entry, err = rq.attachmentEntry(p)
		if err != nil {
			return errorResponse(err)
		}
	} else {
		entry = rq.entry(p, terms[p.ID], modeSingle)
		if p.Type == db.TypePage {
			entry.Meta = ""
		}
	}
	v.Post = entry
	if p.Type == db.TypePost {
		v.SingleNav = template.HTML(rq.singleNav(p))
	}
	v.Comments = template.HTML(*comments)
	return rq.render(http.StatusOK, kind, v)
}

func sanitizeClass(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if c == '-' || c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// singleNav renders the previous/next post links (nav-single).
func (rq *request) singleNav(p *db.Post) string {
	var b strings.Builder
	prev, err := db.AdjacentPost(rq.ctx, rq.site.db.R, p, true)
	b.WriteString(`<span class="nav-previous">`)
	if err == nil && prev != nil {
		fmt.Fprintf(&b, `<a href="%s" rel="prev"><span class="meta-nav">&larr;</span> %s</a>`, esc(rq.st.links.PostURL(prev)), rq.title(prev.Title))
	}
	b.WriteString("</span>\n\t\t\t\t\t<span class=\"nav-next\">")
	next, err := db.AdjacentPost(rq.ctx, rq.site.db.R, p, false)
	if err == nil && next != nil {
		fmt.Fprintf(&b, `<a href="%s" rel="next">%s <span class="meta-nav">&rarr;</span></a>`, esc(rq.st.links.PostURL(next)), rq.title(next.Title))
	}
	b.WriteString("</span>")
	return b.String()
}

// attachmentEntry builds the image.php view of an attachment.
func (rq *request) attachmentEntry(p *db.Post) (*theme.Post, error) {
	l := rq.st.links
	m, err := db.GetMedia(rq.ctx, rq.site.db.R, p.ID)
	if err != nil {
		return nil, err
	}
	e := &theme.Post{ID: p.ID, Class: rq.postClass(p, nil, "image-attachment"), Title: template.HTML(rq.title(p.Title))}
	if m == nil {
		return e, nil
	}
	parent := rq.st.ix.posts[p.ParentID]
	local := p.DateGMT.In(rq.st.s.Loc)
	parentLink := ""
	if parent != nil {
		parentLink = fmt.Sprintf(` in <a href="%s" title="Go to %s" rel="gallery">%s</a>`,
			esc(l.PostURL(parent)), esc(render.StripTags(rq.title(parent.Title))), rq.title(parent.Title))
	}
	e.AttachmentMeta = template.HTML(fmt.Sprintf(`<span class="meta-prep meta-prep-entry-date">Published </span> <span class="entry-date"><time class="entry-date" datetime="%s">%s</time></span> at <a href="%s" title="Link to full-size image">%d &times; %d</a>%s.`,
		theme.FormatPHPDate("c", local), esc(theme.FormatPHPDate(rq.st.s.DateFormat, local)), esc(l.UploadURL(m.File)), m.Width, m.Height, parentLink))

	// Previous/next image among the parent's attachments, and the link
	// target of the image itself (next image, or the file).
	var siblings []db.Post
	if p.ParentID != 0 {
		all, err := db.Children(rq.ctx, rq.site.db.R, p.ParentID, db.TypeAttachment)
		if err != nil {
			return nil, err
		}
		for _, a := range all {
			if strings.HasPrefix(a.MimeType, "image/") {
				siblings = append(siblings, a)
			}
		}
	}
	idx := -1
	for i, a := range siblings {
		if a.ID == p.ID {
			idx = i
		}
	}
	var nav strings.Builder
	nav.WriteString(`<span class="previous-image">`)
	if idx > 0 {
		fmt.Fprintf(&nav, `<a href='%s'>&larr; Previous</a>`, esc(l.PostURL(&siblings[idx-1])))
	}
	nav.WriteString("</span>\n\t\t\t\t\t\t<span class=\"next-image\">")
	if idx >= 0 && idx+1 < len(siblings) {
		fmt.Fprintf(&nav, `<a href='%s'>Next &rarr;</a>`, esc(l.PostURL(&siblings[idx+1])))
	}
	nav.WriteString("</span>")
	e.ImageNav = template.HTML(nav.String())

	next := l.UploadURL(m.File)
	if len(siblings) > 1 && idx >= 0 {
		next = l.PostURL(&siblings[(idx+1)%len(siblings)])
	}
	file, w, h := m.File, m.Width, m.Height
	if fw, fh := fitBox(w, h, 960, 960); fw != w {
		// Pick the smallest generated size that covers the 960px box.
		w, h = fw, fh
		file = m.File
	}
	img := fmt.Sprintf(`<img width="%d" height="%d" src="%s" class="attachment-960x960 size-960x960" alt="%s" decoding="async" />`,
		w, h, esc(l.UploadURL(file)), esc(m.Alt))
	e.Attachment = template.HTML(fmt.Sprintf("<a href=\"%s\" title=\"%s\" rel=\"attachment\">\n\t\t\t\t\t\t\t\t\t%s\t\t\t\t\t\t\t\t</a>",
		esc(next), esc(render.StripTags(p.Title)), img))
	if strings.TrimSpace(p.Excerpt) != "" {
		e.Caption = template.HTML(render.Autop(render.Texturize(p.Excerpt, nil)))
	}
	rq.env.Post = p
	e.Content = template.HTML(rq.st.pipe.Filter(rq.env, p.Content))
	return e, nil
}

// fitBox scales w×h down to fit into maxW×maxH.
func fitBox(w, h, maxW, maxH int) (int, int) {
	if w <= maxW && h <= maxH || w == 0 || h == 0 {
		return w, h
	}
	rw, rh := float64(maxW)/float64(w), float64(maxH)/float64(h)
	r := rw
	if rh < r {
		r = rh
	}
	return max(1, int(float64(w)*r+0.5)), max(1, int(float64(h)*r+0.5))
}
