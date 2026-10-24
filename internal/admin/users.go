package admin

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
)

// User management. Every change needs a recent re-authentication of the
// acting administrator; an administrator cannot demote or delete
// themselves, and the last administrator cannot be demoted.
func (a *Admin) registerUsers(mux *http.ServeMux) {
	mux.Handle("GET /admin/users/{$}", a.page(auth.CapListUsers, a.userList))
	mux.Handle("POST /admin/users/bulk", a.page(auth.CapEditUsers, a.userBulk))
	mux.Handle("GET /admin/users/new", a.page(auth.CapCreateUsers, a.userNewPage))
	mux.Handle("POST /admin/users/new", a.page(auth.CapCreateUsers, a.userCreate))
	mux.Handle("GET /admin/users/{id}/edit", a.page(auth.CapEditUsers, a.userEditPage))
	mux.Handle("POST /admin/users/{id}/edit", a.page(auth.CapEditUsers, a.userSave))
	mux.Handle("POST /admin/users/{id}/security/{action}", a.page(auth.CapEditUsers, a.userSecurity))
	mux.Handle("GET /admin/users/delete", a.page(auth.CapDeleteUsers, a.userDeletePage))
	mux.Handle("POST /admin/users/delete", a.page(auth.CapDeleteUsers, a.userDelete))
}

// needReauth redirects to the re-authentication unless it is recent;
// back is the page to return to afterwards.
func (a *Admin) needReauth(w http.ResponseWriter, r *http.Request, c *reqCtx, back string) bool {
	if a.auth.RecentlyAuthenticated(c.cur) {
		return false
	}
	seeOther(w, r, "/admin/reauth?next="+url.QueryEscape(back))
	return true
}

type userListData struct {
	basePage
	Views    []statusView
	Rows     []userListRow
	Role     string
	Search   string
	Total    int
	Paged    int
	Pages    int
	PageURL  func(int) string
	Roles    []option
	CanAdd   bool
	CanDel   bool
	OrderBy  string
	Order    string
	SortURL  func(string) string
	SelfID   int64
	Messages string
}

type userListRow struct {
	db.UserRow
	RoleLabel string
	Factors   string
	LastLogin string
	Invited   bool
}

var userMessages = map[string]notice{
	"roles":   {"success", "Roles changed."},
	"deleted": {"success", "Users deleted."},
	"saved":   {"success", "User updated."},
	"created": {"success", "User created."},
	"2fa":     {"success", "Two-factor authentication was reset. The user sets it up again with the setup code below."},
	"code":    {"success", "New setup code created."},
	"ended":   {"success", "All sessions of the user were logged out."},
	"unlock":  {"success", "Login locks lifted."},
	"reset":   {"success", "A password reset link was sent to the user."},
	"invite":  {"success", "New invitation created."},
	"self":    {"error", "You cannot change your own role or delete your own account here."},
	"last":    {"error", "The site needs at least one administrator."},
	"nomail":  {"error", "Email is not configured (Settings › Email)."},
	"content": {"error", "Choose a user who gets the content of the deleted users."},
}

const usersPerPage = 20

// GET /admin/users/
func (a *Admin) userList(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	q := r.URL.Query()
	d := &userListData{basePage: a.base(ctx, c, "Users", "users-all"), SelfID: c.user.ID,
		CanAdd: auth.Can(c.user, auth.CapCreateUsers), CanDel: auth.Can(c.user, auth.CapDeleteUsers)}
	d.Scripts = []string{"list.js"}
	if msg, ok := userMessages[q.Get("msg")]; ok && d.Notice == nil {
		d.Notice = &msg
	}
	d.Role, d.Search = q.Get("role"), strings.TrimSpace(q.Get("s"))
	if d.Role != "" && !auth.ValidRole(d.Role) {
		d.Role = ""
	}
	d.OrderBy, d.Order = q.Get("orderby"), q.Get("order")
	d.Paged = max(1, int(atoi64(q.Get("paged"))))
	counts, err := db.RoleCounts(ctx, a.db.R)
	if err != nil {
		return err
	}
	all := 0
	for _, n := range counts {
		all += n
	}
	d.Views = append(d.Views, statusView{Label: "All", URL: "/admin/users/", Count: all, Current: d.Role == ""})
	for _, role := range auth.Roles {
		if counts[role.Name] > 0 {
			d.Views = append(d.Views, statusView{Label: role.Label, URL: "/admin/users/?role=" + role.Name, Count: counts[role.Name], Current: d.Role == role.Name})
		}
		d.Roles = append(d.Roles, option{Value: role.Name, Label: role.Label})
	}
	rows, total, err := db.AdminUsers(ctx, a.db.R, db.AdminUserQuery{Role: d.Role, Search: d.Search, OrderBy: d.OrderBy,
		Desc: d.Order == "desc", Limit: usersPerPage, Offset: (d.Paged - 1) * usersPerPage})
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, u := range rows {
		row := userListRow{UserRow: u, RoleLabel: auth.RoleLabel(u.Role), Factors: a.factorSummary(ctx, u.ID)}
		if u.LastLogin > 0 {
			row.LastLogin = time.Unix(u.LastLogin, 0).In(c.settings.Loc).Format("2006/01/02 15:04")
		}
		row.Invited, _ = db.HasUserToken(ctx, a.db.R, u.ID, auth.TokenInvite, now)
		d.Rows = append(d.Rows, row)
	}
	d.Total = total
	d.Pages = (total + usersPerPage - 1) / usersPerPage
	d.PageURL = func(n int) string { return withQuery(r.URL.RequestURI(), "paged", n) }
	d.SortURL = func(col string) string {
		order := "asc"
		if d.OrderBy == col && d.Order != "desc" {
			order = "desc"
		}
		return withQuery(r.URL.RequestURI(), "orderby", col, "order", order, "paged", "")
	}
	return a.render(w, "users", d)
}

// factorSummary describes the second factors of a user ("Passkeys: 2,
// App"), or "" if there are none.
func (a *Admin) factorSummary(ctx context.Context, userID int64) string {
	var parts []string
	if has, _ := a.auth.HasTOTP(ctx, userID); has {
		parts = append(parts, "Authenticator app")
	}
	if keys, _ := a.auth.Passkeys(ctx, userID); len(keys) > 0 {
		parts = append(parts, strconv.Itoa(len(keys))+" "+plural(len(keys), "passkey", "passkeys"))
	}
	return strings.Join(parts, ", ")
}

// POST /admin/users/bulk: change role or go to the delete confirmation.
func (a *Admin) userBulk(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	ids := parseIDs(r.PostForm["users[]"])
	if len(ids) == 0 {
		return seeOther(w, r, "/admin/users/")
	}
	if r.PostFormValue("action") == "delete" {
		if !auth.Can(c.user, auth.CapDeleteUsers) {
			return errForbidden
		}
		var s []string
		for _, id := range ids {
			s = append(s, strconv.FormatInt(id, 10))
		}
		return seeOther(w, r, "/admin/users/delete?ids="+strings.Join(s, ","))
	}
	role := r.PostFormValue("new_role")
	if !auth.ValidRole(role) || !auth.Can(c.user, auth.CapPromoteUsers) {
		return badRequest("Please choose a role.")
	}
	if a.needReauth(w, r, c, "/admin/users/") {
		return nil
	}
	for _, id := range ids {
		if id == c.user.ID {
			return seeOther(w, r, "/admin/users/?msg=self")
		}
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if err := a.changeRole(ctx, tx, id, role); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errLastAdmin) {
		return seeOther(w, r, "/admin/users/?msg=last")
	}
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "users_role_changed", "user", 0, map[string]any{"ids": ids, "role": role})
	return seeOther(w, r, "/admin/users/?msg=roles")
}

var errLastAdmin = errors.New("last administrator")

// changeRole sets the role inside tx, keeping at least one administrator.
func (a *Admin) changeRole(ctx context.Context, tx *sql.Tx, userID int64, role string) error {
	u, err := db.GetUser(ctx, tx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || u.Role == role {
		return err
	}
	if err := db.SetUserRole(ctx, tx, userID, role); err != nil {
		return err
	}
	if u.Role == auth.RoleAdministrator {
		counts, err := db.RoleCounts(ctx, tx)
		if err != nil {
			return err
		}
		if counts[auth.RoleAdministrator] == 0 {
			return errLastAdmin
		}
	}
	// A changed role takes effect immediately; sessions keep working.
	return nil
}

type userFormData struct {
	basePage
	Error       string
	Login       string
	Email       string
	Display     string
	Role        string
	Roles       []option
	Mode        string // "invite" or "password"
	MailReady   bool
	Reauth      bool
	Edit        *db.User
	Nickname    string
	URL         string
	Bio         string
	CanPromote  bool
	Factors     string
	CodesLeft   int
	Sessions    int
	LastLogin   string
	Invited     bool
	HasFactor   bool
	OneTimeLink string
	SetupCode   string
	Activity    []activityRow
}

func roleOptions(selected string) []option {
	var out []option
	for _, r := range auth.Roles {
		out = append(out, option{Value: r.Name, Label: r.Label, Selected: r.Name == selected})
	}
	return out
}

// GET /admin/users/new
func (a *Admin) userNewPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderUserNew(w, r, c, &userFormData{Role: auth.RoleAuthor, Mode: "invite"}, "")
}

func (a *Admin) renderUserNew(w http.ResponseWriter, r *http.Request, c *reqCtx, d *userFormData, errMsg string) error {
	ctx := r.Context()
	d.basePage = a.base(ctx, c, "Add User", "users-new")
	d.Scripts = []string{"profile.js"}
	d.Error, d.Roles = errMsg, roleOptions(d.Role)
	d.MailReady = a.mail.Ready(ctx)
	d.Reauth = a.auth.RecentlyAuthenticated(c.cur)
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "user-new", d)
}

var reLogin = regexp.MustCompile(`^[A-Za-z0-9 _.@-]{1,60}$`)

// POST /admin/users/new
func (a *Admin) userCreate(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if a.needReauth(w, r, c, "/admin/users/new") {
		return nil
	}
	d := &userFormData{Login: strings.Join(strings.Fields(r.PostFormValue("user_login")), " "),
		Email: strings.TrimSpace(r.PostFormValue("email")), Display: strings.TrimSpace(r.PostFormValue("display_name")),
		Role: r.PostFormValue("role"), Mode: r.PostFormValue("mode")}
	if d.Mode != "password" {
		d.Mode = "invite"
	}
	switch {
	case !reLogin.MatchString(d.Login):
		return a.renderUserNew(w, r, c, d, "The username may contain 1–60 letters, digits, spaces and the characters _ . @ -")
	case !validEmail(d.Email):
		return a.renderUserNew(w, r, c, d, "Please enter a valid email address.")
	case !auth.ValidRole(d.Role):
		return a.renderUserNew(w, r, c, d, "Please choose a role.")
	case len(d.Display) > 250:
		return a.renderUserNew(w, r, c, d, "The display name is too long.")
	}
	// Logins and email addresses are both login names: neither may collide.
	for _, chk := range []struct{ field, value string }{{"login", d.Login}, {"email", d.Login}, {"email", d.Email}, {"login", d.Email}} {
		taken, err := db.LoginTaken(ctx, a.db.R, chk.field, chk.value, 0)
		if err != nil {
			return err
		}
		if taken {
			return a.renderUserNew(w, r, c, d, "This username or email address is already used by another account.")
		}
	}
	hash := "!" // no password yet: the invitation link sets it
	if d.Mode == "password" {
		pw := r.PostFormValue("pass1")
		if pw != r.PostFormValue("pass2") {
			return a.renderUserNew(w, r, c, d, "The passwords do not match.")
		}
		if err := auth.CheckPasswordPolicy(pw, d.Login, d.Email); err != nil {
			return a.renderUserNew(w, r, c, d, err.Error())
		}
		var err error
		if hash, err = auth.HashPassword(ctx, pw); err != nil {
			return err
		}
	}
	nicename, err := a.uniqueNicename(ctx, d.Login, 0)
	if err != nil {
		return err
	}
	display := d.Display
	if display == "" {
		display = d.Login
	}
	u := &db.User{Login: d.Login, Nicename: nicename, Email: d.Email, DisplayName: display, PassHash: hash, Role: d.Role,
		RegisteredGMT: db.Time{Time: time.Now().UTC().Truncate(time.Second)}}
	if err := db.InsertUser(ctx, a.db.W, u); err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "user_created", "user", u.ID, map[string]any{"role": u.Role, "mode": d.Mode})
	a.site.Invalidate()
	if d.Mode == "password" {
		code, err := a.auth.NewSetupCode(ctx, u.ID)
		if err != nil {
			return err
		}
		a.flash.put(a.flashKey(c, "user"), userFlash{SetupCode: code})
	} else if err := a.inviteUser(ctx, r, c, u, r.PostFormValue("send_mail") != ""); err != nil {
		return err
	}
	return seeOther(w, r, "/admin/users/"+strconv.FormatInt(u.ID, 10)+"/edit?msg=created")
}

// userFlash carries one-time secrets from a POST to the edit page.
type userFlash struct {
	Link, SetupCode string
}

func (a *Admin) flashKey(c *reqCtx, kind string) string {
	return kind + ":" + hex.EncodeToString(c.cur.Session.IDHash)
}

// inviteUser creates an invitation link, shows it once to the
// administrator and mails it if wanted and possible.
func (a *Admin) inviteUser(ctx context.Context, r *http.Request, c *reqCtx, u *db.User, send bool) error {
	tok, err := a.auth.NewUserToken(ctx, u, auth.TokenInvite, auth.InviteTTL, "")
	if err != nil {
		return err
	}
	link := strings.TrimRight(a.auth.Home(), "/") + "/login/invite/" + tok
	a.flash.put(a.flashKey(c, "user"), userFlash{Link: link})
	a.auth.Audit(ctx, r, c.user.ID, "user_invited", "user", u.ID, map[string]any{"mailed": send})
	if send && a.mail.Ready(ctx) {
		return a.mail.Enqueue(ctx, u.Email, "invitation", mail.Data{"Login": u.Login, "Role": auth.RoleLabel(u.Role),
			"Inviter": c.user.DisplayName, "Link": link, "Valid": "72 hours"})
	}
	return nil
}

// uniqueNicename derives the URL name (author archive) from the login.
func (a *Admin) uniqueNicename(ctx context.Context, login string, except int64) (string, error) {
	base := content.SanitizeTitle(login)
	if base == "" {
		base = "user"
	}
	name := base
	for i := 2; ; i++ {
		taken, err := db.LoginTaken(ctx, a.db.R, "nicename", name, except)
		if err != nil || !taken {
			return name, err
		}
		name = base + "-" + strconv.Itoa(i)
	}
}

// targetUser loads the user of the {id} path value.
func (a *Admin) targetUser(r *http.Request) (*db.User, error) {
	u, err := db.GetUser(r.Context(), a.db.R, atoi64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return u, err
}

// GET /admin/users/{id}/edit
func (a *Admin) userEditPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	u, err := a.targetUser(r)
	if err != nil {
		return err
	}
	if u.ID == c.user.ID {
		return seeOther(w, r, "/admin/profile")
	}
	nick, _ := db.UserMeta(r.Context(), a.db.R, u.ID, "nickname")
	return a.renderUserEdit(w, r, c, u, &userFormData{Email: u.Email, Display: u.DisplayName, Nickname: nick, URL: u.URL,
		Bio: u.Description, Role: u.Role}, "")
}

func (a *Admin) renderUserEdit(w http.ResponseWriter, r *http.Request, c *reqCtx, u *db.User, d *userFormData, errMsg string) error {
	ctx := r.Context()
	d.basePage = a.base(ctx, c, "Edit User", "users-all")
	d.Edit, d.Error = u, errMsg
	d.Roles = roleOptions(d.Role)
	d.CanPromote = auth.Can(c.user, auth.CapPromoteUsers)
	d.MailReady = a.mail.Ready(ctx)
	d.Reauth = a.auth.RecentlyAuthenticated(c.cur)
	if msg, ok := userMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	if v, ok := a.flash.take(a.flashKey(c, "user")); ok {
		f := v.(userFlash)
		d.OneTimeLink, d.SetupCode = f.Link, f.SetupCode
	}
	d.Factors = a.factorSummary(ctx, u.ID)
	d.HasFactor = d.Factors != ""
	d.CodesLeft, _ = db.RecoveryCodesLeft(ctx, a.db.R, u.ID)
	sessions, err := db.UserSessions(ctx, a.db.R, u.ID)
	if err != nil {
		return err
	}
	d.Sessions = len(sessions)
	d.Invited, _ = db.HasUserToken(ctx, a.db.R, u.ID, auth.TokenInvite, time.Now().Unix())
	entries, err := db.AuditLog(ctx, a.db.R, u.ID, 10, 0)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Action == auth.ActLogin && d.LastLogin == "" {
			d.LastLogin = time.Unix(e.Time, 0).In(c.settings.Loc).Format("2006/01/02 15:04")
		}
		d.Activity = append(d.Activity, activityRow{When: time.Unix(e.Time, 0).In(c.settings.Loc).Format("2006/01/02 15:04"),
			Action: actionLabel(e.Action), IP: e.IP})
	}
	d.Scripts = []string{"profile.js"}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "user-edit", d)
}

// POST /admin/users/{id}/edit
func (a *Admin) userSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	old, err := a.targetUser(r)
	if err != nil {
		return err
	}
	back := "/admin/users/" + strconv.FormatInt(old.ID, 10) + "/edit"
	if old.ID == c.user.ID {
		return seeOther(w, r, "/admin/profile")
	}
	if a.needReauth(w, r, c, back) {
		return nil
	}
	u, nickname, email, errMsg := readProfileForm(r, old)
	role := r.PostFormValue("role")
	form := &userFormData{Email: email, Display: u.DisplayName, Nickname: nickname, URL: u.URL, Bio: u.Description, Role: role}
	if errMsg != "" {
		return a.renderUserEdit(w, r, c, old, form, errMsg)
	}
	if role == "" || !auth.Can(c.user, auth.CapPromoteUsers) {
		role = old.Role
	}
	if !auth.ValidRole(role) {
		return a.renderUserEdit(w, r, c, old, form, "Please choose a role.")
	}
	if !strings.EqualFold(email, old.Email) {
		if taken, err := a.emailTaken(ctx, email, old.ID); err != nil || taken {
			if err != nil {
				return err
			}
			return a.renderUserEdit(w, r, c, old, form, "This email address is already used by another account.")
		}
		u.Email = email
	}
	err = a.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := db.UpdateUserProfile(ctx, tx, &u); err != nil {
			return err
		}
		if err := db.SetUserMeta(ctx, tx, u.ID, "nickname", nickname); err != nil {
			return err
		}
		if u.Email != old.Email {
			// Pending links went to the old address.
			if err := db.DeleteUserTokens(ctx, tx, u.ID, ""); err != nil {
				return err
			}
		}
		return a.changeRole(ctx, tx, u.ID, role)
	})
	if errors.Is(err, errLastAdmin) {
		return seeOther(w, r, back+"?msg=last")
	}
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "user_updated", "user", u.ID, map[string]any{"email_changed": u.Email != old.Email,
		"role": role, "old_role": old.Role})
	if u.Email != old.Email {
		a.notifyEmailChanged(ctx, &u, old.Email)
	}
	a.site.Invalidate()
	return seeOther(w, r, back+"?msg=saved")
}

// POST /admin/users/{id}/security/{action}: reset-2fa, setup-code,
// end-sessions, unlock, send-reset, invite.
func (a *Admin) userSecurity(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	u, err := a.targetUser(r)
	if err != nil {
		return err
	}
	if u.ID == c.user.ID {
		return seeOther(w, r, "/admin/profile/security")
	}
	back := "/admin/users/" + strconv.FormatInt(u.ID, 10) + "/edit"
	if a.needReauth(w, r, c, back) {
		return nil
	}
	hasFactor, err := a.auth.HasSecondFactor(ctx, u.ID)
	if err != nil {
		return err
	}
	switch r.PathValue("action") {
	case "reset-2fa":
		err := a.db.Tx(ctx, func(tx *sql.Tx) error {
			if err := db.DeleteSecondFactors(ctx, tx, u.ID); err != nil {
				return err
			}
			return db.DeleteUserSessions(ctx, tx, u.ID, nil)
		})
		if err != nil {
			return err
		}
		code, err := a.auth.NewSetupCode(ctx, u.ID)
		if err != nil {
			return err
		}
		a.auth.Audit(ctx, r, u.ID, auth.Act2FAReset, "user", u.ID, map[string]any{"by": c.user.ID})
		a.flash.put(a.flashKey(c, "user"), userFlash{SetupCode: code})
		return seeOther(w, r, back+"?msg=2fa")
	case "setup-code":
		if hasFactor {
			return badRequest("The user already has a second factor. Reset two-factor authentication first.")
		}
		code, err := a.auth.NewSetupCode(ctx, u.ID)
		if err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, "setup_code_issued", "user", u.ID, nil)
		a.flash.put(a.flashKey(c, "user"), userFlash{SetupCode: code})
		return seeOther(w, r, back+"?msg=code")
	case "end-sessions":
		if err := db.DeleteUserSessions(ctx, a.db.W, u.ID, nil); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, auth.ActSessionsRevoke, "user", u.ID, nil)
		return seeOther(w, r, back+"?msg=ended")
	case "unlock":
		if err := auth.UnlockUser(ctx, a.db, u.ID); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, auth.ActUserUnlocked, "user", u.ID, nil)
		return seeOther(w, r, back+"?msg=unlock")
	case "send-reset":
		if !a.mail.Ready(ctx) {
			return seeOther(w, r, back+"?msg=nomail")
		}
		tok, err := a.auth.NewUserToken(ctx, u, auth.TokenReset, auth.ResetTTL, "")
		if err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, "password_reset_sent", "user", u.ID, nil)
		if err := a.mail.Enqueue(ctx, u.Email, "password_reset", mail.Data{"Login": u.Login, "IP": a.auth.ClientIP(r),
			"Valid": "one hour", "Link": strings.TrimRight(a.auth.Home(), "/") + "/login/reset/" + tok}); err != nil {
			return err
		}
		return seeOther(w, r, back+"?msg=reset")
	case "invite":
		if hasFactor {
			return badRequest("The user has already set up the account.")
		}
		if err := a.inviteUser(ctx, r, c, u, r.PostFormValue("send_mail") != ""); err != nil {
			return err
		}
		return seeOther(w, r, back+"?msg=invite")
	}
	return errNotFound
}

type userDeleteData struct {
	basePage
	Users    []db.User
	IDs      string
	Others   []option
	Content  int
	Error    string
	HasSelf  bool
	LastAdm  bool
	Reassign int64
}

// GET /admin/users/delete?ids=1,2
func (a *Admin) userDeletePage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderUserDelete(w, r, c, strings.Split(r.URL.Query().Get("ids"), ","), "")
}

func (a *Admin) renderUserDelete(w http.ResponseWriter, r *http.Request, c *reqCtx, rawIDs []string, errMsg string) error {
	ctx := r.Context()
	d := &userDeleteData{basePage: a.base(ctx, c, "Delete Users", "users-all"), Error: errMsg}
	selected := map[int64]bool{}
	var ids []string
	for _, id := range parseIDs(rawIDs) {
		u, err := db.GetUser(ctx, a.db.R, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if u.ID == c.user.ID {
			d.HasSelf = true
			continue
		}
		n, err := db.UserContentCount(ctx, a.db.R, u.ID)
		if err != nil {
			return err
		}
		d.Content += n
		d.Users = append(d.Users, *u)
		selected[u.ID] = true
		ids = append(ids, strconv.FormatInt(u.ID, 10))
	}
	if len(d.Users) == 0 {
		return seeOther(w, r, "/admin/users/?msg=self")
	}
	d.IDs = strings.Join(ids, ",")
	all, err := db.Users(ctx, a.db.R)
	if err != nil {
		return err
	}
	for _, u := range all {
		if !selected[u.ID] {
			d.Others = append(d.Others, option{Value: strconv.FormatInt(u.ID, 10), Label: u.DisplayName + " (" + u.Login + ")",
				Selected: u.ID == c.user.ID})
		}
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "user-delete", d)
}

// POST /admin/users/delete
func (a *Admin) userDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	rawIDs := strings.Split(r.PostFormValue("ids"), ",")
	if a.needReauth(w, r, c, "/admin/users/delete?ids="+url.QueryEscape(r.PostFormValue("ids"))) {
		return nil
	}
	ids := parseIDs(rawIDs)
	reassign := atoi64(r.PostFormValue("reassign_user"))
	for _, id := range ids {
		if id == c.user.ID || id == reassign {
			return a.renderUserDelete(w, r, c, rawIDs, "You cannot delete your own account or give the content to a deleted user.")
		}
	}
	if reassign != 0 {
		if _, err := db.GetUser(ctx, a.db.R, reassign); err != nil {
			return badRequest("Unknown user.")
		}
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if err := db.DeleteUser(ctx, tx, id, reassign); err != nil {
				return err
			}
		}
		counts, err := db.RoleCounts(ctx, tx)
		if err != nil {
			return err
		}
		if counts[auth.RoleAdministrator] == 0 {
			return errLastAdmin
		}
		return nil
	})
	switch {
	case errors.Is(err, db.ErrUserHasContent):
		return a.renderUserDelete(w, r, c, rawIDs, "Choose a user who gets the content of the deleted users.")
	case errors.Is(err, errLastAdmin):
		return seeOther(w, r, "/admin/users/?msg=last")
	case err != nil:
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "users_deleted", "user", 0, map[string]any{"ids": ids, "reassign": reassign})
	a.site.Invalidate()
	return seeOther(w, r, "/admin/users/?msg=deleted")
}
