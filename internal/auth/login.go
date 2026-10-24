package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
)

// Login rate limits. Failures are counted per account name and client
// network (so an attacker can only lock themselves out), plus a much higher
// account-wide limit against distributed guessing that browsers with a
// valid device cookie of that account are exempt from (OWASP "device
// cookies"). Counters reset after a quiet day.
const (
	loginIPMax     = 10 // attempts per IPv4 address / IPv6 /64 ...
	loginNetMax    = 40 // ... and per IPv6 /48
	loginIPWindow  = 15 * time.Minute
	lockThreshold  = 5 // failures per account and network before a lock ...
	lockBase       = time.Minute
	lockMax        = time.Hour
	acctThreshold  = 50 // failures per account from untrusted devices
	acctLockBase   = 15 * time.Minute
	failureForgive = 24 * time.Hour
)

// ErrLoginFailed is the only error a login reports to the visitor: wrong
// user, wrong password, locked account and rate limit look the same, so
// the response does not reveal whether an account exists.
var ErrLoginFailed = errors.New("login failed")

// LoginFailedMessage is shown for every failed login.
const LoginFailedMessage = "The username, email address or password is incorrect, or there were too many failed attempts. Please try again later."

// Authenticate checks a login name (or email) and password. It enforces
// the rate limits and lockouts, upgrades old password hashes and writes
// the audit log. On success the caller continues with the second factor.
//
// Every failure costs one argon2id run, whether the account exists, is
// locked or has an imported hash, so timing reveals nothing either.
func (s *Service) Authenticate(ctx context.Context, r *http.Request, login, password string) (*db.User, error) {
	ip := s.ClientIP(r)
	now := s.now()
	if !s.ipLimit.allow(httpx.NetKey(ip), now) || !s.netLimit.allow(httpx.NetKey48(ip), now) {
		VerifyDummy(ctx, password)
		s.Audit(ctx, r, 0, ActLoginLocked, "ip", 0, map[string]any{"reason": "ip rate limit"})
		return nil, ErrLoginFailed
	}
	user, err := db.UserByLogin(ctx, s.db.R, login)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var uid int64
	acct := "n:" + strings.ToLower(strings.TrimSpace(login))
	detail := map[string]any{"known_account": false} // never log what was typed: it may be a password
	if user != nil {
		uid = user.ID
		acct = "u:" + strconv.FormatInt(user.ID, 10)
		detail = map[string]any{"login": user.Login}
	}
	netKey := acct + "|" + httpx.NetKey(ip)
	trusted := user != nil && s.deviceUser(r) == user.ID
	forgive := now.Add(-failureForgive).Unix()

	// Count the attempt before checking it (atomic, so parallel guesses
	// each count).
	n1, l1, err := db.ReserveAttempt(ctx, s.db.W, netKey, now.Unix(), forgive)
	if err != nil {
		return nil, err
	}
	n2, l2 := 0, int64(0)
	if !trusted {
		if n2, l2, err = db.ReserveAttempt(ctx, s.db.W, acct, now.Unix(), forgive); err != nil {
			return nil, err
		}
	}
	if l1 > now.Unix() || l2 > now.Unix() || n1 > lockThreshold || n2 > acctThreshold {
		if err := s.lockIfNeeded(ctx, netKey, acct, n1, n2, now); err != nil {
			return nil, err
		}
		VerifyDummy(ctx, password)
		s.Audit(ctx, r, uid, ActLoginLocked, "user", uid, detail)
		return nil, ErrLoginFailed
	}
	ok, rehash := false, false
	if user != nil {
		ok, rehash = VerifyPassword(ctx, password, user.PassHash)
		if !strings.HasPrefix(user.PassHash, "$argon2id$") {
			VerifyDummy(ctx, password) // imported hashes are faster than argon2id
		}
	} else {
		VerifyDummy(ctx, password)
	}
	if !ok {
		if err := s.lockIfNeeded(ctx, netKey, acct, n1, n2, now); err != nil {
			return nil, err
		}
		s.Audit(ctx, r, uid, ActLoginFailed, "user", uid, detail)
		return nil, ErrLoginFailed
	}
	for _, k := range []string{netKey, acct} {
		if err := db.ClearLoginFailure(ctx, s.db.W, k); err != nil {
			return nil, err
		}
	}
	if rehash {
		if h, err := HashPassword(ctx, password); err == nil {
			if err := db.SetUserPassHash(ctx, s.db.W, user.ID, h); err != nil {
				return nil, err
			}
			user.PassHash = h
		}
	}
	return user, nil
}

// lockIfNeeded locks the account/network counter after lockThreshold
// failures (lockBase, doubling with every further lock up to lockMax) and
// the account counter after acctThreshold failures.
func (s *Service) lockIfNeeded(ctx context.Context, netKey, acct string, n1, n2 int, now time.Time) error {
	if n1 >= lockThreshold {
		if err := db.LockAttempts(ctx, s.db.W, netKey, now.Unix(), secs(lockBase), secs(lockMax)); err != nil {
			return err
		}
	}
	if n2 >= acctThreshold {
		if err := db.LockAttempts(ctx, s.db.W, acct, now.Unix(), secs(acctLockBase), secs(lockMax)); err != nil {
			return err
		}
	}
	return nil
}

func secs(d time.Duration) int64 { return int64(d / time.Second) }

// UnlockUser clears all login and second-factor counters of a user
// (CLI and admin unlock, new password set by an administrator).
func (s *Service) UnlockUser(ctx context.Context, userID int64) error {
	return UnlockUser(ctx, s.db, userID)
}

// UnlockPassword lifts only the password locks of a user (after a
// password reset by email link): proving control of the mailbox must not
// reset the second-factor throttling.
func UnlockPassword(ctx context.Context, d *db.DB, userID int64) error {
	id := strconv.FormatInt(userID, 10)
	for _, prefix := range []string{"u:" + id + "|", "u:" + id} {
		if err := db.ClearAttemptsPrefix(ctx, d.W, prefix); err != nil {
			return err
		}
	}
	return nil
}

// UnlockUser clears all login and second-factor counters of a user.
func UnlockUser(ctx context.Context, d *db.DB, userID int64) error {
	id := strconv.FormatInt(userID, 10)
	for _, prefix := range []string{"u:" + id + "|", "u:" + id, "mfa:" + id, "setup:" + id} {
		if err := db.ClearAttemptsPrefix(ctx, d.W, prefix); err != nil {
			return err
		}
	}
	return nil
}

// ipLimiter is a sliding-window counter per client network.
type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *ipLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 100000 {
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > l.window {
				delete(l.hits, k)
			}
		}
		if len(l.hits) > 100000 {
			return false // under attack: refuse new clients rather than grow
		}
	}
	ts := l.hits[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > l.window {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.max {
		l.hits[key] = ts
		return false
	}
	l.hits[key] = append(ts, now)
	return true
}
