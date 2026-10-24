package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

// Personal access tokens for AI agents (MCP interface). Every user who can
// write posts manages their own tokens; administrators can see and revoke
// the tokens of others but never create one for someone else.

func (a *Admin) registerTokens(mux *http.ServeMux) {
	mux.Handle("GET /admin/profile/tokens", a.page(auth.CapEditPosts, a.tokensPage))
	mux.Handle("POST /admin/profile/tokens", a.page(auth.CapEditPosts, a.tokenCreate))
	mux.Handle("POST /admin/profile/tokens/revoke-all", a.page(auth.CapEditPosts, a.tokenRevokeAll))
	mux.Handle("POST /admin/profile/tokens/{tid}/revoke", a.page(auth.CapEditPosts, a.tokenRevoke))
	mux.Handle("POST /admin/users/{id}/tokens/{tid}/revoke", a.page(auth.CapEditUsers, a.userTokenRevoke))
}

type tokenRow struct {
	ID                        int64
	Name, Prefix              string
	Scopes                    []string
	Created, Expires, LastUse string
	LastIP                    string
	Expired                   bool
	RevokeURL                 string
}

type scopeOption struct {
	Name, Label string
	Checked     bool
}

type tokensData struct {
	basePage
	Enabled   bool
	CanManage bool // may turn the interface on
	Endpoint  string
	Tokens    []tokenRow
	Scopes    []scopeOption
	Days      []int
	FormDays  int
	Name      string
	Error     string
	Reauth    bool
	Max       int
	New       *newToken // shown once after creating
}

type newToken struct {
	Token, Name string
}

var tokenMessages = map[string]notice{
	"revoked":    {"success", "Access token revoked."},
	"revokedall": {"success", "All access tokens revoked."},
}

// tokenRows lists the tokens of a user; revoke is the URL prefix of the
// revoke action.
func (a *Admin) tokenRows(ctx context.Context, c *reqCtx, userID int64, revoke string) ([]tokenRow, error) {
	list, err := db.AccessTokens(ctx, a.db.R, userID)
	if err != nil {
		return nil, err
	}
	when := func(t int64) string {
		if t == 0 {
			return "never"
		}
		return time.Unix(t, 0).In(c.settings.Loc).Format("2006/01/02 15:04")
	}
	now := time.Now().Unix()
	var rows []tokenRow
	for _, t := range list {
		rows = append(rows, tokenRow{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: auth.TokenScopes(t),
			Created: when(t.Created), Expires: when(t.Expires), LastUse: when(t.LastUsed), LastIP: t.LastIP, Expired: t.Expires <= now,
			RevokeURL: revoke + strconv.FormatInt(t.ID, 10) + "/revoke"})
	}
	return rows, nil
}

// GET /admin/profile/tokens
func (a *Admin) tokensPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderTokens(w, r, c, "", nil, 0, "")
}

func (a *Admin) renderTokens(w http.ResponseWriter, r *http.Request, c *reqCtx, name string, scopes []string, days int, errMsg string) error {
	ctx := r.Context()
	d := &tokensData{basePage: a.base(ctx, c, "Access Tokens", "tokens"), Enabled: c.settings.MCPEnabled,
		Endpoint: strings.TrimRight(c.settings.Home, "/") + "/mcp", Days: auth.TokenLifetimes, FormDays: days, Name: name,
		Error: errMsg, Reauth: a.auth.RecentlyAuthenticated(c.cur), Max: auth.MaxAccessTokens,
		CanManage: auth.Can(c.user, auth.CapManageOptions)}
	if msg, ok := tokenMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	if v, ok := a.flash.take(a.flashKey(c, "token")); ok {
		d.New = v.(*newToken)
	}
	if d.FormDays == 0 {
		d.FormDays = 30
	}
	if scopes == nil {
		scopes = []string{auth.ScopeWrite}
	}
	usable := auth.UsableScopes(c.user)
	for _, sc := range auth.Scopes {
		if contains(usable, sc.Name) {
			d.Scopes = append(d.Scopes, scopeOption{Name: sc.Name, Label: sc.Label, Checked: contains(scopes, sc.Name)})
		}
	}
	var err error
	if d.Tokens, err = a.tokenRows(ctx, c, c.user.ID, "/admin/profile/tokens/"); err != nil {
		return err
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "tokens", d)
}

// POST /admin/profile/tokens
func (a *Admin) tokenCreate(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if !c.settings.MCPEnabled {
		return &httpError{http.StatusForbidden, "The MCP interface is turned off."}
	}
	if a.needReauth(w, r, c, "/admin/profile/tokens") {
		return nil
	}
	name := r.PostFormValue("name")
	scopes := r.PostForm["scopes"]
	days, _ := strconv.Atoi(r.PostFormValue("days"))
	tok, _, err := a.auth.CreateAccessToken(r.Context(), r, c.user, name, scopes, days)
	var ie *auth.TokenInputError
	if errors.As(err, &ie) {
		if scopes == nil {
			scopes = []string{}
		}
		return a.renderTokens(w, r, c, name, scopes, days, ie.Msg)
	}
	if err != nil {
		return err
	}
	a.flash.put(a.flashKey(c, "token"), &newToken{Token: tok, Name: strings.TrimSpace(name)})
	return seeOther(w, r, "/admin/profile/tokens")
}

// POST /admin/profile/tokens/{tid}/revoke
func (a *Admin) tokenRevoke(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ok, err := a.auth.RevokeAccessToken(r.Context(), r, c.user.ID, c.user.ID, atoi64(r.PathValue("tid")))
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound
	}
	return seeOther(w, r, "/admin/profile/tokens?msg=revoked")
}

// POST /admin/profile/tokens/revoke-all
func (a *Admin) tokenRevokeAll(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if err := a.auth.RevokeAccessTokens(r.Context(), r, c.user.ID, c.user.ID); err != nil {
		return err
	}
	return seeOther(w, r, "/admin/profile/tokens?msg=revokedall")
}

// POST /admin/users/{id}/tokens/{tid}/revoke
func (a *Admin) userTokenRevoke(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	u, err := a.targetUser(r)
	if err != nil {
		return err
	}
	ok, err := a.auth.RevokeAccessToken(r.Context(), r, c.user.ID, u.ID, atoi64(r.PathValue("tid")))
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound
	}
	return seeOther(w, r, "/admin/users/"+strconv.FormatInt(u.ID, 10)+"/edit?msg=tokenrevoked#tokens")
}
