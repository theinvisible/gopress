// Package admin is the GoPress administration area: login with mandatory
// two-factor authentication, dashboard, posts and pages, comment moderation
// and the user's profile. It follows the structure of wp-admin and uses its
// stylesheets.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/content"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
	"github.com/theinvisible/gopress/internal/site"
)

// Options configures the admin.
type Options struct {
	Log *slog.Logger
}

// Admin serves /admin/ and the login pages.
type Admin struct {
	db      *db.DB
	auth    *auth.Service
	site    *site.Site
	content *content.Service
	log     *slog.Logger
	tpl     *templates
	assets  *assets
	flash   *flashStore
}

// New creates the admin.
func New(d *db.DB, a *auth.Service, s *site.Site, cs *content.Service, opts Options) (*Admin, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	ad := &Admin{db: d, auth: a, site: s, content: cs, log: opts.Log, flash: newFlashStore()}
	var err error
	if ad.assets, err = loadAssets(); err != nil {
		return nil, err
	}
	if ad.tpl, err = loadTemplates(ad.assets); err != nil {
		return nil, err
	}
	return ad, nil
}

// Register adds the admin routes to mux.
func (a *Admin) Register(mux *http.ServeMux) {
	mux.Handle("GET /admin/static/", a.assets)

	// Login and two-factor authentication.
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("POST /login/passkey/begin", a.passkeyLoginBegin)
	mux.HandleFunc("POST /login/passkey/finish", a.passkeyLoginFinish)
	mux.Handle("GET /login/2fa", a.pending(a.secondFactorPage))
	mux.Handle("POST /login/2fa", a.pending(a.secondFactorSubmit))
	mux.Handle("POST /login/2fa/passkey/begin", a.pending(a.secondFactorPasskeyBegin))
	mux.Handle("POST /login/2fa/passkey/finish", a.pending(a.secondFactorPasskeyFinish))
	mux.Handle("POST /logout", a.anySession(a.logout))

	// WordPress addresses: bookmarks keep working, bots posting to
	// wp-login.php get nothing.
	mux.HandleFunc("GET /wp-login.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})
	mux.HandleFunc("GET /wp-admin/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})

	// Two-factor enrollment (also reachable before it is complete).
	mux.Handle("GET /admin/setup-2fa", a.enrolling(a.enrollPage))
	mux.Handle("POST /admin/setup-2fa/totp", a.enrolling(a.enrollTOTP))
	mux.Handle("POST /admin/setup-2fa/passkey/begin", a.enrolling(a.enrollPasskeyBegin))
	mux.Handle("POST /admin/setup-2fa/passkey/finish", a.enrolling(a.enrollPasskeyFinish))
	mux.Handle("GET /admin/recovery-codes", a.page(auth.CapRead, a.recoveryCodesPage))

	// Re-authentication for sensitive actions.
	mux.Handle("GET /admin/reauth", a.page(auth.CapRead, a.reauthPage))
	mux.Handle("POST /admin/reauth", a.page(auth.CapRead, a.reauthSubmit))
	mux.Handle("POST /admin/reauth/passkey/begin", a.page(auth.CapRead, a.reauthPasskeyBegin))
	mux.Handle("POST /admin/reauth/passkey/finish", a.page(auth.CapRead, a.reauthPasskeyFinish))

	mux.Handle("GET /admin/{$}", a.page(auth.CapRead, a.dashboard))
	mux.Handle("POST /admin/quick-draft", a.page(auth.CapEditPosts, a.quickDraft))

	a.registerPosts(mux)
	a.registerComments(mux)
	a.registerProfile(mux)

	mux.Handle("/admin/", a.page(auth.CapRead, func(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
		return errNotFound
	}))
}

// reqCtx is the per-request admin context.
type reqCtx struct {
	cur      *auth.Current
	user     *db.User
	settings *site.Settings
	links    *site.Links
}

type handler func(w http.ResponseWriter, r *http.Request, c *reqCtx) error

// httpError is an error with a status code and a message for the user.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

var (
	errNotFound  = &httpError{http.StatusNotFound, "The requested page does not exist."}
	errForbidden = &httpError{http.StatusForbidden, "Sorry, you are not allowed to do that."}
	errBadCSRF   = &httpError{http.StatusForbidden, "The link you followed has expired. Please go back, reload the page and try again."}
)

func badRequest(msg string) error { return &httpError{http.StatusBadRequest, msg} }

// securityHeaders hardens every admin and login response: no caching, a
// strict CSP (no inline scripts, no framing) and no referrer leaking admin
// URLs to other sites.
func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	httpx.NoStore(w)
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
		"img-src 'self' data: blob: https://secure.gravatar.com; font-src 'self' data:; connect-src 'self'; "+
		"frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-Robots-Tag", "noindex, nofollow")
}

const maxFormBytes = 4 << 20 // post content can be large

// serve runs h with the common checks. allow decides whether the session
// state may access the handler; cap is checked for full sessions.
func (a *Admin) serve(w http.ResponseWriter, r *http.Request, allow func(*auth.Current) bool, capability string, h handler) {
	securityHeaders(w)
	ctx := r.Context()
	cur, err := a.auth.Load(ctx, r)
	if err != nil {
		a.fail(w, r, nil, err)
		return
	}
	if cur == nil {
		if r.Method == http.MethodGet && !isAPI(r) {
			http.Redirect(w, r, "/login?redirect_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		a.fail(w, r, nil, &httpError{http.StatusUnauthorized, "Your session has expired. Please log in again."})
		return
	}
	if !allow(cur) {
		switch cur.Session.State {
		case db.SessionMFA:
			http.Redirect(w, r, "/login/2fa", http.StatusFound)
		case db.SessionEnroll:
			http.Redirect(w, r, "/admin/setup-2fa", http.StatusFound)
		default:
			http.Redirect(w, r, "/admin/", http.StatusFound)
		}
		return
	}
	c := &reqCtx{cur: cur, user: cur.User}
	if c.settings, err = a.site.Settings(ctx); err != nil {
		a.fail(w, r, c, err)
		return
	}
	if c.links, err = a.site.Links(ctx); err != nil {
		a.fail(w, r, c, err)
		return
	}
	if cur.Session.State == db.SessionFull && capability != "" && !canAny(cur.User, capability) {
		a.fail(w, r, c, errForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if !a.auth.CheckCSRF(r, cur) {
			a.fail(w, r, c, errBadCSRF)
			return
		}
	}
	if err := h(w, r, c); err != nil {
		a.fail(w, r, c, err)
	}
}

// canAny checks "cap" or "cap1|cap2" (any of them).
func canAny(u *db.User, caps string) bool {
	for _, c := range strings.Split(caps, "|") {
		if auth.Can(u, c) {
			return true
		}
	}
	return false
}

func isFull(cur *auth.Current) bool { return cur.Session.State == db.SessionFull }

// page wraps an admin page that needs a complete login and capability.
func (a *Admin) page(capability string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.serve(w, r, isFull, capability, h)
	})
}

// enrolling wraps the two-factor setup pages: reachable while the setup is
// pending and later from the profile.
func (a *Admin) enrolling(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.serve(w, r, func(cur *auth.Current) bool {
			return cur.Session.State == db.SessionEnroll || cur.Session.State == db.SessionFull
		}, auth.CapRead, h)
	})
}

// pending wraps the second-factor step of the login.
func (a *Admin) pending(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		cur, err := a.auth.Load(r.Context(), r)
		if err != nil {
			a.fail(w, r, nil, err)
			return
		}
		if cur == nil || cur.Session.State != db.SessionMFA {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusFound)
			} else {
				a.fail(w, r, nil, &httpError{http.StatusUnauthorized, "Your login has expired. Please log in again."})
			}
			return
		}
		a.serve(w, r, func(c *auth.Current) bool { return c.Session.State == db.SessionMFA }, "", h)
	})
}

// anySession wraps handlers that work in every session state (logout).
func (a *Admin) anySession(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.serve(w, r, func(*auth.Current) bool { return true }, "", h)
	})
}

func isAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/admin/api/") || strings.HasSuffix(r.URL.Path, "/begin") ||
		strings.HasSuffix(r.URL.Path, "/finish") || r.Header.Get("Accept") == "application/json"
}

// fail renders an error page (or JSON for API requests).
func (a *Admin) fail(w http.ResponseWriter, r *http.Request, c *reqCtx, err error) {
	var he *httpError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &he):
	case errors.As(err, &mbe):
		he = &httpError{http.StatusRequestEntityTooLarge, "The submitted data is too large."}
	case errors.Is(err, context.Canceled):
		return
	default:
		a.log.Error("admin", "path", r.URL.Path, "err", err)
		he = &httpError{http.StatusInternalServerError, "Something went wrong. Please try again."}
	}
	if isAPI(r) {
		writeJSON(w, he.code, map[string]string{"error": he.msg})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(he.code)
	a.tpl.render(w, "error", &errorPage{Message: he.msg, Assets: a.assets})
}

type errorPage struct {
	Message string
	Assets  *assets
}

// flashStore keeps one-time data between a POST and the following GET of
// the same session (e.g. new recovery codes). Entries expire after a few
// minutes and are removed when read.
type flashStore struct {
	mu sync.Mutex
	m  map[string]flashEntry
}

type flashEntry struct {
	v       any
	expires time.Time
}

func newFlashStore() *flashStore { return &flashStore{m: map[string]flashEntry{}} }

func (f *flashStore) put(key string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for k, e := range f.m {
		if now.After(e.expires) {
			delete(f.m, k)
		}
	}
	f.m[key] = flashEntry{v: v, expires: now.Add(5 * time.Minute)}
}

func (f *flashStore) take(key string) (any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.m[key]
	delete(f.m, key)
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.v, true
}
