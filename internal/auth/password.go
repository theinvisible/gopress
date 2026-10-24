// Package auth implements the admin authentication: password hashing
// (argon2id, plus verification of imported WordPress hashes), sessions,
// CSRF protection, login rate limiting, two-factor authentication (TOTP,
// passkeys, recovery codes), capabilities and the audit log.
package auth

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// argon2id parameters (OWASP: m >= 19 MiB, t >= 2). Hashes with other
// parameters are upgraded on the next login.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// MaxPasswordBytes bounds the work per login attempt (WordPress: 4096).
const MaxPasswordBytes = 4096

// hashSlots caps concurrent password hashes: each argon2id run takes 64 MiB,
// so a login flood must not be able to exhaust memory.
var hashSlots = make(chan struct{}, 4)

func acquire(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release() { <-hashSlots }

// HashPassword returns the argon2id hash of pw in PHC string format.
func HashPassword(ctx context.Context, pw string) (string, error) {
	if len(pw) > MaxPasswordBytes {
		return "", errors.New("password too long")
	}
	salt := make([]byte, argonSaltLen)
	rand.Read(salt)
	if err := acquire(ctx); err != nil {
		return "", err
	}
	defer release()
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// dummyHash is verified for unknown users so a failed login takes the same
// time whether the account exists or not.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword(context.Background(), "gopress-dummy-password")
	return h
})

// VerifyDummy burns the time of one password verification.
func VerifyDummy(ctx context.Context, pw string) {
	VerifyPassword(ctx, pw, dummyHash())
}

// VerifyPassword checks pw against a stored hash. It accepts GoPress'
// argon2id hashes and the formats an imported WordPress database may
// contain (wp_check_password): WordPress 6.8+ "$wp$" bcrypt, plain bcrypt,
// phpass "$P$"/"$H$" and legacy MD5. needsRehash is true when the password
// matched but the hash should be replaced by a current argon2id hash.
func VerifyPassword(ctx context.Context, pw, stored string) (ok, needsRehash bool) {
	if len(pw) > MaxPasswordBytes || stored == "" {
		return false, false
	}
	switch {
	case strings.HasPrefix(stored, "$argon2id$"):
		ok, current := verifyArgon2(ctx, pw, stored)
		return ok, ok && !current
	case strings.HasPrefix(stored, "$wp$2"):
		// WordPress 6.8: bcrypt over base64(HMAC-SHA384(password)).
		mac := hmac.New(sha512.New384, []byte("wp-sha384"))
		mac.Write([]byte(pw))
		pre := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		return verifyBcrypt(ctx, pre, stored[3:]), true
	case strings.HasPrefix(stored, "$2y$") || strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$"):
		ok := verifyBcrypt(ctx, pw, stored)
		return ok, ok
	case strings.HasPrefix(stored, "$P$") || strings.HasPrefix(stored, "$H$"):
		ok := subtle.ConstantTimeCompare([]byte(phpassHash(pw, stored)), []byte(stored)) == 1
		return ok, ok
	case len(stored) == 32 && isHex(stored):
		sum := md5.Sum([]byte(pw))
		ok := subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(stored))) == 1
		return ok, ok
	}
	return false, false
}

func verifyArgon2(ctx context.Context, pw, stored string) (ok, current bool) {
	// $argon2id$v=19$m=65536,t=3,p=1$salt$hash
	parts := strings.Split(stored, "$")
	if len(parts) != 6 {
		return false, false
	}
	var version int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, false
	}
	// Refuse absurd parameters from a tampered database.
	if m == 0 || m > 1<<20 || t == 0 || t > 20 || p == 0 || p > 16 {
		return false, false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 128 {
		return false, false
	}
	if acquire(ctx) != nil {
		return false, false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	release()
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, m == argonMemory && t == argonTime && p == argonThreads && len(want) == argonKeyLen
}

func verifyBcrypt(ctx context.Context, pw, stored string) bool {
	// PHP's bcrypt uses only the first 72 bytes.
	if len(pw) > 72 {
		pw = pw[:72]
	}
	if acquire(ctx) != nil {
		return false
	}
	defer release()
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(pw)) == nil
}

const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// phpassHash computes the portable phpass hash of pw with the setting
// (prefix, cost, salt) of stored, as PasswordHash::crypt_private does.
func phpassHash(pw, setting string) string {
	if len(setting) < 12 {
		return "*0"
	}
	countLog2 := strings.IndexByte(itoa64, setting[3])
	if countLog2 < 7 || countLog2 > 30 {
		return "*0"
	}
	salt := setting[4:12]
	sum := md5.Sum([]byte(salt + pw))
	h := sum[:]
	for range 1 << countLog2 {
		s := md5.Sum(append(h[:16:16], pw...))
		h = s[:]
	}
	return setting[:12] + phpassEncode(h, 16)
}

func phpassEncode(in []byte, count int) string {
	var out strings.Builder
	for i := 0; i < count; {
		v := int(in[i])
		i++
		out.WriteByte(itoa64[v&0x3f])
		if i < count {
			v |= int(in[i]) << 8
		}
		out.WriteByte(itoa64[(v>>6)&0x3f])
		if i >= count {
			break
		}
		i++
		if i < count {
			v |= int(in[i]) << 16
		}
		out.WriteByte(itoa64[(v>>12)&0x3f])
		if i >= count {
			break
		}
		i++
		out.WriteByte(itoa64[(v>>18)&0x3f])
	}
	return out.String()
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// Password policy (NIST SP 800-63B): length, no composition rules, no
// commonly used or context-specific passwords.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

// ErrWeakPassword describes why a new password was rejected.
type ErrWeakPassword struct{ Reason string }

func (e *ErrWeakPassword) Error() string { return e.Reason }

// CheckPasswordPolicy validates a new password for a user.
func CheckPasswordPolicy(pw, login, email string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case !utf8.ValidString(pw):
		return &ErrWeakPassword{"The password contains invalid characters."}
	case n < MinPasswordLen:
		return &ErrWeakPassword{fmt.Sprintf("The password must be at least %d characters long.", MinPasswordLen)}
	case n > MaxPasswordLen:
		return &ErrWeakPassword{fmt.Sprintf("The password must be at most %d characters long.", MaxPasswordLen)}
	}
	lower := strings.ToLower(pw)
	if lower == strings.ToLower(login) || lower == strings.ToLower(email) ||
		(email != "" && lower == strings.ToLower(strings.SplitN(email, "@", 2)[0])) {
		return &ErrWeakPassword{"The password must not be your username or email address."}
	}
	if isCommonPassword(lower) {
		return &ErrWeakPassword{"This password is too common. Please choose another one."}
	}
	return nil
}

// commonpw.txt.gz: passwords of 12+ characters from the SecLists
// "xato-net-10-million-passwords-1000000" and "100k-most-used-passwords-NCSC"
// lists (MIT license, github.com/danielmiessler/SecLists), lowercased and
// sorted.
//
//go:embed commonpw.txt.gz
var commonPWGz []byte

var commonPasswords = sync.OnceValue(func() []string {
	zr, err := gzip.NewReader(bytes.NewReader(commonPWGz))
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
})

func isCommonPassword(lower string) bool {
	_, found := slices.BinarySearch(commonPasswords(), lower)
	return found
}
