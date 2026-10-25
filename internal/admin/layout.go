package admin

import (
	"context"
	"html"
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
	NewItems  []subItem // the toolbar's "New" menu (by capability)
	Pending   int       // comments awaiting moderation
	Notice    *notice
	Assets    *assets
	Scripts   []string // extra scripts (asset paths)
	Styles    []string // extra stylesheets (asset paths)
	// Live enables live updates (live.js); LiveTopics are the page's
	// topics (e.g. "post:12"), checked when the stream opens.
	Live       bool
	LiveTopics string
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
		SiteName: html.UnescapeString(c.settings.Name), // stored escaped
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
		p.Live = a.live != nil
		if auth.Can(c.user, auth.CapModerateComments) {
			counts, err := db.CommentStatusCounts(ctx, a.db.R, 0)
			if err == nil {
				p.Pending = counts[db.CommentPending]
			}
		}
		p.Menu = a.menu(c.user, screen, p.Pending)
		for _, it := range []struct {
			cap string
			sub subItem
		}{{auth.CapEditPosts, subItem{ID: "post", Label: "Post", URL: "/admin/posts/new"}},
			{auth.CapUploadFiles, subItem{ID: "media", Label: "Media", URL: "/admin/media/"}},
			{auth.CapEditPages, subItem{ID: "page", Label: "Page", URL: "/admin/pages/new"}},
			{auth.CapCreateUsers, subItem{ID: "user", Label: "User", URL: "/admin/users/new"}}} {
			if auth.Can(c.user, it.cap) {
				p.NewItems = append(p.NewItems, it.sub)
			}
		}
		// Failed second-factor attempts since the last login: someone
		// else may know the password.
		if n, _ := db.UserMeta(ctx, a.db.R, c.user.ID, metaMFAWarning); n != "" && n != "0" {
			p.Notice = &notice{"error", n + " failed two-factor attempts happened since your last login. If that was not you, someone knows your password: change it now."}
			db.SetUserMeta(ctx, a.db.W, c.user.ID, metaMFAWarning, "")
		}
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
		if auth.Can(u, auth.CapManageCategories) {
			m[len(m)-1].Sub = append(m[len(m)-1].Sub,
				subItem{ID: "posts-categories", Label: "Categories", URL: "/admin/categories/"},
				subItem{ID: "posts-tags", Label: "Tags", URL: "/admin/tags/"})
		}
	}
	if auth.CanEditType(u, db.TypePage) {
		m = append(m, menuItem{ID: "pages", Label: "Pages", Icon: "admin-page", URL: "/admin/pages/", Sub: []subItem{
			{ID: "pages-all", Label: "All Pages", URL: "/admin/pages/"},
			{ID: "pages-new", Label: "Add Page", URL: "/admin/pages/new"},
		}})
	}
	if auth.Can(u, auth.CapUploadFiles) {
		m = append(m, menuItem{ID: "media", Label: "Media", Icon: "admin-media", URL: "/admin/media/", Sub: []subItem{
			{ID: "media-library", Label: "Library", URL: "/admin/media/"},
		}})
	}
	if auth.Can(u, auth.CapModerateComments) {
		m = append(m, menuItem{ID: "comments", Label: "Comments", Icon: "admin-comments", URL: "/admin/comments/", Badge: pending})
	}
	m = append(m, menuItem{Separator: true})
	if auth.Can(u, auth.CapEditThemeOptions) {
		m = append(m, menuItem{ID: "appearance", Label: "Appearance", Icon: "admin-appearance", URL: "/admin/widgets", Sub: []subItem{
			{ID: "appearance-widgets", Label: "Widgets", URL: "/admin/widgets"},
			{ID: "appearance-customize", Label: "Customize", URL: "/admin/customize"},
			{ID: "appearance-menus", Label: "Menus", URL: "/admin/menus"},
		}})
	}
	if auth.Can(u, auth.CapListUsers) {
		sub := []subItem{{ID: "users-all", Label: "All Users", URL: "/admin/users/"}}
		if auth.Can(u, auth.CapCreateUsers) {
			sub = append(sub, subItem{ID: "users-new", Label: "Add User", URL: "/admin/users/new"})
		}
		m = append(m, menuItem{ID: "users", Label: "Users", Icon: "admin-users", URL: "/admin/users/", Sub: sub})
	}
	if auth.Can(u, auth.CapManageOptions) {
		var sub []subItem
		for _, s := range settingsScreens {
			sub = append(sub, subItem{ID: "settings-" + s.ID, Label: s.Label, URL: "/admin/settings/" + s.ID})
		}
		m = append(m, menuItem{ID: "settings", Label: "Settings", Icon: "admin-settings", URL: "/admin/settings/general", Sub: sub})
	}
	profile := []subItem{
		{ID: "profile", Label: "Profile", URL: "/admin/profile"},
		{ID: "security", Label: "Security", URL: "/admin/profile/security"},
	}
	if auth.CanUseTokens(u) {
		profile = append(profile, subItem{ID: "tokens", Label: "Access Tokens", URL: "/admin/profile/tokens"})
	}
	m = append(m, menuItem{ID: "profile", Label: "Profile", Icon: "id", URL: "/admin/profile", Sub: profile})
	m = a.menuWithPlugins(m, func(c string) bool { return auth.Can(u, c) })
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
