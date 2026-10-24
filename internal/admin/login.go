package admin

import (
	"context"
	"encoding/hex"
	"errors"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

// loginPageData is the data of the login screens.
type loginPageData struct {
	Title      string
	SiteName   string
	Home       string
	Assets     *assets
	CSRF       string
	Error      string
	Message    string
	Login      string
	RedirectTo string
	// LostPassword shows the "Lost your password?" link (mail works).
	LostPassword bool
	// Second factor step.
	HasTOTP    bool
	HasPasskey bool
}

func (a *Admin) loginData(ctx context.Context, title string) *loginPageData {
	d := &loginPageData{Title: title, SiteName: "GoPress", Home: "/", Assets: a.assets}
	if s, err := a.site.Settings(ctx); err == nil {
		d.SiteName, d.Home = html.UnescapeString(s.Name), s.Home+"/"
	}
	return d
}

var loginMessages = map[string]string{
	"loggedout": "You are now logged out.",
	"expired":   "Your login has expired. Please log in again.",
	"tries":     "Too many failed attempts. Please log in again.",
	"lost":      "If an account with that username or email address exists, an email with a link to choose a new password is on its way. Check your inbox.",
	"reset":     "Your password was changed. Please log in.",
}

// GET /login
func (a *Admin) loginPage(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w, r)
	ctx := r.Context()
	if cur, _ := a.auth.Load(ctx, r); cur != nil && cur.Session.State == db.SessionFull {
		http.Redirect(w, r, safeRedirect(r.URL.Query().Get("redirect_to")), http.StatusFound)
		return
	}
	d := a.loginData(ctx, "Log In")
	d.CSRF = a.auth.LoginFormToken(w, r)
	d.RedirectTo = safeRedirect(r.URL.Query().Get("redirect_to"))
	d.Message = loginMessages[r.URL.Query().Get("msg")]
	d.LostPassword = a.mail.Ready(ctx)
	a.render(w, "login", d)
}

// POST /login: first factor (password).
func (a *Admin) loginSubmit(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w, r)
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		a.fail(w, r, nil, badRequest("Invalid request."))
		return
	}
	d := a.loginData(ctx, "Log In")
	d.RedirectTo = safeRedirect(r.PostFormValue("redirect_to"))
	d.Login = strings.TrimSpace(r.PostFormValue("log"))
	if !a.auth.CheckLoginForm(r) {
		d.CSRF = a.auth.LoginFormToken(w, r)
		d.Error = "The login form has expired. Please try again."
		w.WriteHeader(http.StatusForbidden)
		a.render(w, "login", d)
		return
	}
	user, err := a.auth.Authenticate(ctx, r, d.Login, r.PostFormValue("pwd"))
	if err != nil {
		if !errors.Is(err, auth.ErrLoginFailed) {
			a.fail(w, r, nil, err)
			return
		}
		d.CSRF = a.auth.LoginFormToken(w, r)
		d.Error = auth.LoginFailedMessage
		w.WriteHeader(http.StatusUnauthorized)
		a.render(w, "login", d)
		return
	}
	remember := r.PostFormValue("rememberme") != ""
	has, err := a.auth.HasSecondFactor(ctx, user.ID)
	if err != nil {
		a.fail(w, r, nil, err)
		return
	}
	state, next := db.SessionMFA, "/login/2fa?redirect_to="+urlQueryEscape(d.RedirectTo)
	if !has {
		state, next = db.SessionEnroll, "/admin/setup-2fa"
	}
	if _, err := a.auth.StartSession(ctx, w, r, user, state, remember); err != nil {
		a.fail(w, r, nil, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// POST /login/passkey/begin: usernameless passkey login (both factors).
func (a *Admin) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !a.auth.CheckLoginForm(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "The login form has expired. Please reload the page."})
		return
	}
	opts, err := a.auth.BeginPasskeyLogin(r.Context(), r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Passkey sign-in is not available."})
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// POST /login/passkey/finish
func (a *Admin) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w, r)
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !a.auth.CheckLoginForm(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "The login form has expired. Please reload the page."})
		return
	}
	user, err := a.auth.FinishPasskeyLogin(ctx, r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The passkey could not be verified."})
		return
	}
	if has, err := a.auth.HasSecondFactor(ctx, user.ID); err != nil || !has {
		// A passkey without any factor left cannot happen; be strict.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The passkey could not be verified."})
		return
	}
	if _, err := a.auth.StartSession(ctx, w, r, user, db.SessionFull, r.URL.Query().Get("remember") == "1"); err != nil {
		a.fail(w, r, nil, err)
		return
	}
	a.auth.IssueDeviceCookie(w, user.ID)
	a.auth.Audit(ctx, r, user.ID, auth.ActLogin, "user", user.ID, map[string]any{"method": "passkey"})
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeRedirect(r.URL.Query().Get("redirect_to"))})
}

// GET /login/2fa
func (a *Admin) secondFactorPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	d, err := a.secondFactorData(r.Context(), c)
	if err != nil {
		return err
	}
	d.RedirectTo = safeRedirect(r.URL.Query().Get("redirect_to"))
	return a.render(w, "login-2fa", d)
}

func (a *Admin) secondFactorData(ctx context.Context, c *reqCtx) (*loginPageData, error) {
	d := a.loginData(ctx, "Two-Factor Authentication")
	d.CSRF = c.cur.CSRF
	var err error
	if d.HasTOTP, err = a.auth.HasTOTP(ctx, c.user.ID); err != nil {
		return nil, err
	}
	keys, err := a.auth.Passkeys(ctx, c.user.ID)
	d.HasPasskey = len(keys) > 0
	return d, err
}

// POST /login/2fa: TOTP or recovery code.
func (a *Admin) secondFactorSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	method := "totp"
	if r.PostFormValue("recovery_code") != "" {
		method = "recovery_code"
	}
	if stop, err := a.reserveSecondFactor(w, r, c, method); stop || err != nil {
		return err
	}
	ok := false
	var err error
	if code := r.PostFormValue("recovery_code"); code != "" {
		ok, err = a.auth.UseRecoveryCode(ctx, c.user.ID, code)
	} else {
		ok, err = a.auth.VerifyTOTP(ctx, c.user.ID, r.PostFormValue("code"))
	}
	if err != nil {
		return err
	}
	redirectTo := safeRedirect(r.PostFormValue("redirect_to"))
	if !ok {
		if err := a.secondFactorFailed(ctx, r, c, method); err != nil {
			return err
		}
		d, err := a.secondFactorData(ctx, c)
		if err != nil {
			return err
		}
		d.RedirectTo = redirectTo
		d.Error = "The code is incorrect or was already used."
		w.WriteHeader(http.StatusUnauthorized)
		return a.render(w, "login-2fa", d)
	}
	if method == "recovery_code" {
		a.auth.Audit(ctx, r, c.user.ID, auth.ActRecoveryUsed, "user", c.user.ID, nil)
	}
	return a.completeLogin(w, r, c, method, redirectTo)
}

// reserveSecondFactor counts an attempt before the factor is checked
// (parallel requests cannot exceed the limits). When the limits are reached
// a pending login is discarded and the user starts over with the password;
// it reports stop=true when it has answered the request.
func (a *Admin) reserveSecondFactor(w http.ResponseWriter, r *http.Request, c *reqCtx, method string) (bool, error) {
	ctx := r.Context()
	err := a.auth.ReserveSecondFactor(ctx, c.cur)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, auth.ErrTooManyAttempts) {
		return true, err
	}
	a.auth.Audit(ctx, r, c.user.ID, auth.ActLoginLocked, "user", c.user.ID, map[string]any{"second_factor": method})
	if c.cur.Session.State != db.SessionMFA {
		return true, &httpError{http.StatusTooManyRequests, "Too many failed attempts. Please try again later."}
	}
	a.auth.EndSession(ctx, w, c.cur)
	if isAPI(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Too many failed attempts.", "redirect": "/login?msg=tries"})
	} else {
		http.Redirect(w, r, "/login?msg=tries", http.StatusSeeOther)
	}
	return true, nil
}

// secondFactorFailed records a failed second factor.
func (a *Admin) secondFactorFailed(ctx context.Context, r *http.Request, c *reqCtx, method string) error {
	a.auth.Audit(ctx, r, c.user.ID, auth.ActMFAFailed, "user", c.user.ID, map[string]any{"method": method})
	return a.auth.SecondFactorFailed(ctx, c.user.ID)
}

// secondFactorOK clears the failure counter; failed attempts since the
// last success are shown to the user once (someone else may know the
// password).
func (a *Admin) secondFactorOK(ctx context.Context, userID int64) error {
	failed, err := a.auth.SecondFactorSucceeded(ctx, userID)
	if err != nil || failed == 0 {
		return err
	}
	return db.SetUserMeta(ctx, a.db.W, userID, metaMFAWarning, strconv.Itoa(failed))
}

const metaMFAWarning = "mfa_failures_unseen"

// completeLogin replaces the pending session by a full one.
func (a *Admin) completeLogin(w http.ResponseWriter, r *http.Request, c *reqCtx, method, redirectTo string) error {
	ctx := r.Context()
	if err := a.secondFactorOK(ctx, c.user.ID); err != nil {
		return err
	}
	if _, err := a.auth.StartSession(ctx, w, r, c.user, db.SessionFull, c.cur.Session.Remember); err != nil {
		return err
	}
	a.auth.IssueDeviceCookie(w, c.user.ID)
	a.auth.Audit(ctx, r, c.user.ID, auth.ActLogin, "user", c.user.ID, map[string]any{"method": "password+" + method})
	if isAPI(r) {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": redirectTo})
		return nil
	}
	return seeOther(w, r, redirectTo)
}

// POST /login/2fa/passkey/begin
func (a *Admin) secondFactorPasskeyBegin(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	opts, err := a.auth.BeginPasskeyAssertion(r.Context(), c.cur)
	if err != nil {
		return badRequest("No passkey is available for this account.")
	}
	writeJSON(w, http.StatusOK, opts)
	return nil
}

// POST /login/2fa/passkey/finish
func (a *Admin) secondFactorPasskeyFinish(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if stop, err := a.reserveSecondFactor(w, r, c, "passkey"); stop || err != nil {
		return err
	}
	if err := a.auth.FinishPasskeyAssertion(r.Context(), c.cur, r); err != nil {
		if err := a.secondFactorFailed(r.Context(), r, c, "passkey"); err != nil {
			return err
		}
		return &httpError{http.StatusUnauthorized, "The passkey could not be verified."}
	}
	return a.completeLogin(w, r, c, "passkey", safeRedirect(r.URL.Query().Get("redirect_to")))
}

// POST /logout
func (a *Admin) logout(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	a.auth.EndSession(r.Context(), w, c.cur)
	a.auth.Audit(r.Context(), r, c.user.ID, auth.ActLogout, "user", c.user.ID, nil)
	return seeOther(w, r, "/login?msg=loggedout")
}

// enrollPageData is the two-factor setup screen.
type enrollPageData struct {
	basePage
	Required bool // setup is mandatory before the admin can be used
	HasTOTP  bool
	Passkeys []db.WebAuthnCredential
	TOTP     *auth.TOTPSetup
	// QR is the TOTP QR code as data URI. html/template refuses data:
	// URLs in src; this one is a PNG generated by the server.
	QR    template.URL
	Error string
}

// GET /admin/setup-2fa
func (a *Admin) enrollPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderEnroll(w, r, c, "")
}

func (a *Admin) renderEnroll(w http.ResponseWriter, r *http.Request, c *reqCtx, errMsg string) error {
	ctx := r.Context()
	d := &enrollPageData{basePage: a.base(ctx, c, "Two-Factor Authentication", "security"), Error: errMsg}
	d.Required = c.cur.Session.State != db.SessionFull
	if c.cur.Session.State == db.SessionEnroll {
		// First the one-time setup code.
		if errMsg != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		return a.render(w, "setup-code", d)
	}
	d.Scripts = []string{"passkey.js"}
	var err error
	if d.HasTOTP, err = a.auth.HasTOTP(ctx, c.user.ID); err != nil {
		return err
	}
	if d.Passkeys, err = a.auth.Passkeys(ctx, c.user.ID); err != nil {
		return err
	}
	if !d.HasTOTP {
		if d.TOTP, err = a.auth.NewTOTPSetup(c.user); err != nil {
			return err
		}
		if strings.HasPrefix(d.TOTP.QR, "data:image/png;base64,") {
			d.QR = template.URL(d.TOTP.QR)
		}
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "setup-2fa", d)
}

// POST /admin/setup-2fa/code: the one-time setup code issued by an
// administrator or the CLI unlocks the enrollment.
func (a *Admin) enrollCode(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if c.cur.Session.State != db.SessionEnroll {
		return seeOther(w, r, "/admin/setup-2fa")
	}
	ok, err := a.auth.CheckSetupCode(ctx, c.user.ID, r.PostFormValue("setup_code"))
	if err != nil {
		return err
	}
	if !ok {
		a.auth.Audit(ctx, r, c.user.ID, auth.ActMFAFailed, "user", c.user.ID, map[string]any{"method": "setup_code"})
		return a.renderEnroll(w, r, c, "The setup code is incorrect or has expired. Ask an administrator for a new one.")
	}
	if _, err := a.auth.StartSession(ctx, w, r, c.user, db.SessionSetup, c.cur.Session.Remember); err != nil {
		return err
	}
	return seeOther(w, r, "/admin/setup-2fa")
}

// POST /admin/setup-2fa/totp
func (a *Admin) enrollTOTP(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if c.cur.Session.State == db.SessionFull && !a.auth.RecentlyAuthenticated(c.cur) {
		return seeOther(w, r, "/admin/reauth?next="+urlQueryEscape("/admin/setup-2fa"))
	}
	if has, err := a.auth.HasTOTP(ctx, c.user.ID); err != nil || has {
		return err
	}
	if err := a.auth.EnableTOTP(ctx, c.user, r.PostFormValue("token"), r.PostFormValue("code")); err != nil {
		if errors.Is(err, auth.ErrTOTPExists) {
			return seeOther(w, r, "/admin/profile/security")
		}
		if errors.Is(err, auth.ErrInvalidCode) {
			return a.renderEnroll(w, r, c, "The code is incorrect or the setup has expired. Please scan the new QR code and try again.")
		}
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, auth.ActTOTPAdded, "user", c.user.ID, nil)
	next, err := a.afterEnroll(w, r, c)
	if err != nil {
		return err
	}
	return seeOther(w, r, next)
}

// POST /admin/setup-2fa/passkey/begin
func (a *Admin) enrollPasskeyBegin(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if c.cur.Session.State == db.SessionFull && !a.auth.RecentlyAuthenticated(c.cur) {
		return &httpError{http.StatusForbidden, "Please confirm your identity first."}
	}
	opts, err := a.auth.BeginPasskeyRegistration(r.Context(), c.cur)
	if err != nil {
		a.log.Info("passkey registration", "err", err)
		return badRequest("Passkeys are not available on this site address. Use the site's host name (not an IP address) and HTTPS.")
	}
	writeJSON(w, http.StatusOK, opts)
	return nil
}

// POST /admin/setup-2fa/passkey/finish?name=…
func (a *Admin) enrollPasskeyFinish(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := a.auth.FinishPasskeyRegistration(ctx, c.cur, r, r.URL.Query().Get("name")); err != nil {
		return badRequest("The passkey could not be registered.")
	}
	a.auth.Audit(ctx, r, c.user.ID, auth.ActPasskeyAdded, "user", c.user.ID, nil)
	next, err := a.afterEnroll(w, r, c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": next})
	return nil
}

// afterEnroll finishes a mandatory setup: the session becomes a full one
// and the first recovery codes are generated. It returns the next page.
func (a *Admin) afterEnroll(w http.ResponseWriter, r *http.Request, c *reqCtx) (string, error) {
	ctx := r.Context()
	if c.cur.Session.State != db.SessionSetup {
		return "/admin/profile/security?msg=factor-added", nil
	}
	cur, err := a.auth.StartSession(ctx, w, r, c.user, db.SessionFull, c.cur.Session.Remember)
	if err != nil {
		return "", err
	}
	// Any other session from before the first factor ends here.
	if err := db.DeleteUserSessions(ctx, a.db.W, c.user.ID, cur.Session.IDHash); err != nil {
		return "", err
	}
	a.auth.IssueDeviceCookie(w, c.user.ID)
	a.auth.Audit(ctx, r, c.user.ID, auth.ActLogin, "user", c.user.ID, map[string]any{"method": "password+enrollment"})
	if left, err := db.RecoveryCodesLeft(ctx, a.db.R, c.user.ID); err != nil || left > 0 {
		return "/admin/", err
	}
	codes, err := a.auth.NewRecoveryCodes(ctx, c.user.ID)
	if err != nil {
		return "", err
	}
	a.auth.Audit(ctx, r, c.user.ID, auth.ActRecoveryNew, "user", c.user.ID, nil)
	a.flash.put("codes:"+hex.EncodeToString(cur.Session.IDHash), codes)
	return "/admin/recovery-codes", nil
}

type recoveryPageData struct {
	basePage
	Codes []string
}

// GET /admin/recovery-codes: shows freshly generated codes exactly once.
func (a *Admin) recoveryCodesPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	v, ok := a.flash.take("codes:" + hex.EncodeToString(c.cur.Session.IDHash))
	if !ok {
		return seeOther(w, r, "/admin/profile/security")
	}
	d := &recoveryPageData{basePage: a.base(r.Context(), c, "Recovery Codes", "security"), Codes: v.([]string)}
	return a.render(w, "recovery-codes", d)
}

type reauthPageData struct {
	basePage
	Next       string
	Error      string
	HasTOTP    bool
	HasPasskey bool
}

func safeNext(next string) string { return safeRedirect(next) }

// GET /admin/reauth?next=…: confirm identity before sensitive changes.
func (a *Admin) reauthPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderReauth(w, r, c, safeNext(r.URL.Query().Get("next")), "")
}

func (a *Admin) renderReauth(w http.ResponseWriter, r *http.Request, c *reqCtx, next, errMsg string) error {
	ctx := r.Context()
	d := &reauthPageData{basePage: a.base(ctx, c, "Confirm Your Identity", "security"), Next: next, Error: errMsg}
	d.Scripts = []string{"passkey.js"}
	var err error
	if d.HasTOTP, err = a.auth.HasTOTP(ctx, c.user.ID); err != nil {
		return err
	}
	keys, err := a.auth.Passkeys(ctx, c.user.ID)
	if err != nil {
		return err
	}
	d.HasPasskey = len(keys) > 0
	if errMsg != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	return a.render(w, "reauth", d)
}

// POST /admin/reauth: password plus TOTP (or recovery code).
func (a *Admin) reauthSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	next := safeNext(r.PostFormValue("next"))
	// Same rate limits and lockout as the login.
	if _, err := a.auth.Authenticate(ctx, r, c.user.Login, r.PostFormValue("pwd")); err != nil {
		if errors.Is(err, auth.ErrLoginFailed) {
			return a.renderReauth(w, r, c, next, "The password or code is incorrect.")
		}
		return err
	}
	if stop, err := a.reserveSecondFactor(w, r, c, "reauth"); stop || err != nil {
		return err
	}
	var ok bool
	var err error
	if code := r.PostFormValue("recovery_code"); code != "" {
		ok, err = a.auth.UseRecoveryCode(ctx, c.user.ID, code)
	} else {
		ok, err = a.auth.VerifyTOTP(ctx, c.user.ID, r.PostFormValue("code"))
	}
	if err != nil {
		return err
	}
	if !ok {
		if err := a.secondFactorFailed(ctx, r, c, "reauth"); err != nil {
			return err
		}
		return a.renderReauth(w, r, c, next, "The password or code is incorrect.")
	}
	if err := a.secondFactorOK(ctx, c.user.ID); err != nil {
		return err
	}
	if err := a.auth.MarkReauth(ctx, c.cur); err != nil {
		return err
	}
	return seeOther(w, r, next)
}

// POST /admin/reauth/passkey/begin
func (a *Admin) reauthPasskeyBegin(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.secondFactorPasskeyBegin(w, r, c)
}

// POST /admin/reauth/passkey/finish
func (a *Admin) reauthPasskeyFinish(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if stop, err := a.reserveSecondFactor(w, r, c, "reauth-passkey"); stop || err != nil {
		return err
	}
	if err := a.auth.FinishPasskeyAssertion(ctx, c.cur, r); err != nil {
		if err := a.secondFactorFailed(ctx, r, c, "reauth-passkey"); err != nil {
			return err
		}
		return &httpError{http.StatusUnauthorized, "The passkey could not be verified."}
	}
	if err := a.secondFactorOK(ctx, c.user.ID); err != nil {
		return err
	}
	if err := a.auth.MarkReauth(ctx, c.cur); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(r.URL.Query().Get("next"))})
	return nil
}
