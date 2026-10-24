// Package antispam implements the built-in comment spam checks: a honeypot
// field, a signed timestamp token (bots post too fast or replay old forms)
// and a per-IP rate limit.
package antispam

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Form field names added to the comment form.
const (
	HoneypotField = "gp_hp"    // must stay empty (hidden from humans)
	TokenField    = "gp_token" // "<unix ts>.<hmac>"
)

// Limits.
const (
	MinFillTime = 3 * time.Second
	MaxFormAge  = 48 * time.Hour
	RateWindow  = 10 * time.Minute
	RateMax     = 5   // per client (IPv4 address or IPv6 /64)
	GlobalMax   = 100 // for the whole site per RateWindow
)

// Reasons returned by Check.
const (
	OK          = ""
	Honeypot    = "honeypot"
	BadToken    = "bad-token"
	TooFast     = "too-fast"
	Expired     = "expired"
	RateLimited = "rate-limited"
	Reused      = "token-reused"
)

// Guard checks comment submissions.
type Guard struct {
	secret []byte

	mu     sync.Mutex
	hits   map[string][]time.Time
	global []time.Time
	used   map[string]time.Time // consumed tokens -> expiry
}

// New returns a Guard signing tokens with secret.
func New(secret []byte) *Guard {
	return &Guard{secret: secret, hits: map[string][]time.Time{}, used: map[string]time.Time{}}
}

func (g *Guard) sign(postID int64, ts int64) string {
	m := hmac.New(sha256.New, g.secret)
	m.Write([]byte(strconv.FormatInt(postID, 10) + ":" + strconv.FormatInt(ts, 10)))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// Token returns the form token for a post, issued at now.
func (g *Guard) Token(postID int64, now time.Time) string {
	ts := now.Unix()
	return strconv.FormatInt(ts, 10) + "." + g.sign(postID, ts)
}

// Check validates a submitted form for postID from ip and returns OK or a
// rejection reason. A successful check counts towards the rate limit.
func (g *Guard) Check(form url.Values, postID int64, ip string, now time.Time) string {
	if form.Get(HoneypotField) != "" {
		return Honeypot
	}
	tsStr, sig, ok := strings.Cut(form.Get(TokenField), ".")
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if !ok || err != nil || !hmac.Equal([]byte(sig), []byte(g.sign(postID, ts))) {
		return BadToken
	}
	age := now.Sub(time.Unix(ts, 0))
	if age < MinFillTime {
		return TooFast
	}
	if age > MaxFormAge {
		return Expired
	}

	key := clientKey(ip)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.used[sig]; ok {
		return Reused
	}
	recent := prune(g.hits[key], now)
	g.global = prune(g.global, now)
	if len(recent) >= RateMax || len(g.global) >= GlobalMax {
		g.hits[key] = recent
		return RateLimited
	}
	g.hits[key] = append(recent, now)
	g.global = append(g.global, now)
	g.used[sig] = time.Unix(ts, 0).Add(MaxFormAge)
	if len(g.hits) > 10000 || len(g.used) > 10000 { // drop stale entries
		for k, v := range g.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > RateWindow {
				delete(g.hits, k)
			}
		}
		for k, exp := range g.used {
			if now.After(exp) {
				delete(g.used, k)
			}
		}
	}
	return OK
}

func prune(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < RateWindow {
			out = append(out, t)
		}
	}
	return out
}

// clientKey groups IPv6 clients by /64 (one customer network), IPv4 by
// address.
func clientKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is6() {
		p, _ := addr.Prefix(64)
		return p.String()
	}
	return addr.String()
}

var reLink = regexp.MustCompile(`(?i)<a [^>]*href`)

// CountLinks counts links like WordPress' check_comment() does
// (preg_match_all('/<a [^>]*href/i')).
func CountLinks(text string) int {
	return len(reLink.FindAllStringIndex(text, -1))
}
