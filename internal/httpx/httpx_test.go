package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestTrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.29.0.0/16"), netip.MustParsePrefix("::1/128")}
	var seen http.Header
	var https bool
	var ip string
	h := TrustedProxies(trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, https, ip = r.Header.Clone(), IsHTTPS(r), ClientIP(r, trusted)
	}))
	send := func(remote string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for _, k := range forwarded {
			r.Header.Set(k, "x")
		}
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Real-Ip", "198.51.100.7")
		r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	send("203.0.113.9:1234")
	for _, k := range forwarded {
		if seen.Get(k) != "" {
			t.Errorf("untrusted peer: %s kept", k)
		}
	}
	if https || ip != "203.0.113.9" {
		t.Errorf("untrusted peer: https=%v ip=%s", https, ip)
	}
	for _, remote := range []string{"172.29.0.2:1234", "[::1]:1234", "[::ffff:172.29.0.2]:1234"} {
		send(remote)
		if !https || ip != "198.51.100.7" || seen.Get("X-Forwarded-Proto") != "https" {
			t.Errorf("trusted peer %s: https=%v ip=%s", remote, https, ip)
		}
	}
	send("garbage")
	if https {
		t.Error("unparsable peer trusted")
	}
}

func TestForwardedFromTrustedProxy(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.29.0.2/32")}
	req := func(h http.Header) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "172.29.0.2:1234"
		r.Header = h
		return r
	}
	for _, tt := range []struct {
		h   http.Header
		ip  string
		tls bool
	}{
		// The client's own lines come first; the proxy appends the real one.
		{http.Header{"X-Forwarded-For": {"6.6.6.6", "198.51.100.7"}}, "198.51.100.7", false},
		{http.Header{"X-Forwarded-For": {"6.6.6.6, 198.51.100.7"}, "X-Real-Ip": {"6.6.6.6"}}, "198.51.100.7", false},
		{http.Header{"X-Real-Ip": {"198.51.100.7"}}, "198.51.100.7", false},
		{http.Header{"X-Forwarded-For": {"garbage"}, "X-Real-Ip": {"6.6.6.6"}}, "172.29.0.2", false},
		{http.Header{"X-Forwarded-Proto": {"https", "http"}}, "172.29.0.2", false},
		{http.Header{"X-Forwarded-Proto": {"http, https"}}, "172.29.0.2", true},
		{http.Header{"X-Forwarded-Proto": {"HTTPS"}}, "172.29.0.2", true},
	} {
		r := req(tt.h)
		if ip := ClientIP(r, trusted); ip != tt.ip {
			t.Errorf("%v: ip %s, want %s", tt.h, ip, tt.ip)
		}
		if IsHTTPS(r) != tt.tls {
			t.Errorf("%v: https %v", tt.h, !tt.tls)
		}
	}
}
