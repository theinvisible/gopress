package admin

import (
	"html"
	"html/template"
	"net/http"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/theme"
)

type dashboardData struct {
	basePage
	Posts, Pages, Approved, Pending, Spam int
	Scheduled, Published                  []postRow
	Comments                              []commentRow
	Drafts                                []postRow
	AutoApprove                           bool
	CanPost, CanModerate                  bool
	Plugins                               []pluginBox
}

// postRow is a post prepared for lists.
type postRow struct {
	ID      int64
	Title   template.HTML
	Status  string
	EditURL string
	ViewURL string
	Date    string
	Excerpt string
}

// commentRow is a comment prepared for lists.
type commentRow struct {
	db.Comment
	PostTitle   template.HTML
	PostEditURL string
	PostViewURL string
	Text        template.HTML
	When        string
	Avatar      string
}

// GET /admin/
func (a *Admin) dashboard(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	d := &dashboardData{basePage: a.base(ctx, c, "Dashboard", "dashboard"), Plugins: a.pluginDashboard(ctx, c)}
	d.Scripts = []string{"comments.js"}
	d.CanPost = auth.CanEditType(c.user, db.TypePost)
	d.CanModerate = auth.Can(c.user, auth.CapModerateComments)
	if pc, err := db.PostStatusCounts(ctx, a.db.R, db.TypePost, 0); err == nil {
		d.Posts = pc[db.StatusPublish]
	} else {
		return err
	}
	if pc, err := db.PostStatusCounts(ctx, a.db.R, db.TypePage, 0); err == nil {
		d.Pages = pc[db.StatusPublish]
	} else {
		return err
	}
	cc, err := db.CommentStatusCounts(ctx, a.db.R, 0)
	if err != nil {
		return err
	}
	d.Approved, d.Pending, d.Spam = cc[db.CommentApproved], cc[db.CommentPending], cc[db.CommentSpam]
	d.AutoApprove = d.CanModerate && !c.settings.CommentModeration && !c.settings.CommentPreviouslyApproved

	list := func(status string, asc bool, limit int, authorID int64) ([]postRow, error) {
		posts, _, err := db.AdminPosts(ctx, a.db.R, db.AdminPostQuery{Type: db.TypePost, Status: status,
			Ascending: asc, Limit: limit, AuthorID: authorID})
		if err != nil {
			return nil, err
		}
		rows := make([]postRow, len(posts))
		for i := range posts {
			rows[i] = a.postRow(c, &posts[i], "M jS, g:i a")
		}
		return rows, nil
	}
	if d.CanPost {
		if d.Scheduled, err = list(db.StatusFuture, true, 5, 0); err != nil {
			return err
		}
		if d.Published, err = list(db.StatusPublish, false, 5, 0); err != nil {
			return err
		}
		if d.Drafts, err = list(db.StatusDraft, false, 3, c.user.ID); err != nil {
			return err
		}
	}
	if d.CanModerate {
		cs, _, err := db.AdminComments(ctx, a.db.R, db.AdminCommentQuery{Limit: 5})
		if err != nil {
			return err
		}
		if d.Comments, err = a.commentRows(r, c, cs); err != nil {
			return err
		}
	}
	if r.URL.Query().Get("msg") == "draft-saved" {
		if d.Notice == nil {
			d.Notice = &notice{"success", "Draft saved."}
		}
	}
	return a.render(w, "dashboard", d)
}

func (a *Admin) postRow(c *reqCtx, p *db.Post, dateFormat string) postRow {
	row := postRow{ID: p.ID, Title: escTitle(p.Title), Status: p.Status, EditURL: editURL(p)}
	if p.Title == "" {
		row.Title = "(no title)"
	}
	if p.Status == db.StatusPublish || p.Status == db.StatusPrivate {
		row.ViewURL = c.links.PostURL(p)
	} else {
		row.ViewURL = previewURL(c, p)
	}
	if !p.Date.IsZero() {
		row.Date = theme.FormatPHPDate(dateFormat, p.Date.Time)
	}
	row.Excerpt = truncateWords(render.PlainText(p.Content), 10)
	return row
}

func (a *Admin) commentRows(r *http.Request, c *reqCtx, cs []db.Comment) ([]commentRow, error) {
	ctx := r.Context()
	titles := map[int64]*db.Post{}
	rows := make([]commentRow, len(cs))
	for i, cm := range cs {
		p, ok := titles[cm.PostID]
		if !ok {
			var err error
			if p, err = db.GetPost(ctx, a.db.R, cm.PostID); err != nil {
				p = &db.Post{ID: cm.PostID}
			}
			titles[cm.PostID] = p
		}
		row := commentRow{Comment: cm, PostTitle: escTitle(p.Title), PostEditURL: editURL(p), Avatar: avatarURL(cm.AuthorEmail, 50)}
		if p.Status == db.StatusPublish {
			row.PostViewURL = c.links.PostURL(p)
		}
		row.Text = content.CommentHTML(cm.Content)
		row.When = theme.FormatPHPDate("Y/m/d \\a\\t g:i a", cm.Date.Time)
		rows[i] = row
	}
	return rows, nil
}

func editURL(p *db.Post) string {
	if p.Type == db.TypePage {
		return "/admin/pages/" + itoa(p.ID) + "/edit"
	}
	return "/admin/posts/" + itoa(p.ID) + "/edit"
}

func previewURL(c *reqCtx, p *db.Post) string {
	return c.links.URL("/?p=" + itoa(p.ID) + "&preview=true")
}

func truncateWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) <= n {
		return strings.Join(f, " ")
	}
	return strings.Join(f[:n], " ") + "…"
}

// POST /admin/quick-draft
func (a *Admin) quickDraft(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	title := strings.TrimSpace(r.PostFormValue("post_title"))
	body := strings.TrimSpace(r.PostFormValue("content"))
	if title == "" && body == "" {
		return seeOther(w, r, "/admin/")
	}
	var b strings.Builder
	for _, para := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		if para = strings.TrimSpace(para); para != "" {
			b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(para), "\n", "<br>\n") + "</p>\n")
		}
	}
	in := &content.PostInput{Type: db.TypePost, Title: title, Content: b.String(), Status: db.StatusDraft}
	if _, err := a.editor(r, c).Save(ctx, nil, in); err != nil {
		return err
	}
	return seeOther(w, r, "/admin/?msg=draft-saved")
}
