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
