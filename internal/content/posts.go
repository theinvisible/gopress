package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/media"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/site"
)

// Site is what the content service needs from the public site.
type Site interface {
	Settings(ctx context.Context) (*site.Settings, error)
	Invalidate()
}

// Service performs content changes.
type Service struct {
	db      *db.DB
	site    Site
	auth    *auth.Service
	log     *slog.Logger
	now     func() time.Time
	mail    Mailer
	media   *media.Store
	uploads uploadLimiter
}

// New creates the service.
func New(d *db.DB, s Site, a *auth.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: d, site: s, auth: a, log: log, now: time.Now}
}

// Editor performs changes on behalf of a user; every method checks the
// user's capabilities.
type Editor struct {
	s    *Service
	user *db.User
	r    *http.Request // for the audit log (client address)
}

// As returns an editor acting as user.
func (s *Service) As(user *db.User, r *http.Request) *Editor {
	return &Editor{s: s, user: user, r: r}
}

// Errors reported to the user.
var (
	ErrForbidden = errors.New("Sorry, you are not allowed to do that.")
	ErrInvalid   = errors.New("invalid input")
	ErrNotFound  = errors.New("not found")
)

// ValidationError is a user-facing input problem.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// PageTemplates are the page templates of the theme (value -> label).
var PageTemplates = []struct{ Value, Label string }{
	{"", "Default template"},
	{"page-templates/full-width.php", "Full-width Page Template, No Sidebar"},
	{"page-templates/front-page.php", "Front Page Template"},
}

// PostInput is the editable state of a post or page as submitted by the
// editor screen.
type PostInput struct {
	Type          string
	Title         string
	Content       string
	Excerpt       string
	Status        string // draft, pending, publish (incl. scheduled), private
	Slug          string // empty: derived from the title when published
	Date          string // local "YYYY-MM-DDTHH:MM" (datetime-local); empty: keep / now
	Password      string
	Sticky        bool
	CommentStatus string // open, closed
	PingStatus    string
	ParentID      int64
	MenuOrder     int
	Template      string
	Categories    []int64
	Tags          []string
	FeaturedMedia int64
	// Keep... flags leave fields untouched when a form does not carry them
	// (quick edit, quick draft).
	KeepContent, KeepTerms, KeepExcerpt bool
}

// Save creates (p == nil) or updates p from in and returns the stored post.
func (e *Editor) Save(ctx context.Context, p *db.Post, in *PostInput) (*db.Post, error) {
	settings, err := e.s.site.Settings(ctx)
	if err != nil {
		return nil, err
	}
	isNew := p == nil
	var old db.Post
	if isNew {
		if in.Type != db.TypePost && in.Type != db.TypePage {
			return nil, ErrInvalid
		}
		if !auth.CanEditType(e.user, in.Type) {
			return nil, ErrForbidden
		}
		p = &db.Post{Type: in.Type, AuthorID: e.user.ID, CommentStatus: settings.DefaultCommentStatus,
			PingStatus: "closed", ContentFormat: db.FormatHTML}
		if p.CommentStatus == "" {
			p.CommentStatus = "open"
		}
		if in.Type == db.TypePage {
			p.CommentStatus = "closed" // WordPress default for pages
		}
	} else {
		if !auth.CanEditPost(e.user, p) {
			return nil, ErrForbidden
		}
		if p.Status == db.StatusTrash {
			return nil, &ValidationError{"This item is in the Trash. Restore it before editing."}
		}
		old = *p
		cp := *p
		p = &cp
	}
	now := e.s.now()
	loc := settings.Loc
	if loc == nil {
		loc = time.UTC
	}

	// Content.
	p.Title = strings.TrimSpace(in.Title)
	if !in.KeepContent {
		p.Content = in.Content
		p.ContentFormat = db.FormatHTML
	}
	if !in.KeepExcerpt {
		p.Excerpt = strings.TrimSpace(in.Excerpt)
	}
	if !auth.Can(e.user, auth.CapUnfilteredHTML) {
		p.Title = render.SanitizeText(p.Title)
		p.Content = render.SanitizePost(p.Content)
		p.Excerpt = render.SanitizeText(p.Excerpt)
	}

	// Status and visibility.
	status := in.Status
	switch status {
	case db.StatusDraft, db.StatusPending, db.StatusPublish, db.StatusPrivate:
	case db.StatusFuture:
		status = db.StatusPublish
	default:
		return nil, &ValidationError{"Unknown status."}
	}
	if (status == db.StatusPublish || status == db.StatusPrivate) && !auth.CanPublishType(e.user, p.Type) {
		// Contributors submit for review instead (like WordPress).
		if !isNew && (old.Status == db.StatusPublish || old.Status == db.StatusPrivate || old.Status == db.StatusFuture) {
			return nil, ErrForbidden
		}
		status = db.StatusPending
	}
	p.Password = ""
	if status != db.StatusPrivate {
		p.Password = strings.TrimSpace(in.Password)
		if len(p.Password) > 255 {
			return nil, &ValidationError{"The password is too long."}
		}
	}

	// Dates: an explicit date wins; publishing without one uses now;
	// drafts keep a floating date (GMT zero) like WordPress.
	if in.Date != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", in.Date, loc)
		if err != nil {
			if t, err = time.ParseInLocation("2006-01-02T15:04:05", in.Date, loc); err != nil {
				return nil, &ValidationError{"Invalid date."}
			}
		}
		p.Date = db.Time{Time: wall(t)}
		p.DateGMT = db.Time{Time: t.UTC()}
	} else if p.Date.IsZero() || (status == db.StatusPublish && p.DateGMT.IsZero()) {
		p.Date = db.Time{Time: wall(now.In(loc))}
		if status == db.StatusDraft || status == db.StatusPending {
			p.DateGMT = db.Time{}
		} else {
			p.DateGMT = db.Time{Time: now.UTC()}
		}
	}
	if status == db.StatusPublish && !p.DateGMT.IsZero() && p.DateGMT.After(now) {
		status = db.StatusFuture
	}
	if (status == db.StatusPublish || status == db.StatusPrivate) && p.DateGMT.IsZero() {
		p.DateGMT = db.Time{Time: now.UTC()}
	}
	p.Status = status
	p.Modified = db.Time{Time: wall(now.In(loc))}
	p.ModifiedGMT = db.Time{Time: now.UTC()}

	// Discussion, page attributes.
	if in.CommentStatus == "open" || in.CommentStatus == "closed" {
		p.CommentStatus = in.CommentStatus
	}
	if in.PingStatus == "open" || in.PingStatus == "closed" {
		p.PingStatus = in.PingStatus
	}
	if p.Type == db.TypePage {
		p.MenuOrder = in.MenuOrder
		if !slices.ContainsFunc(PageTemplates, func(t struct{ Value, Label string }) bool { return t.Value == in.Template }) {
			return nil, &ValidationError{"Unknown page template."}
		}
		p.Template = in.Template
		if err := e.checkParent(ctx, p, in.ParentID); err != nil {
			return nil, err
		}
		p.ParentID = in.ParentID
	}
	if in.FeaturedMedia > 0 {
		if m, err := db.GetPost(ctx, e.s.db.R, in.FeaturedMedia); err != nil || m.Type != db.TypeAttachment {
			return nil, &ValidationError{"The featured image does not exist."}
		}
	}
	p.FeaturedMediaID = max(in.FeaturedMedia, 0)

	// Slug: drafts may stay without one; everything else gets a unique one.
	slug := SanitizeTitle(in.Slug)
	if slug == "" && p.Slug != "" && in.Slug == "" && !isNew {
		slug = p.Slug
	}
	if slug == "" && status != db.StatusDraft && status != db.StatusPending {
		slug = SanitizeTitle(p.Title)
		if slug == "" {
			slug = strconv.FormatInt(p.ID, 10) // fixed below for new posts
		}
	}

	err = e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		if isNew {
			p.Slug = ""
			if err := db.InsertPost(ctx, tx, p); err != nil {
				return err
			}
			p.GUID = strings.TrimRight(settings.Home, "/") + "/?p=" + strconv.FormatInt(p.ID, 10)
			if slug == "0" {
				slug = strconv.FormatInt(p.ID, 10)
			}
		}
		var err error
		if p.Slug, err = UniqueSlug(ctx, tx, slug, p); err != nil {
			return err
		}
		if !isNew && old.Status == db.StatusPublish && old.Slug != "" && old.Slug != p.Slug {
			if err := db.AddSlugRedirect(ctx, tx, old.Slug, p.ID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM slug_redirects WHERE post_id = ? AND old_slug = ?`, p.ID, p.Slug); err != nil {
			return err
		}
		if err := db.UpdatePost(ctx, tx, p); err != nil {
			return err
		}
		if p.Type == db.TypePost && !in.KeepTerms {
			if err := e.setTerms(ctx, tx, p, in, settings); err != nil {
				return err
			}
		}
		// Making posts sticky changes the front page for everyone: editors
		// and administrators only (WordPress: edit_others_posts).
		if p.Type == db.TypePost && auth.Can(e.user, auth.CapEditOthersPosts) {
			if err := setSticky(ctx, tx, settings, p.ID, in.Sticky && p.Status != db.StatusPrivate); err != nil {
				return err
			}
		}
		if p.FeaturedMediaID > 0 {
			err = db.SetPostMeta(ctx, tx, p.ID, "_thumbnail_id", strconv.FormatInt(p.FeaturedMediaID, 10))
		} else {
			err = db.DeletePostMeta(ctx, tx, p.ID, "_thumbnail_id")
		}
		if err != nil {
			return err
		}
		if err := e.saveRevision(ctx, tx, isNew, &old, p); err != nil {
			return err
		}
		if err := db.DeleteAutosave(ctx, tx, p.ID, e.user.ID); err != nil {
			return err
		}
		return db.RecountTerms(ctx, tx)
	})
	if err != nil {
		return nil, err
	}
	action := "post_updated"
	if isNew {
		action = "post_created"
	}
	e.s.audit(ctx, e.r, e.user.ID, action, p.Type, p.ID, map[string]any{"status": p.Status})
	e.s.site.Invalidate()
	return p, nil
}

// Now returns the service's current time.
func (s *Service) Now() time.Time { return s.now() }

// AddCategory creates a category (or returns the existing one with that
// name below parent).
func (s *Service) AddCategory(ctx context.Context, u *db.User, r *http.Request, name string, parent int64) (*db.Term, error) {
	if !auth.Can(u, auth.CapManageCategories) {
		return nil, ErrForbidden
	}
	name = strings.TrimSpace(render.SanitizeText(name))
	if name == "" || len(name) > 200 {
		return nil, &ValidationError{"Please enter a category name (up to 200 characters)."}
	}
	if parent != 0 {
		if t, err := db.GetTerm(ctx, s.db.R, parent); err != nil || t == nil || t.Taxonomy != db.TaxCategory {
			return nil, &ValidationError{"The parent category does not exist."}
		}
	}
	cats, err := db.Terms(ctx, s.db.R, db.TaxCategory)
	if err != nil {
		return nil, err
	}
	for _, t := range cats {
		if strings.EqualFold(t.Name, name) && t.ParentID == parent {
			return &t, nil
		}
	}
	base := SanitizeTitle(name)
	if base == "" {
		return nil, &ValidationError{"Invalid category name."}
	}
	slug := base
	for n := 2; ; n++ {
		t, err := db.TermBySlug(ctx, s.db.R, db.TaxCategory, slug)
		if err != nil {
			return nil, err
		}
		if t == nil {
			break
		}
		slug = base + "-" + strconv.Itoa(n)
	}
	t := &db.Term{Taxonomy: db.TaxCategory, Slug: slug, Name: name, ParentID: parent}
	if err := db.InsertTerm(ctx, s.db.W, t); err != nil {
		return nil, err
	}
	s.audit(ctx, r, u.ID, "category_created", "term", t.ID, map[string]any{"name": name})
	s.site.Invalidate()
	return t, nil
}

// wall returns the wall clock of t as a UTC time (the storage convention
// of *_local columns).
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func (e *Editor) checkParent(ctx context.Context, p *db.Post, parentID int64) error {
	if parentID == 0 {
		return nil
	}
	if parentID == p.ID {
		return &ValidationError{"A page cannot be its own parent."}
	}
	for id, depth := parentID, 0; id != 0; depth++ {
		if depth > 100 {
			return &ValidationError{"The page hierarchy is too deep."}
		}
		parent, err := db.GetPost(ctx, e.s.db.R, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && parent.Type != db.TypePage) {
			return &ValidationError{"The parent page does not exist."}
		}
		if err != nil {
			return err
		}
		if parent.ParentID == p.ID && p.ID != 0 {
			return &ValidationError{"A page cannot be moved below one of its own child pages."}
		}
		id = parent.ParentID
	}
	return nil
}

// setTerms assigns categories (existing ones only; the default category
// if none) and tags (created on demand).
func (e *Editor) setTerms(ctx context.Context, tx *sql.Tx, p *db.Post, in *PostInput, settings *site.Settings) error {
	var cats []int64
	for _, id := range in.Categories {
		t, err := db.GetTerm(ctx, tx, id)
		if err != nil || t == nil || t.Taxonomy != db.TaxCategory {
			return &ValidationError{"Unknown category."}
		}
		if !slices.Contains(cats, id) {
			cats = append(cats, id)
		}
	}
	if len(cats) == 0 {
		var def string
		if ok, _ := db.GetOption(ctx, tx, "default_category", &def); ok {
			if id, err := strconv.ParseInt(def, 10, 64); err == nil {
				if t, err := db.GetTerm(ctx, tx, id); err == nil && t != nil && t.Taxonomy == db.TaxCategory {
					cats = []int64{id}
				}
			}
		}
	}
	if err := db.SetPostTerms(ctx, tx, p.ID, db.TaxCategory, cats); err != nil {
		return err
	}
	var tags []int64
	for _, name := range in.Tags {
		name = strings.TrimSpace(render.SanitizeText(name))
		if name == "" {
			continue
		}
		if len(name) > 200 {
			return &ValidationError{"A tag name is too long."}
		}
		id, err := ensureTag(ctx, tx, name)
		if err != nil {
			return err
		}
		if !slices.Contains(tags, id) {
			tags = append(tags, id)
		}
	}
	return db.SetPostTerms(ctx, tx, p.ID, db.TaxTag, tags)
}

// ensureTag returns the id of the tag with name (or its slug), creating it
// if needed.
func ensureTag(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM terms WHERE taxonomy = 'post_tag' AND name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	slug := SanitizeTitle(name)
	if slug == "" {
		return 0, &ValidationError{fmt.Sprintf("Invalid tag %q.", name)}
	}
	if t, err := db.TermBySlug(ctx, tx, db.TaxTag, slug); err == nil && t != nil {
		return t.ID, nil
	}
	t := &db.Term{Taxonomy: db.TaxTag, Slug: slug, Name: name}
	if err := db.InsertTerm(ctx, tx, t); err != nil {
		return 0, err
	}
	return t.ID, nil
}

func setSticky(ctx context.Context, tx *sql.Tx, settings *site.Settings, id int64, sticky bool) error {
	ids := slices.Clone(settings.Sticky)
	has := slices.Contains(ids, id)
	switch {
	case sticky && !has:
		ids = append(ids, id)
	case !sticky && has:
		ids = slices.DeleteFunc(ids, func(x int64) bool { return x == id })
	default:
		return nil
	}
	if ids == nil {
		ids = []int64{}
	}
	return db.SetOption(ctx, tx, "sticky_posts", ids)
}

// revisionsKept is the number of revisions stored per post.
const revisionsKept = 25

// saveRevision stores the new state as a revision when title, content or
// excerpt changed. For posts without revisions (imported ones) the
// previous state is stored first, so the first edit can be undone.
func (e *Editor) saveRevision(ctx context.Context, tx *sql.Tx, isNew bool, old, p *db.Post) error {
	if !isNew && old.Title == p.Title && old.Content == p.Content && old.Excerpt == p.Excerpt {
		return nil
	}
	now := db.Time{Time: e.s.now().UTC()}
	if !isNew {
		existing, err := db.Revisions(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			prev := &db.Revision{PostID: p.ID, AuthorID: old.AuthorID, Kind: "revision", Title: old.Title,
				Content: old.Content, Excerpt: old.Excerpt, ContentFormat: old.ContentFormat,
				CreatedGMT: db.Time{Time: old.ModifiedGMT.Time}}
			if prev.CreatedGMT.IsZero() || !prev.CreatedGMT.Before(now.Time) {
				prev.CreatedGMT = db.Time{Time: now.Add(-time.Second)}
			}
			if err := db.InsertRevision(ctx, tx, prev); err != nil {
				return err
			}
		}
	}
	rev := &db.Revision{PostID: p.ID, AuthorID: e.user.ID, Kind: "revision", Title: p.Title, Content: p.Content,
		Excerpt: p.Excerpt, ContentFormat: p.ContentFormat, CreatedGMT: now}
	if err := db.InsertRevision(ctx, tx, rev); err != nil {
		return err
	}
	return db.PruneRevisions(ctx, tx, p.ID, revisionsKept)
}

// Autosave stores the user's unsaved editor state of p. Published posts
// are never changed by an autosave; it is offered for restore instead.
func (e *Editor) Autosave(ctx context.Context, p *db.Post, title, content, excerpt string) (*db.Revision, error) {
	if !auth.CanEditPost(e.user, p) {
		return nil, ErrForbidden
	}
	if !auth.Can(e.user, auth.CapUnfilteredHTML) {
		title, content, excerpt = render.SanitizeText(title), render.SanitizePost(content), render.SanitizeText(excerpt)
	}
	rev := &db.Revision{PostID: p.ID, AuthorID: e.user.ID, Title: title, Content: content, Excerpt: excerpt,
		ContentFormat: db.FormatHTML, CreatedGMT: db.Time{Time: e.s.now().UTC()}}
	if err := db.SetAutosave(ctx, e.s.db.W, rev); err != nil {
		return nil, err
	}
	return rev, nil
}

// RestoreRevision makes a revision the current content of p.
func (e *Editor) RestoreRevision(ctx context.Context, p *db.Post, rev *db.Revision) (*db.Post, error) {
	if rev.PostID != p.ID {
		return nil, ErrInvalid
	}
	if !auth.CanEditPost(e.user, p) {
		return nil, ErrForbidden
	}
	in := e.InputFrom(ctx, p)
	in.Title, in.Content, in.Excerpt = rev.Title, rev.Content, rev.Excerpt
	if rev.ContentFormat == db.FormatWPAutop {
		in.Content = ClassicToHTML(rev.Content)
	}
	in.KeepTerms = true
	return e.Save(ctx, p, in)
}

// InputFrom returns the PostInput that reproduces p (without terms).
func (e *Editor) InputFrom(ctx context.Context, p *db.Post) *PostInput {
	in := &PostInput{Type: p.Type, Title: p.Title, Content: p.Content, Excerpt: p.Excerpt, Status: p.Status,
		Slug: p.Slug, Password: p.Password, CommentStatus: p.CommentStatus, PingStatus: p.PingStatus,
		ParentID: p.ParentID, MenuOrder: p.MenuOrder, Template: p.Template, FeaturedMedia: p.FeaturedMediaID}
	if p.Status == db.StatusFuture {
		in.Status = db.StatusPublish
	}
	if !p.DateGMT.IsZero() {
		in.Date = p.Date.Format("2006-01-02T15:04:05")
	}
	if settings, err := e.s.site.Settings(ctx); err == nil {
		in.Sticky = slices.Contains(settings.Sticky, p.ID)
	}
	return in
}

// ClassicToHTML converts classic WordPress content (wpautop at render
// time) into the HTML the editor stores.
func ClassicToHTML(content string) string {
	return classicPipeline.ClassicToHTML(content)
}

// classicPipeline knows the shortcodes whose paragraphs wpautop removes.
var classicPipeline = render.NewPipeline()

// EditorContent returns p's content as HTML for the editor.
func EditorContent(p *db.Post) string {
	if p.ContentFormat == db.FormatWPAutop {
		return ClassicToHTML(p.Content)
	}
	return p.Content
}

// Trash moves p to the trash, remembering its status.
func (e *Editor) Trash(ctx context.Context, p *db.Post) error {
	if !auth.CanDeletePost(e.user, p) {
		return ErrForbidden
	}
	if p.Status == db.StatusTrash {
		return nil
	}
	err := e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.SetPostMeta(ctx, tx, p.ID, "_wp_trash_meta_status", p.Status); err != nil {
			return err
		}
		if err := db.SetPostMeta(ctx, tx, p.ID, "_wp_trash_meta_time", strconv.FormatInt(e.s.now().Unix(), 10)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE posts SET status = 'trash' WHERE id = ?`, p.ID); err != nil {
			return err
		}
		return db.RecountTerms(ctx, tx)
	})
	if err != nil {
		return err
	}
	e.s.audit(ctx, e.r, e.user.ID, "post_trashed", p.Type, p.ID, nil)
	e.s.site.Invalidate()
	return nil
}

// Restore takes p out of the trash as a draft (WordPress 5.6+ behavior).
func (e *Editor) Restore(ctx context.Context, p *db.Post) error {
	if p.Status != db.StatusTrash {
		return nil
	}
	probe := *p
	probe.Status = db.StatusDraft
	if !auth.CanDeletePost(e.user, &probe) {
		return ErrForbidden
	}
	err := e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, k := range []string{"_wp_trash_meta_status", "_wp_trash_meta_time"} {
			if err := db.DeletePostMeta(ctx, tx, p.ID, k); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE posts SET status = 'draft' WHERE id = ?`, p.ID)
		return err
	})
	if err != nil {
		return err
	}
	e.s.audit(ctx, e.r, e.user.ID, "post_restored", p.Type, p.ID, nil)
	e.s.site.Invalidate()
	return nil
}

// Delete removes a trashed post permanently.
func (e *Editor) Delete(ctx context.Context, p *db.Post) error {
	if p.Status != db.StatusTrash {
		return &ValidationError{"Only items in the trash can be deleted permanently."}
	}
	probe := *p
	probe.Status = db.StatusDraft
	if !auth.CanDeletePost(e.user, &probe) {
		return ErrForbidden
	}
	err := e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.DeleteMenuItemsFor(ctx, tx, []string{p.Type}, p.ID); err != nil {
			return err
		}
		if err := db.DeletePost(ctx, tx, p.ID); err != nil {
			return err
		}
		return db.RecountTerms(ctx, tx)
	})
	if err != nil {
		return err
	}
	e.s.audit(ctx, e.r, e.user.ID, "post_deleted", p.Type, p.ID, map[string]any{"title": p.Title})
	e.s.site.Invalidate()
	return nil
}

// Duplicate copies p as a new draft of the current user, like the
// Duplicate Post plugin: same title, content, excerpt, terms, template and
// meta; no date, slug or comments.
func (e *Editor) Duplicate(ctx context.Context, p *db.Post) (*db.Post, error) {
	// Only what the user may edit: a copy would otherwise reveal protected
	// content, passwords and meta of other users' posts.
	if !auth.CanEditType(e.user, p.Type) || !auth.CanEditPost(e.user, p) {
		return nil, ErrForbidden
	}
	in := e.InputFrom(ctx, p)
	in.Status, in.Slug, in.Date, in.Sticky = db.StatusDraft, "", "", false
	in.Content = EditorContent(p)
	terms, err := db.PostTerms(ctx, e.s.db.R, []int64{p.ID})
	if err != nil {
		return nil, err
	}
	for _, t := range terms[p.ID] {
		if t.Taxonomy == db.TaxCategory {
			in.Categories = append(in.Categories, t.ID)
		} else if t.Taxonomy == db.TaxTag {
			in.Tags = append(in.Tags, t.Name)
		}
	}
	cp, err := e.Save(ctx, nil, in)
	if err != nil {
		return nil, err
	}
	// Copy custom meta (not WordPress internals, which are handled above).
	meta, err := db.AllPostMeta(ctx, e.s.db.R, p.ID)
	if err != nil {
		return nil, err
	}
	err = e.s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, kv := range meta {
			if strings.HasPrefix(kv[0], "_wp_") || kv[0] == "_thumbnail_id" || kv[0] == "_edit_lock" || kv[0] == "_edit_last" {
				continue
			}
			if err := db.AddPostMeta(ctx, tx, cp.ID, kv[0], kv[1]); err != nil {
				return err
			}
		}
		return db.AddPostMeta(ctx, tx, cp.ID, "_dp_original", strconv.FormatInt(p.ID, 10))
	})
	return cp, err
}

// PublishDue publishes scheduled posts whose date has come.
func (s *Service) PublishDue(ctx context.Context) error {
	ids, err := db.DueFuturePosts(ctx, s.db.R, db.Time{Time: s.now().UTC()})
	if err != nil || len(ids) == 0 {
		return err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE posts SET status = 'publish' WHERE id = ? AND status = 'future'`, id); err != nil {
				return err
			}
		}
		return db.RecountTerms(ctx, tx)
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		s.audit(ctx, nil, 0, "post_published_scheduled", "post", id, nil)
	}
	s.site.Invalidate()
	return nil
}

// RunScheduler publishes due posts every minute until ctx is done.
func (s *Service) RunScheduler(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.PublishDue(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("publish scheduled posts", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) audit(ctx context.Context, r *http.Request, userID int64, action, objType string, objID int64, detail map[string]any) {
	if s.auth != nil {
		s.auth.Audit(ctx, r, userID, action, objType, objID, detail)
	}
}

// CommentHTML renders a comment's text for the admin: paragraphs and the
// comment allowlist, nothing else.
func CommentHTML(text string) template.HTML {
	return template.HTML(render.SanitizeComment(render.Autop(text)))
}
