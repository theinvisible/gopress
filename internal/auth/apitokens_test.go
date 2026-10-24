package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
)

func TestAccessTokenLifecycle(t *testing.T) {
	s, d, clk := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "ann", RoleAuthor, "correct horse battery")

	tok, at, err := s.CreateAccessToken(ctx, nil, u, "Release bot", []string{ScopeWrite, ScopeMedia}, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) != len(TokenPrefix)+43 || !strings.HasPrefix(tok, at.Prefix) {
		t.Fatalf("token format: %q prefix %q", tok, at.Prefix)
	}
	// Only the hash is stored.
	var n int
	d.R.QueryRow(`SELECT COUNT(*) FROM access_tokens WHERE hash = ? OR instr(prefix || name || scopes, ?) > 0`, tok, tok[len(TokenPrefix):]).Scan(&n)
	if n != 0 {
		t.Fatal("token stored in clear")
	}

	got, gt, err := s.VerifyAccessToken(ctx, tok)
	if err != nil || got.ID != u.ID || gt.ID != at.ID {
		t.Fatalf("verify: %v", err)
	}
	if !Can(got, CapEditPosts) || !Can(got, CapUploadFiles) || Can(got, CapPublishPosts) || Can(got, CapEditPublishedPosts) {
		t.Fatalf("caps of the scoped user wrong: %v", got.CapLimit)
	}
	last := "A"
	if strings.HasSuffix(tok, last) {
		last = "B"
	}
	for _, bad := range []string{"", tok + "x", tok[:len(tok)-1] + last, "Bearer " + tok, strings.ToUpper(tok)} {
		if _, _, err := s.VerifyAccessToken(ctx, bad); !errors.Is(err, ErrTokenRejected) {
			t.Errorf("token %q accepted", bad)
		}
	}

	// Expiry.
	clk.add(31 * 24 * time.Hour)
	if _, _, err := s.VerifyAccessToken(ctx, tok); !errors.Is(err, ErrTokenRejected) {
		t.Fatal("expired token accepted")
	}
	clk.add(-31 * 24 * time.Hour)

	// Revocation: only by the owner's id.
	other := addUser(t, d, "bob", RoleAuthor, "correct horse battery")
	if ok, _ := s.RevokeAccessToken(ctx, nil, other.ID, other.ID, at.ID); ok {
		t.Fatal("revoked a foreign token")
	}
	if ok, err := s.RevokeAccessToken(ctx, nil, u.ID, u.ID, at.ID); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if _, _, err := s.VerifyAccessToken(ctx, tok); !errors.Is(err, ErrTokenRejected) {
		t.Fatal("revoked token accepted")
	}
}

func TestAccessTokenRules(t *testing.T) {
	s, d, _ := newTestService(t)
	ctx := context.Background()
	contrib := addUser(t, d, "con", RoleContributor, "correct horse battery")
	sub := addUser(t, d, "sub", RoleSubscriber, "correct horse battery")
	admin := addUser(t, d, "adm", RoleAdministrator, "correct horse battery")

	var ie *TokenInputError
	if _, _, err := s.CreateAccessToken(ctx, nil, sub, "x", []string{ScopeWrite}, 30); !errors.As(err, &ie) {
		t.Fatalf("subscriber got a token: %v", err)
	}
	if _, _, err := s.CreateAccessToken(ctx, nil, contrib, "x", []string{ScopePublish}, 30); !errors.As(err, &ie) {
		t.Fatalf("contributor got the publish scope: %v", err)
	}
	if _, _, err := s.CreateAccessToken(ctx, nil, contrib, "x", []string{"admin"}, 30); !errors.As(err, &ie) {
		t.Fatalf("unknown scope: %v", err)
	}
	for _, days := range []int{0, -1, 3650} {
		if _, _, err := s.CreateAccessToken(ctx, nil, contrib, "x", []string{ScopeWrite}, days); !errors.As(err, &ie) {
			t.Fatalf("lifetime %d accepted", days)
		}
	}
	for _, name := range []string{"", "   ", "a\nb", strings.Repeat("x", 81)} {
		if _, _, err := s.CreateAccessToken(ctx, nil, contrib, name, []string{ScopeWrite}, 30); !errors.As(err, &ie) {
			t.Fatalf("name %q accepted", name)
		}
	}

	// An administrator's token never grants more than its scopes, and no
	// scope grants unfiltered HTML, others' content or administration.
	tok, _, err := s.CreateAccessToken(ctx, nil, admin, "all", []string{ScopeWrite, ScopePublish, ScopeMedia, ScopeTerms}, 7)
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := s.VerifyAccessToken(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{CapUnfilteredHTML, CapEditOthersPosts, CapEditOthersPages, CapDeletePosts, CapManageOptions,
		CapEditUsers, CapCreateUsers, CapModerateComments, CapEditThemeOptions, CapActivatePlugins, CapEditPrivatePosts} {
		if Can(u, c) {
			t.Errorf("token grants %s", c)
		}
	}
	if !Can(u, CapPublishPosts) || !Can(u, CapManageCategories) {
		t.Error("granted scopes missing")
	}

	// Limit per user.
	for i := 1; i < MaxAccessTokens; i++ {
		if _, _, err := s.CreateAccessToken(ctx, nil, admin, "t", []string{ScopeWrite}, 7); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateAccessToken(ctx, nil, admin, "t", []string{ScopeWrite}, 7); !errors.As(err, &ie) {
		t.Fatalf("token %d created: %v", MaxAccessTokens+1, err)
	}

	// Demotion makes the tokens useless; deleting the user removes them.
	if err := db.SetUserRole(ctx, d.W, admin.ID, RoleSubscriber); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.VerifyAccessToken(ctx, tok); !errors.Is(err, ErrTokenRejected) {
		t.Fatal("token of a demoted user accepted")
	}
	if _, err := d.W.Exec(`DELETE FROM users WHERE id = ?`, admin.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	d.R.QueryRow(`SELECT COUNT(*) FROM access_tokens WHERE user_id = ?`, admin.ID).Scan(&n)
	if n != 0 {
		t.Fatal("tokens survive the user")
	}
}

func TestAuditMarksTokenRequests(t *testing.T) {
	s, d, _ := newTestService(t)
	ctx := context.Background()
	u := addUser(t, d, "ann", RoleAuthor, "correct horse battery")
	_, at, err := s.CreateAccessToken(ctx, nil, u, "bot", []string{ScopeWrite}, 7)
	if err != nil {
		t.Fatal(err)
	}
	s.Audit(WithAccessToken(ctx, at, "192.0.2.7"), nil, u.ID, "post_created", "post", 9, map[string]any{"status": "draft"})
	var ip, detail string
	d.R.QueryRow(`SELECT ip, detail FROM audit_log WHERE action = 'post_created'`).Scan(&ip, &detail)
	if ip != "192.0.2.7" || !strings.Contains(detail, `"via":"mcp"`) || !strings.Contains(detail, `"token_name":"bot"`) || !strings.Contains(detail, `"status":"draft"`) {
		t.Fatalf("audit: %q %q", ip, detail)
	}
}
