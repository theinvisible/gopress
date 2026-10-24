package content

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/site"
)

type testSite struct {
	d           *db.DB
	invalidated int
}

func (s *testSite) Settings(ctx context.Context) (*site.Settings, error) {
	return site.LoadSettings(ctx, s.d.R)
}
func (s *testSite) Invalidate() { s.invalidated++ }

type env struct {
	ctx   context.Context
	d     *db.DB
	site  *testSite
	svc   *Service
	now   time.Time
	admin *db.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for k, v := range map[string]any{"home": "https://example.org", "timezone_string": "Europe/Vienna",
		"default_category": "1", "sticky_posts": []int64{}, "default_comment_status": "open"} {
		if err := db.SetOption(ctx, d.W, k, v); err != nil {
			t.Fatal(err)
		}
	}
	db.InsertTerm(ctx, d.W, &db.Term{ID: 1, Taxonomy: db.TaxCategory, Slug: "uncategorized", Name: "Uncategorized"})
	db.InsertTerm(ctx, d.W, &db.Term{ID: 2, Taxonomy: db.TaxCategory, Slug: "news", Name: "News"})
	e := &env{ctx: ctx, d: d, site: &testSite{d: d}, now: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
	e.svc = New(d, e.site, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.svc.now = func() time.Time { return e.now }
	e.admin = e.user(t, "admin", auth.RoleAdministrator)
	return e
}

func (e *env) user(t *testing.T, login, role string) *db.User {
	u := &db.User{Login: login, Nicename: login, Email: login + "@example.org", DisplayName: login, Role: role,
		RegisteredGMT: db.Time{Time: e.now}}
	if err := db.InsertUser(e.ctx, e.d.W, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) as(u *db.User) *Editor { return e.svc.As(u, nil) }

func TestSanitizeTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Hello World!":             "hello-world",
		"Über Größe & Maße":        "uber-grosse-masse",
		"  Déjà vu — again…  ":     "deja-vu-again",
		"C++ / Go: 10 tips (2026)": "c-go-10-tips-2026",
		"It’s “quoted”":            "its-quoted",
		"<b>bold</b> title":        "bold-title",
		"dots.in.title":            "dots-in-title",
		"Привет":                   "%d0%bf%d1%80%d0%b8%d0%b2%d0%b5%d1%82",
		"100% sure":                "100-sure",
		"a%20b":                    "a%20b",
		"æøå":                      "aeoa",
	} {
		if got := SanitizeTitle(in); got != want {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveDraftThenPublish(t *testing.T) {
	e := newEnv(t)
	ed := e.as(e.admin)
	p, err := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "Hello World", Content: "<p>x</p>", Status: db.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "" || !p.DateGMT.IsZero() || p.Status != db.StatusDraft || p.GUID != "https://example.org/?p=1" {
		t.Errorf("draft = %+v", p)
	}
	terms, _ := db.PostTerms(e.ctx, e.d.R, []int64{p.ID})
	if len(terms[p.ID]) != 1 || terms[p.ID][0].ID != 1 {
		t.Errorf("default category not assigned: %v", terms[p.ID])
	}
	in := &PostInput{Type: db.TypePost, Title: "Hello World", Content: "<p>y</p>", Status: db.StatusPublish,
		Categories: []int64{2}, Tags: []string{"Go", "go", "Web Dev"}}
	p, err = ed.Save(e.ctx, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "hello-world" || p.Status != db.StatusPublish || !p.DateGMT.Equal(e.now) ||
		p.Date.Format("15:04") != "12:00" {
		t.Errorf("published = %+v", p)
	}
	terms, _ = db.PostTerms(e.ctx, e.d.R, []int64{p.ID})
	if len(terms[p.ID]) != 3 {
		t.Errorf("terms = %v", terms[p.ID])
	}
	news, _ := db.GetTerm(e.ctx, e.d.R, 2)
	if news.Count != 1 {
		t.Errorf("category count = %d", news.Count)
	}
	// A second post with the same title gets a unique slug.
	p2, _ := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "Hello World", Status: db.StatusPublish})
	if p2.Slug != "hello-world-2" {
		t.Errorf("second slug = %q", p2.Slug)
	}
	// Changing the slug of a published post keeps the old one as redirect.
	in.Slug = "Hello Go"
	p, _ = ed.Save(e.ctx, p, in)
	if ids, _ := db.SlugRedirects(e.ctx, e.d.R, "hello-world"); p.Slug != "hello-go" || len(ids) != 1 {
		t.Errorf("slug %q, redirects %v", p.Slug, ids)
	}
	if e.site.invalidated == 0 {
		t.Error("site not invalidated")
	}
	revs, _ := db.Revisions(e.ctx, e.d.R, p.ID)
	if len(revs) != 2 {
		t.Errorf("revisions = %d", len(revs))
	}
}

func TestScheduling(t *testing.T) {
	e := newEnv(t)
	ed := e.as(e.admin)
	p, err := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "Later", Status: db.StatusPublish,
		Date: "2026-10-25T08:30"}) // CET: DST ends on this day
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != db.StatusFuture || p.DateGMT.Format(db.TimeLayout) != "2026-10-25 07:30:00" {
		t.Fatalf("scheduled = %s %s", p.Status, p.DateGMT.Format(db.TimeLayout))
	}
	e.svc.PublishDue(e.ctx)
	if got, _ := db.GetPost(e.ctx, e.d.R, p.ID); got.Status != db.StatusFuture {
		t.Error("published too early")
	}
	e.now = e.now.Add(24 * time.Hour)
	e.svc.PublishDue(e.ctx)
	if got, _ := db.GetPost(e.ctx, e.d.R, p.ID); got.Status != db.StatusPublish {
		t.Error("not published when due")
	}
}

func TestRolesAndSanitizing(t *testing.T) {
	e := newEnv(t)
	contrib := e.user(t, "contrib", auth.RoleContributor)
	author := e.user(t, "author", auth.RoleAuthor)
	p, err := e.as(contrib).Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "<script>x</script>Hi",
		Content: `<p onclick="evil()">ok</p><script>alert(1)</script>`, Status: db.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != db.StatusPending || strings.Contains(p.Content, "script") || strings.Contains(p.Content, "onclick") ||
		strings.Contains(p.Title, "script") {
		t.Errorf("contributor post = %s %q %q", p.Status, p.Title, p.Content)
	}
	if _, err := e.as(contrib).Save(e.ctx, nil, &PostInput{Type: db.TypePage, Title: "x", Status: db.StatusDraft}); !errors.Is(err, ErrForbidden) {
		t.Errorf("contributor created a page: %v", err)
	}
	if _, err := e.as(author).Save(e.ctx, p, &PostInput{Type: db.TypePost, Title: "x", Status: db.StatusDraft}); !errors.Is(err, ErrForbidden) {
		t.Errorf("author edited another user's post: %v", err)
	}
	if err := e.as(author).Trash(e.ctx, p); !errors.Is(err, ErrForbidden) {
		t.Errorf("author trashed another user's post: %v", err)
	}
	// Administrators keep their HTML.
	ap, _ := e.as(e.admin).Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "a", Content: `<iframe src="https://v.example"></iframe>`, Status: db.StatusDraft})
	if !strings.Contains(ap.Content, "iframe") {
		t.Error("administrator HTML filtered")
	}
}

func TestTrashRestoreDeleteDuplicate(t *testing.T) {
	e := newEnv(t)
	ed := e.as(e.admin)
	p, _ := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "T", Content: "<p>c</p>", Status: db.StatusPublish,
		Categories: []int64{2}, Tags: []string{"x"}})
	db.AddPostMeta(e.ctx, e.d.W, p.ID, "custom", "v")
	cp, err := ed.Duplicate(e.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	terms, _ := db.PostTerms(e.ctx, e.d.R, []int64{cp.ID})
	if cp.Status != db.StatusDraft || cp.Title != "T" || cp.Slug != "" || len(terms[cp.ID]) != 2 {
		t.Errorf("duplicate = %+v %v", cp, terms[cp.ID])
	}
	if v, ok, _ := db.PostMeta(e.ctx, e.d.R, cp.ID, "custom"); !ok || v != "v" {
		t.Error("meta not copied")
	}
	if err := ed.Delete(e.ctx, p); err == nil {
		t.Error("deleted a post that is not in the trash")
	}
	if err := ed.Trash(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.GetTerm(e.ctx, e.d.R, 2); n.Count != 0 {
		t.Errorf("count after trash = %d", n.Count)
	}
	p, _ = db.GetPost(e.ctx, e.d.R, p.ID)
	if err := ed.Restore(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	p, _ = db.GetPost(e.ctx, e.d.R, p.ID)
	if p.Status != db.StatusDraft {
		t.Errorf("restored status = %s", p.Status)
	}
	ed.Trash(e.ctx, p)
	p, _ = db.GetPost(e.ctx, e.d.R, p.ID)
	if err := ed.Delete(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetPost(e.ctx, e.d.R, p.ID); err == nil {
		t.Error("post still exists")
	}
}

func TestPageParentsAndAutosave(t *testing.T) {
	e := newEnv(t)
	ed := e.as(e.admin)
	a, _ := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePage, Title: "A", Status: db.StatusPublish})
	b, _ := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePage, Title: "B", Status: db.StatusPublish, ParentID: a.ID})
	// Same slug allowed below different parents.
	c, _ := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePage, Title: "B", Status: db.StatusPublish})
	if b.Slug != "b" || c.Slug != "b" {
		t.Errorf("slugs %q %q", b.Slug, c.Slug)
	}
	var ve *ValidationError
	if _, err := ed.Save(e.ctx, a, &PostInput{Type: db.TypePage, Title: "A", Status: db.StatusPublish, ParentID: b.ID}); !errors.As(err, &ve) {
		t.Errorf("cycle accepted: %v", err)
	}
	if _, err := ed.Save(e.ctx, a, &PostInput{Type: db.TypePage, Title: "A", Status: db.StatusPublish, Template: "../../etc"}); !errors.As(err, &ve) {
		t.Errorf("bad template accepted: %v", err)
	}
	// Autosaves never change the post itself.
	if _, err := ed.Autosave(e.ctx, a, "A2", "<p>draft</p>", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetPost(e.ctx, e.d.R, a.ID)
	as, _ := db.Autosave(e.ctx, e.d.R, a.ID, e.admin.ID)
	if got.Title != "A" || as == nil || as.Title != "A2" {
		t.Errorf("post %q, autosave %+v", got.Title, as)
	}
	// Saving discards the autosave.
	ed.Save(e.ctx, got, &PostInput{Type: db.TypePage, Title: "A3", Status: db.StatusPublish})
	if as, _ := db.Autosave(e.ctx, e.d.R, a.ID, e.admin.ID); as != nil {
		t.Error("autosave survived a save")
	}
}

func TestModerateComments(t *testing.T) {
	e := newEnv(t)
	ed := e.as(e.admin)
	p, _ := ed.Save(e.ctx, nil, &PostInput{Type: db.TypePost, Title: "P", Status: db.StatusPublish})
	c := &db.Comment{PostID: p.ID, Type: "comment", Status: db.CommentPending, Content: "hi", Author: "x"}
	db.InsertComment(e.ctx, e.d.W, c)
	if n, err := ed.ModerateComments(e.ctx, CommentApprove, []int64{c.ID}); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if got, _ := db.GetPost(e.ctx, e.d.R, p.ID); got.CommentCount != 1 {
		t.Errorf("comment count = %d", got.CommentCount)
	}
	reply, err := ed.ReplyComment(e.ctx, c, "thanks")
	if err != nil || reply.ParentID != c.ID || reply.Status != db.CommentApproved {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	ed.ModerateComments(e.ctx, CommentSpam, []int64{c.ID})
	if n, _ := ed.EmptyComments(e.ctx, db.CommentSpam); n != 1 {
		t.Errorf("emptied %d", n)
	}
	got, _ := db.GetComment(e.ctx, e.d.R, reply.ID)
	if got.ParentID != 0 {
		t.Errorf("orphaned reply parent = %d", got.ParentID)
	}
	editor := e.user(t, "author", auth.RoleAuthor)
	if _, err := e.as(editor).ModerateComments(e.ctx, CommentApprove, []int64{reply.ID}); !errors.Is(err, ErrForbidden) {
		t.Errorf("author moderated: %v", err)
	}
}
