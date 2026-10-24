package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
)

// One-time link purposes and lifetimes.
const (
	TokenReset      = "reset"       // lost password
	TokenInvite     = "invite"      // new account: set password, enroll 2FA
	TokenAdminEmail = "admin_email" // confirm a new admin email address

	ResetTTL      = time.Hour
	InviteTTL     = 72 * time.Hour
	AdminEmailTTL = 48 * time.Hour
)

// ErrTokenInvalid is returned for unknown, used or expired links.
var ErrTokenInvalid = errors.New("This link is invalid or has expired.")

// tokenBind fingerprints the password hash (keyed, so the stored value
// tells nothing about the hash): a link stops working when the password
// changes, e.g. after another reset.
func (s *Service) tokenBind(u *db.User) []byte {
	m := hmac.New(sha256.New, s.recovKey)
	m.Write([]byte("token-bind\x00" + u.PassHash))
	return m.Sum(nil)
}

// NewUserToken creates a one-time link token for u. Older tokens of the
// same purpose are replaced. data is stored with it (e.g. an address to
// confirm). The token is returned once; only its hash is stored.
func (s *Service) NewUserToken(ctx context.Context, u *db.User, purpose string, ttl time.Duration, data string) (string, error) {
	tok := newToken()
	h := sha256.Sum256([]byte(tok))
	err := db.InsertUserToken(ctx, s.db.W, &db.UserToken{Hash: h[:], UserID: u.ID, Purpose: purpose,
		Expires: s.now().Add(ttl).Unix(), Bind: s.tokenBind(u), Data: data})
	if err != nil {
		return "", err
	}
	return tok, nil
}

// CheckUserToken returns the user and stored data of a valid token
// without using it up.
func (s *Service) CheckUserToken(ctx context.Context, tok, purpose string) (*db.User, *db.UserToken, error) {
	if len(tok) != 43 {
		return nil, nil, ErrTokenInvalid
	}
	h := sha256.Sum256([]byte(tok))
	t, err := db.GetUserToken(ctx, s.db.R, h[:], purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	if t.Expires < s.now().Unix() {
		return nil, nil, ErrTokenInvalid
	}
	u, err := db.GetUser(ctx, s.db.R, t.UserID)
	if err != nil {
		return nil, nil, ErrTokenInvalid
	}
	if !hmac.Equal(t.Bind, s.tokenBind(u)) {
		return nil, nil, ErrTokenInvalid
	}
	return u, t, nil
}

// UseUserToken checks and consumes a token in one step: of two
// concurrent uses only one succeeds.
func (s *Service) UseUserToken(ctx context.Context, tok, purpose string) (*db.User, *db.UserToken, error) {
	u, t, err := s.CheckUserToken(ctx, tok, purpose)
	if err != nil {
		return nil, nil, err
	}
	ok, err := db.ConsumeUserToken(ctx, s.db.W, t.Hash)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ErrTokenInvalid
	}
	return u, t, nil
}

// Lost-password requests: per client network and per account.
const (
	lostIPMax      = 5
	lostIPWindow   = 15 * time.Minute
	lostAccountGap = 10 * time.Minute // at most one reset mail per account in this time
)

// AllowLostPassword applies the per-network rate limit of the lost
// password form.
func (s *Service) AllowLostPassword(r *http.Request) bool {
	return s.lostLimit.allow(httpx.NetKey(s.ClientIP(r)), s.now())
}

// lostPerDay is the most reset mails one account gets per day.
const lostPerDay = 5

// ResetRequestTooSoon reports whether no reset link may be sent now: one
// was sent shortly before (a pending token newer than lostAccountGap), or
// the account got lostPerDay links already (mail flooding).
func (s *Service) ResetRequestTooSoon(ctx context.Context, userID int64) bool {
	if ok, err := db.HasUserToken(ctx, s.db.R, userID, TokenReset, s.now().Add(ResetTTL-lostAccountGap).Unix()); err != nil || ok {
		return true
	}
	now := s.now().Unix()
	n, _, err := db.ReserveAttempt(ctx, s.db.W, "lost:"+strconv.FormatInt(userID, 10), now, now-24*3600)
	return err != nil || n > lostPerDay
}
