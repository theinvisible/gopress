package site

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

// fakeViewer accepts the session cookie value "good".
type fakeViewer struct{ v *Visitor }

func (fakeViewer) PreviewPost(*http.Request, int64) (*db.Post, bool) { return nil, false }
func (f fakeViewer) Visitor(r *http.Request) *Visitor {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value == "good" {
		return f.v
	}
	return nil
}

func testVisitor() *Visitor {
	return &Visitor{ID: 1, Login: "admin", Name: "Admin", DisplayName: "Admin", Email: "admin@example.org", CSRF: "tok",
		ShowBar: true, Can: func(string) bool { return true }, CanEdit: func(*db.Post) bool { return true },
		BarStyles: []string{"/admin/static/x/admin-bar.min.css"}, AvatarURL: "https://secure.gravatar.com/avatar/x"}
}

func vget(h http.Handler, path, cookie string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestVisitorPages(t *testing.T) {
	s, h := newTestSite(t)
	v := testVisitor()
	s.SetPreviewer(fakeViewer{v})
	post := "/2024/02/hello-world/"
	anon := vget(h, post, "", nil)
	if strings.Contains(anon.Body.String(), "wpadminbar") || strings.Contains(anon.Body.String(), "post-edit-link") {
		t.Error("toolbar or edit link for anonymous visitors")
	}
	// A forged cookie gets the cached anonymous page.
	if rec := vget(h, post, "forged", nil); strings.Contains(rec.Body.String(), "wpadminbar") || rec.Header().Get("Cache-Control") == "private, no-store" {
		t.Error("forged session cookie personalized the page")
	}
	rec := vget(h, post, "good", nil)
	body := rec.Body.String()
	for _, want := range []string{`<div id="wpadminbar"`, `href="/admin/posts/10/edit">Edit Post</a>`, `name="_csrf" value="tok"`,
		` admin-bar `, `<span class="edit-link"><a class="post-edit-link" href="/admin/posts/10/edit">`, `admin-bar.min.css`,
		`<p class="logged-in-as">`} {
		if !strings.Contains(body, want) {
			t.Errorf("logged-in page lacks %q", want)
		}
	}
	if strings.Contains(body, `id="author"`) {
		t.Error("name field shown to a logged-in user")
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("logged-in page cacheable: %q", rec.Header().Get("Cache-Control"))
	}
	// The personal page did not end up in the cache.
	if strings.Contains(vget(h, post, "", nil).Body.String(), "wpadminbar") {
		t.Error("toolbar leaked into the cache")
	}
	// Toolbar switched off: edit links stay.
	v.ShowBar = false
	if b := vget(h, post, "good", nil).Body.String(); strings.Contains(b, "wpadminbar") || !strings.Contains(b, "post-edit-link") {
		t.Error("toolbar preference ignored")
	}
	// Customizer draft: only for this request.
	v.Draft = map[string]any{"blogname": "Drafted Name", "custom_css": "body{color:red}",
		"theme_mods": map[string]any{"header_textcolor": "blank"}}
	b := vget(h, "/", "good", nil).Body.String()
	if !strings.Contains(b, "Drafted Name") || !strings.Contains(b, `<style id="wp-custom-css">`) || !strings.Contains(b, "clip: rect(1px, 1px, 1px, 1px)") {
		t.Error("draft not applied")
	}
	if strings.Contains(vget(h, "/", "", nil).Body.String(), "Drafted Name") {
		t.Error("draft visible to visitors")
	}
}

func TestLoggedInComments(t *testing.T) {
	s, h := newTestSite(t)
	v := testVisitor()
	v.ID, v.DisplayName, v.Email = 7, "Editor Ed", "ed@example.org"
	s.SetPreviewer(fakeViewer{v})
	send := func(cookie string, origin bool) *httptest.ResponseRecorder {
		tokenSeq++
		form := url.Values{"comment_post_ID": {"10"}, "comment": {"Logged-in comment " + time.Now().String()}}
		if origin {
			form.Set("_csrf", "tok")
		}
		form.Set("gp_token", s.guard.Token(10, time.Now().Add(-time.Minute-time.Duration(tokenSeq)*time.Second)))
		r := httptest.NewRequest("POST", "/wp-comments-post.php", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin {
			r.Header.Set("Origin", "https://example.org")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := send("good", true); rec.Code != http.StatusSeeOther {
		t.Fatalf("logged-in comment: %d %s", rec.Code, rec.Body)
	}
	var author, email, status string
	var uid int64
	s.db.R.QueryRow(`SELECT author, author_email, status, user_id FROM comments ORDER BY id DESC LIMIT 1`).Scan(&author, &email, &status, &uid)
	if author != "Editor Ed" || email != "ed@example.org" || status != db.CommentApproved || uid != 7 {
		t.Errorf("comment = %q %q %q %d", author, email, status, uid)
	}
	// Without the session's token (and origin) the session is not used:
	// the name and email fields are required.
	if rec := send("good", false); rec.Code != http.StatusBadRequest {
		t.Errorf("cross-origin logged-in comment: %d", rec.Code)
	}
	// Registration required.
	db.SetOption(context.Background(), s.db.W, "comment_registration", "1")
	s.Invalidate()
	if rec := send("", true); rec.Code != http.StatusForbidden {
		t.Errorf("anonymous comment with registration required: %d", rec.Code)
	}
	if b := vget(h, "/2024/02/hello-world/", "", nil).Body.String(); !strings.Contains(b, `class="must-log-in"`) || strings.Contains(b, `id="commentform"`) {
		t.Error("comment form shown although registration is required")
	}
}

func TestCustomizationOutput(t *testing.T) {
	s, h := newTestSite(t)
	ctx := context.Background()
	before := vget(h, "/", "", nil).Body.String()
	if strings.Contains(before, "twentytwelve-header-css") || strings.Contains(before, "header-image") || strings.Contains(before, "wp-custom-css") {
		t.Fatal("customization output without customizations")
	}
	db.SetOption(ctx, s.db.W, "theme_mods", map[string]any{"header_textcolor": "ff0000", "header_image": "/wp-content/uploads/h.jpg",
		"header_image_data": map[string]any{"width": 1000, "height": 200}})
	db.SetOption(ctx, s.db.W, "custom_css", "a{color:red}</style><script>alert(1)</script>")
	s.Invalidate()
	b := vget(h, "/", "", nil).Body.String()
	for _, want := range []string{"color: #ff0000;", `<img src="https://example.org/wp-content/uploads/h.jpg" class="header-image" width="1000" height="200" alt="Test Site" />`,
		`<style id="wp-custom-css">`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(b, "<script>alert(1)") || strings.Count(b, "</style>") != strings.Count(b, "<style") {
		t.Error("custom CSS broke out of the style element")
	}
}
