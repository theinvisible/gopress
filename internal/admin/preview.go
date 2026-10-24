package admin

import (
	"net/http"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
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
