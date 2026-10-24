package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/theinvisible/gopress/internal/db"
)

// Passkeys (WebAuthn). The relying party is the host of the site's base
// URL; user verification (PIN/biometrics) is always required, so a passkey
// is phishing-resistant multi-factor authentication on its own.

const metaWebAuthnHandle = "webauthn_handle"

// ErrPasskey is returned for any failed passkey ceremony.
var ErrPasskey = errors.New("passkey verification failed")

type waUser struct {
	u      *db.User
	handle []byte
	creds  []webauthn.Credential
}

func (w *waUser) WebAuthnID() []byte                         { return w.handle }
func (w *waUser) WebAuthnName() string                       { return w.u.Login }
func (w *waUser) WebAuthnDisplayName() string                { return displayName(w.u) }
func (w *waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func displayName(u *db.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Login
}

var waCache sync.Map // home URL -> *webauthn.WebAuthn

func (s *Service) webauthn() (*webauthn.WebAuthn, error) {
	home := strings.TrimRight(s.Home(), "/")
	if w, ok := waCache.Load(home); ok {
		return w.(*webauthn.WebAuthn), nil
	}
	u, err := url.Parse(home)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("passkeys need a base URL with a host name")
	}
	w, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: s.opts.SiteName(),
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
	})
	if err != nil {
		return nil, err
	}
	waCache.Store(home, w)
	return w, nil
}

// loadWAUser loads user with its passkeys; create makes a user handle if
// the user has none yet.
func (s *Service) loadWAUser(ctx context.Context, u *db.User, create bool) (*waUser, error) {
	h, err := db.UserMeta(ctx, s.db.R, u.ID, metaWebAuthnHandle)
	if err != nil {
		return nil, err
	}
	handle, _ := base64.RawURLEncoding.DecodeString(h)
	if len(handle) == 0 && create {
		handle = make([]byte, 64)
		rand.Read(handle)
		if err := db.SetUserMeta(ctx, s.db.W, u.ID, metaWebAuthnHandle, base64.RawURLEncoding.EncodeToString(handle)); err != nil {
			return nil, err
		}
	}
	stored, err := db.WebAuthnCredentials(ctx, s.db.R, u.ID)
	if err != nil {
		return nil, err
	}
	wu := &waUser{u: u, handle: handle}
	for _, c := range stored {
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(c.Credential), &cred); err == nil {
			wu.creds = append(wu.creds, cred)
		}
	}
	return wu, nil
}

// Passkeys returns the stored passkeys of a user (for the profile list).
func (s *Service) Passkeys(ctx context.Context, userID int64) ([]db.WebAuthnCredential, error) {
	return db.WebAuthnCredentials(ctx, s.db.R, userID)
}

func sessionKey(prefix string, cur *Current) string {
	return prefix + hex.EncodeToString(cur.Session.IDHash)
}

// BeginPasskeyRegistration starts adding a passkey for the logged-in user
// and returns the options for navigator.credentials.create().
func (s *Service) BeginPasskeyRegistration(ctx context.Context, cur *Current) (any, error) {
	wa, err := s.webauthn()
	if err != nil {
		return nil, err
	}
	wu, err := s.loadWAUser(ctx, cur.User, true)
	if err != nil {
		return nil, err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(wu.creds))
	for _, c := range wu.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sd, err := wa.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(exclude),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		return nil, err
	}
	s.ceremonies.put(sessionKey("reg:", cur), sd, s.now())
	return opts, nil
}

// FinishPasskeyRegistration verifies the authenticator response in r's
// body and stores the passkey under name.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, cur *Current, r *http.Request, name string) error {
	v, ok := s.ceremonies.take(sessionKey("reg:", cur), s.now())
	if !ok {
		return ErrPasskey
	}
	wa, err := s.webauthn()
	if err != nil {
		return err
	}
	wu, err := s.loadWAUser(ctx, cur.User, true)
	if err != nil {
		return err
	}
	cred, err := wa.FinishRegistration(wu, *v.(*webauthn.SessionData), r)
	if err != nil {
		s.log.Info("passkey registration failed", "user", cur.User.ID, "err", err)
		return ErrPasskey
	}
	return s.storeCredential(ctx, cur.User.ID, cred, name)
}

func (s *Service) storeCredential(ctx context.Context, userID int64, cred *webauthn.Credential, name string) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	return db.InsertWebAuthnCredential(ctx, s.db.W, &db.WebAuthnCredential{
		ID: cred.ID, UserID: userID, Name: truncate(name, 60), Credential: string(data), Created: s.now().Unix(),
	})
}

// BeginPasskeyLogin starts a usernameless passkey login. The ceremony is
// bound to the login page's pre-session cookie.
func (s *Service) BeginPasskeyLogin(ctx context.Context, r *http.Request) (any, error) {
	pre := preKey(r)
	if pre == "" {
		return nil, ErrPasskey
	}
	wa, err := s.webauthn()
	if err != nil {
		return nil, err
	}
	opts, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	s.ceremonies.put("login:"+pre, sd, s.now())
	return opts, nil
}

// FinishPasskeyLogin verifies a usernameless passkey login and returns the
// user. A passkey login skips the password and its account lockout: it is
// not guessable and already includes user verification.
func (s *Service) FinishPasskeyLogin(ctx context.Context, r *http.Request) (*db.User, error) {
	pre := preKey(r)
	v, ok := s.ceremonies.take("login:"+pre, s.now())
	if pre == "" || !ok {
		return nil, ErrPasskey
	}
	wa, err := s.webauthn()
	if err != nil {
		return nil, err
	}
	var found *waUser
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		id, err := db.UserIDByMeta(ctx, s.db.R, metaWebAuthnHandle, base64.RawURLEncoding.EncodeToString(userHandle))
		if err != nil || id == 0 {
			return nil, ErrPasskey
		}
		u, err := db.GetUser(ctx, s.db.R, id)
		if err != nil {
			return nil, ErrPasskey
		}
		wu, err := s.loadWAUser(ctx, u, false)
		if err != nil {
			return nil, ErrPasskey
		}
		found = wu
		return wu, nil
	}
	_, cred, err := wa.FinishPasskeyLogin(handler, *v.(*webauthn.SessionData), r)
	if err != nil || found == nil {
		s.Audit(ctx, r, 0, ActLoginFailed, "passkey", 0, nil)
		return nil, ErrPasskey
	}
	if err := s.updateCredential(ctx, cred); err != nil {
		return nil, err
	}
	return found.u, nil
}

// BeginPasskeyAssertion starts a passkey check for the session's user: the
// second factor after the password, or a re-authentication.
func (s *Service) BeginPasskeyAssertion(ctx context.Context, cur *Current) (any, error) {
	wa, err := s.webauthn()
	if err != nil {
		return nil, err
	}
	wu, err := s.loadWAUser(ctx, cur.User, false)
	if err != nil {
		return nil, err
	}
	if len(wu.creds) == 0 {
		return nil, ErrPasskey
	}
	opts, sd, err := wa.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	s.ceremonies.put(sessionKey("assert:", cur), sd, s.now())
	return opts, nil
}

// FinishPasskeyAssertion verifies the passkey check started with
// BeginPasskeyAssertion.
func (s *Service) FinishPasskeyAssertion(ctx context.Context, cur *Current, r *http.Request) error {
	v, ok := s.ceremonies.take(sessionKey("assert:", cur), s.now())
	if !ok {
		return ErrPasskey
	}
	wa, err := s.webauthn()
	if err != nil {
		return err
	}
	wu, err := s.loadWAUser(ctx, cur.User, false)
	if err != nil {
		return err
	}
	cred, err := wa.FinishLogin(wu, *v.(*webauthn.SessionData), r)
	if err != nil {
		return ErrPasskey
	}
	return s.updateCredential(ctx, cred)
}

// updateCredential stores the new sign counter and flags after a login.
// A counter that did not increase hints at a cloned authenticator; it is
// logged (synced passkeys always report 0, so it cannot be enforced).
func (s *Service) updateCredential(ctx context.Context, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		s.log.Warn("passkey sign counter did not increase (possible cloned authenticator)", "credential", hex.EncodeToString(cred.ID))
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	return db.UpdateWebAuthnCredential(ctx, s.db.W, cred.ID, string(data), s.now().Unix())
}

// HasSecondFactor reports whether a user has TOTP or a passkey set up.
// Recovery codes alone do not count.
func (s *Service) HasSecondFactor(ctx context.Context, userID int64) (bool, error) {
	if ok, err := s.HasTOTP(ctx, userID); err != nil || ok {
		return ok, err
	}
	creds, err := db.WebAuthnCredentials(ctx, s.db.R, userID)
	return len(creds) > 0, err
}

// SecondFactorCount counts the TOTP and passkey factors of a user.
func (s *Service) SecondFactorCount(ctx context.Context, userID int64) (int, error) {
	n := 0
	if ok, err := s.HasTOTP(ctx, userID); err != nil {
		return 0, err
	} else if ok {
		n++
	}
	creds, err := db.WebAuthnCredentials(ctx, s.db.R, userID)
	return n + len(creds), err
}

// FindPasskey returns the stored passkey of a user with the base64url id.
func (s *Service) FindPasskey(ctx context.Context, userID int64, id string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return nil, false
	}
	creds, _ := db.WebAuthnCredentials(ctx, s.db.R, userID)
	for _, c := range creds {
		if bytes.Equal(c.ID, raw) {
			return raw, true
		}
	}
	return nil, false
}
