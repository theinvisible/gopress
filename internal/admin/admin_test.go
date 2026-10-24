package admin

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
	"github.com/theinvisible/gopress/internal/media"
	"github.com/theinvisible/gopress/internal/site"
	"github.com/theinvisible/gopress/internal/theme"
	"github.com/theinvisible/gopress/themes"
)

const home = "https://example.org"

type testEnv struct {
	t       *testing.T
	d       *db.DB
	auth    *auth.Service
	h       http.Handler
	users   map[string]*db.User
	mail    *mail.Service
	mailDir string
	pub     *site.Site
	cs      *content.Service
	uploads string
}

// mails delivers the queued mail and returns the messages sent so far
// (to → raw messages).
func (e *testEnv) mails() map[string][]string {
	e.t.Helper()
	e.mail.Flush(context.Background())
	files, _ := filepath.Glob(filepath.Join(e.mailDir, "*.eml"))
	out := map[string][]string{}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		m, err := netmail.ReadMessage(bytes.NewReader(b))
		if err != nil {
			e.t.Fatal(err)
		}
		body, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
		to := strings.Trim(m.Header.Get("To"), "<>")
		out[to] = append(out[to], m.Header.Get("Subject")+"\n"+string(body))
	}
	return out
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for k, v := range map[string]any{"blogname": "Test", "home": home, "permalink_structure": "/%postname%/",
		"timezone_string": "UTC", "default_category": "1"} {
		db.SetOption(ctx, d.W, k, v)
	}
	db.InsertTerm(ctx, d.W, &db.Term{ID: 1, Taxonomy: db.TaxCategory, Slug: "uncategorized", Name: "Uncategorized"})
	e := &testEnv{t: t, d: d, users: map[string]*db.User{}}
	for _, role := range []string{auth.RoleAdministrator, auth.RoleEditor, auth.RoleAuthor, auth.RoleContributor, auth.RoleSubscriber} {
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
	db.InsertPost(ctx, d.W, &db.Post{ID: 30, Type: db.TypePost, Status: db.StatusDraft, Slug: "", AuthorID: e.users["author"].ID,
		Title: "Secret draft", Content: "<p>draft text</p>", Date: now, Modified: now, ModifiedGMT: now})
	th, err := theme.Load(themes.FS, "twentytwelve")
	if err != nil {
		t.Fatal(err)
	}
	logOut := io.Discard
	if os.Getenv("GP_TEST_LOG") != "" { // GP_TEST_LOG=1 go test … shows the server log
		logOut = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	e.uploads = t.TempDir()
	pub := site.New(d, th, site.Options{BaseURL: home, Secret: []byte("k"), Log: log, UploadsDir: e.uploads})
	e.mailDir = t.TempDir()
	e.mail = mail.New(d, mail.Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Home: func() string { return home },
		Sink: e.mailDir, Log: log})
	e.auth = auth.New(d, auth.Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Home: func() string { return home }, Log: log,
		Notify: func(ctx context.Context, to string, data map[string]any) { e.mail.Enqueue(ctx, to, "security", data) }})
	cs := content.New(d, pub, e.auth, log)
	cs.SetMailer(e.mail)
	store, err := media.Open(e.uploads)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cs.SetMediaStore(store)
	pub.SetCommentNotifier(cs.NotifyComment)
	e.pub, e.cs = pub, cs
	ad, err := New(d, e.auth, pub, cs, Options{Log: log, Mail: e.mail, MailSink: true})
	if err != nil {
		t.Fatal(err)
	}
	ad.sync = true
	pub.SetPreviewer(ad.Previewer())
	mux := http.NewServeMux()
	mux.Handle("/", pub)
	ad.Register(mux)
	e.h = site.SecurityHeaders(mux)
	return e
}

// session logs a user in with the given session state.
func (e *testEnv) session(role, state string) *auth.Current {
	rec := httptest.NewRecorder()
	cur, err := e.auth.StartSession(context.Background(), rec, httptest.NewRequest("GET", home+"/login", nil), e.users[role], state, false)
	if err != nil {
		e.t.Fatal(err)
	}
	cur.Session.UserAgent = rec.Result().Cookies()[0].Value // keep the token at hand
	return cur
}

func (e *testEnv) do(method, target string, cur *auth.Current, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, home+target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cur != nil {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cur.Session.UserAgent})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

var adminPages = []string{"/admin/", "/admin/posts/", "/admin/posts/new", "/admin/posts/10/edit", "/admin/pages/",
	"/admin/pages/new", "/admin/comments/", "/admin/profile", "/admin/profile/security", "/admin/settings/email"}

func TestRequiresLogin(t *testing.T) {
	e := newEnv(t)
	for _, p := range adminPages {
		rec := e.do("GET", p, nil, nil, nil)
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login?redirect_to=") {
			t.Errorf("GET %s without login: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := e.do("POST", "/admin/quick-draft", nil, url.Values{"post_title": {"x"}}, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST without login: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/api/media", nil, nil, map[string]string{"Accept": "application/json"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("API without login: %d", rec.Code)
	}
}

func TestTwoFactorGate(t *testing.T) {
	e := newEnv(t)
	enroll := e.session(auth.RoleAdministrator, db.SessionEnroll)
	for _, p := range append(adminPages, "/admin/api/media") {
		if p == "/admin/profile/security" || p == "/admin/profile" {
			continue
		}
		rec := e.do("GET", p, enroll, nil, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/setup-2fa" {
			t.Errorf("GET %s while enrolling: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := e.do("GET", "/admin/setup-2fa", enroll, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("setup page while enrolling: %d", rec.Code)
	}
	mfa := e.session(auth.RoleAdministrator, db.SessionMFA)
	for _, p := range []string{"/admin/", "/admin/setup-2fa", "/admin/profile"} {
		rec := e.do("GET", p, mfa, nil, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login/2fa" {
			t.Errorf("GET %s with pending second factor: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	// The pending login cannot be used for previews either.
	if rec := e.do("GET", "/?p=30&preview=true", mfa, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("preview with pending login: %d", rec.Code)
	}
}

func TestCapabilities(t *testing.T) {
	e := newEnv(t)
	type c struct {
		role, path string
		want       int
	}
	for _, tc := range []c{
		{auth.RoleAdministrator, "/admin/comments/", 200},
		{auth.RoleEditor, "/admin/pages/", 200},
		{auth.RoleAuthor, "/admin/posts/", 200},
		{auth.RoleAuthor, "/admin/pages/", 403},
		{auth.RoleAuthor, "/admin/comments/", 403},
		{auth.RoleAuthor, "/admin/posts/10/edit", 403}, // someone else's published post
		{auth.RoleAuthor, "/admin/posts/30/edit", 200}, // own draft
		{auth.RoleContributor, "/admin/posts/30/edit", 403},
		{auth.RoleSubscriber, "/admin/posts/", 403},
		{auth.RoleSubscriber, "/admin/profile", 200},
		{auth.RoleSubscriber, "/admin/api/media", 403},
	} {
		cur := e.session(tc.role, db.SessionFull)
		if rec := e.do("GET", tc.path, cur, nil, nil); rec.Code != tc.want {
			t.Errorf("%s GET %s = %d, want %d", tc.role, tc.path, rec.Code, tc.want)
		}
	}
	// Writes are checked as well, not only pages.
	contrib := e.session(auth.RoleContributor, db.SessionFull)
	rec := e.do("POST", "/admin/posts/10/trash", contrib, url.Values{"_csrf": {contrib.CSRF}}, map[string]string{"Origin": home})
	if rec.Code != http.StatusForbidden {
		t.Errorf("contributor trashing a post: %d", rec.Code)
	}
	if p, _ := db.GetPost(context.Background(), e.d.R, 10); p.Status != db.StatusPublish {
		t.Error("post was trashed")
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	cur := e.session(auth.RoleAdministrator, db.SessionFull)
	form := func(tok string) url.Values {
		return url.Values{"post_title": {"Draft"}, "content": {"x"}, "_csrf": {tok}}
	}
	for name, tc := range map[string]struct {
		tok string
		hdr map[string]string
	}{
		"no token":   {"", map[string]string{"Origin": home}},
		"bad token":  {"x" + cur.CSRF, map[string]string{"Origin": home}},
		"cross-site": {cur.CSRF, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}},
	} {
		if rec := e.do("POST", "/admin/quick-draft", cur, form(tc.tok), tc.hdr); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	rec := e.do("POST", "/admin/quick-draft", cur, form(cur.CSRF), map[string]string{"Origin": home, "Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("valid request: %d %s", rec.Code, rec.Body.String())
	}
	// Logout is a POST with token too (no logout CSRF).
	if rec := e.do("POST", "/logout", cur, url.Values{}, map[string]string{"Origin": home}); rec.Code != http.StatusForbidden {
		t.Errorf("logout without token: %d", rec.Code)
	}
}

func TestLoginEndpoints(t *testing.T) {
	e := newEnv(t)
	// Bots posting to the WordPress login get nothing.
	if rec := e.do("POST", "/wp-login.php", nil, url.Values{"log": {"admin"}, "pwd": {"x"}}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("POST /wp-login.php: %d", rec.Code)
	}
	if rec := e.do("GET", "/wp-admin/edit.php", nil, nil, nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("GET /wp-admin/edit.php: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// No open redirects.
	rec := e.do("GET", "/login?redirect_to="+url.QueryEscape("https://evil.example/"), nil, nil, nil)
	if !strings.Contains(rec.Body.String(), `name="redirect_to" value="/admin/"`) {
		t.Error("redirect_to not sanitized")
	}
	for _, h := range []string{"//evil.example", "/admin/../\\evil", "https://example.org/admin/"} {
		if got := safeRedirect(h); got != "/admin/" && !strings.HasPrefix(got, "/admin/") || strings.HasPrefix(got, "//") {
			t.Errorf("safeRedirect(%q) = %q", h, got)
		}
	}
	// Headers of login and admin pages.
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("login headers: %v", h)
	}
	// A login form post without the pre-session cookie (login CSRF) fails.
	rec = e.do("POST", "/login", nil, url.Values{"log": {"administrator"}, "pwd": {"x"}, "_csrf": {"forged"}}, map[string]string{"Origin": home})
	if rec.Code != http.StatusForbidden {
		t.Errorf("login without form token: %d", rec.Code)
	}
}

func TestPreview(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/?p=30&preview=true", nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("anonymous preview: %d", rec.Code)
	}
	// The draft's author and editors may preview it, a contributor may not.
	for role, want := range map[string]int{auth.RoleAuthor: 200, auth.RoleEditor: 200, auth.RoleContributor: 404} {
		rec := e.do("GET", "/?p=30&preview=true", e.session(role, db.SessionFull), nil, nil)
		if rec.Code != want {
			t.Errorf("%s preview: %d, want %d", role, rec.Code, want)
		}
		if want == 200 && (rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "draft text")) {
			t.Errorf("%s preview: cache %q", role, rec.Header().Get("Cache-Control"))
		}
	}
	// Previews never leak into the page cache.
	if rec := e.do("GET", "/?p=30&preview=true", nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("anonymous preview after an authorized one: %d", rec.Code)
	}
}

func TestSetupCodeGate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	enroll := e.session(auth.RoleAdministrator, db.SessionEnroll)
	same := map[string]string{"Origin": home}
	// Before the setup code: no factor can be registered.
	if rec := e.do("GET", "/admin/setup-2fa", enroll, nil, nil); !strings.Contains(rec.Body.String(), `name="setup_code"`) {
		t.Fatal("setup page does not ask for the setup code")
	}
	for _, p := range []string{"/admin/setup-2fa/totp", "/admin/setup-2fa/passkey/begin"} {
		rec := e.do("POST", p, enroll, url.Values{"_csrf": {enroll.CSRF}}, same)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/setup-2fa" {
			t.Errorf("POST %s before the setup code: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := e.do("POST", "/admin/setup-2fa/code", enroll, url.Values{"_csrf": {enroll.CSRF}, "setup_code": {"AAAA-BBBB-CCCC"}}, same)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("wrong setup code: %d", rec.Code)
	}
	code, _ := e.auth.NewSetupCode(ctx, e.users["administrator"].ID)
	rec = e.do("POST", "/admin/setup-2fa/code", enroll, url.Values{"_csrf": {enroll.CSRF}, "setup_code": {code}}, same)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid setup code: %d %s", rec.Code, rec.Body.String())
	}
	var setupCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			setupCookie = c.Value
		}
	}
	setup := &auth.Current{Session: &db.Session{UserAgent: setupCookie}}
	if rec := e.do("GET", "/admin/setup-2fa", setup, nil, nil); !strings.Contains(rec.Body.String(), `action="/admin/setup-2fa/totp"`) {
		t.Error("enrollment not offered after the setup code")
	}
	// Another factor appears (the owner enrolled elsewhere): older
	// enrollment sessions cannot register one more.
	db.SetTOTP(ctx, e.d.W, e.users["administrator"].ID, []byte("x"), 0, 1)
	rec = e.do("GET", "/admin/setup-2fa", setup, nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?msg=expired" {
		t.Errorf("stale enrollment session: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestContentPermissionFixes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	same := map[string]string{"Origin": home}
	// Contributors cannot duplicate (and so read) other users' posts.
	contrib := e.session(auth.RoleContributor, db.SessionFull)
	if rec := e.do("POST", "/admin/posts/10/duplicate", contrib, url.Values{"_csrf": {contrib.CSRF}}, same); rec.Code != http.StatusForbidden {
		t.Errorf("contributor duplicate: %d", rec.Code)
	}
	// Contributors have no media access (upload_files).
	if rec := e.do("GET", "/admin/api/media", contrib, nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("contributor media API: %d", rec.Code)
	}
	// Authors cannot make posts sticky.
	author := e.session(auth.RoleAuthor, db.SessionFull)
	rec := e.do("POST", "/admin/posts/30/edit", author, url.Values{"_csrf": {author.CSRF}, "post_title": {"x"}, "content": {"<p>y</p>"},
		"sticky": {"1"}, "publish": {"1"}, "visibility": {"public"}}, same)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("author publish: %d %s", rec.Code, rec.Body.String())
	}
	var sticky []int64
	db.GetOption(ctx, e.d.R, "sticky_posts", &sticky)
	if len(sticky) != 0 {
		t.Errorf("author made a post sticky: %v", sticky)
	}
	// Trashed posts cannot be changed by quick edit.
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	e.do("POST", "/admin/posts/30/trash", admin, url.Values{"_csrf": {admin.CSRF}}, same)
	rec = e.do("POST", "/admin/posts/30/quick-edit", admin, url.Values{"_csrf": {admin.CSRF}, "title": {"x"}, "status": {"publish"}}, same)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("quick edit of a trashed post: %d", rec.Code)
	}
	if p, _ := db.GetPost(ctx, e.d.R, 30); p.Status != db.StatusTrash {
		t.Errorf("trashed post status changed to %s", p.Status)
	}
}

func TestAdminScriptCSP(t *testing.T) {
	e := newEnv(t)
	csp := e.do("GET", "/login", nil, nil, nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src "+home+"/admin/static/;") || strings.Contains(csp, "script-src 'self'") {
		t.Errorf("admin CSP = %s", csp)
	}
}
