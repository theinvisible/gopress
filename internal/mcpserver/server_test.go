package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/media"
	"github.com/theinvisible/gopress/internal/site"
	"github.com/theinvisible/gopress/internal/theme"
	"github.com/theinvisible/gopress/themes"
)

const home = "https://example.org"

type env struct {
	t       *testing.T
	d       *db.DB
	auth    *auth.Service
	srv     *Server
	h       http.Handler
	ts      *httptest.Server
	users   map[string]*db.User
	uploads string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for k, v := range map[string]any{"blogname": "Test &amp; Co", "home": home, "permalink_structure": "/%postname%/",
		"timezone_string": "UTC", "default_category": "1", "mcp_enabled": "1"} {
		db.SetOption(ctx, d.W, k, v)
	}
	db.InsertTerm(ctx, d.W, &db.Term{ID: 1, Taxonomy: db.TaxCategory, Slug: "uncategorized", Name: "Uncategorized"})
	db.InsertTerm(ctx, d.W, &db.Term{ID: 2, Taxonomy: db.TaxCategory, Slug: "releases", Name: "Releases"})
	e := &env{t: t, d: d, users: map[string]*db.User{}}
	for _, role := range []string{auth.RoleAdministrator, auth.RoleEditor, auth.RoleAuthor, auth.RoleContributor} {
		u := &db.User{Login: role, Nicename: role, Email: role + "@example.org", DisplayName: role, Role: role,
			PassHash: "$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", RegisteredGMT: db.Time{Time: time.Now()}}
		if err := db.InsertUser(ctx, d.W, u); err != nil {
			t.Fatal(err)
		}
		e.users[role] = u
	}
	now := db.Time{Time: time.Now().UTC()}
	db.InsertPost(ctx, d.W, &db.Post{ID: 10, Type: db.TypePost, Status: db.StatusPublish, Slug: "hello", AuthorID: e.users["administrator"].ID,
		Title: "Hello", Content: "<p>Hi</p>", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now, CommentStatus: "open"})
	db.InsertPost(ctx, d.W, &db.Post{ID: 11, Type: db.TypePost, Status: db.StatusPublish, Slug: "members", AuthorID: e.users["administrator"].ID,
		Title: "Members", Content: "<p>members only</p>", Password: "pw", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 30, Type: db.TypePost, Status: db.StatusDraft, AuthorID: e.users["editor"].ID,
		Title: "Editor's secret draft", Content: "<p>draft text</p>", Date: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 50, Type: db.TypePage, Status: db.StatusPrivate, Slug: "secret-plan", AuthorID: e.users["administrator"].ID,
		Title: "Secret plan", Content: "<p>x</p>", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 51, Type: db.TypePost, Status: db.StatusDraft, AuthorID: e.users["administrator"].ID,
		Title: "Embed", Content: `<p>video</p><iframe src="https://video.example/1"></iframe>`, Date: now, Modified: now, ModifiedGMT: now})
	db.InsertPost(ctx, d.W, &db.Post{ID: 52, Type: db.TypeAttachment, Status: db.StatusInherit, ParentID: 30, AuthorID: e.users["editor"].ID,
		Title: "draft image", MimeType: "image/png", Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	db.InsertMedia(ctx, d.W, &db.Media{PostID: 52, File: "2026/10/draft.png", Mime: "image/png", Width: 1, Height: 1})
	th, err := theme.Load(themes.FS, "twentytwelve")
	if err != nil {
		t.Fatal(err)
	}
	logOut := io.Discard
	if os.Getenv("GP_TEST_LOG") != "" {
		logOut = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	e.uploads = t.TempDir()
	pub := site.New(d, th, site.Options{BaseURL: home, Secret: []byte("k"), Log: log, UploadsDir: e.uploads})
	e.auth = auth.New(d, auth.Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Home: func() string { return home }, Log: log})
	cs := content.New(d, pub, e.auth, log)
	store, err := media.Open(e.uploads)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cs.SetMediaStore(store)
	e.srv = New(Options{DB: d, Auth: e.auth, Content: cs, Site: pub, Next: pub, Log: log})
	mux := http.NewServeMux()
	mux.Handle("/", pub)
	mux.Handle("/mcp", e.srv)
	e.h = site.SecurityHeaders(mux)
	e.ts = httptest.NewServer(e.h)
	t.Cleanup(e.ts.Close)
	return e
}

func (e *env) token(role string, scopes ...string) string {
	e.t.Helper()
	tok, _, err := e.auth.CreateAccessToken(context.Background(), nil, e.users[role], "test", scopes, 7)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

type bearer struct{ tok string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) client(tok string) *mcp.ClientSession {
	e.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{tok}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return cs
}

// tryCall runs a tool and returns its structured result, or the error text.
func tryCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, err.Error()
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return nil, b.String()
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out, ""
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	out, errText := tryCall(t, cs, name, args)
	if errText != "" {
		t.Fatalf("%s: %s", name, errText)
	}
	return out
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func TestToolsPerScope(t *testing.T) {
	e := newEnv(t)
	ro := toolNames(t, e.client(e.token(auth.RoleAuthor)))
	if want := []string{"get_post", "get_site_info", "list_posts", "list_terms"}; !slices.Equal(ro, want) {
		t.Errorf("read-only tools: %v", ro)
	}
	all := toolNames(t, e.client(e.token(auth.RoleEditor, auth.ScopeWrite, auth.ScopePublish, auth.ScopeMedia, auth.ScopeTerms)))
	if want := []string{"create_category", "create_post", "get_post", "get_site_info", "list_media", "list_posts", "list_terms",
		"update_post", "upload_media"}; !slices.Equal(all, want) {
		t.Errorf("all tools: %v", all)
	}
	info := mustCall(t, e.client(e.token(auth.RoleAuthor, auth.ScopeWrite)), "get_site_info", nil)
	if info["name"] != "Test & Co" || info["can_publish"] != false || info["url"] != home+"/" {
		t.Errorf("site info: %v", info)
	}
}

func TestPostLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cs := e.client(e.token(auth.RoleAuthor, auth.ScopeWrite))

	md := "Version **2.0** is out.\n\n```sh\ngopress serve\n```\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n"
	p := mustCall(t, cs, "create_post", map[string]any{"title": "GoPress 2.0", "content": md, "categories": []int{2},
		"tags": []string{"release", "go"}, "status": "publish"})
	// The author's token has no publish scope: submitted for review.
	if p["status"] != db.StatusPending || p["note"] == nil {
		t.Errorf("status without publish scope: %v %v", p["status"], p["note"])
	}
	body := p["content"].(string)
	for _, want := range []string{"<strong>2.0</strong>", `<code class="language-sh">`} {
		if !strings.Contains(body, want) {
			t.Errorf("content lacks %q: %s", want, body)
		}
	}
	for _, bad := range []string{"<script", "onerror"} {
		if strings.Contains(body, bad) {
			t.Errorf("content kept %q: %s", bad, body)
		}
	}
	if p["edit_url"] != home+"/admin/posts/"+jsonID(p)+"/edit" {
		t.Errorf("edit_url: %v", p["edit_url"])
	}
	if cats := p["categories"].([]any); len(cats) != 1 || cats[0].(map[string]any)["name"] != "Releases" {
		t.Errorf("categories: %v", p["categories"])
	}

	// Read back, update with optimistic locking.
	got := mustCall(t, cs, "get_post", map[string]any{"id": p["id"]})
	if got["modified"] != p["modified"] || got["mine"] != true {
		t.Fatalf("get_post: %v", got)
	}
	if _, errText := tryCall(t, cs, "update_post", map[string]any{"id": p["id"], "expected_modified": "2001-01-01T00:00:00Z", "title": "x"}); !strings.Contains(errText, "changed since") {
		t.Errorf("stale update: %q", errText)
	}
	time.Sleep(1100 * time.Millisecond) // modified has second precision
	up := mustCall(t, cs, "update_post", map[string]any{"id": p["id"], "expected_modified": got["modified"], "title": "GoPress 2.0 released",
		"tags": []string{"release"}})
	if up["title"] != "GoPress 2.0 released" || up["content"] != got["content"] || up["modified"] == got["modified"] {
		t.Errorf("update: %v", up)
	}
	if cats := up["categories"].([]any); len(cats) != 1 || !slices.Equal(anyStrings(up["tags"]), []string{"release"}) {
		t.Errorf("terms after update: %v %v", up["categories"], up["tags"])
	}
	list := mustCall(t, cs, "list_posts", map[string]any{"mine": true})
	if list["total"].(float64) != 1 {
		t.Errorf("own posts: %v", list)
	}

	// Audit entries name the token.
	var detail string
	e.d.R.QueryRowContext(ctx, `SELECT detail FROM audit_log WHERE action = 'post_updated'`).Scan(&detail)
	if !strings.Contains(detail, `"via":"mcp"`) {
		t.Errorf("audit: %q", detail)
	}
}

func jsonID(m map[string]any) string {
	b, _ := json.Marshal(m["id"])
	return string(b)
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestAdminTokenIsLimited(t *testing.T) {
	e := newEnv(t)
	cs := e.client(e.token(auth.RoleAdministrator, auth.ScopeWrite, auth.ScopePublish))
	p := mustCall(t, cs, "create_post", map[string]any{"title": "Live", "content_format": "html",
		"content": `<p onclick="x()">Hi</p><iframe src="https://evil.example"></iframe><script>x()</script>`, "status": "publish"})
	if p["status"] != db.StatusPublish {
		t.Errorf("publish scope: %v", p["status"])
	}
	// An administrator could post unfiltered HTML in the admin; a token
	// cannot.
	if c := p["content"].(string); strings.Contains(c, "onclick") || strings.Contains(c, "<iframe") || strings.Contains(c, "<script") {
		t.Errorf("unfiltered HTML through a token: %s", c)
	}
	// Others' content stays out of reach, even for an administrator.
	if _, errText := tryCall(t, cs, "get_post", map[string]any{"id": 30}); errText != "not found" {
		t.Errorf("foreign draft readable: %q", errText)
	}
	got := mustCall(t, e.client(e.token(auth.RoleEditor, auth.ScopeWrite, auth.ScopePublish)), "get_post", map[string]any{"id": 10})
	if _, errText := tryCall(t, e.client(e.token(auth.RoleEditor, auth.ScopeWrite, auth.ScopePublish)), "update_post",
		map[string]any{"id": 10, "expected_modified": got["modified"], "title": "defaced"}); !strings.Contains(errText, "own posts") {
		t.Errorf("editor changed another user's post: %q", errText)
	}
	// Password protected content of others is not returned.
	if m := mustCall(t, e.client(e.token(auth.RoleAuthor)), "get_post", map[string]any{"url": home + "/members/"}); m["content"] != nil || m["password_protected"] != true {
		t.Errorf("protected content: %v", m)
	}
	list := mustCall(t, e.client(e.token(auth.RoleAuthor)), "list_posts", map[string]any{"status": "any"})
	for _, x := range list["posts"].([]any) {
		if x.(map[string]any)["status"] != db.StatusPublish {
			t.Errorf("foreign unpublished post listed: %v", x)
		}
	}
}

func TestPublishedPostNeedsPublishScope(t *testing.T) {
	e := newEnv(t)
	tok := e.token(auth.RoleAuthor, auth.ScopeWrite, auth.ScopePublish)
	p := mustCall(t, e.client(tok), "create_post", map[string]any{"title": "Out now", "status": "publish"})
	if p["status"] != db.StatusPublish {
		t.Fatalf("status: %v", p["status"])
	}
	cs := e.client(e.token(auth.RoleAuthor, auth.ScopeWrite))
	got := mustCall(t, cs, "get_post", map[string]any{"id": p["id"]})
	if _, errText := tryCall(t, cs, "update_post", map[string]any{"id": p["id"], "expected_modified": got["modified"], "title": "x"}); !strings.Contains(errText, "content:publish") {
		t.Errorf("published post changed without the publish scope: %q", errText)
	}
	if _, errText := tryCall(t, cs, "create_category", map[string]any{"name": "x"}); !strings.Contains(errText, "create_category") && !strings.Contains(errText, "not found") {
		t.Errorf("tool without scope: %q", errText)
	}
}

func TestCategoriesAndMedia(t *testing.T) {
	e := newEnv(t)
	cs := e.client(e.token(auth.RoleEditor, auth.ScopeWrite, auth.ScopeMedia, auth.ScopeTerms))
	cat := mustCall(t, cs, "create_category", map[string]any{"name": "News <b>x</b>", "description": "<script>x</script>about"})
	if cat["name"] != "News x" || strings.Contains(cat["description"].(string), "script") {
		t.Errorf("category: %v", cat)
	}
	terms := mustCall(t, cs, "list_terms", map[string]any{"taxonomy": "category"})
	if n := len(terms["terms"].([]any)); n != 3 {
		t.Errorf("terms: %d", n)
	}

	m := mustCall(t, cs, "upload_media", map[string]any{"filename": "../../photo.jpg", "data": base64.StdEncoding.EncodeToString(photo(t)),
		"alt": `Screenshot "1"`})
	if m["mime"] != "image/jpeg" || !strings.Contains(m["html"].(string), `alt="Screenshot &#34;1&#34;"`) || !strings.Contains(m["url"].(string), home+"/wp-content/uploads/") {
		t.Errorf("upload: %v", m)
	}
	var leaked []string
	filepath.WalkDir(e.uploads, func(p string, de os.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte("GPSLatitude")) {
				leaked = append(leaked, p)
			}
		}
		return nil
	})
	if len(leaked) > 0 {
		t.Errorf("metadata kept in %v", leaked)
	}
	if _, errText := tryCall(t, cs, "upload_media", map[string]any{"filename": "x.zip", "data": "UEsDBA=="}); !strings.Contains(errText, "not allowed") {
		t.Errorf("zip upload: %q", errText)
	}
	if _, errText := tryCall(t, cs, "upload_media", map[string]any{"filename": "x.png", "data": base64.StdEncoding.EncodeToString([]byte("<html><script>"))}); errText == "" {
		t.Error("HTML uploaded as PNG")
	}
	if _, errText := tryCall(t, cs, "upload_media", map[string]any{"filename": "x.png", "data": "!!!"}); !strings.Contains(errText, "base64") {
		t.Errorf("bad base64: %q", errText)
	}
	media := mustCall(t, cs, "list_media", nil)
	if media["total"].(float64) != 2 { // the upload and the editor's own draft image
		t.Errorf("media: %v", media)
	}
}

func photo(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	img.Set(1, 1, color.White)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x00"), []byte("GPSLatitude 48.2082 N")...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	enc := buf.Bytes()
	return append(append(append([]byte{}, enc[:2]...), append(seg, payload...)...), enc[2:]...)
}

// raw sends a JSON-RPC request without the SDK client.
func (e *env) raw(method string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, home+"/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.RemoteAddr = "192.0.2.1:1234"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func TestGuards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.token(auth.RoleAuthor, auth.ScopeWrite)
	ok := map[string]string{"Authorization": "Bearer " + tok}
	if rec := e.raw("POST", ok, initBody); rec.Code != 200 || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("initialize: %d %s", rec.Code, rec.Body)
	}
	if rec := e.raw("GET", ok, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
	if rec := e.raw("POST", map[string]string{"Authorization": "Bearer " + tok, "Origin": "https://evil.example"}, initBody); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", rec.Code)
	}
	if rec := e.raw("POST", map[string]string{"Authorization": "Bearer " + tok, "Origin": home}, initBody); rec.Code != 200 {
		t.Errorf("own origin: %d", rec.Code)
	}
	if rec := e.raw("POST", map[string]string{"Authorization": "Bearer " + tok, "Sec-Fetch-Site": "cross-site"}, initBody); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site fetch: %d", rec.Code)
	}
	// A session cookie is no credential here.
	if rec := e.raw("POST", map[string]string{"Cookie": auth.SessionCookie + "=abc"}, initBody); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("cookie only: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	// Body size.
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"upload_media","arguments":{"filename":"a.png","data":"` +
		strings.Repeat("A", maxBodyBytes) + `"}}}`
	if rec := e.raw("POST", ok, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d", rec.Code)
	}

	// Revoked, expired and foreign tokens; then the failure limit.
	list, _ := db.AccessTokens(ctx, e.d.R, e.users[auth.RoleAuthor].ID)
	e.auth.RevokeAccessToken(ctx, nil, e.users[auth.RoleAuthor].ID, e.users[auth.RoleAuthor].ID, list[0].ID)
	if rec := e.raw("POST", ok, initBody); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", rec.Code)
	}
	for i := 0; i < authFailMax; i++ {
		e.raw("POST", map[string]string{"Authorization": "Bearer gpat_" + strings.Repeat("x", 43)}, initBody)
	}
	good := e.token(auth.RoleAuthor, auth.ScopeWrite)
	if rec := e.raw("POST", map[string]string{"Authorization": "Bearer " + good}, initBody); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: %d", authFailMax, rec.Code)
	}
	var n int
	e.d.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, auth.ActMCPAuthFailed).Scan(&n)
	if n == 0 || n > 3 {
		t.Errorf("auth failure audit entries: %d", n)
	}

	// Switched off: the path is an ordinary unknown URL.
	db.SetOption(ctx, e.d.W, "mcp_enabled", "0")
	e.srv.opts.Site.Invalidate()
	if rec := e.raw("POST", map[string]string{"Authorization": "Bearer " + good, "X-Forwarded-For": "198.51.100.1"}, initBody); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("disabled: %d", rec.Code)
	}
}

func TestCallRateLimit(t *testing.T) {
	e := newEnv(t)
	ok := map[string]string{"Authorization": "Bearer " + e.token(auth.RoleAuthor)}
	for i := 0; i < callsPerMinute; i++ {
		if rec := e.raw("POST", ok, initBody); rec.Code != 200 {
			t.Fatalf("call %d: %d", i, rec.Code)
		}
	}
	if rec := e.raw("POST", ok, initBody); rec.Code != http.StatusTooManyRequests {
		t.Errorf("call %d: %d", callsPerMinute+1, rec.Code)
	}
}

func TestSecureHome(t *testing.T) {
	for h, want := range map[string]bool{"https://example.org": true, "http://localhost:8080": true, "http://127.0.0.1": true,
		"http://[::1]:8080": true, "http://example.org": false, "http://192.168.1.2": false, "ftp://localhost": false} {
		if SecureHome(h) != want {
			t.Errorf("SecureHome(%q) = %v", h, !want)
		}
	}
}

// Regression tests for the M2c security review.
func TestReviewFindings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.token(auth.RoleAuthor, auth.ScopeWrite)

	// JSON-RPC batches would bypass the per-request rate limit.
	batch := `[` + strings.TrimSuffix(strings.Repeat(`{"jsonrpc":"2.0","id":1,"method":"tools/list"},`, 3), ",") + `]`
	if rec := e.raw("POST", map[string]string{"Authorization": "Bearer " + tok}, "\n "+batch); rec.Code != http.StatusBadRequest {
		t.Errorf("batch: %d", rec.Code)
	}

	// The text of others' password protected posts is not searchable.
	cs := e.client(tok)
	if l := mustCall(t, cs, "list_posts", map[string]any{"search": "members only"}); l["total"].(float64) != 0 {
		t.Errorf("protected text found: %v", l)
	}
	if l := mustCall(t, cs, "list_posts", map[string]any{"search": "Members"}); l["total"].(float64) != 1 {
		t.Errorf("title search: %v", l)
	}

	// Content size.
	if _, errText := tryCall(t, cs, "create_post", map[string]any{"title": "big", "content": strings.Repeat("a", maxContent+1)}); !strings.Contains(errText, "larger") {
		t.Errorf("oversized content: %q", errText)
	}

	// Parent pages and featured images must be visible to the user.
	ed := e.client(e.token(auth.RoleEditor, auth.ScopeWrite))
	if _, errText := tryCall(t, ed, "create_post", map[string]any{"type": "page", "title": "child", "parent": 50}); !strings.Contains(errText, "does not exist") {
		t.Errorf("private parent: %q", errText)
	}
	if _, errText := tryCall(t, cs, "create_post", map[string]any{"title": "x", "featured_media": 52}); !strings.Contains(errText, "does not exist") {
		t.Errorf("foreign draft's image as featured image: %q", errText)
	}

	// A title change keeps the stored content as it is.
	adm := e.client(e.token(auth.RoleAdministrator, auth.ScopeWrite))
	got := mustCall(t, adm, "get_post", map[string]any{"id": 51})
	up := mustCall(t, adm, "update_post", map[string]any{"id": 51, "expected_modified": got["modified"], "title": "Embed 2"})
	if !strings.Contains(up["content"].(string), "<iframe") {
		t.Errorf("kept content was filtered: %s", up["content"])
	}

	// Upload metadata needs content:write; refused before storing.
	mo := e.client(e.token(auth.RoleAuthor, auth.ScopeMedia))
	if _, errText := tryCall(t, mo, "upload_media", map[string]any{"filename": "a.jpg", "data": base64.StdEncoding.EncodeToString(photo(t)), "alt": "x"}); !strings.Contains(errText, "content:write") {
		t.Errorf("metadata without content:write: %q", errText)
	}
	var n int
	e.d.R.QueryRow(`SELECT COUNT(*) FROM posts WHERE type = 'attachment' AND author_id = ?`, e.users[auth.RoleAuthor].ID).Scan(&n)
	if n != 0 {
		t.Errorf("file stored although refused: %d", n)
	}
	mustCall(t, mo, "upload_media", map[string]any{"filename": "a.jpg", "data": base64.StdEncoding.EncodeToString(photo(t))})

	// The media list leaves out files of others' unpublished posts.
	if l := mustCall(t, mo, "list_media", nil); l["total"].(float64) != 1 {
		t.Errorf("media list: %v", l)
	}
	_ = ctx
}

func TestLimiterBounded(t *testing.T) {
	l := newLimiter(2, time.Minute)
	now := time.Now()
	for i := range maxKeys {
		l.add(strconv.Itoa(i), now)
	}
	if !l.full("new", now) || l.allow("new", now) {
		t.Error("new key accepted while full of live keys")
	}
	if l.full("1", now) || !l.allow("1", now) {
		t.Error("known key refused")
	}
	later := now.Add(2 * time.Minute) // all expired: pruned
	if l.full("new", later) || len(l.hits) > 1 {
		t.Errorf("not pruned: %d keys", len(l.hits))
	}
}
