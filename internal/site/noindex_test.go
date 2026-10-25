package site

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/httpx"
)

func TestNoIndex(t *testing.T) {
	s, h := newTestSite(t)
	s.opts.NoIndex = true
	srv := NoIndex(SecurityHeaders(h))
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/robots.txt")
	if body := rec.Body.String(); !strings.Contains(body, "Disallow: /\n") || strings.Contains(body, "Sitemap:") {
		t.Errorf("robots.txt: %q", body)
	}
	// Pages and sitemaps stay as on the live site; every response says noindex.
	for _, p := range []string{"/", "/2024/02/hello-world/", "/wp-sitemap.xml", "/nope/"} {
		rec := get(p)
		if !strings.HasPrefix(rec.Header().Get("X-Robots-Tag"), "noindex") {
			t.Errorf("%s: X-Robots-Tag %q", p, rec.Header().Get("X-Robots-Tag"))
		}
		if p != "/nope/" && rec.Code != http.StatusOK {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if body := get("/").Body.String(); !strings.Contains(body, "max-image-preview:large") {
		t.Error("the page markup changed with --noindex")
	}
}

func TestHSTSOnlyFromTrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.29.0.0/16")}
	h := httpx.TrustedProxies(trusted, SecurityHeaders(http.NotFoundHandler()))
	for _, tt := range []struct {
		remote string
		hsts   bool
	}{
		{"203.0.113.9:4000", false}, // a client claiming https directly
		{"172.29.0.2:4000", true},   // Traefik
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tt.remote
		r.Header.Set("X-Forwarded-Proto", "https")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if got := rec.Header().Get("Strict-Transport-Security") != ""; got != tt.hsts {
			t.Errorf("%s: HSTS %v", tt.remote, got)
		}
	}
}
