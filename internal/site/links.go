package site

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
)

// Links builds absolute URLs from the permalink settings and the content
// index. It implements render.Linker.
type Links struct {
	home string
	s    *Settings
	ix   *contentIndex
}

// Home returns the home URL with a trailing slash.
func (l *Links) Home() string { return l.home + "/" }

// URL joins a site-relative path ("/feed/") to the home URL.
func (l *Links) URL(path string) string { return l.home + path }

// UploadURL implements render.Linker.
func (l *Links) UploadURL(file string) string {
	return l.home + "/wp-content/uploads/" + file
}

// PostURL implements render.Linker: the permalink of a post, page or
// attachment.
func (l *Links) PostURL(p *db.Post) string {
	if indexed := l.ix.posts[p.ID]; indexed != nil {
		p = indexed
	}
	switch p.Type {
	case db.TypePage:
		if p.ID == l.s.PageOnFront && l.s.ShowOnFront == "page" {
			return l.Home()
		}
		return l.home + "/" + l.ix.pagePath(p) + "/"
	case db.TypeAttachment:
		return l.attachmentURL(p)
	}
	if p.Status != db.StatusPublish {
		return l.home + "/?p=" + strconv.FormatInt(p.ID, 10)
	}
	return l.home + l.buildPermalink(p)
}

func (l *Links) buildPermalink(p *db.Post) string {
	d := p.Date.Time
	r := strings.NewReplacer(
		"%year%", fmt.Sprintf("%04d", d.Year()),
		"%monthnum%", fmt.Sprintf("%02d", int(d.Month())),
		"%day%", fmt.Sprintf("%02d", d.Day()),
		"%hour%", fmt.Sprintf("%02d", d.Hour()),
		"%minute%", fmt.Sprintf("%02d", d.Minute()),
		"%second%", fmt.Sprintf("%02d", d.Second()),
		"%post_id%", strconv.FormatInt(p.ID, 10),
		"%postname%", p.Slug,
		"%category%", l.primaryCategoryPath(p),
		"%author%", l.authorNicename(p.AuthorID),
	)
	return r.Replace(l.s.Permalink)
}

func (l *Links) primaryCategoryPath(p *db.Post) string {
	// WordPress uses the category with the lowest id; filled by callers
	// that know the post's terms. Without terms fall back to the default.
	return "uncategorized"
}

func (l *Links) authorNicename(id int64) string {
	if u := l.ix.users[id]; u != nil {
		return u.Nicename
	}
	return ""
}

// attachmentURL is get_attachment_link().
func (l *Links) attachmentURL(p *db.Post) string {
	if !l.s.AttachmentPages {
		return l.home + "/?attachment_id=" + strconv.FormatInt(p.ID, 10)
	}
	if parent := l.ix.posts[p.ParentID]; parent != nil && p.ParentID != 0 {
		name := p.Slug
		if _, err := strconv.Atoi(name); err == nil || strings.Contains(l.s.Permalink, "%category%") {
			name = "attachment/" + name
		}
		return strings.TrimSuffix(l.PostURL(parent), "/") + "/" + name + "/"
	}
	return l.home + "/" + p.Slug + "/"
}

// TermURL returns the archive URL of a category or tag.
func (l *Links) TermURL(t *db.Term) string {
	if t.Taxonomy == db.TaxCategory {
		return l.home + "/" + l.s.CategoryBase + "/" + l.ix.termPath(t) + "/"
	}
	return l.home + "/" + l.s.TagBase + "/" + t.Slug + "/"
}

// AuthorURL returns the author archive URL.
func (l *Links) AuthorURL(u *db.User) string {
	return l.home + "/author/" + u.Nicename + "/"
}

// DateURL returns the archive URL of a year, month (m > 0) or day (d > 0).
func (l *Links) DateURL(y, m, d int) string {
	switch {
	case d > 0:
		return fmt.Sprintf("%s/%04d/%02d/%02d/", l.home, y, m, d)
	case m > 0:
		return fmt.Sprintf("%s/%04d/%02d/", l.home, y, m)
	}
	return fmt.Sprintf("%s/%04d/", l.home, y)
}

// SearchURL returns the URL of a search results page.
func (l *Links) SearchURL(q string) string {
	return l.home + "/?s=" + url.QueryEscape(q)
}

// Paged appends /page/N/ to an archive URL (N > 1).
func Paged(base string, n int) string {
	if n <= 1 {
		return base
	}
	if i := strings.IndexByte(base, '?'); i >= 0 { // search: /?s=x -> /page/2/?s=x
		return strings.TrimSuffix(base[:i], "/") + "/page/" + strconv.Itoa(n) + "/" + base[i:]
	}
	return strings.TrimSuffix(base, "/") + "/page/" + strconv.Itoa(n) + "/"
}

// CommentPageURL returns the URL of comment page n of a post.
func CommentPageURL(postURL string, n int) string {
	return strings.TrimSuffix(postURL, "/") + "/comment-page-" + strconv.Itoa(n) + "/"
}

// FeedURL appends feed/ to a URL (the main feed for the home URL).
func FeedURL(base string) string {
	return strings.TrimSuffix(base, "/") + "/feed/"
}
