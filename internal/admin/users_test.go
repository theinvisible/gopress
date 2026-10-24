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

func (e *testEnv) adminWithReauth() *auth.Current {
	cur := e.session(auth.RoleAdministrator, db.SessionFull)
	e.auth.MarkReauth(context.Background(), cur)
	return cur
}

func TestUserAccess(t *testing.T) {
	e := newEnv(t)
	for role, want := range map[string]int{auth.RoleAdministrator: 200, auth.RoleEditor: 403, auth.RoleSubscriber: 403} {
		cur := e.session(role, db.SessionFull)
		for _, p := range []string{"/admin/users/", "/admin/users/new", "/admin/users/" + strconv.FormatInt(e.users[auth.RoleAuthor].ID, 10) + "/edit"} {
			if rec := e.do("GET", p, cur, nil, nil); rec.Code != want {
				t.Errorf("%s GET %s = %d, want %d", role, p, rec.Code, want)
			}
		}
	}
	editor := e.session(auth.RoleEditor, db.SessionFull)
	if res := post(e, "/admin/users/new", editor, url.Values{"user_login": {"x"}, "email": {"x@example.org"}, "role": {"administrator"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("editor creates user: %d", res.StatusCode)
	}
	// A stale login must re-authenticate first.
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	db.SetSessionReauth(context.Background(), e.d.W, admin.Session.IDHash, 0)
	res := post(e, "/admin/users/new", admin, url.Values{"user_login": {"x"}, "email": {"x@example.org"}, "role": {"administrator"}})
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Errorf("create without reauth: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if _, err := db.UserByLogin(context.Background(), e.d.R, "x"); err == nil {
		t.Error("user created without reauth")
	}
}

var reInvite = regexp.MustCompile(`/login/invite/[A-Za-z0-9_-]{43}`)

func TestCreateUserInvite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.adminWithReauth()
	// Collisions: a login equal to another account's email and vice versa.
	for _, f := range []url.Values{
		{"user_login": {"editor@example.org"}, "email": {"new@example.org"}, "role": {"author"}},
		{"user_login": {"newbie"}, "email": {"EDITOR@example.org"}, "role": {"author"}},
		{"user_login": {"<script>"}, "email": {"new@example.org"}, "role": {"author"}},
		{"user_login": {"newbie"}, "email": {"new@example.org"}, "role": {"root"}},
	} {
		if res := post(e, "/admin/users/new", admin, f); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: %d", f, res.StatusCode)
		}
	}
	res := post(e, "/admin/users/new", admin, url.Values{"user_login": {"New Bie"}, "email": {"new@example.org"}, "role": {"author"},
		"display_name": {"<b>New</b>"}, "mode": {"invite"}, "send_mail": {"1"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", res.StatusCode)
	}
	u, err := db.UserByLogin(ctx, e.d.R, "New Bie")
	if err != nil || u.Nicename != "new-bie" || u.Role != auth.RoleAuthor || u.PassHash != "!" {
		t.Fatalf("user = %+v, %v", u, err)
	}
	// The link is shown once on the edit page and mailed.
	page := e.do("GET", res.Header.Get("Location"), admin, nil, nil).Body.String()
	link := reInvite.FindString(page)
	if link == "" || strings.Contains(page, "<b>New</b>") {
		t.Fatalf("edit page: link %q, escaped %v", link, !strings.Contains(page, "<b>New</b>"))
	}
	if again := e.do("GET", res.Header.Get("Location"), admin, nil, nil).Body.String(); reInvite.MatchString(again) {
		t.Error("link shown twice")
	}
	if m := e.mails()["new@example.org"]; len(m) != 1 || !strings.Contains(m[0], link) {
		t.Errorf("invitation mail: %v", m)
	}
	// No password login before the invitation is used.
	if _, err := e.auth.Authenticate(ctx, httpReq(), "New Bie", "!"); err == nil {
		t.Error("login with the placeholder hash")
	}
	b := e.browser()
	b.req("GET", link, nil)
	if res := b.form("/login/invite", "/login/invite", url.Values{"pass1": {"new bie passphrase"}, "pass2": {"new bie passphrase"}}); res.Header.Get("Location") != "/admin/setup-2fa" {
		t.Errorf("invitation: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestCreateUserPassword(t *testing.T) {
	e := newEnv(t)
	admin := e.adminWithReauth()
	res := post(e, "/admin/users/new", admin, url.Values{"user_login": {"pw"}, "email": {"pw@example.org"}, "role": {"editor"},
		"mode": {"password"}, "pass1": {"pw user passphrase"}, "pass2": {"pw user passphrase"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", res.StatusCode)
	}
	page := e.do("GET", res.Header.Get("Location"), admin, nil, nil).Body.String()
	code := regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}`).FindString(page)
	if code == "" {
		t.Fatal("no setup code on the page")
	}
	b := e.browser()
	b.form("/login", "/login", url.Values{"log": {"pw"}, "pwd": {"pw user passphrase"}})
	if res := b.form("/admin/setup-2fa", "/admin/setup-2fa/code", url.Values{"setup_code": {code}}); res.Header.Get("Location") != "/admin/setup-2fa" {
		t.Fatalf("setup code: %d", res.StatusCode)
	}
	c, _ := e.auth.Load(context.Background(), cookieReq(b.jar[auth.SessionCookie]))
	if c == nil || c.Session.State != db.SessionSetup {
		t.Errorf("state after setup code: %+v", c)
	}
}

func TestEditUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.adminWithReauth()
	author := e.users[auth.RoleAuthor]
	path := "/admin/users/" + strconv.FormatInt(author.ID, 10)
	victim := e.session(auth.RoleAuthor, db.SessionFull)
	e.auth.NewUserToken(ctx, author, auth.TokenReset, auth.ResetTTL, "")

	res := post(e, path+"/edit", admin, url.Values{"email": {"author2@example.org"}, "display_name": {"Author Two"}, "role": {"editor"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	got, _ := db.GetUser(ctx, e.d.R, author.ID)
	if got.Email != "author2@example.org" || got.Role != auth.RoleEditor || got.DisplayName != "Author Two" {
		t.Errorf("user = %+v", got)
	}
	if m := e.mails()["author@example.org"]; len(m) != 1 || !strings.Contains(m[0], "author2@example.org") {
		t.Errorf("old address not notified: %v", m)
	}
	if ok, _ := db.HasUserToken(ctx, e.d.R, author.ID, auth.TokenReset, 0); ok {
		t.Error("reset link to the old address still valid")
	}
	// Security actions.
	db.SetTOTP(ctx, e.d.W, author.ID, []byte("x"), 0, 0)
	if res := post(e, path+"/security/reset-2fa", admin, nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset-2fa: %d", res.StatusCode)
	}
	if has, _ := e.auth.HasTOTP(ctx, author.ID); has {
		t.Error("factor not removed")
	}
	if c, _ := e.auth.Load(ctx, withCookie(victim)); c != nil {
		t.Error("session survived the 2FA reset")
	}
	if page := e.do("GET", path+"/edit?msg=2fa", admin, nil, nil).Body.String(); !regexp.MustCompile(`[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}`).MatchString(page) {
		t.Error("no setup code after the reset")
	}
	if m := e.mails()["author2@example.org"]; len(m) != 1 || !strings.Contains(m[0], "reset by an administrator") {
		t.Errorf("user not notified of the reset: %v", m)
	}
	// Own account: redirected to the profile; no self-demotion.
	self := "/admin/users/" + strconv.FormatInt(e.users[auth.RoleAdministrator].ID, 10)
	if res := post(e, self+"/edit", admin, url.Values{"email": {"administrator@example.org"}, "role": {"subscriber"}}); res.Header.Get("Location") != "/admin/profile" {
		t.Errorf("self edit: %s", res.Header.Get("Location"))
	}
	if res := post(e, "/admin/users/bulk", admin, url.Values{"action": {"role"}, "new_role": {"subscriber"},
		"users[]": {strconv.FormatInt(e.users[auth.RoleAdministrator].ID, 10)}}); !strings.Contains(res.Header.Get("Location"), "msg=self") {
		t.Errorf("bulk self demotion: %s", res.Header.Get("Location"))
	}
	if u, _ := db.GetUser(ctx, e.d.R, e.users[auth.RoleAdministrator].ID); u.Role != auth.RoleAdministrator {
		t.Error("administrator demoted themselves")
	}
}

func TestDeleteUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.adminWithReauth()
	author := e.users[auth.RoleAuthor] // owns post 30
	sub := e.users[auth.RoleSubscriber]
	ids := strconv.FormatInt(author.ID, 10) + "," + strconv.FormatInt(sub.ID, 10)
	if rec := e.do("GET", "/admin/users/delete?ids="+ids, admin, nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Attribute all content to") {
		t.Fatalf("confirmation: %d", rec.Code)
	}
	if res := post(e, "/admin/users/delete", admin, url.Values{"ids": {ids}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("delete with content but no new owner: %d", res.StatusCode)
	}
	if _, err := db.GetUser(ctx, e.d.R, sub.ID); err != nil {
		t.Error("partial deletion")
	}
	editor := strconv.FormatInt(e.users[auth.RoleEditor].ID, 10)
	if res := post(e, "/admin/users/delete", admin, url.Values{"ids": {ids}, "reassign_user": {editor}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if _, err := db.GetUser(ctx, e.d.R, author.ID); err == nil {
		t.Error("user not deleted")
	}
	if p, _ := db.GetPost(ctx, e.d.R, 30); p == nil || strconv.FormatInt(p.AuthorID, 10) != editor {
		t.Errorf("post not reassigned: %+v", p)
	}
	// Own account and deleted target are refused.
	self := strconv.FormatInt(e.users[auth.RoleAdministrator].ID, 10)
	post(e, "/admin/users/delete", admin, url.Values{"ids": {self}, "reassign_user": {editor}})
	if _, err := db.GetUser(ctx, e.d.R, e.users[auth.RoleAdministrator].ID); err != nil {
		t.Error("administrator deleted their own account")
	}
	body, _ := io.ReadAll(e.do("GET", "/admin/users/", admin, nil, nil).Body)
	if strings.Contains(string(body), "author@example.org") {
		t.Error("deleted user still listed")
	}
}

func httpReq() *http.Request {
	r, _ := http.NewRequest("POST", home+"/login", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	return r
}
