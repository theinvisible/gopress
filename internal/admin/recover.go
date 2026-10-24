package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
)

// Lost password and invitation links. The token from the email is moved
// into a cookie right away and the browser redirected to a URL without
// it, so it does not stay in the history, in logs or in Referer headers.
// The cookie is SameSite=Lax because the link is opened from a mail
// client (a cross-site navigation).
const linkCookie = "__Host-gopress_link"

func (a *Admin) registerRecover(mux *http.ServeMux) {
	mux.HandleFunc("GET /login/lost", a.lostPage)
	mux.HandleFunc("POST /login/lost", a.lostSubmit)
	mux.HandleFunc("GET /login/reset/{token}", a.linkLanding("/login/reset"))
	mux.HandleFunc("GET /login/reset", a.resetPage(false))
	mux.HandleFunc("POST /login/reset", a.resetSubmit(false))
	mux.HandleFunc("GET /login/invite/{token}", a.linkLanding("/login/invite"))
	mux.HandleFunc("GET /login/invite", a.resetPage(true))
	mux.HandleFunc("POST /login/invite", a.resetSubmit(true))
}

type resetPageData struct {
	loginPageData
	Invite bool
	Action string
}

// GET /login/lost
func (a *Admin) lostPage(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w, r)
	d := a.loginData(r.Context(), "Lost Password")
	d.CSRF = a.auth.LoginFormToken(w, r)
	a.render(w, "lost-password", d)
}

// POST /login/lost. The answer is the same whether the account exists or
// not, and the lookup and mail happen after the response, so its timing
// does not tell either.
func (a *Admin) lostSubmit(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		a.fail(w, r, nil, badRequest("Invalid request."))
		return
	}
	d := a.loginData(r.Context(), "Lost Password")
	d.Login = strings.TrimSpace(r.PostFormValue("user_login"))
	if !a.auth.CheckLoginForm(r) {
		d.CSRF = a.auth.LoginFormToken(w, r)
		d.Error = "The form has expired. Please try again."
		w.WriteHeader(http.StatusForbidden)
		a.render(w, "lost-password", d)
		return
	}
	if !a.auth.AllowLostPassword(r) {
		d.CSRF = a.auth.LoginFormToken(w, r)
		d.Error = "Too many requests. Please try again later."
		w.WriteHeader(http.StatusTooManyRequests)
		a.render(w, "lost-password", d)
		return
	}
	if d.Login != "" && len(d.Login) <= 254 {
		login, ip := d.Login, a.auth.ClientIP(r)
		a.background(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := a.sendResetLink(ctx, login, ip); err != nil {
				a.log.Error("password reset", "err", err)
			}
		})
	}
	http.Redirect(w, r, "/login?msg=lost", http.StatusSeeOther)
}

func (a *Admin) sendResetLink(ctx context.Context, login, ip string) error {
	u, err := db.UserByLogin(ctx, a.db.R, login)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.auth.ResetRequestTooSoon(ctx, u.ID) {
		return nil
	}
	tok, err := a.auth.NewUserToken(ctx, u, auth.TokenReset, auth.ResetTTL, "")
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, nil, u.ID, "password_reset_requested", "user", u.ID, map[string]any{"ip": ip})
	return a.mail.EnqueueUntil(ctx, u.Email, "password_reset", mail.Data{"Login": u.Login, "IP": ip, "Valid": "one hour",
		"Link": strings.TrimRight(a.auth.Home(), "/") + "/login/reset/" + tok}, time.Now().Add(auth.ResetTTL))
}

// background runs f after the response (synchronously in tests).
func (a *Admin) background(f func()) {
	if a.sync {
		f()
		return
	}
	go f()
}

// linkLanding stores the token of an email link in a cookie and
// redirects to target.
func (a *Admin) linkLanding(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.securityHeaders(w, r)
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.SetCookie(w, &http.Cookie{Name: linkCookie, Value: r.PathValue("token"), Path: "/", Secure: true, HttpOnly: true,
			SameSite: http.SameSiteLaxMode, MaxAge: 3600})
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

func linkToken(r *http.Request) string {
	if c, err := r.Cookie(linkCookie); err == nil {
		return c.Value
	}
	return ""
}

func clearLinkCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: linkCookie, Value: "", Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func purposeOf(invite bool) string {
	if invite {
		return auth.TokenInvite
	}
	return auth.TokenReset
}

// linkInvalid shows the login page with an error.
func (a *Admin) linkInvalid(w http.ResponseWriter, r *http.Request, invite bool) {
	clearLinkCookie(w)
	d := a.loginData(r.Context(), "Log In")
	d.CSRF = a.auth.LoginFormToken(w, r)
	d.RedirectTo = "/admin/"
	d.Error = "This link is invalid or has expired."
	if !invite {
		d.Error += " Please request a new one."
	} else {
		d.Error += " Please ask an administrator for a new invitation."
	}
	d.LostPassword = a.mail.Ready(r.Context())
	w.WriteHeader(http.StatusNotFound)
	a.render(w, "login", d)
}

// GET /login/reset and /login/invite
func (a *Admin) resetPage(invite bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.securityHeaders(w, r)
		u, _, err := a.auth.CheckUserToken(r.Context(), linkToken(r), purposeOf(invite))
		if err != nil {
			a.linkInvalid(w, r, invite)
			return
		}
		a.renderReset(w, r, u, invite, "", http.StatusOK)
	}
}

func (a *Admin) renderReset(w http.ResponseWriter, r *http.Request, u *db.User, invite bool, errMsg string, status int) {
	title, action := "Reset Password", "/login/reset"
	if invite {
		title, action = "Welcome", "/login/invite"
	}
	d := &resetPageData{loginPageData: *a.loginData(r.Context(), title), Invite: invite, Action: action}
	d.CSRF = a.auth.LoginFormToken(w, r)
	d.Login, d.Error = u.Login, errMsg
	w.WriteHeader(status)
	a.render(w, "reset-password", d)
}

// POST /login/reset and /login/invite: set the password. A reset ends all
// sessions and lifts login locks; the user then logs in normally,
// including the second factor. An invitation continues directly with
// the two-factor setup (the link stands in for the setup code).
func (a *Admin) resetSubmit(invite bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.securityHeaders(w, r)
		ctx := r.Context()
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			a.fail(w, r, nil, badRequest("Invalid request."))
			return
		}
		tok := linkToken(r)
		u, _, err := a.auth.CheckUserToken(ctx, tok, purposeOf(invite))
		if err != nil {
			a.linkInvalid(w, r, invite)
			return
		}
		if !a.auth.CheckLoginForm(r) {
			a.renderReset(w, r, u, invite, "The form has expired. Please try again.", http.StatusForbidden)
			return
		}
		pw := r.PostFormValue("pass1")
		if pw != r.PostFormValue("pass2") {
			a.renderReset(w, r, u, invite, "The passwords do not match.", http.StatusBadRequest)
			return
		}
		if err := auth.CheckPasswordPolicy(pw, u.Login, u.Email); err != nil {
			a.renderReset(w, r, u, invite, err.Error(), http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(ctx, pw)
		if err != nil {
			a.fail(w, r, nil, err)
			return
		}
		if u, _, err = a.auth.UseUserToken(ctx, tok, purposeOf(invite)); err != nil {
			a.linkInvalid(w, r, invite)
			return
		}
		err = a.db.Tx(ctx, func(tx *sql.Tx) error {
			if err := db.SetUserPassHash(ctx, tx, u.ID, hash); err != nil {
				return err
			}
			if err := db.DeleteUserTokens(ctx, tx, u.ID, auth.TokenReset); err != nil {
				return err
			}
			if _, err := db.DeleteAccessTokens(ctx, tx, u.ID); err != nil {
				return err
			}
			return db.DeleteUserSessions(ctx, tx, u.ID, nil)
		})
		if err == nil {
			err = auth.UnlockPassword(ctx, a.db, u.ID)
		}
		if err != nil {
			a.fail(w, r, nil, err)
			return
		}
		clearLinkCookie(w)
		if !invite {
			a.auth.Audit(ctx, r, u.ID, auth.ActPasswordReset, "user", u.ID, nil)
			http.Redirect(w, r, "/login?msg=reset", http.StatusSeeOther)
			return
		}
		a.auth.Audit(ctx, r, u.ID, "invitation_accepted", "user", u.ID, nil)
		has, err := a.auth.HasSecondFactor(ctx, u.ID)
		if err != nil {
			a.fail(w, r, nil, err)
			return
		}
		state, next := db.SessionSetup, "/admin/setup-2fa"
		if has {
			state, next = db.SessionMFA, "/login/2fa"
		}
		if _, err := a.auth.StartSession(ctx, w, r, u, state, false); err != nil {
			a.fail(w, r, nil, err)
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}
