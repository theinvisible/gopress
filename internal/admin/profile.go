package admin

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

func (a *Admin) registerProfile(mux *http.ServeMux) {
	mux.Handle("GET /admin/profile", a.page(auth.CapRead, a.profilePage))
	mux.Handle("POST /admin/profile", a.page(auth.CapRead, a.profileSave))
	mux.Handle("POST /admin/profile/password", a.page(auth.CapRead, a.passwordSave))
	mux.Handle("GET /admin/profile/security", a.page(auth.CapRead, a.securityPage))
	mux.Handle("POST /admin/profile/security/{action}", a.page(auth.CapRead, a.securityAction))
}

type profileData struct {
	basePage
	Nickname string
	Schemes  []string
	Error    string
	PwError  string
	Reauth   bool // a recent authentication is still valid
	Login    string
	Email    string
	URL      string
	Display  string
	Bio      string
}

var profileMessages = map[string]notice{
	"saved":    {"success", "Profile updated."},
	"password": {"success", "Password changed. All other sessions were logged out."},
}

// GET /admin/profile
func (a *Admin) profilePage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderProfile(w, r, c, nil, "", "")
}

func (a *Admin) renderProfile(w http.ResponseWriter, r *http.Request, c *reqCtx, u *db.User, errMsg, pwErr string) error {
	ctx := r.Context()
	if u == nil {
		u = c.user
	}
	d := &profileData{basePage: a.base(ctx, c, "Profile", "profile"), Schemes: colorSchemes, Error: errMsg, PwError: pwErr,
		Login: u.Login, Email: u.Email, URL: u.URL, Display: u.DisplayName, Bio: u.Description}
	d.Reauth = a.auth.RecentlyAuthenticated(c.cur)
	d.Nickname, _ = db.UserMeta(ctx, a.db.R, c.user.ID, "nickname")
	d.Scripts = []string{"profile.js"}
	if msg, ok := profileMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" && pwErr == "" {
		if d.Notice == nil {
			d.Notice = &msg
		}
	}
	if errMsg != "" || pwErr != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "profile", d)
}

// POST /admin/profile
func (a *Admin) profileSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	u := *c.user
	u.DisplayName = strings.TrimSpace(render.SanitizeText(r.PostFormValue("display_name")))
	u.Description = strings.TrimSpace(render.SanitizeText(r.PostFormValue("description")))
	u.URL = strings.TrimSpace(r.PostFormValue("url"))
	email := strings.TrimSpace(r.PostFormValue("email"))
	nickname := strings.TrimSpace(render.SanitizeText(r.PostFormValue("nickname")))
	color := r.PostFormValue("admin_color")
	if u.DisplayName == "" {
		u.DisplayName = u.Login
	}
	if len(u.DisplayName) > 250 || len(nickname) > 250 || len(u.Description) > 5000 {
		return a.renderProfile(w, r, c, &u, "A field is too long.", "")
	}
	if u.URL != "" {
		pu, err := url.Parse(u.URL)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			return a.renderProfile(w, r, c, &u, "The website must be an http(s) URL.", "")
		}
	}
	if !strings.EqualFold(email, c.user.Email) {
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Address != email {
			return a.renderProfile(w, r, c, &u, "Please enter a valid email address.", "")
		}
		// Changing the email (a login name) is a sensitive change.
		if !a.auth.RecentlyAuthenticated(c.cur) {
			return seeOther(w, r, "/admin/reauth?next="+url.QueryEscape("/admin/profile"))
		}
		if other, err := db.UserByLogin(ctx, a.db.R, email); err == nil && other.ID != u.ID {
			return a.renderProfile(w, r, c, &u, "This email address is already used by another account.", "")
		}
		u.Email = email
	}
	if err := db.UpdateUserProfile(ctx, a.db.W, &u); err != nil {
		return err
	}
	if err := db.SetUserMeta(ctx, a.db.W, u.ID, "nickname", nickname); err != nil {
		return err
	}
	for _, s := range colorSchemes {
		if s == color {
			if err := db.SetUserMeta(ctx, a.db.W, u.ID, "admin_color", color); err != nil {
				return err
			}
		}
	}
	a.auth.Audit(ctx, r, u.ID, auth.ActProfileUpdate, "user", u.ID, map[string]any{"email_changed": u.Email != c.user.Email})
	a.site.Invalidate() // author names appear on the site
	return seeOther(w, r, "/admin/profile?msg=saved")
}

// POST /admin/profile/password (needs a recent authentication)
func (a *Admin) passwordSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if !a.auth.RecentlyAuthenticated(c.cur) {
		return seeOther(w, r, "/admin/reauth?next="+url.QueryEscape("/admin/profile#password"))
	}
	pw, pw2 := r.PostFormValue("pass1"), r.PostFormValue("pass2")
	if pw != pw2 {
		return a.renderProfile(w, r, c, nil, "", "The passwords do not match.")
	}
	if err := auth.CheckPasswordPolicy(pw, c.user.Login, c.user.Email); err != nil {
		return a.renderProfile(w, r, c, nil, "", err.Error())
	}
	hash, err := auth.HashPassword(ctx, pw)
	if err != nil {
		return err
	}
	if err := db.SetUserPassHash(ctx, a.db.W, c.user.ID, hash); err != nil {
		return err
	}
	// A new password ends every other session.
	if err := db.DeleteUserSessions(ctx, a.db.W, c.user.ID, c.cur.Session.IDHash); err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, auth.ActPasswordChange, "user", c.user.ID, nil)
	return seeOther(w, r, "/admin/profile?msg=password")
}

type securityData struct {
	basePage
	HasTOTP     bool
	Passkeys    []passkeyRow
	CodesLeft   int
	Sessions    []sessionRow
	Activity    []activityRow
	FactorCount int
	Reauth      bool
	ReauthUntil string
}

type passkeyRow struct {
	ID, Name, Created, LastUsed string
}

type sessionRow struct {
	ID       string // hex of the hash prefix, for revoking one session
	Current  bool
	Device   string
	IP       string
	LastSeen string
	Created  string
	Remember bool
}

type activityRow struct {
	When, Action, IP string
}

var securityMessages = map[string]notice{
	"factor-added":    {"success", "Second factor added."},
	"totp-removed":    {"success", "Authenticator app removed."},
	"passkey-gone":    {"success", "Passkey removed."},
	"renamed":         {"success", "Passkey renamed."},
	"sessions":        {"success", "All other sessions were logged out."},
	"session":         {"success", "Session logged out."},
	"last-factor":     {"error", "You cannot remove your last second factor. Add another one first."},
	"codes-unchanged": {"info", "Recovery codes unchanged."},
}

// GET /admin/profile/security
func (a *Admin) securityPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	d := &securityData{basePage: a.base(ctx, c, "Security", "security")}
	d.Scripts = []string{"profile.js"}
	if msg, ok := securityMessages[r.URL.Query().Get("msg")]; ok {
		if d.Notice == nil {
			d.Notice = &msg
		}
	}
	var err error
	if d.HasTOTP, err = a.auth.HasTOTP(ctx, c.user.ID); err != nil {
		return err
	}
	keys, err := a.auth.Passkeys(ctx, c.user.ID)
	if err != nil {
		return err
	}
	loc := c.settings.Loc
	if loc == nil {
		loc = time.UTC
	}
	when := func(t int64) string {
		if t == 0 {
			return "never"
		}
		return time.Unix(t, 0).In(loc).Format("2006/01/02 15:04")
	}
	for _, k := range keys {
		d.Passkeys = append(d.Passkeys, passkeyRow{ID: b64url(k.ID), Name: k.Name, Created: when(k.Created), LastUsed: when(k.LastUsed)})
	}
	d.FactorCount = len(keys)
	if d.HasTOTP {
		d.FactorCount++
	}
	if d.CodesLeft, err = db.RecoveryCodesLeft(ctx, a.db.R, c.user.ID); err != nil {
		return err
	}
	sessions, err := db.UserSessions(ctx, a.db.R, c.user.ID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		d.Sessions = append(d.Sessions, sessionRow{ID: hex.EncodeToString(s.IDHash[:8]), Current: string(s.IDHash) == string(c.cur.Session.IDHash),
			Device: deviceName(s.UserAgent), IP: s.IP, LastSeen: when(s.LastSeen), Created: when(s.Created), Remember: s.Remember})
	}
	entries, err := db.AuditLog(ctx, a.db.R, c.user.ID, 15, 0)
	if err != nil {
		return err
	}
	for _, e := range entries {
		d.Activity = append(d.Activity, activityRow{When: when(e.Time), Action: actionLabel(e.Action), IP: e.IP})
	}
	d.Reauth = a.auth.RecentlyAuthenticated(c.cur)
	return a.render(w, "security", d)
}

// POST /admin/profile/security/{action}: remove-totp, remove-passkey,
// rename-passkey, regenerate-codes, logout-others, logout-session.
func (a *Admin) securityAction(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	action := r.PathValue("action")
	// Everything except renaming a passkey or ending other sessions needs
	// a recent authentication.
	if action != "rename-passkey" && action != "logout-others" && action != "logout-session" && !a.auth.RecentlyAuthenticated(c.cur) {
		return seeOther(w, r, "/admin/reauth?next="+url.QueryEscape("/admin/profile/security"))
	}
	count, err := a.auth.SecondFactorCount(ctx, c.user.ID)
	if err != nil {
		return err
	}
	switch action {
	case "remove-totp":
		if count <= 1 {
			return seeOther(w, r, "/admin/profile/security?msg=last-factor")
		}
		if err := db.DeleteTOTP(ctx, a.db.W, c.user.ID); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, auth.ActTOTPRemoved, "user", c.user.ID, nil)
		return seeOther(w, r, "/admin/profile/security?msg=totp-removed")
	case "remove-passkey":
		id, ok := a.auth.FindPasskey(ctx, c.user.ID, r.PostFormValue("id"))
		if !ok {
			return errNotFound
		}
		if count <= 1 {
			return seeOther(w, r, "/admin/profile/security?msg=last-factor")
		}
		if err := db.DeleteWebAuthnCredential(ctx, a.db.W, c.user.ID, id); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, auth.ActPasskeyRemoved, "user", c.user.ID, nil)
		return seeOther(w, r, "/admin/profile/security?msg=passkey-gone")
	case "rename-passkey":
		id, ok := a.auth.FindPasskey(ctx, c.user.ID, r.PostFormValue("id"))
		if !ok {
			return errNotFound
		}
		name := strings.TrimSpace(render.SanitizeText(r.PostFormValue("name")))
		if name == "" || len(name) > 60 {
			return badRequest("Please enter a name of up to 60 characters.")
		}
		if err := db.RenameWebAuthnCredential(ctx, a.db.W, c.user.ID, id, name); err != nil {
			return err
		}
		return seeOther(w, r, "/admin/profile/security?msg=renamed")
	case "regenerate-codes":
		codes, err := a.auth.NewRecoveryCodes(ctx, c.user.ID)
		if err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, auth.ActRecoveryNew, "user", c.user.ID, nil)
		a.flash.put("codes:"+hex.EncodeToString(c.cur.Session.IDHash), codes)
		return seeOther(w, r, "/admin/recovery-codes")
	case "logout-others":
		if err := a.auth.RevokeOtherSessions(ctx, r, c.cur); err != nil {
			return err
		}
		return seeOther(w, r, "/admin/profile/security?msg=sessions")
	case "logout-session":
		prefix, err := hex.DecodeString(r.PostFormValue("id"))
		if err != nil || len(prefix) != 8 {
			return badRequest("Invalid session.")
		}
		if err := a.endSessionByPrefix(ctx, c, prefix); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, auth.ActSessionsRevoke, "user", c.user.ID, map[string]any{"one": true})
		return seeOther(w, r, "/admin/profile/security?msg=session")
	}
	return errNotFound
}

// endSessionByPrefix ends one of the user's other sessions, identified by
// the first bytes of its hash (the full hash never leaves the server).
func (a *Admin) endSessionByPrefix(ctx context.Context, c *reqCtx, prefix []byte) error {
	sessions, err := db.UserSessions(ctx, a.db.R, c.user.ID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if string(s.IDHash[:8]) == string(prefix) && string(s.IDHash) != string(c.cur.Session.IDHash) {
			return db.DeleteSession(ctx, a.db.W, s.IDHash)
		}
	}
	return errors.New("session not found")
}

// deviceName summarizes a user agent ("Firefox on Linux").
func deviceName(ua string) string {
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case ua == "":
		return "Unknown device"
	}
	osName := ""
	switch {
	case strings.Contains(ua, "Android"):
		osName = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		osName = "iOS"
	case strings.Contains(ua, "Mac OS X"):
		osName = "macOS"
	case strings.Contains(ua, "Windows"):
		osName = "Windows"
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	if osName == "" {
		return browser
	}
	return browser + " on " + osName
}

func actionLabel(a string) string { return auth.ActionLabel(a) }

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
