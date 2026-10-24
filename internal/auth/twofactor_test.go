package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

// RFC 6238 appendix B (SHA-1), truncated to 6 digits.
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		if got := totpCode(secret, unix/totpPeriod); got != want {
			t.Errorf("t=%d: %s, want %s", unix, got, want)
		}
	}
	at := time.Unix(1111111111, 0)
	if matchTOTP(secret, "081 804", at) < 0 {
		t.Error("previous step with space not accepted")
	}
	if matchTOTP(secret, "000000", at) >= 0 || matchTOTP(secret, "12345", at) >= 0 {
		t.Error("wrong code accepted")
	}
}

func TestTOTPEnrollAndReplay(t *testing.T) {
	s, d, c := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "admin", RoleAdministrator, "correct horse battery staple")
	other := addUser(t, d, "other", RoleEditor, "correct horse battery staple")

	setup, err := s.NewTOTPSetup(u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(setup.URI, "otpauth://totp/GoPress:admin?") || !strings.HasPrefix(setup.QR, "data:image/png;base64,") {
		t.Errorf("setup = %+v", setup)
	}
	secret, _ := b32.DecodeString(setup.Secret)
	code := func() string { return totpCode(secret, c.now().Unix()/totpPeriod) }

	if err := s.EnableTOTP(ctx, other, setup.Token, code()); err == nil {
		t.Error("setup token accepted for another user")
	}
	if err := s.EnableTOTP(ctx, u, setup.Token, "000000"); err == nil {
		t.Error("wrong confirmation code accepted")
	}
	if err := s.EnableTOTP(ctx, u, setup.Token, code()); err != nil {
		t.Fatal(err)
	}
	// The confirmation code cannot be replayed for login.
	if ok, _ := s.VerifyTOTP(ctx, u.ID, code()); ok {
		t.Error("enrollment code replayed")
	}
	c.add(totpPeriod * time.Second)
	if ok, err := s.VerifyTOTP(ctx, u.ID, code()); !ok || err != nil {
		t.Errorf("valid code rejected: %v", err)
	}
	if ok, _ := s.VerifyTOTP(ctx, u.ID, code()); ok {
		t.Error("code replayed")
	}
	// The secret is stored encrypted and bound to the user.
	enc, _, _, _ := db.TOTP(ctx, d.R, u.ID)
	if strings.Contains(string(enc), string(secret)) {
		t.Error("secret stored in plain text")
	}
	if _, err := s.open(enc, totpAAD(other.ID)); err == nil {
		t.Error("secret decrypts for another user")
	}
	// Expired setup token.
	setup2, _ := s.NewTOTPSetup(u)
	c.add(31 * time.Minute)
	secret2, _ := b32.DecodeString(setup2.Secret)
	if err := s.EnableTOTP(ctx, u, setup2.Token, totpCode(secret2, c.now().Unix()/totpPeriod)); err == nil {
		t.Error("expired setup token accepted")
	}
}

func TestRecoveryCodes(t *testing.T) {
	s, d, _ := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "admin", RoleAdministrator, "correct horse battery staple")
	codes, err := s.NewRecoveryCodes(ctx, u.ID)
	if err != nil || len(codes) != recoveryCodeCount || len(codes[0]) != 19 {
		t.Fatalf("codes = %v, %v", codes, err)
	}
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, " "+strings.ToUpper(codes[3])+" "); !ok {
		t.Error("valid code (upper case, spaces) rejected")
	}
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, codes[3]); ok {
		t.Error("code reused")
	}
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, "aaaa-bbbb-cccc-dddd"); ok {
		t.Error("unknown code accepted")
	}
	// Regenerating invalidates the old codes.
	s.NewRecoveryCodes(ctx, u.ID)
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, codes[0]); ok {
		t.Error("old code valid after regeneration")
	}
	if has, _ := s.HasSecondFactor(ctx, u.ID); has {
		t.Error("recovery codes alone count as second factor")
	}
}
