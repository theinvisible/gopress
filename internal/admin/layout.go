package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

// basePage is the data every admin page template gets.
type basePage struct {
	Title     string
	Screen    string // menu item id of the current page, e.g. "posts-all"
	BodyClass string
	SiteName  string
	Home      string
	User      *db.User
	CSRF      string
	Color     string // admin color scheme
	Menu      []menuItem
	Pending   int // comments awaiting moderation
	Notice    *notice
	Assets    *assets
	Scripts   []string // extra scripts (asset paths)
	Styles    []string // extra stylesheets (asset paths)
}

type notice struct {
	Kind string // success, error, warning, info
	Text string
}

type menuItem struct {
	ID, Label, Icon, URL string
	Current              bool
	Badge                int
	Separator            bool
	Sub                  []subItem
}

type subItem struct {
	ID, Label, URL string
	Current        bool
}

// colorSchemes are the admin color schemes users can choose ("fresh" is
// built into the base stylesheets).
var colorSchemes = []string{"fresh", "light", "modern", "blue", "coffee", "ectoplasm", "midnight", "ocean", "sunrise"}

// base fills the common page data. screen selects the current menu entry.
func (a *Admin) base(ctx context.Context, c *reqCtx, title, screen string) basePage {
	p := basePage{
		Title:    title,
		Screen:   screen,
		SiteName: c.settings.Name,
		Home:     c.links.Home(),
		User:     c.user,
		CSRF:     c.cur.CSRF,
		Color:    "fresh",
		Assets:   a.assets,
	}
	if col, _ := db.UserMeta(ctx, a.db.R, c.user.ID, "admin_color"); col != "" {
		for _, s := range colorSchemes {
			if s == col {
				p.Color = col
			}
		}
	}
	if c.cur.Session.State == db.SessionFull {
		if auth.Can(c.user, auth.CapModerateComments) {
			counts, err := db.CommentStatusCounts(ctx, a.db.R, 0)
			if err == nil {
				p.Pending = counts[db.CommentPending]
			}
		}
		p.Menu = a.menu(c.user, screen, p.Pending)
	}
	p.BodyClass = "screen-" + screen
	return p
}

func (a *Admin) menu(u *db.User, screen string, pending int) []menuItem {
	var m []menuItem
	m = append(m, menuItem{ID: "dashboard", Label: "Dashboard", Icon: "dashboard", URL: "/admin/"})
	m = append(m, menuItem{Separator: true})
	if auth.CanEditType(u, db.TypePost) {
		m = append(m, menuItem{ID: "posts", Label: "Posts", Icon: "admin-post", URL: "/admin/posts/", Sub: []subItem{
			{ID: "posts-all", Label: "All Posts", URL: "/admin/posts/"},
			{ID: "posts-new", Label: "Add Post", URL: "/admin/posts/new"},
		}})
	}
	if auth.CanEditType(u, db.TypePage) {
		m = append(m, menuItem{ID: "pages", Label: "Pages", Icon: "admin-page", URL: "/admin/pages/", Sub: []subItem{
			{ID: "pages-all", Label: "All Pages", URL: "/admin/pages/"},
			{ID: "pages-new", Label: "Add Page", URL: "/admin/pages/new"},
		}})
	}
	if auth.Can(u, auth.CapModerateComments) {
		m = append(m, menuItem{ID: "comments", Label: "Comments", Icon: "admin-comments", URL: "/admin/comments/", Badge: pending})
	}
	m = append(m, menuItem{Separator: true})
	m = append(m, menuItem{ID: "profile", Label: "Profile", Icon: "admin-users", URL: "/admin/profile", Sub: []subItem{
		{ID: "profile", Label: "Profile", URL: "/admin/profile"},
		{ID: "security", Label: "Security", URL: "/admin/profile/security"},
	}})
	for i := range m {
		it := &m[i]
		for j := range it.Sub {
			if it.Sub[j].ID == screen {
				it.Sub[j].Current = true
				it.Current = true
			}
		}
		if it.ID == screen || strings.HasPrefix(screen, it.ID+"-") {
			it.Current = true
		}
	}
	return m
}

// safeRedirect returns target if it is a local admin path, else /admin/.
// It prevents open redirects through redirect_to parameters.
func safeRedirect(target string) string {
	if strings.HasPrefix(target, "/admin/") && !strings.HasPrefix(target, "//") &&
		!strings.ContainsAny(target, "\\\r\n") {
		return target
	}
	return "/admin/"
}

func seeOther(w http.ResponseWriter, r *http.Request, target string) error {
	http.Redirect(w, r, target, http.StatusSeeOther)
	return nil
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
