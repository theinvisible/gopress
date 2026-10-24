package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

const testHome = "https://example.org"

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestService(t *testing.T) (*Service, *db.DB, *clock) {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s := New(d, Options{Secret: []byte("0123456789abcdef0123456789abcdef"), Home: func() string { return testHome },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	c := &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	s.now = c.now
	return s, d, c
}

func addUser(t *testing.T, d *db.DB, login, role, pw string) *db.User {
	t.Helper()
	h, err := HashPassword(context.Background(), pw)
	if err != nil {
		t.Fatal(err)
	}
	u := &db.User{Login: login, Nicename: login, Email: login + "@example.org", Role: role, PassHash: h,
		RegisteredGMT: db.Time{Time: time.Now()}}
	if err := db.InsertUser(context.Background(), d.W, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func loginRequest(ip string) *http.Request {
	r := httptest.NewRequest("POST", testHome+"/login", nil)
	r.RemoteAddr = net.JoinHostPort(ip, "1234")
	return r
}

func TestAuthenticateAndRehash(t *testing.T) {
	s, d, _ := newTestService(t)
	ctx := context.Background()
	// Imported WordPress user with a WP 6.8 hash of "password".
	u := &db.User{Login: "wpuser", Nicename: "wpuser", Email: "wp@example.org", Role: RoleAdministrator,
		PassHash: "$wp$2y$10$x9961j8Ytbmw9wzymZZPEui6ll65QrU6a.7PnsyfSLaK.n9KMOque", RegisteredGMT: db.Time{Time: time.Now()}}
	if err := db.InsertUser(ctx, d.W, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.Authenticate(ctx, loginRequest("192.0.2.1"), "WPUser", "password")
	if err != nil || got.ID != u.ID {
		t.Fatalf("Authenticate = %v, %v", got, err)
	}
	stored, _ := db.GetUser(ctx, d.R, u.ID)
	if !strings.HasPrefix(stored.PassHash, "$argon2id$") {
		t.Errorf("hash not upgraded: %s", stored.PassHash)
	}
	if _, err := s.Authenticate(ctx, loginRequest("192.0.2.1"), "wp@example.org", "password"); err != nil {
		t.Errorf("login by email after rehash: %v", err)
	}
}

func TestLockoutWithoutEnumeration(t *testing.T) {
	s, d, c := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "admin", RoleAdministrator, "correct horse battery staple")
	// Same behavior for an existing and a non-existing account.
	for j, login := range []string{"admin", "ghost"} {
		for i := range lockThreshold {
			if _, err := s.Authenticate(ctx, loginRequest(fmt.Sprintf("192.0.2.%d", 10+j)), login, "wrong"); !errors.Is(err, ErrLoginFailed) {
				t.Fatalf("%s attempt %d: %v", login, i, err)
			}
		}
	}
	// Locked for this network: even the right password fails ...
	if _, err := s.Authenticate(ctx, loginRequest("192.0.2.10"), "admin", "correct horse battery staple"); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("locked account accepted: %v", err)
	}
	// ... but the owner elsewhere is not locked out by the attacker.
	if _, err := s.Authenticate(ctx, loginRequest("198.51.100.1"), "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("other network locked out: %v", err)
	}
	// Attempts during the lock extend it (progressive); it ends eventually.
	c.add(lockMax + time.Minute)
	if _, err := s.Authenticate(ctx, loginRequest("192.0.2.10"), "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
	// Success resets the counters.
	for _, k := range []string{"u:1|192.0.2.10", "u:1"} {
		if f, _ := db.GetLoginFailure(ctx, d.R, k); f.Count != 0 {
			t.Errorf("%s after success = %+v", k, f)
		}
	}
	// The audit log never contains what was typed for unknown accounts.
	entries, _ := db.AuditLog(ctx, d.R, 0, 100, 0)
	for _, e := range entries {
		if strings.Contains(e.Detail, "ghost") {
			t.Errorf("audit log contains typed login: %s", e.Detail)
		}
	}
	_ = u
}

func TestLockoutIsAtomic(t *testing.T) {
	s, d, _ := newTestService(t)
	ctx := context.Background()
	addUser(t, d, "admin", RoleAdministrator, "correct horse battery staple")
	// Parallel guesses from many networks: every one counts.
	const n = acctThreshold + 10
	var wg sync.WaitGroup
	var verified atomic.Int32
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip := fmt.Sprintf("10.%d.%d.1", i/200, i%200)
			if _, err := s.Authenticate(ctx, loginRequest(ip), "admin", "wrong"); errors.Is(err, ErrLoginFailed) {
				verified.Add(1)
			}
		}()
	}
	wg.Wait()
	f, _ := db.GetLoginFailure(ctx, d.R, "u:1")
	if f.Count != n || f.LockedUntil == 0 {
		t.Errorf("account counter after %d parallel failures = %+v", n, f)
	}
	// Locked now for untrusted browsers ...
	if _, err := s.Authenticate(ctx, loginRequest("203.0.113.9"), "admin", "correct horse battery staple"); !errors.Is(err, ErrLoginFailed) {
		t.Error("distributed guessing did not lock the account")
	}
	// ... but not for a browser with the owner's device cookie.
	w := httptest.NewRecorder()
	s.IssueDeviceCookie(w, 1)
	r := loginRequest("203.0.113.10")
	r.AddCookie(w.Result().Cookies()[0])
	if _, err := s.Authenticate(ctx, r, "admin", "correct horse battery staple"); err != nil {
		t.Errorf("device cookie did not exempt the owner: %v", err)
	}
	// A forged device cookie does not.
	r = loginRequest("203.0.113.11")
	r.AddCookie(&http.Cookie{Name: DeviceCookie, Value: "1.AAAA.AAAA"})
	if _, err := s.Authenticate(ctx, r, "admin", "correct horse battery staple"); err != nil && !errors.Is(err, ErrLoginFailed) {
		t.Fatal(err)
	}
}

func TestLockoutDoubles(t *testing.T) {
	s, d, c := newTestService(t)
	ctx := context.Background()
	addUser(t, d, "admin", RoleAdministrator, "correct horse battery staple")
	// Each lock doubles the next one; after a lock ends there are new tries.
	for cycle, want := range []time.Duration{lockBase, 2 * lockBase, 4 * lockBase} {
		for range lockThreshold {
			s.ipLimit = newIPLimiter(loginIPMax, loginIPWindow)
			s.Authenticate(ctx, loginRequest("192.0.2.1"), "admin", "wrong")
		}
		f, _ := db.GetLoginFailure(ctx, d.R, "u:1|192.0.2.1")
		if got := time.Duration(f.LockedUntil-c.now().Unix()) * time.Second; got != want {
			t.Errorf("cycle %d: lock = %v, want %v", cycle, got, want)
		}
		c.add(want + time.Second)
	}
}

func TestIPRateLimit(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	for i := range loginIPMax {
		s.Authenticate(ctx, loginRequest("2001:db8::1"), "user"+string(rune('a'+i)), "x")
	}
	// Another address in the same /64 is limited too.
	if !s.ipLimit.allow("unrelated", s.now()) || s.ipLimit.allow("2001:db8::/64", s.now()) {
		t.Error("IPv6 /64 not limited")
	}
	// A /48 has a budget too (an attacker with many /64s).
	for i := range loginNetMax {
		s.Authenticate(ctx, loginRequest(fmt.Sprintf("2001:db8:1:%x::1", i)), "x", "x")
	}
	if _, err := s.Authenticate(ctx, loginRequest("2001:db8:1:ffff::1"), "x", "x"); !errors.Is(err, ErrLoginFailed) {
		t.Fatal(err)
	}
	if s.netLimit.allow("2001:db8:1::/48", s.now()) {
		t.Error("IPv6 /48 not limited")
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, d, c := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "editor", RoleEditor, "correct horse battery staple")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", testHome+"/login", nil)
	cur, err := s.StartSession(ctx, w, r, u, db.SessionFull, false)
	if err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != SessionCookie || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 0 {
		t.Errorf("cookie = %+v", cookie)
	}
	req := func() *http.Request {
		r := httptest.NewRequest("GET", testHome+"/admin/", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie.Value})
		return r
	}
	got, err := s.Load(ctx, req())
	if err != nil || got == nil || got.User.ID != u.ID || got.CSRF != cur.CSRF {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	// Idle timeout.
	c.add(sessionIdle + time.Second)
	if got, _ := s.Load(ctx, req()); got != nil {
		t.Error("idle session still valid")
	}

	// Absolute timeout despite activity.
	w = httptest.NewRecorder()
	s.StartSession(ctx, w, r, u, db.SessionFull, false)
	cookie = w.Result().Cookies()[0]
	for range 6 {
		c.add(sessionIdle - time.Minute)
		if got, _ := s.Load(ctx, req()); got == nil {
			t.Fatal("active session expired early")
		}
	}
	c.add(time.Hour)
	if got, _ := s.Load(ctx, req()); got != nil {
		t.Error("session valid after absolute timeout")
	}

	// A new login replaces the session the request carried (fixation).
	w = httptest.NewRecorder()
	s.StartSession(ctx, w, r, u, db.SessionFull, true)
	old := w.Result().Cookies()[0]
	if old.MaxAge != int(rememberAbs/time.Second) {
		t.Errorf("remember cookie MaxAge = %d", old.MaxAge)
	}
	r2 := httptest.NewRequest("POST", testHome+"/login", nil)
	r2.AddCookie(&http.Cookie{Name: SessionCookie, Value: old.Value})
	w = httptest.NewRecorder()
	s.StartSession(ctx, w, r2, u, db.SessionFull, false)
	if got, _ := s.Load(ctx, r2); got != nil {
		t.Error("old session survived a new login")
	}
}

func TestCSRF(t *testing.T) {
	s, d, _ := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "admin", RoleAdministrator, "correct horse battery staple")
	cur, _ := s.StartSession(ctx, httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), u, db.SessionFull, false)
	other, _ := s.StartSession(ctx, httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), u, db.SessionFull, false)
	post := func(token string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("POST", testHome+"/admin/x", strings.NewReader(url.Values{"_csrf": {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	same := map[string]string{"Origin": testHome, "Sec-Fetch-Site": "same-origin"}
	if !s.CheckCSRF(post(cur.CSRF, same), cur) {
		t.Error("valid token rejected")
	}
	for name, r := range map[string]*http.Request{
		"missing token":  post("", same),
		"other session":  post(other.CSRF, same),
		"cross-site":     post(cur.CSRF, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}),
		"same-site sub":  post(cur.CSRF, map[string]string{"Origin": "https://sub.example.org", "Sec-Fetch-Site": "same-site"}),
		"foreign origin": post(cur.CSRF, map[string]string{"Origin": "https://evil.example"}),
	} {
		if s.CheckCSRF(r, cur) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestLoginFormToken(t *testing.T) {
	s, _, _ := newTestService(t)
	w := httptest.NewRecorder()
	tok := s.LoginFormToken(w, httptest.NewRequest("GET", testHome+"/login", nil))
	pre := w.Result().Cookies()[0]
	r := httptest.NewRequest("POST", testHome+"/login", strings.NewReader("_csrf="+tok))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(pre)
	if !s.CheckLoginForm(r) {
		t.Error("valid login form rejected")
	}
	r = httptest.NewRequest("POST", testHome+"/login", strings.NewReader("_csrf="+tok))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if s.CheckLoginForm(r) {
		t.Error("login form without pre-session cookie accepted")
	}
}

func TestCapabilities(t *testing.T) {
	admin := &db.User{ID: 1, Role: RoleAdministrator}
	editor := &db.User{ID: 2, Role: RoleEditor}
	author := &db.User{ID: 3, Role: RoleAuthor}
	contrib := &db.User{ID: 4, Role: RoleContributor}
	sub := &db.User{ID: 5, Role: RoleSubscriber}
	own := func(u *db.User, status string) *db.Post {
		return &db.Post{Type: db.TypePost, AuthorID: u.ID, Status: status}
	}
	others := &db.Post{Type: db.TypePost, AuthorID: 1, Status: db.StatusDraft}
	page := &db.Post{Type: db.TypePage, AuthorID: 3, Status: db.StatusDraft}
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"admin edits others", CanEditPost(admin, own(editor, db.StatusPublish)), true},
		{"editor edits others", CanEditPost(editor, others), true},
		{"author edits own published", CanEditPost(author, own(author, db.StatusPublish)), true},
		{"author edits others", CanEditPost(author, others), false},
		{"author edits pages", CanEditPost(author, page), false},
		{"contributor edits own draft", CanEditPost(contrib, own(contrib, db.StatusDraft)), true},
		{"contributor edits own published", CanEditPost(contrib, own(contrib, db.StatusPublish)), false},
		{"contributor publishes", CanPublishType(contrib, db.TypePost), false},
		{"author publishes", CanPublishType(author, db.TypePost), true},
		{"subscriber edits posts", CanEditType(sub, db.TypePost), false},
		{"author unfiltered html", Can(author, CapUnfilteredHTML), false},
		{"editor unfiltered html", Can(editor, CapUnfilteredHTML), true},
		{"editor manage options", Can(editor, CapManageOptions), false},
		{"editor moderates", Can(editor, CapModerateComments), true},
		{"author moderates", Can(author, CapModerateComments), false},
		{"contributor deletes own published", CanDeletePost(contrib, own(contrib, db.StatusPublish)), false},
		{"unknown role", Can(&db.User{Role: "bogus"}, CapRead), false},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}
