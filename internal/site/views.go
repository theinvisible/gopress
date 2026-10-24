package site

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/theme"
)

// request is the state of one page render.
type request struct {
	site      *Site
	st        *state
	r         *http.Request
	ctx       context.Context
	env       *render.Env
	commenter commenter

	// Current object, for "current" markers in menus and widgets.
	curPost  int64
	curTerm  *db.Term
	curYear  int
	curMonth int

	threads map[int64]*commentThreads // comment pagination cache
}

// commenter is the visitor's verified identity (signed cookie); used to
// show their pending comments.
type commenter struct {
	Email string
}

func newRequest(s *Site, st *state, r *http.Request) *request {
	rq := &request{site: s, st: st, r: r, ctx: r.Context()}
	rq.env = &render.Env{Ctx: r.Context(), Q: s.db.R, Links: st.links, SizeLimits: st.sizeLimits}
	return rq
}

// cookieHash is WordPress' COOKIEHASH (md5 of the site URL), so existing
// commenter cookies keep working.
func cookieHash(home string) string {
	sum := md5.Sum([]byte(home))
	return hex.EncodeToString(sum[:])
}

// esc escapes text for HTML (esc_html / esc_attr).
func esc(s string) string { return html.EscapeString(s) }

// title formats a post title like the_title: texturize + capital_P_dangit.
// Titles are trusted HTML from editors.
func (rq *request) title(t string) string {
	return render.Texturize(strings.ReplaceAll(t, "Wordpress", "WordPress"), nil)
}

// --- common view ---

type viewOpts struct {
	title      string // HTML: callers escape user input
	bodyClass  []string
	bodyClass2 []string // classes after "paged" (archive specifics)
	paged      int
	pagedClass string // e.g. "category" for category-paged-2
	feeds      []feedLink
	canonical  string
	shortlink  string
	fullWidth  bool
	noSidebar  bool
	// frontTemplate is Twenty Twelve's front page template.
	frontTemplate bool
	themeClass    []string // twentytwelve_body_class() additions
	isFront       bool
	singular      *db.Post
	replyJS       bool
	commentForm   bool
}

type feedLink struct{ title, href string }

func (rq *request) baseView(o viewOpts) *theme.View {
	s := rq.st.s
	th := rq.site.theme
	v := &theme.View{
		Lang: s.Lang, Charset: "UTF-8", Title: template.HTML(o.title),
		SiteName: s.Name, SiteDescription: s.Description,
		HomeURL: rq.st.links.Home(), IsFront: o.isFront,
	}

	var head strings.Builder
	if s.BlogPublic {
		head.WriteString("<meta name='robots' content='max-image-preview:large' />\n")
	} else {
		head.WriteString("<meta name='robots' content='noindex, nofollow' />\n")
	}
	feeds := append([]feedLink{
		{s.Name + " &raquo; Feed", rq.st.links.URL("/feed/")},
		{s.Name + " &raquo; Comments Feed", rq.st.links.URL("/comments/feed/")},
	}, o.feeds...)
	for _, f := range feeds {
		fmt.Fprintf(&head, "<link rel=\"alternate\" type=\"application/rss+xml\" title=\"%s\" href=\"%s\" />\n", f.title, esc(f.href))
	}
	for _, css := range []struct{ id, file string }{
		{"wp-blocks", "css/wp-blocks.css"},
		{"twentytwelve-fonts", "fonts/font-open-sans.css"},
		{"twentytwelve-style", "style.css"},
		{"twentytwelve-block-style", "css/blocks.css"},
	} {
		fmt.Fprintf(&head, "<link rel='stylesheet' id='%s-css' href='%s' media='all' />\n", css.id, th.AssetPath(css.file))
	}
	fmt.Fprintf(&head, "<script defer id=\"twentytwelve-navigation-js\" src=\"%s\"></script>\n", th.AssetPath("js/navigation.js"))
	if o.replyJS {
		fmt.Fprintf(&head, "<script async id=\"comment-reply-js\" src=\"%s\"></script>\n", th.AssetPath("js/comment-reply.min.js"))
	}
	if o.commentForm {
		fmt.Fprintf(&head, "<script defer id=\"gopress-comment-guard-js\" src=\"%s\"></script>\n", coreAssetPath("comment-guard.js"))
	}
	if o.canonical != "" {
		fmt.Fprintf(&head, "<link rel=\"canonical\" href=\"%s\" />\n", esc(o.canonical))
	}
	if o.shortlink != "" {
		fmt.Fprintf(&head, "<link rel='shortlink' href='%s' />\n", esc(o.shortlink))
	}
	bgClass := false
	if css := rq.customBackgroundCSS(); css != "" {
		bgClass = true
		fmt.Fprintf(&head, "<style id=\"custom-background-css\">\n%s\n</style>", css)
	}
	v.Head = template.HTML(head.String())

	classes := append([]string{}, o.bodyClass...)
	if o.paged > 1 {
		classes = append(classes, "paged")
	}
	classes = append(classes, o.bodyClass2...)
	if bgClass {
		classes = append(classes, "custom-background")
	}
	classes = append(classes, "wp-embed-responsive")
	if o.paged > 1 {
		classes = append(classes, "paged-"+strconv.Itoa(o.paged))
		if o.pagedClass != "" {
			classes = append(classes, o.pagedClass+"-paged-"+strconv.Itoa(o.paged))
		}
	}
	classes = append(classes, "wp-theme-"+th.Name)
	// twentytwelve_body_class(): full width without an active main
	// sidebar or with the full-width template.
	if o.fullWidth || !rq.sidebarActive("sidebar-1") {
		classes = append(classes, "full-width")
	}
	classes = append(classes, o.themeClass...)
	classes = append(classes, "custom-font-enabled")
	if !rq.st.ix.multiAuthor {
		classes = append(classes, "single-author")
	}
	v.BodyClass = strings.Join(classes, " ")

	v.Menu = template.HTML(rq.primaryMenu(o.isFront, o.singular))
	if !o.noSidebar && !o.fullWidth && !o.frontTemplate {
		v.Sidebar = template.HTML(rq.sidebar("sidebar-1"))
	}
	v.SearchForm = template.HTML(rq.searchForm())
	v.Credit = template.HTML(`<a href="https://github.com/theinvisible/gopress" class="imprint" title="Semantic Personal Publishing Platform">
				Proudly powered by GoPress			</a>`)
	return v
}

// customBackgroundCSS is _custom_background_cb() for the theme mods.
func (rq *request) customBackgroundCSS() string {
	s := rq.st.s
	color := s.ThemeMod("background_color")
	image := s.ThemeMod("background_image")
	if color == "e6e6e6" { // Twenty Twelve's default color is not output
		color = ""
	}
	if color == "" && image == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("body.custom-background {")
	if color != "" {
		fmt.Fprintf(&b, " background-color: #%s;", strings.TrimPrefix(color, "#"))
	}
	if image != "" {
		if strings.HasPrefix(image, "/") && !strings.HasPrefix(image, "//") {
			image = s.Home + image // stored root-relative by the importer
		}
		posX := s.ThemeMod("background_position_x")
		if posX == "" {
			posX = "left"
		}
		posY := s.ThemeMod("background_position_y")
		if posY == "" {
			posY = "top"
		}
		size := s.ThemeMod("background_size")
		if size == "" {
			size = "auto"
		}
		repeat := s.ThemeMod("background_repeat")
		if repeat == "" {
			repeat = "repeat"
		}
		attach := s.ThemeMod("background_attachment")
		if attach == "" {
			attach = "scroll"
		}
		fmt.Fprintf(&b, " background-image: url(\"%s\"); background-position: %s %s; background-size: %s; background-repeat: %s; background-attachment: %s;",
			cssURL(image), cssIdent(posX), cssIdent(posY), cssIdent(size), cssIdent(repeat), cssIdent(attach))
	}
	b.WriteString(" }")
	return b.String()
}

// cssURL makes a URL safe inside url("...").
func cssURL(u string) string {
	return strings.NewReplacer(`"`, "%22", `\`, "%5C", "\n", "", "\r", "", "<", "%3C", ">", "%3E").Replace(u)
}

// cssIdent keeps only characters valid in simple CSS keywords.
func cssIdent(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '-' || c == ' ' || c == '%' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// pageMenu is wp_page_menu() with show_home, as Twenty Twelve uses when no
// menu is assigned.
func (rq *request) pageMenu(isFront bool, current *db.Post) string {
	ix := rq.st.ix
	var b strings.Builder
	b.WriteString("<div class=\"nav-menu\"><ul>\n")
	if isFront {
		fmt.Fprintf(&b, "<li class=\"current_page_item\"><a href=\"%s\">Home</a></li>", esc(rq.st.links.Home()))
	} else {
		fmt.Fprintf(&b, "<li><a href=\"%s\">Home</a></li>", esc(rq.st.links.Home()))
	}
	ancestors := map[int64]bool{}
	var currentID int64
	if current != nil {
		if current.Type == db.TypePage {
			currentID = current.ID
		}
		for p := ix.posts[current.ParentID]; p != nil && p.Type == db.TypePage && !ancestors[p.ID]; p = ix.posts[p.ParentID] {
			ancestors[p.ID] = true
		}
	}
	var walk func(parent int64, depth int)
	walk = func(parent int64, depth int) {
		indent := strings.Repeat("\t", depth)
		for _, p := range ix.childPages(parent) {
			children := ix.childPages(p.ID)
			cls := "page_item page-item-" + strconv.FormatInt(p.ID, 10)
			if len(children) > 0 {
				cls += " page_item_has_children"
			}
			aria := ""
			switch {
			case p.ID == currentID:
				cls += " current_page_item"
				aria = ` aria-current="page"`
			case ancestors[p.ID]:
				cls += " current_page_ancestor"
				if current != nil && current.ParentID == p.ID {
					cls += " current_page_parent"
				}
			}
			fmt.Fprintf(&b, "%s<li class=\"%s\"><a href=\"%s\"%s>%s</a>", indent, cls, esc(rq.st.links.PostURL(p)), aria, rq.title(p.Title))
			if len(children) > 0 {
				fmt.Fprintf(&b, "\n%s<ul class='children'>\n", indent)
				walk(p.ID, depth+1)
				fmt.Fprintf(&b, "%s</ul>\n%s", indent, indent)
			}
			b.WriteString("</li>\n")
		}
	}
	walk(0, 0)
	b.WriteString("</ul></div>\n")
	return b.String()
}

func (rq *request) searchForm() string {
	return fmt.Sprintf(`<form role="search" method="get" id="searchform" class="searchform" action="%s">
				<div>
					<label class="screen-reader-text" for="s">Search for:</label>
					<input type="text" value="%s" name="s" id="s" />
					<input type="submit" id="searchsubmit" value="Search" />
				</div>
			</form>`, esc(rq.st.links.Home()), esc(rq.r.URL.Query().Get("s")))
}

// render renders a theme page into a response.
func (rq *request) render(status int, kind string, v *theme.View) *response {
	var buf bytes.Buffer
	if err := rq.site.theme.Render(&buf, kind, v); err != nil {
		return errorResponse(err)
	}
	return htmlResponse(status, buf.Bytes())
}

func (rq *request) notFound() *response {
	v := rq.baseView(viewOpts{title: "Page not found | " + esc(rq.st.s.Name), bodyClass: []string{"error404"}, noSidebar: true})
	return rq.render(http.StatusNotFound, "404", v)
}

// --- post entries ---

type entryMode int

const (
	modeLoop entryMode = iota
	modeSingle
	modeSearch
)

func (rq *request) postClass(p *db.Post, terms []db.Term, extra ...string) string {
	cls := []string{"post-" + strconv.FormatInt(p.ID, 10), p.Type, "type-" + p.Type, "status-" + p.Status}
	if p.Type == db.TypePost {
		if rq.isSticky(p.ID) {
			cls = append(cls, "sticky")
		}
		cls = append(cls, "format-standard")
	}
	if p.FeaturedMediaID != 0 {
		cls = append(cls, "has-post-thumbnail")
	}
	cls = append(cls, "hentry")
	if len(extra) > 0 { // post_class($class): extra classes come first
		cls = append(append([]string{}, extra...), cls...)
	}
	for _, t := range terms {
		if t.Taxonomy == db.TaxCategory {
			cls = append(cls, "category-"+t.Slug)
		}
	}
	for _, t := range terms {
		if t.Taxonomy == db.TaxTag {
			cls = append(cls, "tag-"+t.Slug)
		}
	}
	return strings.Join(cls, " ")
}

func (rq *request) isSticky(id int64) bool {
	for _, s := range rq.st.s.Sticky {
		if s == id {
			return true
		}
	}
	return false
}

// entries builds the loop entries for posts (content loaded).
func (rq *request) entries(posts []db.Post, mode entryMode) ([]*theme.Post, error) {
	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	terms, err := db.PostTerms(rq.ctx, rq.site.db.R, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*theme.Post, 0, len(posts))
	for i := range posts {
		out = append(out, rq.entry(&posts[i], terms[posts[i].ID], mode))
	}
	return out, nil
}

func (rq *request) entry(p *db.Post, terms []db.Term, mode entryMode) *theme.Post {
	l := rq.st.links
	url := l.PostURL(p)
	e := &theme.Post{ID: p.ID, Class: rq.postClass(p, terms), Title: template.HTML(rq.title(p.Title))}
	if mode != modeSingle {
		e.URL = url
	}
	if rq.st.s.CommentsOpen(p) {
		e.CommentsLink = template.HTML(rq.commentsPopupLink(p, url))
	}
	rq.env.Post = p
	// Twenty Twelve shows the featured image unless the post is password
	// protected (content.php) or the page uses the front page template.
	if p.FeaturedMediaID != 0 && p.Password == "" && p.Type != db.TypeAttachment && p.Template != "page-templates/front-page.php" {
		if m, err := db.GetMedia(rq.ctx, rq.site.db.R, p.FeaturedMediaID); err == nil && m != nil {
			e.Thumbnail = template.HTML(render.PostThumbnail(rq.env, m))
		}
	}
	switch {
	case p.Password != "":
		e.Content = template.HTML(rq.passwordForm(p))
	case mode == modeSearch:
		e.Summary = template.HTML(rq.excerpt(p))
	default:
		e.Content = template.HTML(rq.st.pipe.Content(rq.env, p.Content, p.ContentFormat, p.ID, render.ContentOptions{
			Teaser: mode == modeLoop, MoreText: `Continue reading <span class="meta-nav">&rarr;</span>`, URL: url,
		}))
	}
	if p.Type == db.TypePost || mode == modeSearch {
		e.Meta = template.HTML(rq.entryMeta(p, terms, url))
	}
	return e
}

// excerpt is the_excerpt(): the manual excerpt or wp_trim_excerpt(),
// filtered (texturize, smilies, autop).
func (rq *request) excerpt(p *db.Post) string {
	text := p.Excerpt
	if strings.TrimSpace(text) == "" {
		text = rq.st.pipe.Excerpt(rq.env, p.Content, p.ContentFormat)
	}
	text = render.Texturize(text, nil)
	text = render.ConvertSmilies(text)
	return render.Autop(text)
}

func (rq *request) passwordForm(p *db.Post) string {
	return `<p>This content is password protected. To view it please enter your password below:</p>`
}

// commentsPopupLink is comments_popup_link() with Twenty Twelve's texts.
func (rq *request) commentsPopupLink(p *db.Post, url string) string {
	switch n := p.CommentCount; n {
	case 0:
		return fmt.Sprintf(`<a href="%s#respond"><span class="leave-reply">Leave a reply</span></a>`, esc(url))
	case 1:
		return fmt.Sprintf(`<a href="%s#comments">1 Reply</a>`, esc(url))
	default:
		return fmt.Sprintf(`<a href="%s#comments">%d Replies</a>`, esc(url), n)
	}
}

// entryMeta is twentytwelve_entry_meta().
func (rq *request) entryMeta(p *db.Post, terms []db.Term, url string) string {
	l := rq.st.links
	var cats, tags []string
	for i := range terms {
		t := &terms[i]
		link := fmt.Sprintf(`<a href="%s" rel="%s">%s</a>`, esc(l.TermURL(t)), map[bool]string{true: "category tag", false: "tag"}[t.Taxonomy == db.TaxCategory], render.Texturize(t.Name, nil))
		if t.Taxonomy == db.TaxCategory {
			cats = append(cats, link)
		} else {
			tags = append(tags, link)
		}
	}
	local := localTime(p.Date, rq.st.s.Loc)
	date := fmt.Sprintf(`<a href="%s" title="%s" rel="bookmark"><time class="entry-date" datetime="%s">%s</time></a>`,
		esc(url), esc(theme.FormatPHPDate(rq.st.s.TimeFormat, local)), theme.FormatPHPDate("c", local), esc(theme.FormatPHPDate(rq.st.s.DateFormat, local)))
	author := ""
	if u := rq.st.ix.users[p.AuthorID]; u != nil {
		author = fmt.Sprintf(`<span class="author vcard"><a class="url fn n" href="%s" title="View all posts by %s" rel="author">%s</a></span>`,
			esc(l.AuthorURL(u)), esc(u.DisplayName), esc(u.DisplayName))
	}
	by := `<span class="by-author"> by ` + author + `</span>`
	switch {
	case len(tags) > 0:
		return fmt.Sprintf("This entry was posted in %s and tagged %s on %s%s.", strings.Join(cats, ", "), strings.Join(tags, ", "), date, by)
	case len(cats) > 0:
		return fmt.Sprintf("This entry was posted in %s on %s%s.", strings.Join(cats, ", "), date, by)
	}
	return fmt.Sprintf("This entry was posted on %s%s.", date, by)
}

// contentNav is twentytwelve_content_nav() for a paged archive.
func contentNav(id string, base string, paged, maxPages int) string {
	if maxPages <= 1 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<nav id=\"%s\" class=\"navigation\">\n\t\t\t<h3 class=\"assistive-text\">Post navigation</h3>\n", id)
	if paged < maxPages {
		fmt.Fprintf(&b, "\t\t\t\t<div class=\"nav-previous\"><a href=\"%s\" ><span class=\"meta-nav\">&larr;</span> Older posts</a></div>\n", esc(Paged(base, paged+1)))
	}
	if paged > 1 {
		fmt.Fprintf(&b, "\t\t\t\t<div class=\"nav-next\"><a href=\"%s\" >Newer posts <span class=\"meta-nav\">&rarr;</span></a></div>\n", esc(Paged(base, paged-1)))
	}
	b.WriteString("\t\t\t</nav><!-- .navigation -->")
	return b.String()
}

func maxPages(total, perPage int) int {
	if perPage <= 0 {
		return 1
	}
	return (total + perPage - 1) / perPage
}

// --- listings ---

func (rq *request) home(paged int) *response {
	s := rq.st.s
	posts, total, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{Type: db.TypePost, Limit: s.PostsPerPage, Offset: (paged - 1) * s.PostsPerPage})
	if err != nil {
		return errorResponse(err)
	}
	pages := maxPages(total, s.PostsPerPage)
	if paged > 1 && paged > pages {
		return rq.notFound()
	}
	title := esc(s.Name)
	if s.Description != "" {
		title += " | " + esc(s.Description)
	}
	if paged > 1 {
		title += " | Page " + strconv.Itoa(paged)
	}
	v := rq.baseView(viewOpts{title: title, bodyClass: []string{"home", "blog"}, paged: paged, isFront: paged == 1})
	if v.Posts, err = rq.entries(posts, modeLoop); err != nil {
		return errorResponse(err)
	}
	if paged == 1 {
		for _, p := range v.Posts {
			p.Featured = rq.isSticky(p.ID)
		}
	}
	v.NavBelow = template.HTML(contentNav("nav-below", rq.st.links.Home(), paged, pages))
	return rq.render(http.StatusOK, "index", v)
}

func (rq *request) termArchive(t *db.Term, paged int) *response {
	s := rq.st.s
	ids := []int64{t.ID}
	kind, label, feedLabel := "tag", "Tag Archives", "Tag Feed"
	if t.Taxonomy == db.TaxCategory {
		ids = rq.st.ix.termAndChildren(t)
		kind, label, feedLabel = "category", "Category Archives", "Category Feed"
	}
	posts, total, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{Type: db.TypePost, TermIDs: ids, Limit: s.PostsPerPage, Offset: (paged - 1) * s.PostsPerPage})
	if err != nil {
		return errorResponse(err)
	}
	pages := maxPages(total, s.PostsPerPage)
	if total == 0 && paged > 1 || paged > pages && paged > 1 {
		return rq.notFound()
	}
	rq.curTerm = t
	url := rq.st.links.TermURL(t)
	name := render.Texturize(t.Name, nil)
	v := rq.baseView(viewOpts{
		title: archiveTitle(escKeepEntities(t.Name), s.Name, paged), paged: paged, pagedClass: kind,
		bodyClass: []string{"archive"}, bodyClass2: []string{kind, kind + "-" + t.Slug, kind + "-" + strconv.FormatInt(t.ID, 10)},
		feeds: []feedLink{{s.Name + " &raquo; " + esc(t.Name) + " " + feedLabel, FeedURL(url)}},
	})
	if v.Posts, err = rq.entries(posts, modeLoop); err != nil {
		return errorResponse(err)
	}
	v.ArchiveTitle = template.HTML(label + ": <span>" + name + "</span>")
	if t.Description != "" {
		v.ArchiveMeta = template.HTML(render.Autop(render.Texturize(t.Description, nil)))
	}
	v.NavBelow = template.HTML(contentNav("nav-below", url, paged, pages))
	return rq.render(http.StatusOK, "archive", v)
}

func archiveTitle(name, site string, paged int) string {
	t := name + " | " + esc(site)
	if paged > 1 {
		t += " | Page " + strconv.Itoa(paged)
	}
	return t
}

func (rq *request) authorArchive(u *db.User, paged int) *response {
	s := rq.st.s
	posts, total, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{Type: db.TypePost, AuthorID: u.ID, Limit: s.PostsPerPage, Offset: (paged - 1) * s.PostsPerPage})
	if err != nil {
		return errorResponse(err)
	}
	pages := maxPages(total, s.PostsPerPage)
	if paged > 1 && paged > pages {
		return rq.notFound()
	}
	url := rq.st.links.AuthorURL(u)
	v := rq.baseView(viewOpts{
		title: archiveTitle(esc(u.DisplayName), s.Name, paged), paged: paged, pagedClass: "author",
		bodyClass: []string{"archive"}, bodyClass2: []string{"author", "author-" + u.Nicename, "author-" + strconv.FormatInt(u.ID, 10)},
		feeds: []feedLink{{s.Name + " &raquo; Posts by " + esc(u.DisplayName) + " Feed", FeedURL(url)}},
	})
	if v.Posts, err = rq.entries(posts, modeLoop); err != nil {
		return errorResponse(err)
	}
	v.ArchiveTitle = template.HTML(fmt.Sprintf(`Author Archives: <span class="vcard"><a class="url fn n" href="%s" rel="me">%s</a></span>`, esc(url), esc(u.DisplayName)))
	v.NavAbove = template.HTML(contentNav("nav-above", url, paged, pages))
	v.NavBelow = template.HTML(contentNav("nav-below", url, paged, pages))
	if u.Description != "" {
		v.AuthorInfo = template.HTML(fmt.Sprintf(`<div class="author-info">
			<div class="author-avatar">
				%s
			</div><!-- .author-avatar -->
			<div class="author-description">
				<h2>About %s</h2>
				<p>%s</p>
			</div><!-- .author-description	-->
		</div><!-- .author-info -->`, rq.avatar(u.Email, 68), esc(u.DisplayName), esc(u.Description)))
	}
	return rq.render(http.StatusOK, "archive", v)
}

func (rq *request) dateArchive(y, m, d, paged int) *response {
	s := rq.st.s
	posts, total, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{Type: db.TypePost, Year: y, Month: m, Day: d, Limit: s.PostsPerPage, Offset: (paged - 1) * s.PostsPerPage})
	if err != nil {
		return errorResponse(err)
	}
	if total == 0 {
		return rq.notFound()
	}
	pages := maxPages(total, s.PostsPerPage)
	if paged > pages {
		return rq.notFound()
	}
	if d == 0 {
		rq.curYear, rq.curMonth = y, m
	}
	url := rq.st.links.DateURL(y, m, d)
	first := localTime(posts[0].Date, s.Loc)
	var title, header string
	switch {
	case d > 0:
		title = fmt.Sprintf("%02d | %s | %d | %s", d, monthName(m), y, esc(s.Name))
		header = "Daily Archives: <span>" + esc(theme.FormatPHPDate(s.DateFormat, first)) + "</span>"
	case m > 0:
		title = fmt.Sprintf("%s | %d | %s", monthName(m), y, esc(s.Name))
		header = "Monthly Archives: <span>" + esc(theme.FormatPHPDate("F Y", first)) + "</span>"
	default:
		// WordPress omits the year from the document title here.
		title = esc(s.Name)
		header = "Yearly Archives: <span>" + esc(theme.FormatPHPDate("Y", first)) + "</span>"
	}
	if paged > 1 {
		title += " | Page " + strconv.Itoa(paged)
	}
	v := rq.baseView(viewOpts{title: title, paged: paged, pagedClass: "date", bodyClass: []string{"archive", "date"}})
	if v.Posts, err = rq.entries(posts, modeLoop); err != nil {
		return errorResponse(err)
	}
	v.ArchiveTitle = template.HTML(header)
	v.NavBelow = template.HTML(contentNav("nav-below", url, paged, pages))
	return rq.render(http.StatusOK, "archive", v)
}

func monthName(m int) string {
	if m < 1 || m > 12 {
		return ""
	}
	return [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}[m-1]
}

func (rq *request) search(q string, paged int) *response {
	s := rq.st.s
	q = limitQuery(q)
	var posts []db.Post
	total := 0
	if q != "" {
		ids, n, err := db.Search(rq.ctx, rq.site.db.R, q, s.PostsPerPage, (paged-1)*s.PostsPerPage)
		if err != nil {
			return errorResponse(err)
		}
		total = n
		if len(ids) > 0 {
			list, _, err := db.ListPosts(rq.ctx, rq.site.db.R, db.PostQuery{IDs: ids, Statuses: []string{db.StatusPublish, db.StatusInherit}})
			if err != nil {
				return errorResponse(err)
			}
			byID := map[int64]db.Post{}
			for _, p := range list {
				byID[p.ID] = p
			}
			for _, id := range ids {
				if p, ok := byID[id]; ok {
					posts = append(posts, p)
				}
			}
		}
	}
	pages := maxPages(total, s.PostsPerPage)
	if paged > 1 && paged > pages {
		return rq.notFound()
	}
	cls := "search-no-results"
	if total > 0 {
		cls = "search-results"
	}
	title := " " + esc(q) + " | Search Results  | " + esc(s.Name)
	if paged > 1 {
		title += " | Page " + strconv.Itoa(paged)
	}
	v := rq.baseView(viewOpts{
		title: title, paged: paged, pagedClass: "search", bodyClass: []string{"search", cls},
		feeds: []feedLink{{s.Name + " &raquo; Search Results for &#8220;" + esc(q) + "&#8221; Feed", rq.st.links.URL("/search/" + pathEscape(q) + "/feed/rss2/")}},
	})
	v.SearchQuery = q
	var err error
	if v.Posts, err = rq.entries(posts, modeSearch); err != nil {
		return errorResponse(err)
	}
	base := rq.st.links.SearchURL(q)
	v.NavAbove = template.HTML(contentNav("nav-above", base, paged, pages))
	v.NavBelow = template.HTML(contentNav("nav-below", base, paged, pages))
	resp := rq.render(http.StatusOK, "search", v)
	resp.header.Set("X-Robots-Tag", "noindex")
	return resp
}

func pathEscape(s string) string {
	return strings.ReplaceAll(templateURLEscape(s), "%2F", "/")
}

func templateURLEscape(s string) string {
	return strings.ReplaceAll(template.URLQueryEscaper(s), "+", "%20")
}
