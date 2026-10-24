package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/theinvisible/gopress/internal/db"
)

// Personal access tokens let AI agents use the MCP interface on behalf of
// a user. A token carries scopes; the capabilities of a request made with
// it are those of the user's role limited to the scopes (db.User.CapLimit),
// so every capability check applies unchanged. No scope grants
// unfiltered_html, editing other users' content, deleting or anything
// administrative: content from agents is always sanitized.

// Token scopes.
const (
	ScopeWrite   = "content:write"
	ScopePublish = "content:publish"
	ScopeMedia   = "media:upload"
	ScopeTerms   = "terms:manage"
)

// Scopes lists the scopes with a description, in display order.
var Scopes = []struct{ Name, Label string }{
	{ScopeWrite, "Write drafts: create posts and pages as drafts or for review, edit own unpublished ones"},
	{ScopePublish, "Publish: publish and schedule, edit own published posts and pages"},
	{ScopeMedia, "Upload media"},
	{ScopeTerms, "Create categories"},
}

var scopeCaps = map[string][]string{
	ScopeWrite:   {CapEditPosts, CapEditPages},
	ScopePublish: {CapPublishPosts, CapPublishPages, CapEditPublishedPosts, CapEditPublishedPages},
	ScopeMedia:   {CapUploadFiles},
	ScopeTerms:   {CapManageCategories},
}

// Token lifetimes that can be chosen, in days.
var TokenLifetimes = []int{7, 30, 90, 365}

const (
	// TokenPrefix starts every token (recognizable for secret scanners).
	TokenPrefix = "gpat_"
	// MaxAccessTokens is the most tokens one user may have.
	MaxAccessTokens = 10
	tokenTouchEvery = time.Minute
)

// Audit actions of access tokens.
const (
	ActTokenCreated    = "token_created"
	ActTokenRevoked    = "token_revoked"
	ActTokensRevokeAll = "tokens_revoked"
	ActMCPAuthFailed   = "mcp_auth_failed"
)

func init() {
	actionLabels[ActTokenCreated] = "Access token created"
	actionLabels[ActTokenRevoked] = "Access token revoked"
	actionLabels[ActTokensRevokeAll] = "All access tokens revoked"
	actionLabels[ActMCPAuthFailed] = "Rejected access token"
	noticeInterval[ActTokenCreated] = 0
}

// TokenInputError is a user-facing problem with a token request.
type TokenInputError struct{ Msg string }

func (e *TokenInputError) Error() string { return e.Msg }

// ErrTokenRejected is returned for unknown, expired or unusable tokens.
var ErrTokenRejected = errors.New("invalid access token")

// ValidScope reports whether name is a known scope.
func ValidScope(name string) bool { _, ok := scopeCaps[name]; return ok }

// UsableScopes returns the scopes that give u at least one capability.
func UsableScopes(u *db.User) []string {
	var out []string
	for _, sc := range Scopes {
		if slices.ContainsFunc(scopeCaps[sc.Name], func(c string) bool { return roleCaps[u.Role][c] }) {
			out = append(out, sc.Name)
		}
	}
	return out
}

// CanUseTokens reports whether u may create and use access tokens.
func CanUseTokens(u *db.User) bool { return u != nil && roleCaps[u.Role][CapEditPosts] }

// TokenScopes splits the stored scope list.
func TokenScopes(t *db.AccessToken) []string { return strings.Fields(t.Scopes) }

// HasScope reports whether t carries scope.
func HasScope(t *db.AccessToken, scope string) bool { return slices.Contains(TokenScopes(t), scope) }

func capLimit(scopes []string) map[string]bool {
	m := map[string]bool{CapRead: true}
	for _, sc := range scopes {
		for _, c := range scopeCaps[sc] {
			m[c] = true
		}
	}
	return m
}

func hashAccessToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

// CreateAccessToken creates a token for u and returns it; it is shown
// once, only its hash is stored. The caller checks re-authentication.
func (s *Service) CreateAccessToken(ctx context.Context, r *http.Request, u *db.User, name string, scopes []string, days int) (string, *db.AccessToken, error) {
	if !CanUseTokens(u) {
		return "", nil, &TokenInputError{"Your role cannot use access tokens."}
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", nil, &TokenInputError{"Please enter a name of up to 80 characters."}
	}
	if !slices.Contains(TokenLifetimes, days) {
		return "", nil, &TokenInputError{"Please choose an expiry."}
	}
	usable := UsableScopes(u)
	var list []string
	for _, sc := range Scopes { // stored in a fixed order, without duplicates
		if slices.Contains(scopes, sc.Name) {
			if !slices.Contains(usable, sc.Name) {
				return "", nil, &TokenInputError{"Your role does not allow the scope " + sc.Name + "."}
			}
			list = append(list, sc.Name)
		}
	}
	for _, sc := range scopes {
		if !ValidScope(sc) {
			return "", nil, &TokenInputError{"Unknown scope."}
		}
	}
	tok := TokenPrefix + newToken()
	now := s.now()
	t := &db.AccessToken{UserID: u.ID, Name: name, Prefix: tok[:len(TokenPrefix)+6], Hash: hashAccessToken(tok),
		Scopes: strings.Join(list, " "), Created: now.Unix(), Expires: now.Add(time.Duration(days) * 24 * time.Hour).Unix()}
	err := s.db.Tx(ctx, func(tx *sql.Tx) error { return db.InsertAccessToken(ctx, tx, t, MaxAccessTokens) })
	if errors.Is(err, db.ErrTooManyTokens) {
		return "", nil, &TokenInputError{"You have the maximum number of access tokens. Revoke one first."}
	}
	if err != nil {
		return "", nil, err
	}
	s.Audit(ctx, r, u.ID, ActTokenCreated, "token", t.ID, map[string]any{"name": name, "scopes": t.Scopes, "days": days})
	return tok, t, nil
}

// VerifyAccessToken returns the user of a valid token with its
// capabilities limited to the token's scopes.
func (s *Service) VerifyAccessToken(ctx context.Context, tok string) (*db.User, *db.AccessToken, error) {
	if len(tok) != len(TokenPrefix)+43 || !strings.HasPrefix(tok, TokenPrefix) {
		return nil, nil, ErrTokenRejected
	}
	t, err := db.AccessTokenByHash(ctx, s.db.R, hashAccessToken(tok))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrTokenRejected
	}
	if err != nil {
		return nil, nil, err
	}
	if t.Expires <= s.now().Unix() {
		return nil, nil, ErrTokenRejected
	}
	u, err := db.GetUser(ctx, s.db.R, t.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrTokenRejected
	}
	if err != nil {
		return nil, nil, err
	}
	if !CanUseTokens(u) { // demoted since
		return nil, nil, ErrTokenRejected
	}
	u.CapLimit = capLimit(TokenScopes(t))
	return u, t, nil
}

// TouchAccessToken records the use of t (at most once a minute).
func (s *Service) TouchAccessToken(ctx context.Context, t *db.AccessToken, ip string) {
	now := s.now().Unix()
	if now-t.LastUsed < int64(tokenTouchEvery/time.Second) && t.LastIP == ip {
		return
	}
	if err := db.TouchAccessToken(ctx, s.db.W, t.ID, now, ip); err != nil {
		s.log.Warn("access token use", "err", err)
	}
}

// RevokeAccessToken deletes token id of user userID; actor did it.
func (s *Service) RevokeAccessToken(ctx context.Context, r *http.Request, actor, userID, id int64) (bool, error) {
	ok, err := db.DeleteAccessToken(ctx, s.db.W, userID, id)
	if ok {
		s.Audit(ctx, r, actor, ActTokenRevoked, "user", userID, map[string]any{"token": id})
	}
	return ok, err
}

// RevokeAccessTokens deletes all tokens of user userID (actor 0: the
// system, e.g. after a password reset).
func (s *Service) RevokeAccessTokens(ctx context.Context, r *http.Request, actor, userID int64) error {
	n, err := db.DeleteAccessTokens(ctx, s.db.W, userID)
	if n > 0 {
		s.Audit(ctx, r, actor, ActTokensRevokeAll, "user", userID, map[string]any{"count": n})
	}
	return err
}

// Requests made with an access token carry it in their context, so the
// audit log records which token acted.
type tokenCtxKey struct{}

type tokenCtx struct {
	token *db.AccessToken
	ip    string
}

// WithAccessToken returns ctx marked as acting through token t from ip.
func WithAccessToken(ctx context.Context, t *db.AccessToken, ip string) context.Context {
	return context.WithValue(ctx, tokenCtxKey{}, &tokenCtx{t, ip})
}

// AccessTokenFrom returns the token of a request context, or nil.
func AccessTokenFrom(ctx context.Context) *db.AccessToken {
	if v, ok := ctx.Value(tokenCtxKey{}).(*tokenCtx); ok {
		return v.token
	}
	return nil
}
