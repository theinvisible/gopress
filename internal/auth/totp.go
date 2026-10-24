package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/theinvisible/gopress/internal/db"
)

// TOTP parameters (RFC 6238 defaults understood by every authenticator app).
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1 // accept one step before and after
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// totpCode computes the code of a time step (RFC 4226 HOTP).
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1000000)
}

// matchTOTP returns the time step code matches at t (within the skew), or
// -1.
func matchTOTP(secret []byte, code string, t time.Time) int64 {
	code = normalizeCode(code)
	if len(code) != totpDigits {
		return -1
	}
	now := t.Unix() / totpPeriod
	found := int64(-1)
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		// Compare all candidates in constant time.
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, now+d)), []byte(code)) == 1 {
			found = now + d
		}
	}
	return found
}

func normalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(code))
}

// TOTPSetup is a TOTP secret offered to a user during enrollment.
type TOTPSetup struct {
	Secret string // base32, for manual entry
	URI    string // otpauth:// URI
	QR     string // PNG data URI of the URI
	Token  string // sealed secret for the confirmation form
}

// NewTOTPSetup creates a fresh secret for user. The secret is not stored
// until the user confirms it with a valid code (EnableTOTP); meanwhile it
// travels in the form, encrypted and bound to the user and a deadline.
func (s *Service) NewTOTPSetup(u *db.User) (*TOTPSetup, error) {
	secret := make([]byte, 20)
	rand.Read(secret)
	sec := b32.EncodeToString(secret)
	issuer := s.opts.SiteName()
	label := url.PathEscape(issuer + ":" + u.Login)
	uri := "otpauth://totp/" + label + "?" + url.Values{
		"secret": {sec}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {strconv.Itoa(totpDigits)}, "period": {strconv.Itoa(totpPeriod)},
	}.Encode()
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		return nil, err
	}
	payload := fmt.Sprintf("%d|%d|%s", u.ID, s.now().Add(30*time.Minute).Unix(), sec)
	sealed, err := s.seal([]byte(payload), "totp-setup")
	if err != nil {
		return nil, err
	}
	return &TOTPSetup{
		Secret: sec,
		URI:    uri,
		QR:     "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
		Token:  base64.RawURLEncoding.EncodeToString(sealed),
	}, nil
}

// ErrInvalidCode is returned for a wrong or replayed code.
var ErrInvalidCode = errors.New("invalid code")

// EnableTOTP confirms a TOTP setup with the first code and stores the
// secret.
func (s *Service) EnableTOTP(ctx context.Context, u *db.User, token, code string) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ErrInvalidCode
	}
	plain, err := s.open(raw, "totp-setup")
	if err != nil {
		return ErrInvalidCode
	}
	parts := strings.SplitN(string(plain), "|", 3)
	if len(parts) != 3 || parts[0] != strconv.FormatInt(u.ID, 10) {
		return ErrInvalidCode
	}
	if deadline, _ := strconv.ParseInt(parts[1], 10, 64); s.now().Unix() > deadline {
		return ErrInvalidCode
	}
	secret, err := b32.DecodeString(parts[2])
	if err != nil {
		return ErrInvalidCode
	}
	step := matchTOTP(secret, code, s.now())
	if step < 0 {
		return ErrInvalidCode
	}
	enc, err := s.seal(secret, totpAAD(u.ID))
	if err != nil {
		return err
	}
	return db.SetTOTP(ctx, s.db.W, u.ID, enc, step, s.now().Unix())
}

// VerifyTOTP checks a code of user. Each time step is accepted only once.
func (s *Service) VerifyTOTP(ctx context.Context, userID int64, code string) (bool, error) {
	enc, _, ok, err := db.TOTP(ctx, s.db.R, userID)
	if err != nil || !ok {
		return false, err
	}
	secret, err := s.open(enc, totpAAD(userID))
	if err != nil {
		return false, err
	}
	step := matchTOTP(secret, code, s.now())
	if step < 0 {
		return false, nil
	}
	return db.UseTOTPStep(ctx, s.db.W, userID, step)
}

// HasTOTP reports whether TOTP is set up for a user.
func (s *Service) HasTOTP(ctx context.Context, userID int64) (bool, error) {
	_, _, ok, err := db.TOTP(ctx, s.db.R, userID)
	return ok, err
}

func totpAAD(userID int64) string { return "totp:" + strconv.FormatInt(userID, 10) }

// seal encrypts data with AES-256-GCM under the TOTP key; aad binds the
// ciphertext to its purpose (and user), so it cannot be moved elsewhere.
func (s *Service) seal(data []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(s.totpKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return gcm.Seal(nonce, nonce, data, []byte(aad)), nil
}

func (s *Service) open(sealed []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(s.totpKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("sealed data too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(aad))
}

// Recovery codes: 10 single-use codes of 80 bits, shown once. Only an
// HMAC under a key derived from the site secret is stored, so a leaked
// database alone does not allow brute-forcing them offline.
const recoveryCodeCount = 10

// NewRecoveryCodes replaces the recovery codes of a user and returns them
// for display.
func (s *Service) NewRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	hashes := make([][]byte, recoveryCodeCount)
	for i := range codes {
		b := make([]byte, 10)
		rand.Read(b)
		c := strings.ToLower(b32.EncodeToString(b)) // 16 chars
		codes[i] = c[0:4] + "-" + c[4:8] + "-" + c[8:12] + "-" + c[12:16]
		hashes[i] = s.recoveryHash(codes[i])
	}
	if err := db.SetRecoveryCodes(ctx, s.db.W, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// UseRecoveryCode consumes a recovery code; it reports whether it was
// valid and unused.
func (s *Service) UseRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	return db.UseRecoveryCode(ctx, s.db.W, userID, s.recoveryHash(code), s.now().Unix())
}

func (s *Service) recoveryHash(code string) []byte {
	m := hmac.New(sha256.New, s.recovKey)
	m.Write([]byte("recovery:" + strings.ToLower(normalizeCode(code))))
	return m.Sum(nil)
}
