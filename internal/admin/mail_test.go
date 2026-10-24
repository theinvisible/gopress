package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func post(e *testEnv, path string, cur *auth.Current, form url.Values) *http.Response {
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", cur.CSRF)
	return e.do("POST", path, cur, form, map[string]string{"Origin": home}).Result()
}

func TestEmailSettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if rec := e.do("GET", "/admin/settings/email", e.session(auth.RoleEditor, db.SessionFull), nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("editor: %d", rec.Code)
	}
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	db.SetSessionReauth(ctx, e.d.W, admin.Session.IDHash, 0) // login was long ago
	form := url.Values{"host": {"smtp.example.org"}, "port": {"587"}, "security": {"starttls"}, "user": {"u"},
		"password": {"smtp secret pw"}, "from": {"site@example.org"}}
	// Without a recent authentication: redirected, nothing stored.
	if res := post(e, "/admin/settings/email", admin, form); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Fatalf("save without reauth: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if c, _ := e.mail.Config(ctx); c != nil {
		t.Fatal("stored without reauth")
	}
	e.auth.MarkReauth(ctx, admin)
	form.Set("security", "none") // plain text only to localhost
	if res := post(e, "/admin/settings/email", admin, form); res.StatusCode != http.StatusBadRequest {
		t.Errorf("plain SMTP to a remote host: %d", res.StatusCode)
	}
	form.Set("security", "starttls")
	if res := post(e, "/admin/settings/email", admin, form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	page := e.do("GET", "/admin/settings/email", admin, nil, nil).Body.String()
	if strings.Contains(page, "smtp secret pw") || !strings.Contains(page, "smtp.example.org") || !strings.Contains(page, "(unchanged)") {
		t.Error("password shown or settings missing on the page")
	}
	// Saving without a password keeps it for the same server, but a new
	// server needs it again.
	form.Del("password")
	form.Set("from_name", "Site")
	post(e, "/admin/settings/email", admin, form)
	if c, _ := e.mail.Config(ctx); c == nil || c.FromName != "Site" || c.PasswordEnc == "" {
		t.Errorf("config = %+v", c)
	}
	form.Set("port", "2525")
	if res := post(e, "/admin/settings/email", admin, form); res.StatusCode != http.StatusBadRequest {
		t.Errorf("new port without password: %d", res.StatusCode)
	}
	// Test mail goes to the admin's own address only.
	post(e, "/admin/settings/email/test", admin, url.Values{"to": {"someone@example.com"}})
	if m := e.mails(); len(m["administrator@example.org"]) != 1 || len(m["someone@example.com"]) != 0 {
		t.Errorf("test mail: %v", m)
	}
}

func TestSecurityNotices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cur := e.session(auth.RoleEditor, db.SessionFull)
	e.auth.MarkReauth(ctx, cur)
	if res := post(e, "/admin/profile/password", cur, url.Values{"pass1": {"a brand new passphrase"}, "pass2": {"a brand new passphrase"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("password change: %d", res.StatusCode)
	}
	m := e.mails()["editor@example.org"]
	if len(m) != 1 || !strings.Contains(m[0], "Password changed") {
		t.Errorf("notice = %q", m)
	}
	if strings.Contains(m[0], "a brand new passphrase") {
		t.Error("password in the mail")
	}
	// Lockout notices are limited to one per day.
	for range 3 {
		e.auth.Audit(ctx, nil, e.users[auth.RoleEditor].ID, auth.ActLoginLocked, "user", 0, nil)
	}
	if n := len(e.mails()["editor@example.org"]); n != 2 {
		t.Errorf("mails after three lockouts = %d, want 2", n)
	}
}

func TestCommentNotifications(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	db.SetOption(ctx, e.d.W, "admin_email", "owner@example.org")
	db.InsertPost(ctx, e.d.W, &db.Post{ID: 40, Type: db.TypePost, Status: db.StatusPublish, Slug: "by-author", AuthorID: e.users[auth.RoleAuthor].ID,
		Title: "Author's\r\nBcc: x@example.com post", Content: "<p>x</p>", Date: db.Time{Time: time.Now()}, DateGMT: db.Time{Time: time.Now()},
		CommentStatus: "open"})
	e.pub.Invalidate()
	c := &db.Comment{PostID: 40, Type: "comment", Author: "Visitor", AuthorEmail: "visitor@example.com", AuthorIP: "192.0.2.7",
		Content: "Nice post", Status: db.CommentPending, Date: db.Time{Time: time.Now()}, DateGMT: db.Time{Time: time.Now()}}
	db.InsertComment(ctx, e.d.W, c)
	e.cs.NotifyComment(ctx, c, false)
	m := e.mails()
	// The author cannot moderate: only the admin address is asked.
	if len(m["owner@example.org"]) != 1 || len(m["author@example.org"]) != 0 || !strings.Contains(m["owner@example.org"][0], "192.0.2.7") {
		t.Fatalf("moderation mails: %v", m)
	}
	if strings.Contains(m["owner@example.org"][0], "\nBcc:") {
		t.Error("post title broke the subject line")
	}
	// Approving notifies the post author.
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	if res := post(e, "/admin/api/comments/"+strconv.FormatInt(c.ID, 10)+"/approve", admin, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d", res.StatusCode)
	}
	m = e.mails()
	if len(m["author@example.org"]) != 1 || !strings.Contains(m["author@example.org"][0], "Nice post") {
		t.Errorf("author notice: %v", m["author@example.org"])
	}
	// comments_notify off: no mail.
	db.SetOption(ctx, e.d.W, "comments_notify", "0")
	e.pub.Invalidate()
	c2 := *c
	c2.ID, c2.Status = 0, db.CommentApproved
	db.InsertComment(ctx, e.d.W, &c2)
	e.cs.NotifyComment(ctx, &c2, false)
	if n := len(e.mails()["author@example.org"]); n != 1 {
		t.Errorf("mail sent although comments_notify is off (%d)", n)
	}
}
