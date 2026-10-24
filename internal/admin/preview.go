package admin

import (
	"encoding/json"
	"net/http"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/site"
)

// previewer implements site.Previewer.
type previewer struct{ a *Admin }

// Previewer returns the preview authorization for the public site.
func (a *Admin) Previewer() *previewer { return &previewer{a} }

// PreviewPost allows previews to logged-in users who may edit the post and
// shows their newest autosave if there is one.
func (pv *previewer) PreviewPost(r *http.Request, id int64) (*db.Post, bool) {
	ctx := r.Context()
	cur, err := pv.a.auth.Load(ctx, r)
	if err != nil || cur == nil || cur.Session.State != db.SessionFull || id <= 0 {
		return nil, false
	}
	p, err := db.GetPost(ctx, pv.a.db.R, id)
	if err != nil || p.Status == db.StatusTrash || !auth.CanEditPost(cur.User, p) {
		return nil, false
	}
	if as, err := db.Autosave(ctx, pv.a.db.R, p.ID, cur.User.ID); err == nil && as != nil && as.CreatedGMT.After(p.ModifiedGMT.Time) {
		p.Title, p.Content, p.Excerpt, p.ContentFormat = as.Title, as.Content, as.Excerpt, as.ContentFormat
	}
	return p, true
}

// metaShowBar is the user preference "Show Toolbar when viewing site".
const metaShowBar = "show_admin_bar_front"

// metaCustomizeDraft holds the unpublished customizer values of a user.
const metaCustomizeDraft = "customize_draft"

// Visitor identifies a logged-in user on the public site (site.Previewer).
// Only complete logins count. Customizer drafts apply only inside the
// customizer's preview frame (Sec-Fetch-Dest: iframe from this origin),
// never to normal page views.
func (pv *previewer) Visitor(r *http.Request) *site.Visitor {
	ctx := r.Context()
	cur, err := pv.a.auth.Load(ctx, r)
	if err != nil || cur == nil || cur.Session.State != db.SessionFull {
		return nil
	}
	u := cur.User
	v := &site.Visitor{ID: u.ID, Login: u.Login, Name: u.DisplayName, Email: u.Email, URL: u.URL, CSRF: cur.CSRF,
		DisplayName: u.DisplayName, AvatarURL: avatarURL(u.Email, 64),
		Can:     func(c string) bool { return auth.Can(u, c) },
		CanEdit: func(p *db.Post) bool { return auth.CanEditPost(u, p) },
		BarStyles: []string{pv.a.assets.URL("wp/wp-includes/css/dashicons.min.css"),
			pv.a.assets.URL("wp/wp-includes/css/admin-bar.min.css")}}
	show, _ := db.UserMeta(ctx, pv.a.db.R, u.ID, metaShowBar)
	v.ShowBar = show != "false"
	if auth.Can(u, auth.CapModerateComments) {
		if counts, err := db.CommentStatusCounts(ctx, pv.a.db.R, 0); err == nil {
			v.Pending = counts[db.CommentPending]
		}
	}
	if r.Header.Get("Sec-Fetch-Dest") == "iframe" && r.Header.Get("Sec-Fetch-Site") == "same-origin" && auth.Can(u, auth.CapEditThemeOptions) {
		if raw, _ := db.UserMeta(ctx, pv.a.db.R, u.ID, metaCustomizeDraft); raw != "" {
			var d map[string]any
			if json.Unmarshal([]byte(raw), &d) == nil {
				v.Draft = d
				v.ShowBar = false // like WordPress' customizer preview
			}
		}
	}
	return v
}
