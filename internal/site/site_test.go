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
	att := db.Post{ID: 40, Type: db.TypeAttachment, Status: db.StatusInherit, Slug: "a", ParentID: 10, MimeType: "image/png",
		Date: at("2024-02-10 12:00:00"), DateGMT: at("2024-02-10 11:00:00")}
	must(db.InsertPost(ctx, q, &att))
	must(db.InsertMedia(ctx, q, &db.Media{PostID: 40, File: "2024/a.png", Mime: "image/png", Width: 300, Height: 300,
		Sizes: map[string]db.MediaSize{"thumbnail": {File: "a-150x150.png", Width: 150, Height: 150}}}))
	att2 := db.Post{ID: 41, Type: db.TypeAttachment, Status: db.StatusInherit, Slug: "link", MimeType: "image/png",
		Date: at("2024-02-10 12:00:00"), DateGMT: at("2024-02-10 11:00:00")}
	must(db.InsertPost(ctx, q, &att2))
	must(db.InsertMedia(ctx, q, &db.Media{PostID: 41, File: "2024/link.png", Mime: "image/png"}))
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
	os.WriteFile(filepath.Join(uploads, "2024", "a.png"), []byte("file"), 0o644)
	os.WriteFile(filepath.Join(uploads, "2024", "a-150x150.png"), []byte("thumb"), 0o644)
	os.WriteFile(filepath.Join(uploads, "2024", "plugin-cache.json"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(uploads, "2024", "orphan.png"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(uploads, "2024", ".htaccess"), []byte("secret"), 0o644)
	os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(uploads, "2024", "link.png"))
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
		// Canonical feed URLs as in WordPress: RSS2 is the default feed.
		{"/feed/rss2/", 301, "https://example.org/feed/", ""},
		{"/feed/atom/", 301, "https://example.org/feed/", ""},
		{"/rss2/", 301, "https://example.org/feed/", ""},
		{"/comments/feed/rss2/", 301, "https://example.org/comments/feed/", ""},
		{"/category/news/feed/rss2/", 301, "https://example.org/category/news/feed/", ""},
		{"/search/hello/feed/rss2/", 200, "", "Search Results"},
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
	if rec := get(t, h, "/wp-content/uploads/2024/a.png"); rec.Code != 200 || rec.Body.String() != "file" {
		t.Errorf("upload: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, h, "/wp-content/uploads/2024/a-150x150.png"); rec.Code != 200 {
		t.Errorf("generated size: %d", rec.Code)
	}
	for _, p := range []string{"/wp-content/uploads/2024/", "/wp-content/uploads/../secret.txt", "/wp-content/uploads/%2e%2e/secret.txt",
		"/static/twentytwelve/x/../../secret.txt", "/wp-content/uploads/2024/plugin-cache.json", "/wp-content/uploads/2024/orphan.png",
		"/wp-content/uploads/2024/.htaccess", "/wp-content/uploads/2024/link.png"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = p
		req.URL.RawPath = ""
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 && strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s leaked a protected file", p)
		}
		if strings.HasSuffix(p, "/") && rec.Code != 404 {
			t.Errorf("%s: directory listing status %d", p, rec.Code)
		}
	}
}

var tokenSeq int

func postComment(t *testing.T, h http.Handler, s *Site, form url.Values, tokenAge time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	if form.Get(antispam.TokenField) == "" {
		tokenSeq++ // tokens are single-use: make each one unique
		form.Set(antispam.TokenField, s.guard.Token(10, time.Now().Add(-tokenAge-time.Duration(tokenSeq)*time.Second)))
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
	var signed *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == commenterCookie {
			signed = c
		}
	}
	if signed == nil || !signed.HttpOnly {
		t.Errorf("signed commenter cookie missing or not HttpOnly: %v", rec.Result().Cookies())
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
	f.Del(antispam.TokenField) // fresh token, same content
	if rec := postComment(t, h, s, f, time.Minute); rec.Code != http.StatusConflict {
		t.Errorf("duplicate: %d", rec.Code)
	}
	f = base("hi")
	f.Set("comment_post_ID", "20") // comments closed
	if rec := postComment(t, h, s, f, time.Minute); rec.Code != http.StatusForbidden {
		t.Errorf("closed: %d", rec.Code)
	}
	f = base("hi")
	req := httptest.NewRequest(http.MethodPost, "/wp-comments-post.php", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site post: %d", rec.Code)
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
	// Random query parameters and unsigned commenter cookies cannot bypass
	// or flood the cache.
	for _, c := range []*http.Cookie{{Name: "comment_author_email_x", Value: "a@b.c"}, {Name: commenterCookie, Value: "Zm9v.bad"}} {
		req = httptest.NewRequest(http.MethodGet, "/?utm_source=x", nil)
		req.AddCookie(c)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "HIT" {
			t.Errorf("cookie %s bypassed the cache", c.Name)
		}
	}
}

func TestPendingCommentsOnlyForSignedCommenter(t *testing.T) {
	s, h := newTestSite(t)
	s.db.W.Exec(`INSERT INTO comments (id, post_id, type, status, author, author_email, content, date_local, date_gmt)
		VALUES (200, 10, 'comment', 'pending', 'Eve', 'eve@example.org', 'Pending text', '2024-02-12 10:00:00', '2024-02-12 09:00:00')`)
	page := func(c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/2024/02/hello-world/", nil)
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if strings.Contains(page(nil).Body.String(), "Pending text") {
		t.Error("pending comment visible to everyone")
	}
	forged := &http.Cookie{Name: "comment_author_email_" + cookieHash("https://example.org"), Value: "eve%40example.org"}
	if strings.Contains(page(forged).Body.String(), "Pending text") {
		t.Error("pending comment visible with a forged WordPress cookie")
	}
	rec := page(&http.Cookie{Name: commenterCookie, Value: s.signCommenter("eve@example.org")})
	if !strings.Contains(rec.Body.String(), "Pending text") || !strings.Contains(rec.Body.String(), "awaiting moderation") {
		t.Error("pending comment not shown to its author")
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("personalized page Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}
