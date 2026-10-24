package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

// Second-factor limits: a pending login allows MaxSecondFactorTry
// attempts; per user, mfaThreshold failures (from any session, also
// re-authentication) lock second-factor checks for mfaLockBase, doubling
// up to an hour. Attempts are reserved before the check, so parallel
// requests cannot exceed the limits.
const (
	mfaThreshold    = 10
	mfaLockBase     = 5 * time.Minute
	passkeyBeginMax = 30 // usernameless passkey ceremonies per IP and window
)

// ErrTooManyAttempts is returned when the second-factor limits are reached.
var ErrTooManyAttempts = errors.New("too many attempts")

func mfaKey(userID int64) string { return "mfa:" + strconv.FormatInt(userID, 10) }

// ReserveSecondFactor counts a second-factor attempt of cur's user before
// the factor is checked. It fails when the pending login used up its tries
// or the user's second factor is locked.
func (s *Service) ReserveSecondFactor(ctx context.Context, cur *Current) error {
	if cur.Session.State == db.SessionMFA {
		_, ok, err := db.ReserveSessionAttempt(ctx, s.db.W, cur.Session.IDHash, MaxSecondFactorTry)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTooManyAttempts
		}
	}
	now := s.now()
	n, locked, err := db.ReserveAttempt(ctx, s.db.W, mfaKey(cur.User.ID), now.Unix(), now.Add(-failureForgive).Unix())
	if err != nil {
		return err
	}
	if locked > now.Unix() || n > mfaThreshold {
		return ErrTooManyAttempts
	}
	return nil
}

// SecondFactorFailed locks second-factor checks after too many failures.
func (s *Service) SecondFactorFailed(ctx context.Context, userID int64) error {
	f, err := db.GetLoginFailure(ctx, s.db.R, mfaKey(userID))
	if err != nil || f.Count < mfaThreshold {
		return err
	}
	return db.LockAttempts(ctx, s.db.W, mfaKey(userID), s.now().Unix(), secs(mfaLockBase), secs(lockMax))
}

// SecondFactorSucceeded clears the user's failure counter and returns
// how many failed attempts there were since the last success (shown to
// the user: a correct password with wrong codes means the password is
// known to someone else).
func (s *Service) SecondFactorSucceeded(ctx context.Context, userID int64) (int, error) {
	f, err := db.GetLoginFailure(ctx, s.db.R, mfaKey(userID))
	if err != nil {
		return 0, err
	}
	if err := db.ClearLoginFailure(ctx, s.db.W, mfaKey(userID)); err != nil {
		return 0, err
	}
	return max(f.Count-1, 0), nil // the successful attempt was counted too
}

// Setup codes: an account without a second factor (new, imported, or
// after a 2FA reset) can only enroll with a one-time code issued by an
// administrator or the CLI, so a leaked password alone cannot be used to
// register an attacker's factor.
const (
	setupCodeTTL  = 72 * time.Hour
	setupMaxTries = 5
	metaSetupCode = "setup_code"
)

// NewSetupCode creates (replaces) the setup code of a user and returns it.
func (s *Service) NewSetupCode(ctx context.Context, userID int64) (string, error) {
	return NewSetupCode(ctx, s.db, s.recovKey, userID, s.now())
}

// NewSetupCode creates a setup code with the given key (the CLI derives the
// key from the site secret as the server does).
func NewSetupCode(ctx context.Context, d *db.DB, key []byte, userID int64, now time.Time) (string, error) {
	b := make([]byte, 8)
	rand.Read(b)
	raw := strings.ToUpper(b32.EncodeToString(b))[:12]
	code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12]
	exp := now.Add(setupCodeTTL).Unix()
	if err := db.SetUserMeta(ctx, d.W, userID, metaSetupCode, setupCodeHash(key, userID, code)+"|"+strconv.FormatInt(exp, 10)); err != nil {
		return "", err
	}
	if err := db.ClearAttemptsPrefix(ctx, d.W, "setup:"+strconv.FormatInt(userID, 10)); err != nil {
		return "", err
	}
	return code, nil
}

func setupCodeHash(key []byte, userID int64, code string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("setup:" + strconv.FormatInt(userID, 10) + ":" + strings.ToUpper(normalizeCode(code))))
	return hex.EncodeToString(m.Sum(nil))
}

// CheckSetupCode verifies (and consumes) the setup code of a user. After
// setupMaxTries failures the code is invalidated.
func (s *Service) CheckSetupCode(ctx context.Context, userID int64, code string) (bool, error) {
	now := s.now()
	key := "setup:" + strconv.FormatInt(userID, 10)
	n, _, err := db.ReserveAttempt(ctx, s.db.W, key, now.Unix(), now.Add(-setupCodeTTL).Unix())
	if err != nil {
		return false, err
	}
	stored, err := db.UserMeta(ctx, s.db.R, userID, metaSetupCode)
	if err != nil || stored == "" || n > setupMaxTries {
		return false, err
	}
	hash, expStr, _ := strings.Cut(stored, "|")
	exp, _ := strconv.ParseInt(expStr, 10, 64)
	if now.Unix() > exp || !hmac.Equal([]byte(hash), []byte(setupCodeHash(s.recovKey, userID, code))) {
		if n >= setupMaxTries {
			db.SetUserMeta(ctx, s.db.W, userID, metaSetupCode, "")
		}
		return false, nil
	}
	if err := db.SetUserMeta(ctx, s.db.W, userID, metaSetupCode, ""); err != nil {
		return false, err
	}
	return true, db.ClearLoginFailure(ctx, s.db.W, key)
}

// SetupKey returns the key setup codes are signed with, derived from the
// site secret (for the CLI).
func SetupKey(secret []byte) []byte { return deriveKey(secret, "gopress recovery codes") }
