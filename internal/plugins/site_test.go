package plugins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

func (e *hostEnv) get(target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://example.org"+target, nil)
	rec := httptest.NewRecorder()
	e.site.ServeHTTP(rec, r)
	return rec
}

func TestSiteIntegration(t *testing.T) {
	e := newHostEnv(t)
	ctx := context.Background()
	e.site.SetExtensions(e.h)
	now := db.Time{Time: time.Now().UTC()}
	db.InsertPost(ctx, e.d.W, &db.Post{ID: 40, Type: db.TypePost, Status: db.StatusPublish, Slug: "with-plugin", Title: "With plugin",
		Content: `<p>[hello name="Ti"]</p><p>[evil]</p>`, ContentFormat: db.FormatHTML, Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertWidget(ctx, e.d.W, &db.Widget{ID: "tester_widget-2", Sidebar: "sidebar-1", Type: "tester_widget",
		Settings: map[string]any{"title": "Tester", "text": "<b>hi</b>"}})
	e.site.Invalidate()

	page := e.get("/with-plugin/").Body.String()
	for _, want := range []string{`<span class="hello" data-n="1">Hello, Ti!</span>`,
		`<link rel='stylesheet' id='plugin-tester-hello-css' href='/plugins/tester/assets/hello.css?v=`,
		`<script src="/plugins/tester/assets/hello.js?v=`, `<p class="tester-footer">filtered</p>`,
		`<h3 class="widget-title">Tester</h3><p>widget <b>hi</b></p>`,
		`<form action="https://www.paypal.com/cgi-bin/webscr" method="post">`, `data-x="1"`, `aria-label="d"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"onclick", "<script>alert", "javascript:", "<iframe", "undeclared.js"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if i, j := strings.Index(page, "hello.css"), strings.Index(page, "</head>"); i < 0 || i > j {
		t.Error("stylesheet not in the head")
	}
	// Pages without plugin shortcodes get no plugin assets.
	if p := e.get("/hello/").Body.String(); strings.Contains(p, "/plugins/tester/") {
		t.Error("assets on a page that does not need them")
	}

	// Assets: approved ones only, with types and caching headers.
	pkg := e.h.Package("tester")
	rec := e.get("/plugins/tester/assets/hello.js?v=" + pkg.SHA256[:12])
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") ||
		rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("asset: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/plugins/tester/assets/../plugin.json", "/plugins/tester/plugin.wasm", "/plugins/tester/admin/app.html",
		"/plugins/tester/assets/nope.js", "/plugins/unknown/assets/hello.js"} {
		if rec := e.get(p); rec.Code == 200 {
			t.Errorf("%s served", p)
		}
	}

	// Routes and legacy URLs: no cookies, no redirects, sandboxed.
	r := httptest.NewRequest("POST", "https://example.org/plugins/tester/ping?a=1", strings.NewReader("payload"))
	r.Header.Set("Cookie", "gopress_session=secret")
	rec = httptest.NewRecorder()
	e.site.ServeHTTP(rec, r)
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 200 || string(body) != `{"method":"POST","body":"payload","q":["1"]}` ||
		rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("Location") != "" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("route: %d %s %v", rec.Code, body, rec.Header())
	}
	if rec := e.get("/?tester_ping=1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"method":"GET"`) {
		t.Errorf("legacy route: %d %s", rec.Code, rec.Body)
	}
	if rec := e.get("/plugins/tester/other"); rec.Code == 200 || strings.Contains(rec.Body.String(), "method") {
		t.Errorf("undeclared route answered by the plugin: %d", rec.Code) // the site's router handles it
	}
	for range routePerMin {
		e.get("/plugins/tester/ping")
	}
	if rec := e.get("/plugins/tester/ping"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("route rate limit: %d", rec.Code)
	}

	// Deactivated: the shortcode stays text (like WordPress), no plugin
	// output or assets, routes are gone.
	if err := e.h.Deactivate(ctx, "tester"); err != nil {
		t.Fatal(err)
	}
	page = e.get("/with-plugin/").Body.String()
	if strings.Contains(page, "Hello, Ti") || strings.Contains(page, "/plugins/tester/") || strings.Contains(page, "filtered") {
		t.Error("deactivated plugin still renders")
	}
	if rec := e.get("/plugins/tester/ping"); rec.Code == 200 {
		t.Errorf("route of a deactivated plugin: %d", rec.Code)
	}
}

func TestCommentHooks(t *testing.T) {
	e := newHostEnv(t)
	ctx := context.Background()
	filter := e.h.CommentFilter()
	post, _ := db.GetPost(ctx, e.d.R, 10)
	r := httptest.NewRequest("POST", "https://example.org/wp-comments-post.php", nil)
	for content, want := range map[string]string{"nice post": "", "buy viagra": db.CommentSpam, "discard-me": "discard"} {
		if got := filter(r, &db.Comment{Content: content, Status: db.CommentPending}, post); got != want {
			t.Errorf("%q: verdict %q, want %q", content, got, want)
		}
	}
	// A hanging check fails open after its time limit.
	start := time.Now()
	if got := filter(r, &db.Comment{Content: "slow-check"}, post); got != "" || time.Since(start) > 7*time.Second {
		t.Errorf("slow check: %q after %s", got, time.Since(start))
	}
	e.h.CommentStatusChanged(ctx, []int64{1}, "spam")
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, ok, _ := db.KVGet(ctx, e.d.R, "tester", "events", "last_status")
		if ok {
			if !strings.Contains(v, `"status":"spam"`) {
				t.Errorf("event: %s", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("comment_status_changed not delivered")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSanitizeHeadTags(t *testing.T) {
	in := `<meta name="description" content="x &quot;y&quot;"><meta http-equiv="refresh" content="0;url=https://evil">` +
		`<link rel="stylesheet" href="/plugins/x/assets/a.css"><link rel="stylesheet" href="https://evil/a.css">` +
		`<script>alert(1)</script><link rel="import" href="/plugins/x/a.html">`
	got := sanitizeHeadTags(in)
	if got != "<meta name=\"description\" content=\"x &#34;y&#34;\" />\n<link rel=\"stylesheet\" href=\"/plugins/x/assets/a.css\" />\n" {
		t.Errorf("head tags: %q", got)
	}
}
