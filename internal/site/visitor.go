package site

import (
	"crypto/subtle"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
)

// SessionCookie is the admin session cookie (auth.SessionCookie). Pages
// are only personalized when a request carries it; everybody else gets
// the cached page.
const SessionCookie = "__Host-gopress_session"

// Visitor is a logged-in user on the public site: the admin bar, edit
// links, commenting under the account and the customizer preview.
type Visitor struct {
	ID          int64
	Login, Name string
	Email, URL  string
	CSRF        string // for the logout form
	ShowBar     bool
	Pending     int // comments awaiting moderation (moderators)
	Can         func(cap string) bool
	CanEdit     func(p *db.Post) bool
	BarStyles   []string       // admin bar stylesheets (absolute paths)
	Draft       map[string]any // customizer values to preview (preview frame only)
	AvatarURL   string
	DisplayName string
}

// visitor returns the logged-in visitor of r, or nil. The session cookie
// alone decides nothing: the admin validates the session.
func (s *Site) visitor(r *http.Request) *Visitor {
	if s.previewer == nil {
		return nil
	}
	if c, err := r.Cookie(SessionCookie); err != nil || c.Value == "" {
		return nil
	}
	return s.previewer.Visitor(r)
}

// withDraft returns a copy of the state with customizer values applied.
func (st *state) withDraft(d map[string]any) *state {
	if len(d) == 0 {
		return st
	}
	s := *st.s
	str := func(k string) (string, bool) { v, ok := d[k].(string); return v, ok }
	num := func(k string) (int64, bool) {
		switch v := d[k].(type) {
		case float64:
			return int64(v), true
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			return n, err == nil
		}
		return 0, false
	}
	if v, ok := str("blogname"); ok {
		s.Name = v
	}
	if v, ok := str("blogdescription"); ok {
		s.Description = v
	}
	if v, ok := str("custom_css"); ok {
		s.CustomCSS = v
	}
	if v, ok := num("site_icon"); ok {
		s.SiteIcon = v
	}
	if v, ok := str("show_on_front"); ok && (v == "posts" || v == "page") {
		s.ShowOnFront = v
	}
	if v, ok := num("page_on_front"); ok {
		s.PageOnFront = v
	}
	if v, ok := num("page_for_posts"); ok {
		s.PageForPosts = v
	}
	if v, ok := num("menu_primary"); ok {
		s.MenuLocations = map[string]int64{"primary": v}
	}
	if mods, ok := d["theme_mods"].(map[string]any); ok {
		m := make(map[string]any, len(s.ThemeMods)+len(mods))
		for k, v := range s.ThemeMods {
			m[k] = v
		}
		for k, v := range mods {
			m[k] = v
		}
		s.ThemeMods = m
	}
	c := *st
	c.s = &s
	return &c
}

// adminBar renders the toolbar for a logged-in visitor (WordPress'
// #wpadminbar markup, styled by its admin-bar.css; the menus open on
// hover and keyboard focus without JavaScript).
func (rq *request) adminBar() string {
	v := rq.visitor
	if v == nil || !v.ShowBar {
		return ""
	}
	e := html.EscapeString
	var b strings.Builder
	li := func(id, cls, href, inner string) {
		fmt.Fprintf(&b, `<li id="wp-admin-bar-%s"%s><a class="ab-item" href="%s">%s</a></li>`, id, cls, e(href), inner)
	}
	b.WriteString(`<div id="wpadminbar" class="nojq nojs"><div class="quicklinks" id="wp-toolbar" role="navigation" aria-label="Toolbar">`)
	b.WriteString(`<ul id="wp-admin-bar-root-default" class="ab-top-menu">`)
	fmt.Fprintf(&b, `<li id="wp-admin-bar-site-name" class="menupop"><a class="ab-item" aria-haspopup="true" href="/admin/">%s</a><div class="ab-sub-wrapper"><ul id="wp-admin-bar-site-name-default" class="ab-submenu">`,
		rq.st.s.Name)
	li("dashboard", "", "/admin/", "Dashboard")
	if v.Can("edit_theme_options") {
		li("widgets", "", "/admin/widgets", "Widgets")
		li("menus", "", "/admin/menus", "Menus")
	}
	b.WriteString(`</ul></div></li>`)
	if v.Can("edit_theme_options") {
		li("customize", ` class="hide-if-no-customize"`, "/admin/customize", `Customize`)
	}
	if v.Can("moderate_comments") {
		fmt.Fprintf(&b, `<li id="wp-admin-bar-comments"><a class="ab-item" href="/admin/comments/"><span class="ab-icon" aria-hidden="true"></span><span class="ab-label awaiting-mod pending-count count-%d" aria-hidden="true">%d</span><span class="screen-reader-text comments-in-moderation-text">%d Comments in moderation</span></a></li>`,
			v.Pending, v.Pending, v.Pending)
	}
	if v.Can("edit_posts") || v.Can("upload_files") {
		newURL := "/admin/posts/new"
		if !v.Can("edit_posts") {
			newURL = "/admin/media/"
		}
		fmt.Fprintf(&b, `<li id="wp-admin-bar-new-content" class="menupop"><a class="ab-item" aria-haspopup="true" href="%s"><span class="ab-icon" aria-hidden="true"></span><span class="ab-label">New</span></a><div class="ab-sub-wrapper"><ul id="wp-admin-bar-new-content-default" class="ab-submenu">`, newURL)
		if v.Can("edit_posts") {
			li("new-post", "", "/admin/posts/new", "Post")
		}
		if v.Can("upload_files") {
			li("new-media", "", "/admin/media/", "Media")
		}
		if v.Can("edit_pages") {
			li("new-page", "", "/admin/pages/new", "Page")
		}
		if v.Can("create_users") {
			li("new-user", "", "/admin/users/new", "User")
		}
		b.WriteString(`</ul></div></li>`)
	}
	if edit := rq.editLink(); edit != "" {
		b.WriteString(edit)
	}
	b.WriteString(`</ul><ul id="wp-admin-bar-top-secondary" class="ab-top-secondary ab-top-menu">`)
	fmt.Fprintf(&b, `<li id="wp-admin-bar-my-account" class="menupop with-avatar"><a class="ab-item" aria-haspopup="true" href="/admin/profile">Howdy, <span class="display-name">%s</span><img alt="" src="%s" class="avatar avatar-26 photo" height="26" width="26" /></a>`,
		e(v.DisplayName), e(v.AvatarURL))
	b.WriteString(`<div class="ab-sub-wrapper"><ul id="wp-admin-bar-user-actions" class="ab-submenu">`)
	fmt.Fprintf(&b, `<li id="wp-admin-bar-user-info"><a class="ab-item" tabindex="-1" href="/admin/profile"><img alt="" src="%s" class="avatar avatar-64 photo" height="64" width="64" /><span class="display-name">%s</span><span class="username">%s</span></a></li>`,
		e(v.AvatarURL), e(v.DisplayName), e(v.Login))
	li("edit-profile", "", "/admin/profile", "Edit Profile")
	fmt.Fprintf(&b, `<li id="wp-admin-bar-logout"><form method="post" action="/logout" class="gp-ab-logout"><input type="hidden" name="_csrf" value="%s"><button type="submit" class="ab-item">Log Out</button></form></li>`, e(v.CSRF))
	b.WriteString(`</ul></div></li></ul></div></div>`)
	return b.String()
}

// editLink is the toolbar's "Edit …" entry for the shown post or term.
func (rq *request) editLink() string {
	v := rq.visitor
	if p := rq.curPostObj(); p != nil && v.CanEdit(p) {
		label, base := "Edit Post", "/admin/posts/"
		switch p.Type {
		case db.TypePage:
			label, base = "Edit Page", "/admin/pages/"
		case db.TypeAttachment:
			return `<li id="wp-admin-bar-edit"><a class="ab-item" href="/admin/media/">Edit Media</a></li>`
		}
		return fmt.Sprintf(`<li id="wp-admin-bar-edit"><a class="ab-item" href="%s%d/edit">%s</a></li>`, base, p.ID, label)
	}
	if t := rq.curTerm; t != nil && v.Can("manage_categories") {
		label, base := "Edit Category", "/admin/categories/"
		if t.Taxonomy == db.TaxTag {
			label, base = "Edit Tag", "/admin/tags/"
		}
		return fmt.Sprintf(`<li id="wp-admin-bar-edit"><a class="ab-item" href="%s%d/edit">%s</a></li>`, base, t.ID, label)
	}
	return ""
}

// adminBarHead is the toolbar's styles (admin-bar.css plus the page
// offset that WordPress prints with _admin_bar_bump_cb).
func (rq *request) adminBarHead() string {
	v := rq.visitor
	if v == nil || !v.ShowBar {
		return ""
	}
	var b strings.Builder
	for _, href := range v.BarStyles {
		fmt.Fprintf(&b, "<link rel='stylesheet' href='%s' media='all' />\n", html.EscapeString(href))
	}
	b.WriteString(`<style media="screen">html { margin-top: 32px !important; } @media screen and ( max-width: 782px ) { html { margin-top: 46px !important; } }` +
		` #wpadminbar li:focus-within > .ab-sub-wrapper { display: block; } #wpadminbar .gp-ab-logout { margin: 0; }` +
		` #wpadminbar .gp-ab-logout button.ab-item { background: none; border: 0; padding: 0 10px; width: 100%; text-align: left; cursor: pointer; color: inherit; font: inherit; height: 26px; line-height: 26px; }</style>` + "\n")
	return b.String()
}

// formVisitor returns the visitor for a form post (comments) only if it
// comes from this site and carries the session's CSRF token; otherwise
// the post is treated as anonymous.
func (s *Site) formVisitor(r *http.Request, home string) *Visitor {
	if !httpx.StrictSameOrigin(r, home) {
		return nil
	}
	v := s.visitor(r)
	if v == nil || v.CSRF == "" || subtle.ConstantTimeCompare([]byte(r.PostFormValue("_csrf")), []byte(v.CSRF)) != 1 {
		return nil
	}
	return v
}
