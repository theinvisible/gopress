package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func customizeForm(over map[string]string) url.Values {
	f := url.Values{"blogname": {"Test"}, "blogdescription": {""}, "display_header_text": {"1"}, "header_textcolor": {"#515151"},
		"background_color": {"#e6e6e6"}, "background_position": {"left top"}, "background_size": {"auto"}, "background_repeat": {"1"},
		"menu_primary": {"0"}, "show_on_front": {"posts"}, "page_on_front": {"0"}, "page_for_posts": {"0"}, "custom_css": {""}}
	for k, v := range over {
		f.Set(k, v)
	}
	return f
}

func TestCustomizer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if rec := e.do("GET", "/admin/customize", e.session(auth.RoleEditor, db.SessionFull), nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("editor: %d", rec.Code)
	}
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	if rec := e.do("GET", "/admin/customize", admin, nil, nil); rec.Code != 200 {
		t.Fatalf("page: %d", rec.Code)
	}
	draft := customizeForm(map[string]string{"blogname": "Preview <Title>", "custom_css": "body{color:red}</style><script>x</script>"})
	if res := post(e, "/admin/api/customize/draft", admin, draft); res.StatusCode != 200 {
		t.Fatalf("draft: %d", res.StatusCode)
	}
	iframe := map[string]string{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Site": "same-origin"}
	if b := e.do("GET", "/", admin, nil, iframe).Body.String(); !strings.Contains(b, "Preview &lt;Title&gt;") || strings.Contains(b, "<script>x") {
		t.Error("draft not shown in the preview frame (or CSS not sanitized)")
	}
	// Other frames (the editor's live preview) show the published look.
	if b := e.do("GET", "/?gp_live_preview=1", admin, nil, iframe).Body.String(); strings.Contains(b, "Preview &lt;Title&gt;") {
		t.Error("customizer draft in the editor's preview frame")
	}
	if b := e.do("GET", "/", admin, nil, nil).Body.String(); strings.Contains(b, "Preview &lt;Title&gt;") || !strings.Contains(b, "wpadminbar") {
		t.Error("draft shown outside the preview frame, or no toolbar")
	}
	if b := e.do("GET", "/", nil, nil, iframe).Body.String(); strings.Contains(b, "Preview") {
		t.Error("draft shown to visitors")
	}
	if res := post(e, "/admin/api/customize/draft", admin, customizeForm(map[string]string{"header_textcolor": "red;}body{x"})); res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid color: %d", res.StatusCode)
	}
	// Site icon from the media library; publish.
	rec := e.upload(admin, "icon.png", testPNG(600, 600), 0, true)
	var it mediaItem
	json.Unmarshal(rec.Body.Bytes(), &it)
	draft.Set("site_icon", strconv.FormatInt(it.ID, 10))
	if res := post(e, "/admin/customize/publish", admin, draft); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish: %d", res.StatusCode)
	}
	b := e.do("GET", "/", nil, nil, nil).Body.String()
	if !strings.Contains(b, "Preview &lt;Title&gt;") || !strings.Contains(b, `-32x32.png" sizes="32x32"`) || !strings.Contains(b, `rel="apple-touch-icon"`) {
		t.Error("published values not on the site")
	}
	if v, _ := db.UserMeta(ctx, e.d.R, e.users[auth.RoleAdministrator].ID, metaCustomizeDraft); v != "" {
		t.Error("draft kept after publishing")
	}
	// The menu location points to an existing menu only.
	if res := post(e, "/admin/customize/publish", admin, customizeForm(map[string]string{"menu_primary": "999"})); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown menu: %d", res.StatusCode)
	}
}

func TestToolbarPreference(t *testing.T) {
	e := newEnv(t)
	ed := e.session(auth.RoleEditor, db.SessionFull)
	if b := e.do("GET", "/hello/", ed, nil, nil).Body.String(); !strings.Contains(b, "wpadminbar") || !strings.Contains(b, "/admin/posts/10/edit") {
		t.Fatal("no toolbar for a logged-in editor")
	}
	post(e, "/admin/profile", ed, url.Values{"email": {"editor@example.org"}, "display_name": {"editor"}})
	if b := e.do("GET", "/hello/", ed, nil, nil).Body.String(); strings.Contains(b, "wpadminbar") {
		t.Error("toolbar shown although switched off")
	}
	// Pending logins (second factor missing) are not logged in on the site.
	if b := e.do("GET", "/hello/", e.session(auth.RoleAdministrator, db.SessionMFA), nil, nil).Body.String(); strings.Contains(b, "wpadminbar") {
		t.Error("toolbar for a pending login")
	}
}
