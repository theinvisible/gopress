package site

import (
	"net/http"

	"github.com/theinvisible/gopress/internal/httpx"
)

// SecurityHeaders adds defensive HTTP headers to every response.
//
// The CSP allows inline styles (WordPress content and the gallery shortcode
// emit style attributes and <style> blocks) but no inline event handlers or
// foreign scripts; images may come from anywhere (Gravatar, PayPal buttons,
// external images in posts). Scripts must be files from this origin.
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; font-src 'self' data:; frame-src https:; object-src 'none'; " +
		"base-uri 'self'; form-action 'self' https://www.paypal.com https://www.sandbox.paypal.com; frame-ancestors 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		h.Set("Content-Security-Policy", csp)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if httpx.IsHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// NoIndex asks search engines not to index anything (staging instances):
// every response gets an X-Robots-Tag header. Handlers that set their own
// only ever set a noindex variant.
func NoIndex(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}
