package site

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
)

// commenterCookie identifies a commenter so that their own pending
// comments are shown to them. Unlike WordPress' plain email cookie it is
// signed, so nobody can read other people's pending comments by forging it.
const commenterCookie = "gopress_commenter"

func (s *Site) signCommenter(email string) string {
	m := hmac.New(sha256.New, s.opts.Secret)
	m.Write([]byte("commenter:" + email))
	return base64.RawURLEncoding.EncodeToString([]byte(email)) + "." + hex.EncodeToString(m.Sum(nil)[:16])
}

// verifiedCommenter returns the email from a valid commenter cookie.
func (s *Site) verifiedCommenter(r *http.Request) string {
	c, err := r.Cookie(commenterCookie)
	if err != nil || len(c.Value) > 512 {
		return ""
	}
	enc, _, ok := strings.Cut(c.Value, ".")
	if !ok {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return ""
	}
	email := string(raw)
	if !hmac.Equal([]byte(s.signCommenter(email)), []byte(c.Value)) {
		return ""
	}
	return email
}

// personalized returns the commenter email if the response must be
// rendered for this visitor: they have a signed commenter cookie and
// pending comments. Everyone else gets the cached page.
func (s *Site) personalized(ctx context.Context, r *http.Request) string {
	email := s.verifiedCommenter(r)
	if email == "" {
		return ""
	}
	var n int
	if err := s.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE author_email = ? AND status = 'pending'`, email).Scan(&n); err != nil || n == 0 {
		return ""
	}
	return email
}
