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

// Login rate limits.
const (
	loginIPMax     = 10 // attempts per IP (IPv6: /64) ...
	loginIPWindow  = 15 * time.Minute
	lockThreshold  = 5 // failures per account before it is locked ...
	lockBase       = time.Minute
	lockMax        = time.Hour
	failureForgive = 24 * time.Hour // failure counters reset after a quiet day
)

// ErrLoginFailed is the only error a login reports to the visitor: wrong
// user, wrong password, locked account and rate limit look the same, so
// the response does not reveal whether an account exists.
var ErrLoginFailed = errors.New("login failed")

// LoginFailedMessage is shown for every failed login.
const LoginFailedMessage = "The username, email address or password is incorrect, or there were too many failed attempts. Please try again later."

// Authenticate checks a login name (or email) and password. It enforces
// the per-IP rate limit and the progressive per-account lockout, upgrades
// old password hashes and writes the audit log. On success the caller
// continues with the second factor.
func (s *Service) Authenticate(ctx context.Context, r *http.Request, login, password string) (*db.User, error) {
	ip := s.ClientIP(r)
	now := s.now()
	if !s.ipLimit.allow(httpx.NetKey(ip), now) {
		s.Audit(ctx, r, 0, ActLoginLocked, "ip", 0, map[string]any{"login": truncate(login, 60), "reason": "ip rate limit"})
		return nil, ErrLoginFailed
	}
	user, err := db.UserByLogin(ctx, s.db.R, login)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	key := "n:" + strings.ToLower(strings.TrimSpace(login))
	if user != nil {
		key = "u:" + strconv.FormatInt(user.ID, 10)
	}
	f, err := db.GetLoginFailure(ctx, s.db.R, key)
	if err != nil {
		return nil, err
	}
	if f.LockedUntil > now.Unix() {
		var uid int64
		if user != nil {
			uid = user.ID
		}
		s.Audit(ctx, r, uid, ActLoginLocked, "user", uid, map[string]any{"login": truncate(login, 60)})
		return nil, ErrLoginFailed
	}
	ok, rehash := false, false
	if user != nil {
		ok, rehash = VerifyPassword(ctx, password, user.PassHash)
	} else {
		VerifyDummy(ctx, password)
	}
	if !ok {
		if err := s.recordFailure(ctx, key, f, now); err != nil {
			return nil, err
		}
		var uid int64
		if user != nil {
			uid = user.ID
		}
		s.Audit(ctx, r, uid, ActLoginFailed, "user", uid, map[string]any{"login": truncate(login, 60)})
		return nil, ErrLoginFailed
	}
	if f.Count > 0 {
		if err := db.ClearLoginFailure(ctx, s.db.W, key); err != nil {
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

// recordFailure counts a failed attempt; from lockThreshold failures on the
// account is locked for lockBase, doubling with every further failure up to
// lockMax.
func (s *Service) recordFailure(ctx context.Context, key string, f db.LoginFailure, now time.Time) error {
	if now.Unix()-f.Last > int64(failureForgive/time.Second) {
		f.Count = 0
	}
	f.Count++
	f.Last = now.Unix()
	if f.Count >= lockThreshold {
		d := lockBase << min(f.Count-lockThreshold, 10)
		f.LockedUntil = now.Add(min(d, lockMax)).Unix()
	}
	return db.SetLoginFailure(ctx, s.db.W, key, f)
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
