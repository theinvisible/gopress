package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/theme"
)

// postType describes the admin screens of a post type.
type postType struct {
	Type     string
	Base     string // "/admin/posts/"
	Singular string
	Plural   string
	Screen   string // menu id prefix
}

var (
	typePost = postType{db.TypePost, "/admin/posts/", "Post", "Posts", "posts"}
	typePage = postType{db.TypePage, "/admin/pages/", "Page", "Pages", "pages"}
)

func (a *Admin) registerPosts(mux *http.ServeMux) {
	for _, pt := range []postType{typePost, typePage} {
		editCap := auth.CapEditPosts
		if pt.Type == db.TypePage {
			editCap = auth.CapEditPages
		}
		mux.Handle("GET "+pt.Base+"{$}", a.page(editCap, a.withType(pt, a.postList)))
		mux.Handle("POST "+pt.Base+"bulk", a.page(editCap, a.withType(pt, a.postBulk)))
		mux.Handle("GET "+pt.Base+"new", a.page(editCap, a.withType(pt, a.postNew)))
		mux.Handle("POST "+pt.Base+"new", a.page(editCap, a.withType(pt, a.postSave)))
		mux.Handle("GET "+pt.Base+"{id}/edit", a.page(editCap, a.withType(pt, a.postEdit)))
		mux.Handle("POST "+pt.Base+"{id}/edit", a.page(editCap, a.withType(pt, a.postSave)))
		mux.Handle("POST "+pt.Base+"{id}/{action}", a.page(editCap, a.withType(pt, a.postAction)))
		mux.Handle("GET "+pt.Base+"{id}/revisions", a.page(editCap, a.withType(pt, a.revisionsPage)))
	}
	mux.Handle("POST /admin/api/autosave", a.page(auth.CapEditPosts+"|"+auth.CapEditPages, a.apiAutosave))
	mux.Handle("GET /admin/api/media", a.page(auth.CapEditPosts+"|"+auth.CapEditPages, a.apiMedia))
	mux.Handle("GET /admin/api/tags", a.page(auth.CapEditPosts, a.apiTags))
	mux.Handle("POST /admin/api/categories", a.page(auth.CapManageCategories, a.apiAddCategory))
}

type typedHandler func(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error

func (a *Admin) withType(pt postType, h typedHandler) handler {
	return func(w http.ResponseWriter, r *http.Request, c *reqCtx) error { return h(w, r, c, pt) }
}

func (a *Admin) editor(r *http.Request, c *reqCtx) *content.Editor {
	return a.content.As(c.user, r)
}

// loadPost loads the post {id} of the request and checks its type.
func (a *Admin) loadPost(r *http.Request, pt postType) (*db.Post, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, errNotFound
	}
	p, err := db.GetPost(r.Context(), a.db.R, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && p.Type != pt.Type) {
		return nil, errNotFound
	}
	return p, err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// ---- List table (edit.php) ----

type statusView struct {
	Label   string
	URL     string
	Count   int
	Current bool
}

type option struct {
	Value, Label string
	Selected     bool
	Level        int
}

type termLink struct {
	Name, URL string
}

type listRow struct {
	postRow
	Post        *db.Post
	Level       int
	States      []string
	AuthorName  string
	AuthorURL   string
	Cats, Tags  []termLink
	Comments    int
	Pending     int
	CommentsURL string
	DateLabel   string
	DateText    string
	CanEdit     bool
	CanDelete   bool
	Trashed     bool
	QuickEdit   map[string]any
}

type postListData struct {
	basePage
	PT          postType
	Views       []statusView
	Rows        []listRow
	Status      string
	Search      string
	Month       string
	Cat         int64
	OrderBy     string
	Order       string
	Months      []option
	Cats        []option
	Total       int
	Paged       int
	Pages       int
	PageURL     func(int) string
	SortURL     map[string]string
	SortDir     map[string]string
	BulkActions []option
	CanCreate   bool
	Categories  []option // quick edit
	Parents     []option // quick edit (pages)
	Templates   []option
	ShowEmpty   bool
}

const perPage = 20

var statusLabels = []struct{ Status, Label string }{
	{db.StatusPublish, "Published"}, {db.StatusFuture, "Scheduled"}, {db.StatusDraft, "Drafts"},
	{db.StatusPending, "Pending"}, {db.StatusPrivate, "Private"}, {db.StatusTrash, "Trash"},
}

var listMessages = map[string]notice{
	"trashed":    {"success", "Moved to the Trash."},
	"restored":   {"success", "Restored from the Trash."},
	"deleted":    {"success", "Permanently deleted."},
	"duplicated": {"success", "Copy created as a draft."},
	"bulk":       {"success", "Bulk action applied."},
}

// GET /admin/posts/
func (a *Admin) postList(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	ctx := r.Context()
	q := r.URL.Query()
	d := &postListData{basePage: a.base(ctx, c, pt.Plural, pt.Screen+"-all"), PT: pt}
	d.Scripts = []string{"list.js"}
	d.CanCreate = auth.CanEditType(c.user, pt.Type)
	d.Status = q.Get("post_status")
	if !slices.ContainsFunc(statusLabels, func(s struct{ Status, Label string }) bool { return s.Status == d.Status }) {
		d.Status = ""
	}
	d.Search = strings.TrimSpace(q.Get("s"))
	if len(d.Search) > 200 {
		d.Search = d.Search[:200]
	}
	d.Month = q.Get("m")
	if len(d.Month) != 6 {
		d.Month = ""
	}
	d.Cat, _ = strconv.ParseInt(q.Get("cat"), 10, 64)
	d.OrderBy = q.Get("orderby")
	if d.OrderBy != "title" && d.OrderBy != "comments" && d.OrderBy != "date" {
		d.OrderBy = ""
	}
	d.Order = q.Get("order")
	if d.Order != "asc" {
		d.Order = "desc"
	}
	authorID, _ := strconv.ParseInt(q.Get("author"), 10, 64)
	// Users who cannot edit others' posts only see their own (WordPress
	// shows all published ones, but they cannot act on them anyway).
	if !auth.Can(c.user, capFor(pt, auth.CapEditOthersPosts)) {
		authorID = c.user.ID
	}
	d.Paged, _ = strconv.Atoi(q.Get("paged"))
	d.Paged = max(d.Paged, 1)
	if msg, ok := listMessages[q.Get("msg")]; ok {
		d.Notice = &msg
	}

	counts, err := db.PostStatusCounts(ctx, a.db.R, pt.Type, authorID)
	if err != nil {
		return err
	}
	all := 0
	for s, n := range counts {
		if s != db.StatusTrash && s != "auto-draft" && s != db.StatusInherit {
			all += n
		}
	}
	d.Views = append(d.Views, statusView{"All", pt.Base, all, d.Status == ""})
	for _, s := range statusLabels {
		if counts[s.Status] > 0 {
			d.Views = append(d.Views, statusView{s.Label, pt.Base + "?post_status=" + s.Status, counts[s.Status], d.Status == s.Status})
		}
	}

	aq := db.AdminPostQuery{Type: pt.Type, Status: d.Status, Search: d.Search, Month: d.Month, CategoryID: d.Cat,
		AuthorID: authorID, OrderBy: d.OrderBy, Ascending: d.Order == "asc", Limit: perPage, Offset: (d.Paged - 1) * perPage}
	var posts []db.Post
	levels := map[int64]int{}
	hierarchical := pt.Type == db.TypePage && d.Search == "" && d.Month == "" && d.OrderBy == "" && d.Status == "" && authorID == 0
	if hierarchical {
		pages, err := db.AllPages(ctx, a.db.R)
		if err != nil {
			return err
		}
		flat := flattenPages(pages, levels)
		d.Total = len(flat)
		start := min((d.Paged-1)*perPage, len(flat))
		posts = flat[start:min(start+perPage, len(flat))]
	} else {
		if pt.Type == db.TypePage && aq.OrderBy == "" {
			aq.OrderBy, aq.Ascending = "menu_order", true
		}
		if posts, d.Total, err = db.AdminPosts(ctx, a.db.R, aq); err != nil {
			return err
		}
	}
	d.Pages = max(1, (d.Total+perPage-1)/perPage)
	if d.Paged > d.Pages && d.Total > 0 {
		return seeOther(w, r, withQuery(r.URL.RequestURI(), "paged", d.Pages))
	}
	d.PageURL = func(n int) string { return withQuery(r.URL.RequestURI(), "paged", n) }
	d.SortURL, d.SortDir = map[string]string{}, map[string]string{}
	for _, col := range []string{"title", "comments", "date"} {
		cur := d.OrderBy == col || (col == "date" && d.OrderBy == "" && pt.Type == db.TypePost)
		next := "asc"
		if cur && d.Order == "asc" {
			next = "desc"
		}
		if col != "title" && !cur {
			next = "desc"
		}
		d.SortURL[col] = withQuery(r.URL.RequestURI(), "orderby", col, "order", next, "paged", "")
		switch {
		case cur && d.Order == "asc":
			d.SortDir[col] = "sorted asc"
		case cur:
			d.SortDir[col] = "sorted desc"
		case next == "asc":
			d.SortDir[col] = "sortable desc"
		default:
			d.SortDir[col] = "sortable asc"
		}
	}
	if d.Rows, err = a.listRows(ctx, c, pt, posts, levels); err != nil {
		return err
	}

	if d.Status == db.StatusTrash {
		d.BulkActions = []option{{Value: "restore", Label: "Restore"}, {Value: "delete", Label: "Delete permanently"}}
	} else {
		d.BulkActions = []option{{Value: "trash", Label: "Move to Trash"}}
	}
	months, err := db.PostMonths(ctx, a.db.R, pt.Type)
	if err != nil {
		return err
	}
	for _, m := range months {
		y, _ := strconv.Atoi(m[:4])
		mo, _ := strconv.Atoi(m[4:])
		if y == 0 || mo < 1 || mo > 12 {
			continue
		}
		d.Months = append(d.Months, option{Value: m, Label: fmt.Sprintf("%s %d", monthNames[mo-1], y), Selected: m == d.Month})
	}
	if pt.Type == db.TypePost {
		if d.Cats, err = a.categoryOptions(ctx, d.Cat); err != nil {
			return err
		}
		d.Categories = d.Cats
	} else {
		if d.Parents, err = a.parentOptions(ctx, 0, 0); err != nil {
			return err
		}
		for _, t := range content.PageTemplates {
			d.Templates = append(d.Templates, option{Value: t.Value, Label: t.Label})
		}
	}
	return a.render(w, "posts", d)
}

var monthNames = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September",
	"October", "November", "December"}

// capFor maps a post capability to its page equivalent.
func capFor(pt postType, c string) string {
	if pt.Type != db.TypePage {
		return c
	}
	return strings.Replace(c, "posts", "pages", 1)
}

// flattenPages orders pages as a tree (parents before children) and
// records each page's depth.
func flattenPages(pages []db.Post, levels map[int64]int) []db.Post {
	children := map[int64][]db.Post{}
	ids := map[int64]bool{}
	for _, p := range pages {
		ids[p.ID] = true
	}
	for _, p := range pages {
		parent := p.ParentID
		if !ids[parent] {
			parent = 0 // orphans (parent trashed) show at the top level
		}
		children[parent] = append(children[parent], p)
	}
	var out []db.Post
	seen := map[int64]bool{}
	var walk func(parent int64, level int)
	walk = func(parent int64, level int) {
		for _, p := range children[parent] {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			levels[p.ID] = level
			out = append(out, p)
			walk(p.ID, level+1)
		}
	}
	walk(0, 0)
	return out
}

func (a *Admin) listRows(ctx context.Context, c *reqCtx, pt postType, posts []db.Post, levels map[int64]int) ([]listRow, error) {
	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	terms, err := db.PostTerms(ctx, a.db.R, ids)
	if err != nil {
		return nil, err
	}
	pending, err := db.PendingCommentCounts(ctx, a.db.R, ids)
	if err != nil {
		return nil, err
	}
	users, err := db.Users(ctx, a.db.R)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, u := range users {
		names[u.ID] = u.DisplayName
	}
	rows := make([]listRow, len(posts))
	for i := range posts {
		p := &posts[i]
		row := listRow{postRow: a.postRow(c, p, "Y/m/d \\a\\t g:i a"), Post: p, Level: levels[p.ID],
			AuthorName: names[p.AuthorID], AuthorURL: withQuery(pt.Base, "author", p.AuthorID),
			Comments: p.CommentCount, Pending: pending[p.ID], CommentsURL: withQuery("/admin/comments/", "p", p.ID),
			CanEdit: auth.CanEditPost(c.user, p), CanDelete: auth.CanDeletePost(c.user, p), Trashed: p.Status == db.StatusTrash}
		if row.Trashed {
			// Trashed posts can be restored by whoever could delete them.
			probe := *p
			probe.Status = db.StatusDraft
			row.CanDelete = auth.CanDeletePost(c.user, &probe)
		}
		switch p.Status {
		case db.StatusDraft:
			row.States = append(row.States, "Draft")
		case db.StatusPending:
			row.States = append(row.States, "Pending")
		case db.StatusFuture:
			row.States = append(row.States, "Scheduled")
		case db.StatusPrivate:
			row.States = append(row.States, "Private")
		}
		if p.Password != "" {
			row.States = append(row.States, "Password protected")
		}
		if slices.Contains(c.settings.Sticky, p.ID) {
			row.States = append(row.States, "Sticky")
		}
		if p.Type == db.TypePage && c.settings.ShowOnFront == "page" {
			if p.ID == c.settings.PageOnFront {
				row.States = append(row.States, "Front Page")
			} else if p.ID == c.settings.PageForPosts {
				row.States = append(row.States, "Posts Page")
			}
		}
		switch p.Status {
		case db.StatusPublish, db.StatusPrivate:
			row.DateLabel = "Published"
		case db.StatusFuture:
			row.DateLabel = "Scheduled"
		default:
			row.DateLabel = "Last Modified"
			row.Date = theme.FormatPHPDate("Y/m/d \\a\\t g:i a", p.Modified.Time)
		}
		var catIDs []int64
		var tagNames []string
		for _, t := range terms[p.ID] {
			link := termLink{t.Name, withQuery(pt.Base, "cat", t.ID)}
			if t.Taxonomy == db.TaxCategory {
				row.Cats = append(row.Cats, link)
				catIDs = append(catIDs, t.ID)
			} else if t.Taxonomy == db.TaxTag {
				link.URL = withQuery(pt.Base, "s", t.Name)
				row.Tags = append(row.Tags, link)
				tagNames = append(tagNames, t.Name)
			}
		}
		date := ""
		if !p.DateGMT.IsZero() || p.Status == db.StatusPublish {
			date = p.Date.Format("2006-01-02T15:04")
		}
		row.QuickEdit = map[string]any{
			"id": p.ID, "title": p.Title, "slug": p.Slug, "date": date, "password": p.Password,
			"status": p.Status, "comments": p.CommentStatus == "open", "pings": p.PingStatus == "open",
			"sticky": slices.Contains(c.settings.Sticky, p.ID), "categories": catIDs, "tags": strings.Join(tagNames, ", "),
			"parent": p.ParentID, "menu_order": p.MenuOrder, "template": p.Template,
		}
		rows[i] = row
	}
	return rows, nil
}

func (a *Admin) categoryOptions(ctx context.Context, selected int64) ([]option, error) {
	cats, err := db.Terms(ctx, a.db.R, db.TaxCategory)
	if err != nil {
		return nil, err
	}
	byParent := map[int64][]db.Term{}
	for _, t := range cats {
		byParent[t.ParentID] = append(byParent[t.ParentID], t)
	}
	for k := range byParent {
		slices.SortFunc(byParent[k], func(x, y db.Term) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	}
	var out []option
	seen := map[int64]bool{}
	var walk func(parent int64, level int)
	walk = func(parent int64, level int) {
		for _, t := range byParent[parent] {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, option{Value: itoa(t.ID), Label: t.Name, Level: level, Selected: t.ID == selected})
			walk(t.ID, level+1)
		}
	}
	walk(0, 0)
	return out, nil
}

// parentOptions lists the pages that can be the parent of page id.
func (a *Admin) parentOptions(ctx context.Context, id, selected int64) ([]option, error) {
	pages, err := db.AllPages(ctx, a.db.R)
	if err != nil {
		return nil, err
	}
	levels := map[int64]int{}
	flat := flattenPages(pages, levels)
	out := []option{{Value: "0", Label: "(no parent)", Selected: selected == 0}}
	skipBelow := -1
	for _, p := range flat {
		lvl := levels[p.ID]
		if skipBelow >= 0 && lvl > skipBelow {
			continue // descendants of the page itself
		}
		skipBelow = -1
		if p.ID == id {
			skipBelow = lvl
			continue
		}
		title := p.Title
		if title == "" {
			title = "(no title)"
		}
		out = append(out, option{Value: itoa(p.ID), Label: title, Level: lvl, Selected: p.ID == selected})
	}
	return out, nil
}

// POST /admin/posts/bulk
func (a *Admin) postBulk(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	ctx := r.Context()
	action := r.PostFormValue("action")
	ed := a.editor(r, c)
	for _, s := range r.PostForm["post[]"] {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		p, err := db.GetPost(ctx, a.db.R, id)
		if err != nil || p.Type != pt.Type {
			continue
		}
		switch action {
		case "trash":
			err = ed.Trash(ctx, p)
		case "restore":
			err = ed.Restore(ctx, p)
		case "delete":
			err = ed.Delete(ctx, p)
		default:
			return badRequest("Unknown bulk action.")
		}
		if err != nil {
			return a.contentError(err)
		}
	}
	return seeOther(w, r, withQuery(safeRedirect(r.PostFormValue("redirect")), "msg", "bulk"))
}

// contentError maps content errors to HTTP errors.
func (a *Admin) contentError(err error) error {
	var ve *content.ValidationError
	switch {
	case errors.Is(err, content.ErrForbidden):
		return errForbidden
	case errors.Is(err, content.ErrInvalid):
		return badRequest("Invalid request.")
	case errors.As(err, &ve):
		return badRequest(ve.Msg)
	}
	return err
}

// POST /admin/posts/{id}/{action}: trash, restore, delete, duplicate,
// quick-edit, restore-revision.
func (a *Admin) postAction(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	ctx := r.Context()
	p, err := a.loadPost(r, pt)
	if err != nil {
		return err
	}
	ed := a.editor(r, c)
	back := safeRedirect(r.PostFormValue("redirect"))
	if back == "/admin/" {
		back = pt.Base
	}
	switch r.PathValue("action") {
	case "trash":
		if err := ed.Trash(ctx, p); err != nil {
			return a.contentError(err)
		}
		return seeOther(w, r, withQuery(back, "msg", "trashed"))
	case "restore":
		if err := ed.Restore(ctx, p); err != nil {
			return a.contentError(err)
		}
		return seeOther(w, r, withQuery(back, "msg", "restored"))
	case "delete":
		if err := ed.Delete(ctx, p); err != nil {
			return a.contentError(err)
		}
		return seeOther(w, r, withQuery(back, "msg", "deleted"))
	case "duplicate":
		cp, err := ed.Duplicate(ctx, p)
		if err != nil {
			return a.contentError(err)
		}
		return seeOther(w, r, editURL(cp)+"?msg=duplicated")
	case "quick-edit":
		return a.quickEdit(w, r, c, pt, p)
	case "restore-revision":
		id, _ := strconv.ParseInt(r.PostFormValue("revision"), 10, 64)
		rev, err := db.GetRevision(ctx, a.db.R, id)
		if err != nil || rev.PostID != p.ID {
			return errNotFound
		}
		if _, err := ed.RestoreRevision(ctx, p, rev); err != nil {
			return a.contentError(err)
		}
		return seeOther(w, r, editURL(p)+"?msg=restored-revision")
	}
	return errNotFound
}

// quickEdit saves the inline edit form of the list table and answers with
// JSON (the page reloads the row).
func (a *Admin) quickEdit(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType, p *db.Post) error {
	ctx := r.Context()
	in := a.inputFromPost(ctx, p)
	f := r.PostForm
	in.Title = f.Get("title")
	in.Slug = f.Get("slug")
	in.Date = f.Get("date")
	in.Password = f.Get("password")
	in.Status = f.Get("status")
	if in.Status == db.StatusFuture {
		in.Status = db.StatusPublish
	}
	in.CommentStatus = openClosed(f.Get("comments") != "")
	in.PingStatus = openClosed(f.Get("pings") != "")
	in.KeepContent, in.KeepExcerpt = true, true
	if pt.Type == db.TypePost {
		in.Sticky = f.Get("sticky") != ""
		in.Categories = parseIDs(f["categories[]"])
		in.Tags = splitTags(f.Get("tags"))
	} else {
		in.ParentID, _ = strconv.ParseInt(f.Get("parent"), 10, 64)
		in.MenuOrder, _ = strconv.Atoi(f.Get("menu_order"))
		in.Template = f.Get("template")
	}
	if _, err := a.editor(r, c).Save(ctx, p, in); err != nil {
		return a.contentError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func openClosed(open bool) string {
	if open {
		return "open"
	}
	return "closed"
}

func parseIDs(vals []string) []int64 {
	var out []int64
	for _, v := range vals {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// inputFromPost returns the editor input reproducing p (incl. terms).
func (a *Admin) inputFromPost(ctx context.Context, p *db.Post) *content.PostInput {
	in := &content.PostInput{Type: p.Type, Title: p.Title, Content: content.EditorContent(p), Excerpt: p.Excerpt,
		Status: p.Status, Slug: p.Slug, Password: p.Password, CommentStatus: p.CommentStatus, PingStatus: p.PingStatus,
		ParentID: p.ParentID, MenuOrder: p.MenuOrder, Template: p.Template, FeaturedMedia: p.FeaturedMediaID}
	if p.Status == db.StatusPrivate {
		in.Status = db.StatusPrivate
	}
	if !p.DateGMT.IsZero() {
		in.Date = p.Date.Format("2006-01-02T15:04:05")
	}
	if terms, err := db.PostTerms(ctx, a.db.R, []int64{p.ID}); err == nil {
		for _, t := range terms[p.ID] {
			if t.Taxonomy == db.TaxCategory {
				in.Categories = append(in.Categories, t.ID)
			} else if t.Taxonomy == db.TaxTag {
				in.Tags = append(in.Tags, t.Name)
			}
		}
	}
	if s, err := a.site.Settings(ctx); err == nil {
		in.Sticky = slices.Contains(s.Sticky, p.ID)
	}
	return in
}

// ---- Editor screen (post.php) ----

type editorData struct {
	basePage
	PT            postType
	Post          *db.Post
	IsNew         bool
	Action        string
	PostTitle     string
	Content       string
	Excerpt       string
	Slug          string
	SlugBase      string
	SlugSuffix    string
	Permalink     string
	Status        string
	StatusLabel   string
	Visibility    string
	Password      string
	Sticky        bool
	DateValue     string
	DateText      string
	Scheduled     bool
	CommentsOpen  bool
	PingsOpen     bool
	Categories    []option
	CheckedCats   []string
	Tags          []string
	Parents       []option
	Templates     []option
	MenuOrder     int
	FeaturedID    int64
	FeaturedThumb string
	Revisions     []revisionRow
	Autosave      *db.Revision
	AutosaveWhen  string
	CanPublish    bool
	CanDelete     bool
	CanAddCat     bool
	ViewURL       string
	PreviewURL    string
	Error         string
	EditorConfig  map[string]any
}

type revisionRow struct {
	ID     int64
	Author string
	When   string
}

var editorMessages = map[string]notice{
	"saved":             {"success", "Saved."},
	"published":         {"success", "Published."},
	"scheduled":         {"success", "Scheduled."},
	"submitted":         {"success", "Submitted for review."},
	"draft":             {"success", "Draft saved."},
	"duplicated":        {"success", "Copy created as a draft. You are now editing the copy."},
	"restored-revision": {"success", "Revision restored."},
}

// GET /admin/posts/new
func (a *Admin) postNew(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	if !auth.CanEditType(c.user, pt.Type) {
		return errForbidden
	}
	p := &db.Post{Type: pt.Type, Status: db.StatusDraft, CommentStatus: c.settings.DefaultCommentStatus, PingStatus: "closed"}
	if pt.Type == db.TypePage {
		p.CommentStatus = "closed"
	}
	return a.renderEditor(w, r, c, pt, p, true, nil, "")
}

// GET /admin/posts/{id}/edit
func (a *Admin) postEdit(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	p, err := a.loadPost(r, pt)
	if err != nil {
		return err
	}
	if !auth.CanEditPost(c.user, p) {
		return errForbidden
	}
	if p.Status == db.StatusTrash {
		return &httpError{http.StatusConflict, "This item is in the Trash. Restore it before editing."}
	}
	return a.renderEditor(w, r, c, pt, p, false, nil, "")
}

func (a *Admin) renderEditor(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType, p *db.Post, isNew bool, in *content.PostInput, errMsg string) error {
	ctx := r.Context()
	title := "Edit " + pt.Singular
	screen := pt.Screen + "-all"
	if isNew {
		title, screen = "Add "+pt.Singular, pt.Screen+"-new"
	}
	d := &editorData{basePage: a.base(ctx, c, title, screen), PT: pt, Post: p, IsNew: isNew, Error: errMsg}
	d.BodyClass += " post-php gp-editor-screen"
	d.Styles = []string{"editor/editor.css"}
	d.Scripts = []string{"editor/editor.js", "post.js"}
	if msg, ok := editorMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" {
		d.Notice = &msg
	}
	if in == nil {
		if isNew {
			in = &content.PostInput{Type: pt.Type, Status: db.StatusDraft, CommentStatus: p.CommentStatus, PingStatus: p.PingStatus}
		} else {
			in = a.inputFromPost(ctx, p)
		}
	}
	d.Action = pt.Base + "new"
	if !isNew {
		d.Action = pt.Base + itoa(p.ID) + "/edit"
	}
	d.PostTitle, d.Content, d.Excerpt, d.Slug = in.Title, in.Content, in.Excerpt, in.Slug
	d.Status = p.Status
	d.Visibility = "public"
	switch {
	case in.Status == db.StatusPrivate:
		d.Visibility = "private"
	case in.Password != "":
		d.Visibility = "password"
	}
	d.Password, d.Sticky = in.Password, in.Sticky
	d.CommentsOpen, d.PingsOpen = in.CommentStatus == "open", in.PingStatus == "open"
	d.MenuOrder = in.MenuOrder
	d.CanPublish = auth.CanPublishType(c.user, pt.Type)
	d.CanDelete = !isNew && auth.CanDeletePost(c.user, p)
	d.CanAddCat = auth.Can(c.user, auth.CapManageCategories)
	d.StatusLabel = map[string]string{db.StatusPublish: "Published", db.StatusFuture: "Scheduled", db.StatusDraft: "Draft",
		db.StatusPending: "Pending Review", db.StatusPrivate: "Privately Published"}[p.Status]
	if in.Date != "" {
		d.DateValue = in.Date
		if len(d.DateValue) > 16 {
			d.DateValue = d.DateValue[:16]
		}
	}
	d.Scheduled = p.Status == db.StatusFuture
	switch {
	case p.Status == db.StatusFuture:
		d.DateText = "Scheduled for: " + theme.FormatPHPDate("M j, Y \\a\\t H:i", p.Date.Time)
	case p.Status == db.StatusPublish || p.Status == db.StatusPrivate:
		d.DateText = "Published on: " + theme.FormatPHPDate("M j, Y \\a\\t H:i", p.Date.Time)
	case !p.DateGMT.IsZero():
		d.DateText = "Publish on: " + theme.FormatPHPDate("M j, Y \\a\\t H:i", p.Date.Time)
	default:
		d.DateText = "Publish immediately"
	}

	// Permalink line.
	d.SlugBase, d.SlugSuffix = a.slugParts(c, p)
	if !isNew {
		if p.Status == db.StatusPublish || p.Status == db.StatusPrivate {
			d.ViewURL = c.links.PostURL(p)
		}
		d.PreviewURL = previewURL(c, p)
	}

	var err error
	if pt.Type == db.TypePost {
		if d.Categories, err = a.categoryOptions(ctx, 0); err != nil {
			return err
		}
		for _, id := range in.Categories {
			d.CheckedCats = append(d.CheckedCats, itoa(id))
		}
		d.Tags = in.Tags
	} else {
		if d.Parents, err = a.parentOptions(ctx, p.ID, in.ParentID); err != nil {
			return err
		}
		for _, t := range content.PageTemplates {
			d.Templates = append(d.Templates, option{Value: t.Value, Label: t.Label, Selected: t.Value == in.Template})
		}
	}
	d.FeaturedID = in.FeaturedMedia
	if d.FeaturedID > 0 {
		if m, err := db.GetMedia(ctx, a.db.R, d.FeaturedID); err == nil && m != nil {
			d.FeaturedThumb = mediaURL(c, m, "medium")
		}
	}
	if !isNew {
		revs, err := db.Revisions(ctx, a.db.R, p.ID)
		if err != nil {
			return err
		}
		users, _ := db.Users(ctx, a.db.R)
		names := map[int64]string{}
		for _, u := range users {
			names[u.ID] = u.DisplayName
		}
		for _, rv := range revs {
			d.Revisions = append(d.Revisions, revisionRow{ID: rv.ID, Author: names[rv.AuthorID],
				When: theme.FormatPHPDate("M j, Y \\a\\t H:i", rv.CreatedGMT.In(c.settings.Loc))})
		}
		if as, err := db.Autosave(ctx, a.db.R, p.ID, c.user.ID); err == nil && as != nil && as.CreatedGMT.After(p.ModifiedGMT.Time) &&
			(as.Content != content.EditorContent(p) || as.Title != p.Title || as.Excerpt != p.Excerpt) {
			d.Autosave = as
			d.AutosaveWhen = theme.FormatPHPDate("M j, Y \\a\\t H:i", as.CreatedGMT.In(c.settings.Loc))
		}
	}
	d.EditorConfig = map[string]any{
		"postId":     p.ID,
		"postType":   pt.Type,
		"autosave":   "/admin/api/autosave",
		"media":      "/admin/api/media",
		"tags":       "/admin/api/tags",
		"categories": "/admin/api/categories",
		"home":       c.links.Home(),
		"interval":   60,
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "post-edit", d)
}

// slugParts returns the permalink before and after the editable slug.
func (a *Admin) slugParts(c *reqCtx, p *db.Post) (base, suffix string) {
	home := strings.TrimSuffix(c.links.Home(), "/")
	if p.Type == db.TypePage {
		base = home + "/"
		if p.ParentID != 0 {
			if parent, err := db.GetPost(context.Background(), a.db.R, p.ParentID); err == nil {
				pu := c.links.PostURL(parent)
				base = strings.TrimSuffix(pu, "/") + "/"
			}
		}
		return base, "/"
	}
	perma := c.settings.Permalink
	i := strings.Index(perma, "%postname%")
	if i < 0 {
		return home + "/?p=", ""
	}
	probe := *p
	if probe.Date.IsZero() {
		probe.Date = db.Time{Time: a.content.Now().In(c.settings.Loc)}
	}
	probe.Slug = "\x00"
	probe.Status = db.StatusPublish
	full := c.links.PostURL(&probe)
	before, after, _ := strings.Cut(full, "\x00")
	return before, after
}

func mediaURL(c *reqCtx, m *db.Media, size string) string {
	file := m.File
	if s, ok := m.Sizes[size]; ok && s.File != "" {
		dir := ""
		if i := strings.LastIndex(m.File, "/"); i >= 0 {
			dir = m.File[:i+1]
		}
		file = dir + s.File
	}
	return c.links.UploadURL(file)
}

// POST /admin/posts/new, /admin/posts/{id}/edit
func (a *Admin) postSave(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	ctx := r.Context()
	var p *db.Post
	isNew := r.PathValue("id") == ""
	if !isNew {
		var err error
		if p, err = a.loadPost(r, pt); err != nil {
			return err
		}
	}
	f := r.PostForm
	in := &content.PostInput{
		Type: pt.Type, Title: f.Get("post_title"), Content: f.Get("content"), Excerpt: f.Get("excerpt"),
		Slug: f.Get("post_name"), Date: f.Get("post_date"), Password: f.Get("post_password"),
		CommentStatus: openClosed(f.Get("comment_status") != ""), PingStatus: openClosed(f.Get("ping_status") != ""),
		Sticky: f.Get("sticky") != "", FeaturedMedia: atoi64(f.Get("featured_media")),
	}
	if pt.Type == db.TypePost {
		in.Categories = parseIDs(f["post_category[]"])
		in.Tags = splitTags(f.Get("tags"))
	} else {
		in.ParentID = atoi64(f.Get("parent_id"))
		in.MenuOrder, _ = strconv.Atoi(f.Get("menu_order"))
		in.Template = f.Get("page_template")
	}
	// The pressed button decides the status (like WordPress).
	switch {
	case f.Has("publish"):
		in.Status = db.StatusPublish
	case f.Has("save_pending"):
		in.Status = db.StatusPending
	case f.Has("save_draft"):
		in.Status = db.StatusDraft
	default: // "Update" keeps the status
		in.Status = db.StatusDraft
		if p != nil {
			in.Status = p.Status
		}
		if in.Status == db.StatusFuture {
			in.Status = db.StatusPublish
		}
	}
	switch f.Get("visibility") {
	case "private":
		if in.Status == db.StatusPublish {
			in.Status = db.StatusPrivate
		}
		in.Password = ""
	case "password":
	default:
		in.Password = ""
		if in.Status == db.StatusPrivate {
			in.Status = db.StatusPublish
		}
	}
	saved, err := a.editor(r, c).Save(ctx, p, in)
	if err != nil {
		var ve *content.ValidationError
		if errors.As(err, &ve) {
			if p == nil {
				p = &db.Post{Type: pt.Type, Status: db.StatusDraft}
			}
			return a.renderEditor(w, r, c, pt, p, isNew, in, ve.Msg)
		}
		return a.contentError(err)
	}
	msg := "saved"
	switch saved.Status {
	case db.StatusFuture:
		msg = "scheduled"
	case db.StatusPending:
		msg = "submitted"
	case db.StatusDraft:
		msg = "draft"
	case db.StatusPublish:
		if p == nil || p.Status != db.StatusPublish {
			msg = "published"
		}
	}
	return seeOther(w, r, editURL(saved)+"?msg="+msg)
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// ---- Revisions ----

type revisionsData struct {
	basePage
	PT       postType
	Post     *db.Post
	Rows     []revisionRow
	Selected *db.Revision
	Prev     *db.Revision
	Diff     []diffRow
	When     string
	Current  bool
}

// GET /admin/posts/{id}/revisions?revision=…
func (a *Admin) revisionsPage(w http.ResponseWriter, r *http.Request, c *reqCtx, pt postType) error {
	ctx := r.Context()
	p, err := a.loadPost(r, pt)
	if err != nil {
		return err
	}
	if !auth.CanEditPost(c.user, p) {
		return errForbidden
	}
	revs, err := db.Revisions(ctx, a.db.R, p.ID)
	if err != nil {
		return err
	}
	if len(revs) == 0 {
		return seeOther(w, r, editURL(p))
	}
	d := &revisionsData{basePage: a.base(ctx, c, "Compare Revisions of “"+p.Title+"”", pt.Screen+"-all"), PT: pt, Post: p}
	d.BodyClass += " revision-php"
	users, _ := db.Users(ctx, a.db.R)
	names := map[int64]string{}
	for _, u := range users {
		names[u.ID] = u.DisplayName
	}
	sel := 0
	want := atoi64(r.URL.Query().Get("revision"))
	for i, rv := range revs {
		d.Rows = append(d.Rows, revisionRow{ID: rv.ID, Author: names[rv.AuthorID],
			When: theme.FormatPHPDate("M j, Y \\a\\t H:i", rv.CreatedGMT.In(c.settings.Loc))})
		if rv.ID == want {
			sel = i
		}
	}
	d.Selected = &revs[sel]
	d.Current = sel == 0
	d.When = d.Rows[sel].When
	var prevTitle, prevContent string
	if sel+1 < len(revs) {
		d.Prev = &revs[sel+1]
		prevTitle, prevContent = d.Prev.Title, d.Prev.Content
	}
	d.Diff = append(diffLines("Title", prevTitle, d.Selected.Title), diffLines("Content", prevContent, d.Selected.Content)...)
	return a.render(w, "revisions", d)
}

// ---- JSON APIs for the editor ----

// POST /admin/api/autosave (JSON: post_id, title, content, excerpt)
func (a *Admin) apiAutosave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	id := atoi64(r.PostFormValue("post_id"))
	typ := r.PostFormValue("post_type")
	ed := a.editor(r, c)
	var p *db.Post
	if id == 0 {
		// First autosave of a new post creates the draft.
		if typ != db.TypePost && typ != db.TypePage {
			return badRequest("Unknown post type.")
		}
		in := &content.PostInput{Type: typ, Title: r.PostFormValue("title"), Content: r.PostFormValue("content"),
			Excerpt: r.PostFormValue("excerpt"), Status: db.StatusDraft}
		saved, err := ed.Save(ctx, nil, in)
		if err != nil {
			return a.contentError(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"post_id": saved.ID, "edit_url": editURL(saved),
			"preview_url": previewURL(c, saved), "saved": "draft"})
		return nil
	}
	p, err := db.GetPost(ctx, a.db.R, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	// Drafts by their author are saved directly (like WordPress); for
	// everything else the autosave is kept aside.
	if (p.Status == db.StatusDraft || p.Status == db.StatusPending) && p.AuthorID == c.user.ID {
		in := a.inputFromPost(ctx, p)
		in.Title, in.Content, in.Excerpt = r.PostFormValue("title"), r.PostFormValue("content"), r.PostFormValue("excerpt")
		in.KeepTerms = true
		if _, err := ed.Save(ctx, p, in); err != nil {
			return a.contentError(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"post_id": p.ID, "saved": "draft"})
		return nil
	}
	if _, err := ed.Autosave(ctx, p, r.PostFormValue("title"), r.PostFormValue("content"), r.PostFormValue("excerpt")); err != nil {
		return a.contentError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"post_id": p.ID, "saved": "autosave"})
	return nil
}

type mediaItem struct {
	ID      int64             `json:"id"`
	Title   string            `json:"title"`
	Alt     string            `json:"alt"`
	Caption string            `json:"caption"`
	Mime    string            `json:"mime"`
	Width   int               `json:"width"`
	Height  int               `json:"height"`
	URL     string            `json:"url"`
	Thumb   string            `json:"thumb"`
	Page    string            `json:"page"`
	Sizes   map[string]sizeJS `json:"sizes"`
}

type sizeJS struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// GET /admin/api/media?s=&page=&images=1
func (a *Admin) apiMedia(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	q := r.URL.Query()
	page := max(1, int(atoi64(q.Get("page"))))
	const n = 40
	posts, media, total, err := db.AdminMedia(r.Context(), a.db.R, db.MediaQuery{Search: q.Get("s"),
		ImagesOnly: q.Get("images") == "1", Limit: n, Offset: (page - 1) * n})
	if err != nil {
		return err
	}
	items := make([]mediaItem, 0, len(posts))
	for i := range posts {
		p := &posts[i]
		m := media[p.ID]
		if m == nil {
			continue
		}
		it := mediaItem{ID: p.ID, Title: p.Title, Alt: m.Alt, Caption: p.Excerpt, Mime: m.Mime, Width: m.Width,
			Height: m.Height, URL: c.links.UploadURL(m.File), Thumb: mediaURL(c, m, "thumbnail"), Page: c.links.PostURL(p),
			Sizes: map[string]sizeJS{"full": {c.links.UploadURL(m.File), m.Width, m.Height}}}
		for name, s := range m.Sizes {
			it.Sizes[name] = sizeJS{mediaURL(c, m, name), s.Width, s.Height}
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "pages": (total + n - 1) / n})
	return nil
}

// GET /admin/api/tags?q=: tag suggestions.
func (a *Admin) apiTags(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	tags, err := db.Terms(r.Context(), a.db.R, db.TaxTag)
	if err != nil {
		return err
	}
	out := []string{}
	for _, t := range tags {
		if q == "" || strings.Contains(strings.ToLower(t.Name), q) {
			out = append(out, t.Name)
		}
		if len(out) >= 20 {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /admin/api/categories (name, parent): "+ Add New Category".
func (a *Admin) apiAddCategory(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	name := strings.TrimSpace(r.PostFormValue("name"))
	parent := atoi64(r.PostFormValue("parent"))
	t, err := a.content.AddCategory(ctx, c.user, r, name, parent)
	if err != nil {
		return a.contentError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": t.ID, "name": t.Name, "parent": t.ParentID})
	return nil
}
