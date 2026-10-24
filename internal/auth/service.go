package auth

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
)

// Options configures the auth service.
type Options struct {
	// Secret is the site secret (data/secret.key); all keys are derived
	// from it.
	Secret []byte
	// Home returns the site's base URL (scheme://host), used for origin
	// checks and as the WebAuthn relying party.
	Home           func() string
	TrustedProxies []netip.Prefix
	SiteName       func() string
	Log            *slog.Logger
	// Notify, if set, sends a security notice to a user's email address
	// (template data: Login, Event, When, IP).
	Notify func(ctx context.Context, to string, data map[string]any)
}

// Service is the authentication service.
type Service struct {
	db   *db.DB
	opts Options
	log  *slog.Logger
	now  func() time.Time

	csrfKey  []byte
	loginKey []byte
	totpKey  []byte
	recovKey []byte

	ipLimit      *ipLimiter
	netLimit     *ipLimiter
	passkeyLimit *ipLimiter
	lostLimit    *ipLimiter
	ceremonies   *ceremonyStore
	deviceKey    []byte
}

// New creates the service.
func New(d *db.DB, opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.SiteName == nil {
		opts.SiteName = func() string { return "GoPress" }
	}
	s := &Service{db: d, opts: opts, log: opts.Log, now: time.Now}
	s.csrfKey = deriveKey(opts.Secret, "gopress csrf")
	s.loginKey = deriveKey(opts.Secret, "gopress login form")
	s.totpKey = deriveKey(opts.Secret, "gopress totp secret encryption")
	s.recovKey = deriveKey(opts.Secret, "gopress recovery codes")
	s.deviceKey = deriveKey(opts.Secret, "gopress device cookie")
	s.ipLimit = newIPLimiter(loginIPMax, loginIPWindow)
	s.netLimit = newIPLimiter(loginNetMax, loginIPWindow)
	s.passkeyLimit = newIPLimiter(passkeyBeginMax, loginIPWindow)
	s.lostLimit = newIPLimiter(lostIPMax, lostIPWindow)
	s.ceremonies = newCeremonyStore()
	return s
}

func deriveKey(secret []byte, purpose string) []byte {
	k, err := hkdf.Key(sha256.New, secret, nil, purpose, 32)
	if err != nil {
		panic(err)
	}
	return k
}

// ClientIP returns the client address of r.
func (s *Service) ClientIP(r *http.Request) string {
	return httpx.ClientIP(r, s.opts.TrustedProxies)
}

// Home returns the site base URL.
func (s *Service) Home() string { return s.opts.Home() }

// Audit actions.
const (
	ActLogin          = "login"
	ActLoginFailed    = "login_failed"
	ActLoginLocked    = "login_locked"
	ActMFAFailed      = "mfa_failed"
	ActLogout         = "logout"
	ActPasswordChange = "password_changed"
	ActTOTPAdded      = "totp_added"
	ActTOTPRemoved    = "totp_removed"
	ActPasskeyAdded   = "passkey_added"
	ActPasskeyRemoved = "passkey_removed"
	ActRecoveryNew    = "recovery_codes_generated"
	ActRecoveryUsed   = "recovery_code_used"
	ActSessionsRevoke = "sessions_revoked"
	ActProfileUpdate  = "profile_updated"
	Act2FAReset       = "2fa_reset"
	ActUserUnlocked   = "user_unlocked"
	ActPasswordReset  = "password_reset"
)

var actionLabels = map[string]string{
	ActLogin: "Logged in", ActLoginFailed: "Failed login", ActLoginLocked: "Login blocked (too many attempts)",
	ActMFAFailed: "Wrong second factor", ActLogout: "Logged out", ActPasswordChange: "Password changed",
	ActTOTPAdded: "Authenticator app added", ActTOTPRemoved: "Authenticator app removed",
	ActPasskeyAdded: "Passkey added", ActPasskeyRemoved: "Passkey removed",
	ActRecoveryNew: "Recovery codes generated", ActRecoveryUsed: "Recovery code used",
	ActSessionsRevoke: "Sessions logged out", ActProfileUpdate: "Profile updated",
	Act2FAReset: "Two-factor authentication reset by an administrator", ActUserUnlocked: "Login locks lifted",
	ActPasswordReset: "Password reset by email link",
}

// ActionLabel returns a readable label of an audit action.
func ActionLabel(a string) string {
	if l, ok := actionLabels[a]; ok {
		return l
	}
	return strings.ReplaceAll(a, "_", " ")
}

// noticeInterval is the minimum time between two security notices of the
// same kind to one user (a lockout under attack must not flood the inbox).
var noticeInterval = map[string]time.Duration{
	ActPasswordChange: 0, ActPasswordReset: 0, ActTOTPAdded: 0, ActTOTPRemoved: 0, ActPasskeyAdded: 0,
	ActPasskeyRemoved: 0, ActRecoveryUsed: 0, ActRecoveryNew: 0, Act2FAReset: 0,
	ActLoginLocked: 24 * time.Hour, ActMFAFailed: 24 * time.Hour,
}

// Audit records an action. detail may be nil. Errors are logged, never
// returned: auditing must not break the action itself.
func (s *Service) Audit(ctx context.Context, r *http.Request, userID int64, action, objType string, objID int64, detail map[string]any) {
	e := &db.AuditEntry{Time: s.now().Unix(), UserID: userID, Action: action, ObjectType: objType, ObjectID: objID}
	if r != nil {
		e.IP = s.ClientIP(r)
	}
	if tc, ok := ctx.Value(tokenCtxKey{}).(*tokenCtx); ok { // made through the MCP interface
		if e.IP == "" {
			e.IP = tc.ip
		}
		d := map[string]any{"via": "mcp", "token": tc.token.ID, "token_name": tc.token.Name}
		for k, v := range detail {
			d[k] = v
		}
		detail = d
	}
	if detail != nil {
		b, err := json.Marshal(detail)
		if err == nil {
			e.Detail = string(b)
		}
	}
	if err := db.InsertAudit(ctx, s.db.W, e); err != nil {
		s.log.Error("audit", "action", action, "err", err)
	}
	s.log.Info("audit", "action", action, "user", userID, "ip", e.IP, "object", objType, "id", objID)
	if every, ok := noticeInterval[action]; ok && userID != 0 && s.opts.Notify != nil {
		s.notify(ctx, userID, action, e.IP, every)
	}
}

// notify sends a security notice about action to the user, at most once
// per interval for the same action.
func (s *Service) notify(ctx context.Context, userID int64, action, ip string, every time.Duration) {
	u, err := db.GetUser(ctx, s.db.R, userID)
	if err != nil || u.Email == "" {
		return
	}
	now := s.now()
	if every > 0 {
		key := "notice_" + action
		if last, _ := db.UserMeta(ctx, s.db.R, userID, key); last != "" {
			if t, err := strconv.ParseInt(last, 10, 64); err == nil && now.Sub(time.Unix(t, 0)) < every {
				return
			}
		}
		db.SetUserMeta(ctx, s.db.W, userID, key, strconv.FormatInt(now.Unix(), 10))
	}
	if ip == "" {
		ip = "unknown"
	}
	s.opts.Notify(ctx, u.Email, map[string]any{"Login": u.Login, "Event": ActionLabel(action),
		"When": now.UTC().Format("2006-01-02 15:04 UTC"), "IP": ip})
}

// Maintain prunes expired sessions, old login failures and audit entries
// until ctx is done.
func (s *Service) Maintain(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		now := s.now().Unix()
		if err := db.DeleteExpiredSessions(ctx, s.db.W, now); err != nil {
			s.log.Error("prune sessions", "err", err)
		}
		if err := db.PruneLoginFailures(ctx, s.db.W, now-24*3600); err != nil {
			s.log.Error("prune login failures", "err", err)
		}
		if err := db.PruneAudit(ctx, s.db.W, now-365*24*3600); err != nil {
			s.log.Error("prune audit log", "err", err)
		}
		if err := db.PruneUserTokens(ctx, s.db.W, now); err != nil {
			s.log.Error("prune tokens", "err", err)
		}
		if err := db.PruneAccessTokens(ctx, s.db.W, now-30*24*3600); err != nil {
			s.log.Error("prune access tokens", "err", err)
		}
		s.ceremonies.prune(s.now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
