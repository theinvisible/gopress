package admin

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

var reNewToken = regexp.MustCompile(`gpat_[A-Za-z0-9_-]{43}`)

func (e *testEnv) enableMCP() {
	db.SetOption(context.Background(), e.d.W, "mcp_enabled", "1")
	e.pub.Invalidate()
}

func TestMCPSetting(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	db.SetSessionReauth(ctx, e.d.W, admin.Session.IDHash, 0)
	form := url.Values{"default_category": {"1"}, "mcp_enabled": {"1"}}
	if res := post(e, "/admin/settings/writing", admin, form); !strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Fatalf("enabling without reauth: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if st, _ := e.pub.Settings(ctx); st.MCPEnabled {
		t.Fatal("enabled without reauth")
	}
	e.auth.MarkReauth(ctx, admin)
	if res := post(e, "/admin/settings/writing", admin, form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("enable: %d", res.StatusCode)
	}
	if st, _ := e.pub.Settings(ctx); !st.MCPEnabled {
		t.Fatal("not enabled")
	}
	// Turning it off needs no re-authentication.
	db.SetSessionReauth(ctx, e.d.W, admin.Session.IDHash, 0)
	if res := post(e, "/admin/settings/writing", admin, url.Values{"default_category": {"1"}}); res.StatusCode != http.StatusSeeOther ||
		strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Fatalf("disable: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if st, _ := e.pub.Settings(ctx); st.MCPEnabled {
		t.Fatal("not disabled")
	}
}

func TestTokenPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	author := e.session(auth.RoleAuthor, db.SessionFull)
	create := url.Values{"name": {"Release bot"}, "days": {"30"}, "scopes": {auth.ScopeWrite, auth.ScopeMedia}}

	// Subscribers have no token page; while the interface is off nothing
	// can be created.
	if rec := e.do("GET", "/admin/profile/tokens", e.session(auth.RoleSubscriber, db.SessionFull), nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("subscriber: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/profile/tokens", author, nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "turned off") {
		t.Fatalf("page while off: %d", rec.Code)
	}
	if res := post(e, "/admin/profile/tokens", author, create); res.StatusCode != http.StatusForbidden {
		t.Fatalf("create while off: %d", res.StatusCode)
	}
	e.enableMCP()

	// CSRF and re-authentication.
	noCSRF := url.Values{"name": {"x"}, "days": {"30"}, "scopes": {auth.ScopeWrite}} // post() adds the token to create
	if rec := e.do("POST", "/admin/profile/tokens", author, noCSRF, map[string]string{"Origin": home}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without CSRF: %d", rec.Code)
	}
	db.SetSessionReauth(ctx, e.d.W, author.Session.IDHash, 0)
	if res := post(e, "/admin/profile/tokens", author, create); !strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Fatalf("create without reauth: %s", res.Header.Get("Location"))
	}
	e.auth.MarkReauth(ctx, author)

	// A scope beyond the role is refused by the server.
	contrib := e.session(auth.RoleContributor, db.SessionFull)
	res := post(e, "/admin/profile/tokens", contrib, url.Values{"name": {"x"}, "days": {"30"}, "scopes": {auth.ScopePublish}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("contributor with publish scope: %d", res.StatusCode)
	}

	res = post(e, "/admin/profile/tokens", author, create)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", res.StatusCode)
	}
	page := e.do("GET", "/admin/profile/tokens", author, nil, nil).Body.String()
	tok := reNewToken.FindString(page)
	if tok == "" || !strings.Contains(page, "claude mcp add") || !strings.Contains(page, home+"/mcp") {
		t.Fatal("new token not shown")
	}
	if again := e.do("GET", "/admin/profile/tokens", author, nil, nil).Body.String(); strings.Contains(again, tok) || !strings.Contains(again, "Release bot") {
		t.Fatal("token shown twice or missing from the list")
	}
	if u, _, err := e.auth.VerifyAccessToken(ctx, tok); err != nil || u.ID != e.users[auth.RoleAuthor].ID {
		t.Fatalf("created token does not work: %v", err)
	}
	if m := e.mails()["author@example.org"]; len(m) == 0 || !strings.Contains(strings.Join(m, ""), "Access token created") {
		t.Errorf("no security notice: %v", m)
	}
	list, _ := db.AccessTokens(ctx, e.d.R, e.users[auth.RoleAuthor].ID)
	id := strconv.FormatInt(list[0].ID, 10)

	// Others cannot revoke it through their own page; administrators can
	// see and revoke it.
	editor := e.session(auth.RoleEditor, db.SessionFull)
	if res := post(e, "/admin/profile/tokens/"+id+"/revoke", editor, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign revoke: %d", res.StatusCode)
	}
	if res := post(e, "/admin/users/"+strconv.FormatInt(e.users[auth.RoleAuthor].ID, 10)+"/tokens/"+id+"/revoke", editor, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("editor revokes via users: %d", res.StatusCode)
	}
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	edit := e.do("GET", "/admin/users/"+strconv.FormatInt(e.users[auth.RoleAuthor].ID, 10)+"/edit", admin, nil, nil).Body.String()
	if !strings.Contains(edit, "Release bot") || strings.Contains(edit, tok) {
		t.Fatal("token list on the user screen")
	}
	if res := post(e, "/admin/users/"+strconv.FormatInt(e.users[auth.RoleAuthor].ID, 10)+"/tokens/"+id+"/revoke", admin, nil); res.StatusCode != http.StatusSeeOther {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("admin revoke: %d %s", res.StatusCode, b)
	}
	if _, _, err := e.auth.VerifyAccessToken(ctx, tok); err == nil {
		t.Fatal("revoked token works")
	}

	// Own revoke and revoke all.
	for range 2 {
		post(e, "/admin/profile/tokens", author, create)
	}
	list, _ = db.AccessTokens(ctx, e.d.R, e.users[auth.RoleAuthor].ID)
	if res := post(e, "/admin/profile/tokens/"+strconv.FormatInt(list[0].ID, 10)+"/revoke", author, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke: %d", res.StatusCode)
	}
	if res := post(e, "/admin/profile/tokens/revoke-all", author, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke all: %d", res.StatusCode)
	}
	if list, _ = db.AccessTokens(ctx, e.d.R, e.users[auth.RoleAuthor].ID); len(list) != 0 {
		t.Fatal("tokens left")
	}
}

func TestTokensRevokedWithSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.users[auth.RoleAuthor]
	uid := strconv.FormatInt(u.ID, 10)
	admin := e.adminWithReauth()
	for _, action := range []string{"end-sessions", "reset-2fa"} {
		tok, _, err := e.auth.CreateAccessToken(ctx, nil, u, "bot", []string{auth.ScopeWrite}, 7)
		if err != nil {
			t.Fatal(err)
		}
		if res := post(e, "/admin/users/"+uid+"/security/"+action, admin, nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: %d", action, res.StatusCode)
		}
		if _, _, err := e.auth.VerifyAccessToken(ctx, tok); err == nil {
			t.Errorf("token survives %s", action)
		}
	}
}
