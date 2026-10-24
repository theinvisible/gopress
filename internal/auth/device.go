package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DeviceCookie marks a browser in which a user completed a login. Browsers
// with it are exempt from the account-wide login lockout, so attackers
// guessing passwords from many networks cannot lock the owner out (OWASP
// "device cookies"). It grants no access by itself.
const DeviceCookie = "__Host-gopress_device"

const deviceLifetime = 400 * 24 * time.Hour

// IssueDeviceCookie sets the device cookie for userID.
func (s *Service) IssueDeviceCookie(w http.ResponseWriter, userID int64) {
	nonce := make([]byte, 16)
	rand.Read(nonce)
	payload := strconv.FormatInt(userID, 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	m := hmac.New(sha256.New, s.deviceKey)
	m.Write([]byte(payload))
	http.SetCookie(w, &http.Cookie{Name: DeviceCookie, Value: payload + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)),
		Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(deviceLifetime / time.Second)})
}

// deviceUser returns the user of a valid device cookie, or 0.
func (s *Service) deviceUser(r *http.Request) int64 {
	c, err := r.Cookie(DeviceCookie)
	if err != nil || len(c.Value) > 200 {
		return 0
	}
	i := strings.LastIndexByte(c.Value, '.')
	if i < 0 {
		return 0
	}
	payload, sig := c.Value[:i], c.Value[i+1:]
	m := hmac.New(sha256.New, s.deviceKey)
	m.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return 0
	}
	id, err := strconv.ParseInt(strings.SplitN(payload, ".", 2)[0], 10, 64)
	if err != nil {
		return 0
	}
	return id
}
