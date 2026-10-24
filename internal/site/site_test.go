package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/antispam"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/theme"
	"github.com/theinvisible/gopress/themes"
)

func at(s string) db.Time {
	t, _ := time.Parse(db.TimeLayout, s)
	return db.Time{Time: t}
}

// newTestSite builds a small site: a post, a parent and child page, an
// old slug, a category and an approved comment.
func newTestSite(t *testing.T) (*Site, http.Handler) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	q := d.W
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]any{
		"blogname": "Test Site", "blogdescription": "Tagline", "home": "https://example.org",
		"permalink_structure": "/%year%/%monthnum%/%postname%/", "posts_per_page": "10",
		"date_format": "d/m/Y", "time_format": "H:i", "timezone_string": "Europe/Vienna",
		"comment_max_links": "2", "thread_comments": "1", "thread_comments_depth": "5", "comment_previously_approved": "",
	} {
		must(db.SetOption(ctx, q, k, v))
	}
	must(db.InsertUser(ctx, q, &db.User{ID: 1, Login: "admin", Nicename: "admin", DisplayName: "Admin", Role: "administrator", RegisteredGMT: at("2020-01-01 00:00:00")}))
	must(db.InsertTerm(ctx, q, &db.Term{ID: 5, Taxonomy: db.TaxCategory, Slug: "news", Name: "News", Count: 1}))
	posts := []db.Post{
		{ID: 10, Type: db.TypePost, Status: db.StatusPublish, Slug: "hello-world", AuthorID: 1, Title: "Hello \"World\"",
			Content: "First paragraph -- with dashes.\n\nSecond <!--more--> rest", ContentFormat: db.FormatWPAutop,
			Date: at("2024-02-10 12:00:00"), DateGMT: at("2024-02-10 11:00:00"), CommentStatus: "open"},
		{ID: 20, Type: db.TypePage, Status: db.StatusPublish, Slug: "parent", AuthorID: 1, Title: "Parent", Content: "<p>Parent page</p>",
			Date: at("2024-01-01 10:00:00"), DateGMT: at("2024-01-01 09:00:00"), CommentStatus: "closed"},
		{ID: 21, Type: db.TypePage, Status: db.StatusPublish, Slug: "child", ParentID: 20, AuthorID: 1, Title: "Child", Content: "<p>Child page</p>",
			Date: at("2024-01-02 10:00:00"), DateGMT: at("2024-01-02 09:00:00"), CommentStatus: "closed"},
		{ID: 30, Type: db.TypePost, Status: db.StatusDraft, Slug: "secret-draft", AuthorID: 1, Title: "Draft",
			Date: at("2024-03-01 10:00:00"), DateGMT: at("2024-03-01 09:00:00")},
	}
	for i := range posts {
		must(db.InsertPost(ctx, q, &posts[i]))
	}
	must(db.AddPostTerm(ctx, q, 10, 5, 0))
	must(db.AddSlugRedirect(ctx, q, "old-hello", 10))
	must(db.InsertComment(ctx, q, &db.Comment{ID: 100, PostID: 10, Type: "comment", Status: db.CommentApproved, Author: "Alice",
		AuthorEmail: "alice@example.org", Content: "Great <b>post</b>!", Date: at("2024-02-11 10:00:00"), DateGMT: at("2024-02-11 09:00:00")}))
	must(db.UpdateCommentCount(ctx, q, 10))

	th, err := theme.Load(themes.FS, "twentytwelve")
	if err != nil {
		t.Fatal(err)
	}
	uploads := filepath.Join(dir, "uploads")
	os.MkdirAll(filepath.Join(uploads, "2024"), 0o755)
	os.WriteFile(filepath.Join(uploads, "2024", "a.txt"), []byte("file"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o644)
	s := New(d, th, Options{BaseURL: "https://example.org", UploadsDir: uploads, Secret: []byte("k"), CacheBytes: 1 << 20})
	return s, SecurityHeaders(s)
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestRouting(t *testing.T) {
	_, h := newTestSite(t)
	tests := []struct {
		path     string
		status   int
		location string
		contains string
	}{
		{"/", 200, "", "Hello &#8220;World&#8221;"},
		{"/2024/02/hello-world/", 200, "", "First paragraph &#8212; with dashes."},
		{"/2024/02/hello-world", 301, "https://example.org/2024/02/hello-world/", ""},
		{"/2023/01/hello-world/", 301, "https://example.org/2024/02/hello-world/", ""}, // wrong date: guessed by slug
		{"/old-hello/", 301, "https://example.org/2024/02/hello-world/", ""},
		{"/?p=10", 301, "https://example.org/2024/02/hello-world/", ""},
		{"/?p=30", 404, "", ""}, // drafts are not public
		{"/2024/03/secret-draft/", 404, "", ""},
		{"/parent/child/", 200, "", "Child page"},
		{"/child/", 301, "https://example.org/parent/child/", ""},
		{"/category/news/", 200, "", "Category Archives: <span>News</span>"},
		{"/2024/02/", 200, "", "Monthly Archives"},
		{"/2099/", 404, "", ""},
		{"/feed/", 200, "", "<title>Hello “World”</title>"},
		{"/2024/02/hello-world/feed/", 200, "", "Comments on: Hello"},
		{"/wp-sitemap.xml", 200, "", "wp-sitemap-posts-post-1.xml"},
		{"/robots.txt", 200, "", "Sitemap: https://example.org/wp-sitemap.xml"},
		{"/xmlrpc.php", 410, "", ""},
		{"/wp-admin/install.php", 404, "", ""},
		{"/nope/", 404, "", ""},
	}
	for _, tt := range tests {
		rec := get(t, h, tt.path)
		if rec.Code != tt.status {
			t.Errorf("%s: status %d, want %d", tt.path, rec.Code, tt.status)
			continue
		}
		if tt.location != "" && rec.Header().Get("Location") != tt.location {
			t.Errorf("%s: Location %q, want %q", tt.path, rec.Header().Get("Location"), tt.location)
		}
		want := tt.contains
		if want == "" {
			want = tt.location
		}
		if tt.status == 200 && want != "" && !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: body does not contain %q", tt.path, want)
		}
	}
}

func TestReflectedInputIsEscaped(t *testing.T) {
	_, h := newTestSite(t)
	payload := `"><script>alert(1)</script>`
	rec := get(t, h, "/?s="+url.QueryEscape(payload))
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("search query reflected unescaped")
	}
	if !strings.Contains(body, "&#34;&gt;&lt;script&gt;") && !strings.Contains(body, "&quot;&gt;&lt;script&gt;") {
		t.Errorf("escaped query not found in page")
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, h := newTestSite(t)
	rec := get(t, h, "/")
	for _, k := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(k) == "" {
			t.Errorf("missing header %s", k)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("CSP allows inline scripts: %s", csp)
	}
}

func TestUploadsAreConfined(t *testing.T) {
	_, h := newTestSite(t)
	if rec := get(t, h, "/wp-content/uploads/2024/a.txt"); rec.Code != 200 || rec.Body.String() != "file" {
		t.Errorf("upload: %d %q", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/wp-content/uploads/2024/", "/wp-content/uploads/../secret.txt", "/wp-content/uploads/%2e%2e/secret.txt", "/static/twentytwelve/x/../../secret.txt"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = p
		req.URL.RawPath = ""
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 && strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s leaked a file outside uploads", p)
		}
		if strings.HasSuffix(p, "/") && rec.Code != 404 {
			t.Errorf("%s: directory listing status %d", p, rec.Code)
		}
	}
}

func postComment(t *testing.T, h http.Handler, s *Site, form url.Values, tokenAge time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	if form.Get(antispam.TokenField) == "" {
		form.Set(antispam.TokenField, s.guard.Token(10, time.Now().Add(-tokenAge)))
	}
	req := httptest.NewRequest(http.MethodPost, "/wp-comments-post.php", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCommentSubmission(t *testing.T) {
	s, h := newTestSite(t)
	base := func(comment string) url.Values {
		return url.Values{"comment_post_ID": {"10"}, "author": {"Bob"}, "email": {"bob@example.org"}, "comment": {comment}}
	}

	if rec := postComment(t, h, s, base("hi"), time.Second); rec.Code != 400 {
		t.Errorf("too fast: %d", rec.Code)
	}
	f := base("hi")
	f.Set(antispam.HoneypotField, "x")
	if rec := postComment(t, h, s, f, time.Minute); rec.Code != 400 {
		t.Errorf("honeypot: %d", rec.Code)
	}
	f = base(`<script>alert(1)</script><a href="javascript:x()" onclick="y()">link</a> <b>ok</b>`)
	f.Set("url", "javascript:alert(1)")
	rec := postComment(t, h, s, f, time.Minute)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid comment: %d %s", rec.Code, rec.Body.String())
	}
	if c := rec.Result().Cookies(); len(c) == 0 || !c[0].HttpOnly {
		t.Errorf("commenter cookies missing or not HttpOnly: %v", c)
	}
	var content, status, website string
	s.db.R.QueryRow(`SELECT content, status, author_url FROM comments WHERE author = 'Bob'`).Scan(&content, &status, &website)
	for _, bad := range []string{"<script", "javascript:", "onclick"} {
		if strings.Contains(content, bad) || strings.Contains(website, bad) {
			t.Errorf("stored comment contains %q: %q / %q", bad, content, website)
		}
	}
	if status != db.CommentApproved {
		t.Errorf("status = %q", status)
	}
	// The new comment is visible right away: writes invalidate the cache.
	if body := get(t, h, "/2024/02/hello-world/").Body.String(); !strings.Contains(body, "<b>ok</b>") {
		t.Error("approved comment not shown after posting (stale cache?)")
	}

	f = base(`<a href="https://a.example">a</a> <a href="https://b.example">b</a>`)
	postComment(t, h, s, f, time.Minute)
	s.db.R.QueryRow(`SELECT status FROM comments WHERE content LIKE '%a.example%'`).Scan(&status)
	if status != db.CommentPending {
		t.Errorf("comment with 2 links: status %q, want pending", status)
	}
	if rec := postComment(t, h, s, f, time.Minute); rec.Code != http.StatusConflict {
		t.Errorf("duplicate: %d", rec.Code)
	}
	f = base("hi")
	f.Set("comment_post_ID", "20") // comments closed
	if rec := postComment(t, h, s, f, time.Minute); rec.Code != http.StatusForbidden {
		t.Errorf("closed: %d", rec.Code)
	}
	big := base(strings.Repeat("x", maxCommentBody+10))
	if rec := postComment(t, h, s, big, time.Minute); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}
}

func TestPageCache(t *testing.T) {
	_, h := newTestSite(t)
	get(t, h, "/")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != "HIT" || rec.Header().Get("Content-Encoding") != "br" {
		t.Errorf("second request: X-Cache=%q Content-Encoding=%q", rec.Header().Get("X-Cache"), rec.Header().Get("Content-Encoding"))
	}
	etag := rec.Header().Get("ETag")
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	// Visitors with commenter cookies bypass the cache.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "comment_author_x", Value: "y"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") == "HIT" {
		t.Error("cache served a visitor with comment cookies")
	}
}
