package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

// browser is a minimal cookie-keeping client for the test handler.
type browser struct {
	e   *testEnv
	jar map[string]string
}

func (e *testEnv) browser() *browser { return &browser{e: e, jar: map[string]string{}} }

func (b *browser) req(method, target string, form url.Values) *http.Response {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, home+target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", home)
	}
	for k, v := range b.jar {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	b.e.h.ServeHTTP(rec, r)
	res := rec.Result()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(b.jar, c.Name)
		} else {
			b.jar[c.Name] = c.Value
		}
	}
	return res
}

var reCSRF = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// form loads page and posts form to action with the page's form token.
func (b *browser) form(page, action string, form url.Values) *http.Response {
	b.e.t.Helper()
	res := b.req("GET", page, nil)
	body, _ := io.ReadAll(res.Body)
	m := reCSRF.FindSubmatch(body)
	if m == nil {
		b.e.t.Fatalf("no form token on %s (%d)", page, res.StatusCode)
	}
	form.Set("_csrf", string(m[1]))
	return b.req("POST", action, form)
}

var reLink = regexp.MustCompile(`https://example\.org(/login/(?:reset|invite)/[A-Za-z0-9_-]{43})`)

func TestLostPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.users[auth.RoleEditor]
	b := e.browser()
	// Unknown and known accounts get the same answer.
	for _, login := range []string{"nobody", "editor@example.org"} {
		res := b.form("/login/lost", "/login/lost", url.Values{"user_login": {login}})
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login?msg=lost" {
			t.Errorf("%s: %d %s", login, res.StatusCode, res.Header.Get("Location"))
		}
	}
	mails := e.mails()
	if len(mails) != 1 || len(mails["editor@example.org"]) != 1 {
		t.Fatalf("mails = %v", mails)
	}
	// A second request right after does not send another link.
	b.form("/login/lost", "/login/lost", url.Values{"user_login": {"editor"}})
	if n := len(e.mails()["editor@example.org"]); n != 1 {
		t.Errorf("reset mails = %d, want 1", n)
	}
	link := reLink.FindStringSubmatch(mails["editor@example.org"][0])
	if link == nil {
		t.Fatalf("no link in %q", mails["editor@example.org"][0])
	}
	// An existing session ends with the reset.
	old := e.session(auth.RoleEditor, db.SessionFull)

	// The link moves the token into a cookie.
	res := b.req("GET", link[1], nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login/reset" || b.jar[linkCookie] == "" {
		t.Fatalf("landing: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res := b.form("/login/reset", "/login/reset", url.Values{"pass1": {"short"}, "pass2": {"short"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("weak password: %d", res.StatusCode)
	}
	// No form token: refused.
	if res := b.req("POST", "/login/reset", url.Values{"pass1": {"a new long passphrase"}, "pass2": {"a new long passphrase"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("without form token: %d", res.StatusCode)
	}
	res = b.form("/login/reset", "/login/reset", url.Values{"pass1": {"a new long passphrase"}, "pass2": {"a new long passphrase"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login?msg=reset" {
		t.Fatalf("reset: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	got, _ := db.GetUser(ctx, e.d.R, u.ID)
	if ok, _ := auth.VerifyPassword(ctx, "a new long passphrase", got.PassHash); !ok {
		t.Error("password not changed")
	}
	if c, _ := e.auth.Load(ctx, withCookie(old)); c != nil {
		t.Error("old session survived the reset")
	}
	if m := e.mails()["editor@example.org"]; len(m) != 2 || !strings.Contains(m[1], "Password reset") {
		t.Errorf("no security notice: %v", m)
	}
	// The link works once.
	b.req("GET", link[1], nil)
	if res := b.req("GET", "/login/reset", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("used link: %d", res.StatusCode)
	}
	// The reset does not skip the second factor: the next login still
	// has to set one up with a setup code.
	res = b.form("/login", "/login", url.Values{"log": {"editor"}, "pwd": {"a new long passphrase"}})
	if res.Header.Get("Location") != "/admin/setup-2fa" {
		t.Fatalf("login after reset: %s", res.Header.Get("Location"))
	}
	if c, _ := e.auth.Load(ctx, cookieReq(b.jar[auth.SessionCookie])); c == nil || c.Session.State != db.SessionEnroll {
		t.Errorf("session after reset login: %+v", c)
	}
}

func TestResetTokenRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.users[auth.RoleAuthor]
	tok, _ := e.auth.NewUserToken(ctx, u, auth.TokenReset, auth.ResetTTL, "")
	if _, _, err := e.auth.CheckUserToken(ctx, tok, auth.TokenInvite); err == nil {
		t.Error("reset token accepted as invitation")
	}
	// A password change invalidates the link.
	db.SetUserPassHash(ctx, e.d.W, u.ID, "$argon2id$v=19$m=8,t=1,p=1$b3RoZXJzYWx0$b3RoZXJoYXNo")
	if _, _, err := e.auth.CheckUserToken(ctx, tok, auth.TokenReset); err == nil {
		t.Error("link valid after a password change")
	}
	// A newer link replaces the older one.
	u, _ = db.GetUser(ctx, e.d.R, u.ID)
	t1, _ := e.auth.NewUserToken(ctx, u, auth.TokenReset, auth.ResetTTL, "")
	t2, _ := e.auth.NewUserToken(ctx, u, auth.TokenReset, auth.ResetTTL, "")
	if _, _, err := e.auth.CheckUserToken(ctx, t1, auth.TokenReset); err == nil {
		t.Error("older link still valid")
	}
	if _, _, err := e.auth.CheckUserToken(ctx, t2, auth.TokenReset); err != nil {
		t.Error("newest link invalid")
	}
	// Expiry.
	t3, _ := e.auth.NewUserToken(ctx, u, auth.TokenInvite, -time.Minute, "")
	if _, _, err := e.auth.CheckUserToken(ctx, t3, auth.TokenInvite); err == nil {
		t.Error("expired link accepted")
	}
}

func TestInvitation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.users[auth.RoleContributor]
	tok, _ := e.auth.NewUserToken(ctx, u, auth.TokenInvite, auth.InviteTTL, "")
	b := e.browser()
	b.req("GET", "/login/invite/"+tok, nil)
	res := b.form("/login/invite", "/login/invite", url.Values{"pass1": {"contributor passphrase"}, "pass2": {"contributor passphrase"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/setup-2fa" {
		t.Fatalf("invite: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// The invitation stands in for the setup code; nothing else is open.
	c, _ := e.auth.Load(ctx, cookieReq(b.jar[auth.SessionCookie]))
	if c == nil || c.Session.State != db.SessionSetup {
		t.Fatalf("session = %+v", c)
	}
	if res := b.req("GET", "/admin/", nil); res.Header.Get("Location") != "/admin/setup-2fa" {
		t.Errorf("dashboard before 2FA: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// An invitation link cannot reset a password via /login/reset.
	tok2, _ := e.auth.NewUserToken(ctx, u, auth.TokenInvite, auth.InviteTTL, "")
	b2 := e.browser()
	b2.req("GET", "/login/reset/"+tok2, nil)
	if res := b2.req("GET", "/login/reset", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("invitation used as reset link: %d", res.StatusCode)
	}
}

func withCookie(cur *auth.Current) *http.Request { return cookieReq(cur.Session.UserAgent) }

func cookieReq(v string) *http.Request {
	r := httptest.NewRequest("GET", home+"/admin/", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: v})
	return r
}

// Review fix: a password reset by email link keeps the second-factor
// lock (control of the mailbox is not a second factor).
func TestResetKeepsSecondFactorLock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.users[auth.RoleEditor]
	key := "mfa:" + strconv.FormatInt(u.ID, 10)
	db.SetLoginFailure(ctx, e.d.W, key, db.LoginFailure{Count: 10, LockedUntil: time.Now().Add(time.Hour).Unix(), Last: time.Now().Unix()})
	tok, _ := e.auth.NewUserToken(ctx, u, auth.TokenReset, auth.ResetTTL, "")
	b := e.browser()
	b.req("GET", "/login/reset/"+tok, nil)
	if res := b.form("/login/reset", "/login/reset", url.Values{"pass1": {"another long passphrase"}, "pass2": {"another long passphrase"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset: %d", res.StatusCode)
	}
	if f, _ := db.GetLoginFailure(ctx, e.d.R, key); f.LockedUntil == 0 {
		t.Error("second-factor lock lifted by the reset")
	}
}
