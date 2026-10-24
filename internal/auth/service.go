package auth

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
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

	ipLimit    *ipLimiter
	ceremonies *ceremonyStore
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
	s.ipLimit = newIPLimiter(loginIPMax, loginIPWindow)
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
)

// Audit records an action. detail may be nil. Errors are logged, never
// returned: auditing must not break the action itself.
func (s *Service) Audit(ctx context.Context, r *http.Request, userID int64, action, objType string, objID int64, detail map[string]any) {
	e := &db.AuditEntry{Time: s.now().Unix(), UserID: userID, Action: action, ObjectType: objType, ObjectID: objID}
	if r != nil {
		e.IP = s.ClientIP(r)
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
		s.ceremonies.prune(s.now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
