// Package httpx holds HTTP helpers shared by the public site and the admin:
// client addresses behind reverse proxies, same-origin checks and cache
// headers.
package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// ClientIP returns the visitor address. X-Real-Ip/X-Forwarded-For are only
// trusted when the direct peer is one of the trusted proxies (e.g. Traefik),
// so clients reaching the server directly cannot fake their address. The
// proxy must set (overwrite) X-Real-Ip or append to X-Forwarded-For;
// a proxy that passes a client's X-Real-Ip through lets clients choose
// their address.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !fromTrusted(r, trusted) {
		return peer.String()
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-Ip"))); err == nil {
		return ip.Unmap().String()
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			return ip.Unmap().String()
		}
	}
	return peer.String()
}

// forwarded are the headers a reverse proxy sets about the original request.
var forwarded = []string{"X-Real-Ip", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "Forwarded"}

// TrustedProxies removes the proxy headers from requests that do not come
// from one of the trusted proxies, so no handler can be fooled by a
// client that reaches the server directly (e.g. into sending HSTS or
// building https URLs).
func TrustedProxies(trusted []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromTrusted(r, trusted) {
			for _, h := range forwarded {
				r.Header.Del(h)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func fromTrusted(r *http.Request, trusted []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	peer = peer.Unmap()
	for _, p := range trusted {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// IsHTTPS reports whether the visitor used HTTPS: directly, or through a
// trusted proxy (see TrustedProxies) that says so.
func IsHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// NetKey groups IPv6 clients by /64 (one customer network), IPv4 by address.
// Rate limits are keyed by it.
func NetKey(ip string) string {
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

// NetKey48 groups IPv6 clients by /48 (a typical site allocation), IPv4 by
// address: a second, coarser rate limit bucket.
func NetKey48(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is6() {
		p, _ := addr.Prefix(48)
		return p.String()
	}
	return addr.String()
}

// SameOrigin rejects cross-site form posts (another site making visitors'
// browsers submit forms): Sec-Fetch-Site must not be cross-site and an
// Origin header, if present, must be the site itself.
func SameOrigin(r *http.Request, home string) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" && r.Header.Get("Sec-Fetch-Site") == "" {
		return true // old browsers and non-browser clients
	}
	return originMatches(r, origin, home)
}

// StrictSameOrigin is SameOrigin for the admin: requests from other sites,
// including same-site subdomains, and opaque ("null") origins are rejected.
// Requests without these headers (non-browser clients) still need a valid
// CSRF token.
func StrictSameOrigin(r *http.Request, home string) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return originMatches(r, origin, home)
}

func originMatches(r *http.Request, origin, home string) bool {
	h, err := url.Parse(home)
	if err != nil {
		return false
	}
	return origin == h.Scheme+"://"+h.Host || origin == "https://"+r.Host || origin == "http://"+r.Host
}

// NoStore marks a response as personal and uncacheable.
func NoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}
