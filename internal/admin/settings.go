package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
)

func (a *Admin) registerSettings(mux *http.ServeMux) {
	mux.Handle("GET /admin/settings/email", a.page(auth.CapManageOptions, a.emailSettingsPage))
	mux.Handle("POST /admin/settings/email", a.page(auth.CapManageOptions, a.emailSettingsSave))
	mux.Handle("POST /admin/settings/email/test", a.page(auth.CapManageOptions, a.emailSettingsTest))
	mux.Handle("POST /admin/settings/email/queue", a.page(auth.CapManageOptions, a.emailQueueAction))
}

type emailSettingsData struct {
	basePage
	Config      mail.Config
	HasPassword bool
	Configured  bool
	Sink        bool
	Reauth      bool
	Error       string
	Counts      map[string]int
	Queue       []mailRow
}

type mailRow struct {
	ID                                    int64
	To, Subject, Created, Status, LastErr string
	Tries                                 int
}

var emailMessages = map[string]notice{
	"saved":   {"success", "Email settings saved."},
	"removed": {"success", "Email configuration removed."},
	"sent":    {"success", "Test email sent."},
	"retry":   {"success", "The message is queued again."},
	"deleted": {"success", "Message deleted."},
}

// GET /admin/settings/email
func (a *Admin) emailSettingsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderEmailSettings(w, r, c, nil, "")
}

func (a *Admin) renderEmailSettings(w http.ResponseWriter, r *http.Request, c *reqCtx, form *mail.Config, errMsg string) error {
	ctx := r.Context()
	d := &emailSettingsData{basePage: a.base(ctx, c, "Email Settings", "settings-email"), Error: errMsg,
		Sink: a.mailSink, Reauth: a.auth.RecentlyAuthenticated(c.cur)}
	cfg, err := a.mail.Config(ctx)
	if err != nil {
		return err
	}
	if cfg != nil {
		d.Config, d.HasPassword, d.Configured = *cfg, cfg.PasswordEnc != "", true
	} else {
		d.Config = mail.Config{Port: 587, Security: mail.SecurityStartTLS, From: c.settings.AdminEmail}
	}
	if form != nil {
		d.Config = *form
	}
	if msg, ok := emailMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	if d.Counts, err = db.MailQueueCounts(ctx, a.db.R); err != nil {
		return err
	}
	queue, err := db.MailQueue(ctx, a.db.R, 50)
	if err != nil {
		return err
	}
	loc := c.settings.Loc
	for _, m := range queue {
		d.Queue = append(d.Queue, mailRow{ID: m.ID, To: m.To, Subject: m.Subject, Status: m.Status, LastErr: m.LastError,
			Tries: m.Tries, Created: time.Unix(m.Created, 0).In(loc).Format("2006/01/02 15:04")})
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "settings-email", d)
}

// POST /admin/settings/email (needs a recent authentication: the SMTP
// account can send mail in the site's name)
func (a *Admin) emailSettingsSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if !a.auth.RecentlyAuthenticated(c.cur) {
		return seeOther(w, r, "/admin/reauth?next="+url.QueryEscape("/admin/settings/email"))
	}
	if r.PostFormValue("remove") != "" {
		if err := a.mail.DeleteConfig(ctx); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, "mail_settings_removed", "option", 0, nil)
		return seeOther(w, r, "/admin/settings/email?msg=removed")
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("port")))
	cfg := &mail.Config{
		Host: strings.TrimSpace(r.PostFormValue("host")), Port: port, Security: r.PostFormValue("security"),
		User: strings.TrimSpace(r.PostFormValue("user")), From: strings.TrimSpace(r.PostFormValue("from")),
		FromName: strings.TrimSpace(r.PostFormValue("from_name")),
	}
	password := r.PostFormValue("password")
	keep := password == "" && r.PostFormValue("clear_password") == ""
	if len(password) > 1024 {
		return a.renderEmailSettings(w, r, c, cfg, "The password is too long.")
	}
	if err := a.mail.SaveConfig(ctx, cfg, password, keep); err != nil {
		return a.renderEmailSettings(w, r, c, cfg, err.Error())
	}
	a.auth.Audit(ctx, r, c.user.ID, "mail_settings_changed", "option", 0, map[string]any{"host": cfg.Host, "password_changed": !keep})
	a.mail.Wake()
	return seeOther(w, r, "/admin/settings/email?msg=saved")
}

// POST /admin/settings/email/test sends a test message to the current
// user's own address (not to arbitrary ones).
func (a *Admin) emailSettingsTest(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if err := a.mail.SendTest(r.Context(), c.user.Email); err != nil {
		return a.renderEmailSettings(w, r, c, nil, "Sending failed: "+err.Error())
	}
	return seeOther(w, r, "/admin/settings/email?msg=sent")
}

// POST /admin/settings/email/queue: retry or delete a queued message.
func (a *Admin) emailQueueAction(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	switch r.PostFormValue("action") {
	case "retry":
		if err := db.RetryMail(ctx, a.db.W, id, time.Now().Unix()); err != nil {
			return err
		}
		a.mail.Wake()
		return seeOther(w, r, "/admin/settings/email?msg=retry")
	case "delete":
		if err := db.DeleteMail(ctx, a.db.W, id); err != nil {
			return err
		}
		return seeOther(w, r, "/admin/settings/email?msg=deleted")
	}
	return badRequest("Unknown action.")
}
