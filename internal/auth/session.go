package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
)

// Cookie names. The __Host- prefix makes browsers require Secure, Path=/
// and no Domain, so the cookie cannot be set or overwritten by a subdomain
// or over plain HTTP.
const (
	SessionCookie = "__Host-gopress_session"
	PreCookie     = "__Host-gopress_pre"
)

// Session lifetimes.
const (
	sessionAbs         = 12 * time.Hour
	sessionIdle        = 2 * time.Hour
	rememberAbs        = 14 * 24 * time.Hour
	rememberIdle       = 3 * 24 * time.Hour
	pendingLifetime    = 5 * time.Minute // password ok, second factor pending
	enrollLifetime     = 30 * time.Minute
	touchInterval      = time.Minute
	ReauthWindow       = 10 * time.Minute
	MaxSecondFactorTry = 5
)

// Current is the authenticated request context.
type Current struct {
	Session *db.Session
	User    *db.User
	CSRF    string // token for forms and X-CSRF-Token
}

type ctxKey struct{}

// WithCurrent returns ctx carrying cur.
func WithCurrent(ctx context.Context, cur *Current) context.Context {
	return context.WithValue(ctx, ctxKey{}, cur)
}

// FromContext returns the current session, or nil.
func FromContext(ctx context.Context) *Current {
	cur, _ := ctx.Value(ctxKey{}).(*Current)
	return cur
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// StartSession creates a session for user in state and sets the cookie.
// Any session the request already carries is deleted (no fixation).
func (s *Service) StartSession(ctx context.Context, w http.ResponseWriter, r *http.Request, user *db.User, state string, remember bool) (*Current, error) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		db.DeleteSession(ctx, s.db.W, hashToken(c.Value))
	}
	now := s.now()
	abs, idle := sessionAbs, sessionIdle
	switch {
	case state == db.SessionMFA:
		abs, idle = pendingLifetime, pendingLifetime
	case state == db.SessionEnroll:
		abs, idle = enrollLifetime, enrollLifetime
	case remember:
		abs, idle = rememberAbs, rememberIdle
	}
	token := newToken()
	sess := &db.Session{
		IDHash:    hashToken(token),
		UserID:    user.ID,
		State:     state,
		Created:   now.Unix(),
		LastSeen:  now.Unix(),
		Expires:   now.Add(abs).Unix(),
		IdleSecs:  int64(idle / time.Second),
		Remember:  remember,
		IP:        s.ClientIP(r),
		UserAgent: truncate(r.UserAgent(), 255),
	}
	if state == db.SessionFull {
		// A fresh login counts as a recent authentication.
		sess.ReauthAt = now.Unix()
	}
	if err := db.InsertSession(ctx, s.db.W, sess); err != nil {
		return nil, err
	}
	cookie := &http.Cookie{Name: SessionCookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if remember && state == db.SessionFull {
		cookie.MaxAge = int(abs / time.Second)
	}
	http.SetCookie(w, cookie)
	return &Current{Session: sess, User: user, CSRF: s.csrfToken(sess.IDHash)}, nil
}

// Load returns the valid session of r, or nil. Expired sessions are
// deleted.
func (s *Service) Load(ctx context.Context, r *http.Request) (*Current, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" || len(c.Value) > 100 {
		return nil, nil
	}
	h := hashToken(c.Value)
	sess, err := db.GetSession(ctx, s.db.R, h)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now().Unix()
	if now >= sess.Expires || now >= sess.LastSeen+sess.IdleSecs {
		db.DeleteSession(ctx, s.db.W, h)
		return nil, nil
	}
	user, err := db.GetUser(ctx, s.db.R, sess.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		db.DeleteSession(ctx, s.db.W, h)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if now-sess.LastSeen >= int64(touchInterval/time.Second) {
		if err := db.TouchSession(ctx, s.db.W, h, now); err != nil {
			return nil, err
		}
		sess.LastSeen = now
	}
	return &Current{Session: sess, User: user, CSRF: s.csrfToken(h)}, nil
}

// EndSession deletes the session of cur and clears the cookie.
func (s *Service) EndSession(ctx context.Context, w http.ResponseWriter, cur *Current) error {
	ClearSessionCookie(w)
	if cur == nil {
		return nil
	}
	return db.DeleteSession(ctx, s.db.W, cur.Session.IDHash)
}

// ClearSessionCookie removes the session cookie in the browser.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// RevokeOtherSessions logs the user out everywhere except cur.
func (s *Service) RevokeOtherSessions(ctx context.Context, r *http.Request, cur *Current) error {
	if err := db.DeleteUserSessions(ctx, s.db.W, cur.User.ID, cur.Session.IDHash); err != nil {
		return err
	}
	s.Audit(ctx, r, cur.User.ID, ActSessionsRevoke, "user", cur.User.ID, nil)
	return nil
}

// MarkReauth records a successful re-authentication on the session.
func (s *Service) MarkReauth(ctx context.Context, cur *Current) error {
	now := s.now().Unix()
	cur.Session.ReauthAt = now
	return db.SetSessionReauth(ctx, s.db.W, cur.Session.IDHash, now)
}

// RecentlyAuthenticated reports whether cur re-authenticated within the
// re-authentication window (required for sensitive changes).
func (s *Service) RecentlyAuthenticated(cur *Current) bool {
	return s.now().Unix()-cur.Session.ReauthAt < int64(ReauthWindow/time.Second)
}

// csrfToken derives the CSRF token of a session. It is bound to the
// session, so it changes with every login and cannot be reused across
// sessions or users.
func (s *Service) csrfToken(idHash []byte) string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write(idHash)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCSRF verifies a state-changing request: same origin (Origin /
// Sec-Fetch-Site) and the session's CSRF token from the X-CSRF-Token header
// or the _csrf form field.
func (s *Service) CheckCSRF(r *http.Request, cur *Current) bool {
	if cur == nil || !httpx.StrictSameOrigin(r, s.Home()) {
		return false
	}
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		tok = r.PostFormValue("_csrf")
	}
	return tok != "" && hmac.Equal([]byte(tok), []byte(cur.CSRF))
}

// LoginFormToken returns the CSRF token for the login form, bound to a
// short-lived pre-session cookie (set if missing). It prevents login CSRF
// (an attacker logging the victim into the attacker's account).
func (s *Service) LoginFormToken(w http.ResponseWriter, r *http.Request) string {
	pre := ""
	if c, err := r.Cookie(PreCookie); err == nil && len(c.Value) == 43 {
		pre = c.Value
	} else {
		pre = newToken()
		http.SetCookie(w, &http.Cookie{Name: PreCookie, Value: pre, Path: "/", Secure: true, HttpOnly: true,
			SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	}
	return s.loginToken(pre)
}

func (s *Service) loginToken(pre string) string {
	m := hmac.New(sha256.New, s.loginKey)
	m.Write([]byte(pre))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckLoginForm verifies the login form token and origin.
func (s *Service) CheckLoginForm(r *http.Request) bool {
	if !httpx.StrictSameOrigin(r, s.Home()) {
		return false
	}
	c, err := r.Cookie(PreCookie)
	if err != nil || len(c.Value) != 43 {
		return false
	}
	tok := r.PostFormValue("_csrf")
	if tok == "" {
		tok = r.Header.Get("X-CSRF-Token")
	}
	return tok != "" && hmac.Equal([]byte(tok), []byte(s.loginToken(c.Value)))
}

// preKey returns the pre-session cookie value (for binding login
// ceremonies), or "".
func preKey(r *http.Request) string {
	if c, err := r.Cookie(PreCookie); err == nil && len(c.Value) == 43 {
		return c.Value
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
