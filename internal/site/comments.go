package site

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/antispam"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/theme"
)

// commentPages returns the number of comment pages of a post given its
// top-level comment count (get_comment_pages_count()).
func (rq *request) commentPages(topLevel int) int {
	s := rq.st.s
	if !s.PageComments || s.CommentsPerPage <= 0 {
		return 1
	}
	return max(1, maxPages(topLevel, s.CommentsPerPage))
}

// comments renders the comments area (comments.php). It returns nil if
// cpage is out of range.
func (rq *request) comments(p *db.Post, postURL string, cpage int) (*string, error) {
	empty := ""
	if p.Password != "" {
		return &empty, nil
	}
	s := rq.st.s
	all, err := db.PostComments(rq.ctx, rq.site.db.R, p.ID, rq.commenter.Email)
	if err != nil {
		return nil, err
	}
	known := map[int64]bool{}
	for _, c := range all {
		known[c.ID] = true
	}
	children := map[int64][]*db.Comment{}
	var top []*db.Comment
	for i := range all {
		c := &all[i]
		if !s.ThreadComments || c.ParentID == 0 || !known[c.ParentID] {
			top = append(top, c)
		} else {
			children[c.ParentID] = append(children[c.ParentID], c)
		}
	}

	pages := rq.commentPages(len(top))
	page := cpage
	if page == 0 {
		page = 1
		if s.DefaultCommentsPage == "newest" {
			page = pages
		}
	}
	if page > pages {
		return nil, nil
	}
	if s.PageComments && s.CommentsPerPage > 0 && len(top) > 0 {
		start := (page - 1) * s.CommentsPerPage
		end := min(start+s.CommentsPerPage, len(top))
		top = top[start:end]
	}
	if s.CommentOrder == "desc" {
		for i, j := 0, len(top)-1; i < j; i, j = i+1, j-1 {
			top[i], top[j] = top[j], top[i]
		}
	}

	w := &commentWriter{rq: rq, post: p, postURL: postURL, children: children, page: page, pages: pages, open: rq.st.s.CommentsOpen(p)}
	var b strings.Builder
	b.WriteString("\n<div id=\"comments\" class=\"comments-area\">\n\n\t\n")
	if len(top) > 0 {
		title := rq.title(p.Title)
		if p.CommentCount == 1 {
			fmt.Fprintf(&b, "\t\t\t<h2 class=\"comments-title\">\n\t\t\tOne thought on &ldquo;<span>%s</span>&rdquo;\t\t</h2>\n\n", title)
		} else {
			fmt.Fprintf(&b, "\t\t\t<h2 class=\"comments-title\">\n\t\t\t%d thoughts on &ldquo;<span>%s</span>&rdquo;\t\t</h2>\n\n", p.CommentCount, title)
		}
		b.WriteString("\t\t<ol class=\"commentlist\">\n")
		for _, c := range top {
			w.write(&b, c, 1)
		}
		b.WriteString("\t\t</ol><!-- .commentlist -->\n\n")
		if pages > 1 && s.PageComments {
			b.WriteString("\t\t\t\t<nav id=\"comment-nav-below\" class=\"navigation\">\n\t\t\t<h1 class=\"assistive-text section-heading\">Comment navigation</h1>\n")
			if page > 1 {
				fmt.Fprintf(&b, "\t\t\t<div class=\"nav-previous\"><a href=\"%s#comments\" >&larr; Older Comments</a></div>\n", esc(CommentPageURL(postURL, page-1)))
			} else {
				b.WriteString("\t\t\t<div class=\"nav-previous\"></div>\n")
			}
			if page < pages {
				next := CommentPageURL(postURL, page+1)
				if page+1 == pages && s.DefaultCommentsPage == "newest" {
					next = postURL // the newest page is the post itself
				}
				fmt.Fprintf(&b, "\t\t\t<div class=\"nav-next\"><a href=\"%s#comments\" >Newer Comments &rarr;</a></div>\n", esc(next))
			} else {
				b.WriteString("\t\t\t<div class=\"nav-next\"></div>\n")
			}
			b.WriteString("\t\t</nav>\n")
		}
		if !rq.st.s.CommentsOpen(p) && p.CommentCount > 0 {
			b.WriteString("\t\t<p class=\"nocomments\">Comments are closed.</p>\n")
		}
	}
	if rq.st.s.CommentsOpen(p) {
		b.WriteString(rq.commentForm(p))
	}
	b.WriteString("\n</div><!-- #comments .comments-area -->\n")
	out := b.String()
	return &out, nil
}

type commentWriter struct {
	rq        *request
	post      *db.Post
	postURL   string
	children  map[int64][]*db.Comment
	page      int
	pages     int
	alt       int
	threadAlt int
	avatars   int
	open      bool // comments are open (reply links)
}

func (w *commentWriter) classes(c *db.Comment, depth int) string {
	cls := []string{c.Type}
	if c.UserID != 0 {
		if u := w.rq.st.ix.users[c.UserID]; u != nil {
			cls = append(cls, "byuser", "comment-author-"+sanitizeClass(u.Nicename))
			if c.UserID == w.post.AuthorID {
				cls = append(cls, "bypostauthor")
			}
		}
	}
	if w.alt%2 == 1 {
		cls = append(cls, "odd", "alt")
	} else {
		cls = append(cls, "even")
	}
	w.alt++
	if depth == 1 {
		if w.threadAlt%2 == 1 {
			cls = append(cls, "thread-odd", "thread-alt")
		} else {
			cls = append(cls, "thread-even")
		}
		w.threadAlt++
	}
	return strings.Join(append(cls, "depth-"+strconv.Itoa(depth)), " ")
}

// write renders one comment and its replies (twentytwelve_comment()).
func (w *commentWriter) write(b *strings.Builder, c *db.Comment, depth int) {
	rq := w.rq
	s := rq.st.s
	cls := w.classes(c, depth)
	authorLink := commentAuthorLink(c)
	if c.Type == "pingback" || c.Type == "trackback" {
		fmt.Fprintf(b, "\t\t<li class=\"%s\" id=\"comment-%d\">\n\t\t<p>Pingback: %s </p>\n", cls, c.ID, authorLink)
	} else {
		local := localTime(c.Date, s.Loc)
		link := w.postURL
		if w.pages > 1 {
			link = CommentPageURL(w.postURL, w.page)
		}
		link += "#comment-" + strconv.FormatInt(c.ID, 10)
		postAuthor := ""
		if c.UserID != 0 && c.UserID == w.post.AuthorID {
			postAuthor = "<span>Post author</span>"
		}
		fmt.Fprintf(b, "\t\t<li class=\"%s\" id=\"li-comment-%d\">\n\t\t<article id=\"comment-%d\" class=\"comment\">\n\t\t\t<header class=\"comment-meta comment-author vcard\">\n\t\t\t\t", cls, c.ID, c.ID)
		if s.ShowAvatars {
			b.WriteString(w.avatar(c.AuthorEmail, 44))
		}
		fmt.Fprintf(b, "<cite><b class=\"fn\">%s</b> %s</cite><a href=\"%s\"><time datetime=\"%s\">%s at %s</time></a>\t\t\t\t</header><!-- .comment-meta -->\n\n",
			authorLink, postAuthor, esc(link), theme.FormatPHPDate("c", local),
			esc(theme.FormatPHPDate(s.DateFormat, local)), esc(theme.FormatPHPDate(s.TimeFormat, local)))
		if c.Status == db.CommentPending {
			b.WriteString("\t\t\t\t\t\t\t\t<p class=\"comment-awaiting-moderation\">Your comment is awaiting moderation.</p>\n")
		}
		fmt.Fprintf(b, "\t\t\t\t\n\t\t\t\t<section class=\"comment-content comment\">\n\t\t\t\t%s\t\t\t\t\t\t\t\t</section><!-- .comment-content -->\n\n", rq.commentText(c.Content))
		b.WriteString("\t\t\t\t<div class=\"reply\">\n\t\t\t\t")
		if s.ThreadComments && depth < s.ThreadDepth && w.open && c.Status == db.CommentApproved {
			author := esc(c.Author)
			replyBase := w.postURL
			if w.pages > 1 {
				replyBase = CommentPageURL(w.postURL, w.page)
			}
			fmt.Fprintf(b, `<a rel="nofollow" class="comment-reply-link" href="%s?replytocom=%d#respond" data-commentid="%d" data-postid="%d" data-belowelement="comment-%d" data-respondelement="respond" data-replyto="Reply to %s" aria-label="Reply to %s">Reply</a> <span>&darr;</span>`,
				esc(replyBase), c.ID, c.ID, w.post.ID, c.ID, author, author)
		}
		b.WriteString("\t\t\t\t</div><!-- .reply -->\n\t\t\t</article><!-- #comment-## -->\n")
	}
	if kids := w.children[c.ID]; len(kids) > 0 {
		b.WriteString("\t\t\t<ol class=\"children\">\n")
		for _, k := range kids {
			w.write(b, k, depth+1)
		}
		b.WriteString("\t\t</ol><!-- .children -->\n")
	}
	b.WriteString("\t\t</li><!-- #comment-## -->\n")
}

// commentAuthorLink is get_comment_author_link().
func commentAuthorLink(c *db.Comment) string {
	name := c.Author
	if name == "" {
		name = "Anonymous"
	}
	// get_comment_author() is not texturized (unlike comment_author()).
	name = escKeepEntities(render.SanitizeText(name))
	u := strings.TrimSpace(c.AuthorURL)
	if u == "" || u == "http://" {
		return name
	}
	safe := render.EscURL(u)
	if safe == "" || !(strings.HasPrefix(safe, "http://") || strings.HasPrefix(safe, "https://")) {
		return name
	}
	return fmt.Sprintf(`<a href="%s" class="url" rel="ugc external nofollow">%s</a>`, safe, name)
}

// escKeepEntities escapes HTML special characters but keeps existing
// entities (esc_html without double encoding).
func escKeepEntities(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#039;")
		case '&':
			if j := strings.IndexByte(s[i:], ';'); j > 1 && j < 10 && isEntityBody(s[i+1:i+j]) {
				b.WriteByte('&')
			} else {
				b.WriteString("&amp;")
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isEntityBody(s string) bool {
	if strings.HasPrefix(s, "#") {
		s = strings.TrimPrefix(strings.TrimPrefix(s[1:], "x"), "X")
	}
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// commentText applies the comment_text filters to stored comment HTML in
// WordPress' order, then sanitizes the result with the comment allowlist.
// Sanitizing last keeps the typography identical to WordPress and makes the
// output safe regardless of what is stored (imported comments were
// filtered by WordPress, new ones are also filtered on save).
func (rq *request) commentText(content string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(rq.st.s.Home, "https://"), "http://")
	text := render.MakeClickable(content, host)
	text = render.Texturize(text, nil)
	text = render.ConvertSmilies(text)
	text = render.BalanceTags(text)
	text = render.Autop(text)
	text = render.CapitalPDangit(text)
	return render.SanitizeComment(text)
}

// avatar is get_avatar() with Gravatar (SHA-256 of the email address).
func (rq *request) avatar(email string, size int) string {
	w := &commentWriter{rq: rq}
	return w.avatar(email, size)
}

func (w *commentWriter) avatar(email string, size int) string {
	s := w.rq.st.s
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	hash := hex.EncodeToString(sum[:])
	d := s.AvatarDefault
	if d == "mystery" || d == "" {
		d = "mm"
	}
	r := strings.ToLower(s.AvatarRating)
	src := func(sz int) string {
		return fmt.Sprintf("https://secure.gravatar.com/avatar/%s?s=%d&#038;d=%s&#038;r=%s", hash, sz, url.QueryEscape(d), url.QueryEscape(r))
	}
	w.avatars++
	loading := ""
	if w.avatars > 3 {
		loading = " loading='lazy'"
	}
	return fmt.Sprintf("<img alt='' src='%s' srcset='%s 2x' class='avatar avatar-%d photo' height='%d' width='%d'%s decoding='async'/>",
		src(size), src(size*2), size, size, size, loading)
}

// commentForm is comment_form() for visitors, plus the antispam fields.
func (rq *request) commentForm(p *db.Post) string {
	parent := int64(0)
	title := "Leave a Reply"
	if id, err := strconv.ParseInt(rq.r.URL.Query().Get("replytocom"), 10, 64); err == nil && id > 0 {
		if cm, err := db.PostComments(rq.ctx, rq.site.db.R, p.ID, ""); err == nil {
			for _, x := range cm {
				if x.ID == id {
					parent = id
					title = fmt.Sprintf(`Leave a Reply to <a href="#comment-%d">%s</a>`, id, esc(x.Author))
				}
			}
		}
	}
	cancelStyle := ` style="display:none;"`
	if parent != 0 {
		cancelStyle = ""
	}
	path := rq.r.URL.Path // the current URL, including a comment page
	var b strings.Builder
	fmt.Fprintf(&b, "\t<div id=\"respond\" class=\"comment-respond\">\n\t\t<h3 id=\"reply-title\" class=\"comment-reply-title\">%s <small><a rel=\"nofollow\" id=\"cancel-comment-reply-link\" href=\"%s#respond\"%s>Cancel reply</a></small></h3>", title, esc(path), cancelStyle)
	fmt.Fprintf(&b, `<form action="%s" method="post" id="commentform" class="comment-form">`, esc(rq.st.links.URL("/wp-comments-post.php")))
	b.WriteString(`<p class="comment-notes"><span id="email-notes">Your email address will not be published.</span> <span class="required-field-message">Required fields are marked <span class="required">*</span></span></p>`)
	b.WriteString(`<p class="comment-form-comment"><label for="comment">Comment <span class="required">*</span></label> <textarea id="comment" name="comment" cols="45" rows="8" maxlength="65525" required="required"></textarea></p>`)
	b.WriteString(`<p class="comment-form-author"><label for="author">Name <span class="required">*</span></label> <input id="author" name="author" type="text" value="" size="30" maxlength="245" autocomplete="name" required="required" /></p>` + "\n")
	b.WriteString(`<p class="comment-form-email"><label for="email">Email <span class="required">*</span></label> <input id="email" name="email" type="text" value="" size="30" maxlength="100" aria-describedby="email-notes" autocomplete="email" required="required" /></p>` + "\n")
	b.WriteString(`<p class="comment-form-url"><label for="url">Website</label> <input id="url" name="url" type="text" value="" size="30" maxlength="200" autocomplete="url" /></p>` + "\n")
	if rq.st.s.CookiesOptIn {
		b.WriteString(`<p class="comment-form-cookies-consent"><input id="wp-comment-cookies-consent" name="wp-comment-cookies-consent" type="checkbox" value="yes" /> <label for="wp-comment-cookies-consent">Save my name, email, and website in this browser for the next time I comment.</label></p>` + "\n")
	}
	fmt.Fprintf(&b, "<p class=\"form-submit\"><input name=\"submit\" type=\"submit\" id=\"submit\" class=\"submit\" value=\"Post Comment\" /> <input type='hidden' name='comment_post_ID' value='%d' id='comment_post_ID' />\n<input type='hidden' name='comment_parent' id='comment_parent' value='%d' />\n</p>", p.ID, parent)
	fmt.Fprintf(&b, `<p style="display: none !important;" aria-hidden="true"><label>Leave this field empty <input type="text" name="%s" value="" tabindex="-1" autocomplete="off" /></label></p><input type="hidden" name="%s" value="%s" />`,
		antispam.HoneypotField, antispam.TokenField, rq.site.guard.Token(p.ID, time.Now()))
	b.WriteString("</form>\t</div><!-- #respond -->\n\t")
	return b.String()
}

// commentPageOf returns the comment page that shows c (get_page_of_comment())
// and the number of comment pages of its post.
func (rq *request) commentPageOf(c *db.Comment) (page, pages int) {
	s := rq.st.s
	if !s.PageComments || s.CommentsPerPage <= 0 {
		return 1, 1
	}
	if rq.threads == nil {
		rq.threads = map[int64]*commentThreads{}
	}
	t := rq.threads[c.PostID]
	if t == nil {
		all, err := db.PostComments(rq.ctx, rq.site.db.R, c.PostID, "")
		if err != nil {
			return 1, 1
		}
		t = &commentThreads{parent: map[int64]int64{}, rootIndex: map[int64]int{}}
		for _, x := range all {
			t.parent[x.ID] = x.ParentID
		}
		for _, x := range all {
			if !s.ThreadComments || x.ParentID == 0 || t.parent[x.ParentID] == 0 && !hasKey(t.parent, x.ParentID) {
				t.rootIndex[x.ID] = t.roots
				t.roots++
			}
		}
		rq.threads[c.PostID] = t
	}
	root := c.ID
	for i := 0; i < 100 && s.ThreadComments; i++ {
		p, ok := t.parent[root]
		if !ok || p == 0 || !hasKey(t.parent, p) {
			break
		}
		root = p
	}
	pages = rq.commentPages(t.roots)
	return t.rootIndex[root]/s.CommentsPerPage + 1, pages
}

type commentThreads struct {
	parent    map[int64]int64
	rootIndex map[int64]int
	roots     int
}

func hasKey(m map[int64]int64, k int64) bool {
	_, ok := m[k]
	return ok
}
