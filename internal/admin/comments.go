package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/theme"
)

func (a *Admin) registerComments(mux *http.ServeMux) {
	mux.Handle("GET /admin/comments/{$}", a.page(auth.CapModerateComments, a.commentList))
	mux.Handle("POST /admin/comments/bulk", a.page(auth.CapModerateComments, a.commentBulk))
	mux.Handle("POST /admin/comments/empty", a.page(auth.CapModerateComments, a.commentEmpty))
	mux.Handle("POST /admin/api/comments/{id}/{action}", a.page(auth.CapModerateComments, a.commentAction))
	mux.Handle("GET /admin/comments/{id}/edit", a.page(auth.CapModerateComments, a.commentEdit))
	mux.Handle("POST /admin/comments/{id}/edit", a.page(auth.CapModerateComments, a.commentSave))
}

type commentListData struct {
	basePage
	Views     []statusView
	Rows      []commentRow
	Status    string
	Search    string
	PostID    int64
	Post      *db.Post
	Total     int
	Paged     int
	Pages     int
	PageURL   func(int) string
	BulkOpts  []option
	CanEmpty  bool
	EmptyWhat string
}

var commentStatuses = []struct{ Status, Label string }{
	{"", "All"}, {"mine", "Mine"}, {db.CommentPending, "Pending"}, {db.CommentApproved, "Approved"},
	{db.CommentSpam, "Spam"}, {db.CommentTrash, "Trash"},
}

var commentMessages = map[string]notice{
	"bulk":    {"success", "Comments updated."},
	"emptied": {"success", "Comments permanently deleted."},
	"saved":   {"success", "Comment updated."},
}

// GET /admin/comments/
func (a *Admin) commentList(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	q := r.URL.Query()
	d := &commentListData{basePage: a.base(ctx, c, "Comments", "comments")}
	d.Scripts = []string{"list.js", "comments.js"}
	d.Status = q.Get("comment_status")
	valid := false
	for _, s := range commentStatuses {
		if s.Status == d.Status {
			valid = true
		}
	}
	if !valid {
		d.Status = ""
	}
	d.Search = strings.TrimSpace(q.Get("s"))
	if len(d.Search) > 200 {
		d.Search = d.Search[:200]
	}
	d.PostID = atoi64(q.Get("p"))
	if d.PostID > 0 {
		if p, err := db.GetPost(ctx, a.db.R, d.PostID); err == nil {
			d.Post = p
		} else {
			d.PostID = 0
		}
	}
	d.Paged = max(1, int(atoi64(q.Get("paged"))))
	if msg, ok := commentMessages[q.Get("msg")]; ok {
		d.Notice = &msg
	}
	counts, err := db.CommentStatusCounts(ctx, a.db.R, d.PostID)
	if err != nil {
		return err
	}
	base := "/admin/comments/"
	if d.PostID > 0 {
		base = withQuery(base, "p", d.PostID)
	}
	for _, s := range commentStatuses {
		n := 0
		switch s.Status {
		case "":
			n = counts[db.CommentApproved] + counts[db.CommentPending]
		case "mine":
			n = -1 // not counted
		default:
			n = counts[s.Status]
		}
		d.Views = append(d.Views, statusView{Label: s.Label, URL: withQuery(base, "comment_status", s.Status), Count: n, Current: d.Status == s.Status})
	}
	cq := db.AdminCommentQuery{Status: d.Status, Search: d.Search, PostID: d.PostID, Limit: perPage, Offset: (d.Paged - 1) * perPage}
	if d.Status == "mine" {
		cq.Status, cq.UserID = "", c.user.ID
	}
	cs, total, err := db.AdminComments(ctx, a.db.R, cq)
	if err != nil {
		return err
	}
	d.Total = total
	d.Pages = max(1, (total+perPage-1)/perPage)
	d.PageURL = func(n int) string { return withQuery(r.URL.RequestURI(), "paged", n) }
	if d.Rows, err = a.commentRows(r, c, cs); err != nil {
		return err
	}
	switch d.Status {
	case db.CommentSpam:
		d.BulkOpts = []option{{Value: content.CommentUnspam, Label: "Not Spam"}, {Value: content.CommentDelete, Label: "Delete Permanently"}}
		d.CanEmpty, d.EmptyWhat = counts[db.CommentSpam] > 0, "Spam"
	case db.CommentTrash:
		d.BulkOpts = []option{{Value: content.CommentUntrash, Label: "Restore"}, {Value: content.CommentDelete, Label: "Delete Permanently"}}
		d.CanEmpty, d.EmptyWhat = counts[db.CommentTrash] > 0, "Trash"
	default:
		d.BulkOpts = []option{{Value: content.CommentUnapprove, Label: "Unapprove"}, {Value: content.CommentApprove, Label: "Approve"},
			{Value: content.CommentSpam, Label: "Mark as Spam"}, {Value: content.CommentTrash, Label: "Move to Trash"}}
	}
	return a.render(w, "comments", d)
}

// POST /admin/comments/bulk
func (a *Admin) commentBulk(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ids := parseIDs(r.PostForm["comment[]"])
	if _, err := a.editor(r, c).ModerateComments(r.Context(), r.PostFormValue("action"), ids); err != nil {
		return a.contentError(err)
	}
	return seeOther(w, r, withQuery(safeRedirect(r.PostFormValue("redirect")), "msg", "bulk"))
}

// POST /admin/comments/empty (status=spam|trash)
func (a *Admin) commentEmpty(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	status := r.PostFormValue("status")
	if _, err := a.editor(r, c).EmptyComments(r.Context(), status); err != nil {
		return a.contentError(err)
	}
	return seeOther(w, r, "/admin/comments/?comment_status="+status+"&msg=emptied")
}

// POST /admin/api/comments/{id}/{action}: moderation from the list and the
// dashboard without reloading (approve, unapprove, spam, unspam, trash,
// untrash, delete, reply, quick-edit). Answers with the new counts.
func (a *Admin) commentAction(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	id := atoi64(r.PathValue("id"))
	cm, err := db.GetComment(ctx, a.db.R, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	ed := a.editor(r, c)
	res := map[string]any{"ok": true}
	switch action := r.PathValue("action"); action {
	case "reply":
		reply, err := ed.ReplyComment(ctx, cm, r.PostFormValue("content"))
		if err != nil {
			return a.contentError(err)
		}
		res["id"] = reply.ID
	case "quick-edit":
		in := &content.CommentInput{Author: r.PostFormValue("author"), Email: r.PostFormValue("email"),
			URL: r.PostFormValue("url"), Content: r.PostFormValue("content"), Status: cm.Status}
		if err := ed.UpdateComment(ctx, cm, in); err != nil {
			return a.contentError(err)
		}
		cm, _ = db.GetComment(ctx, a.db.R, id)
		res["html"] = string(content.CommentHTML(cm.Content))
	default:
		if _, err := ed.ModerateComments(ctx, action, []int64{id}); err != nil {
			return a.contentError(err)
		}
	}
	counts, err := db.CommentStatusCounts(ctx, a.db.R, 0)
	if err != nil {
		return err
	}
	res["counts"] = counts
	writeJSON(w, http.StatusOK, res)
	return nil
}

type commentEditData struct {
	basePage
	Comment  *db.Comment
	PostLink string
	PostName string
	Date     string
	DateText string
	Error    string
}

// GET /admin/comments/{id}/edit
func (a *Admin) commentEdit(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	cm, err := db.GetComment(r.Context(), a.db.R, atoi64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	return a.renderCommentEdit(w, r, c, cm, "")
}

func (a *Admin) renderCommentEdit(w http.ResponseWriter, r *http.Request, c *reqCtx, cm *db.Comment, errMsg string) error {
	d := &commentEditData{basePage: a.base(r.Context(), c, "Edit Comment", "comments"), Comment: cm, Error: errMsg}
	if p, err := db.GetPost(r.Context(), a.db.R, cm.PostID); err == nil {
		d.PostName = p.Title
		d.PostLink = editURL(p)
	}
	d.Date = cm.Date.Format("2006-01-02T15:04")
	d.DateText = theme.FormatPHPDate("M j, Y \\a\\t H:i", cm.Date.Time)
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "comment-edit", d)
}

// POST /admin/comments/{id}/edit
func (a *Admin) commentSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	cm, err := db.GetComment(ctx, a.db.R, atoi64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	in := &content.CommentInput{Author: r.PostFormValue("author"), Email: r.PostFormValue("email"), URL: r.PostFormValue("url"),
		Content: r.PostFormValue("content"), Status: r.PostFormValue("status"), Date: r.PostFormValue("date")}
	if err := a.editor(r, c).UpdateComment(ctx, cm, in); err != nil {
		var ve *content.ValidationError
		if errors.As(err, &ve) {
			edited := *cm
			edited.Author, edited.AuthorEmail, edited.AuthorURL, edited.Content = in.Author, in.Email, in.URL, in.Content
			return a.renderCommentEdit(w, r, c, &edited, ve.Msg)
		}
		return a.contentError(err)
	}
	return seeOther(w, r, "/admin/comments/?msg=saved")
}
