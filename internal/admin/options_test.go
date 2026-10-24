package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
)

func opt(e *testEnv, name string) string {
	var v any
	db.GetOption(context.Background(), e.d.R, name, &v)
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	}
	b, _ := jsonMarshal(v)
	return string(b)
}

func TestSettingsScreens(t *testing.T) {
	e := newEnv(t)
	admin := e.adminWithReauth()
	ed := e.session(auth.RoleEditor, db.SessionFull)
	for _, s := range []string{"general", "writing", "reading", "discussion", "media", "permalinks", "email"} {
		if rec := e.do("GET", "/admin/settings/"+s, admin, nil, nil); rec.Code != 200 {
			t.Errorf("GET %s: %d", s, rec.Code)
		}
		if rec := e.do("GET", "/admin/settings/"+s, ed, nil, nil); rec.Code != http.StatusForbidden {
			t.Errorf("editor GET %s: %d", s, rec.Code)
		}
	}
	general := url.Values{"blogname": {"My <b>Site</b>"}, "blogdescription": {"Tag line"}, "timezone_string": {"Europe/Vienna"},
		"date_format": {"custom"}, "date_format_custom": {"j. F Y"}, "time_format": {"H:i"}, "start_of_week": {"1"},
		"new_admin_email": {""}}
	if res := post(e, "/admin/settings/general", admin, general); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("general: %d", res.StatusCode)
	}
	if opt(e, "timezone_string") != "Europe/Vienna" || opt(e, "date_format") != "j. F Y" || opt(e, "blogname") != "My &lt;b&gt;Site&lt;/b&gt;" {
		t.Errorf("options: %q %q %q", opt(e, "timezone_string"), opt(e, "date_format"), opt(e, "blogname"))
	}
	// The title is escaped on the site.
	if body := e.do("GET", "/", nil, nil, nil).Body.String(); strings.Contains(body, "My <b>Site</b>") {
		t.Error("site title not escaped")
	}
	for _, bad := range []map[string]string{{"timezone_string": "Mars/Base"}, {"timezone_string": "UTC+99"}, {"blogname": ""},
		{"date_format": "custom", "date_format_custom": "<script>"}} {
		f := url.Values{}
		for k, v := range general {
			f[k] = v
		}
		for k, v := range bad {
			f.Set(k, v)
		}
		if res := post(e, "/admin/settings/general", admin, f); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%v accepted: %d", bad, res.StatusCode)
		}
	}
	general.Set("timezone_string", "UTC+5.5")
	post(e, "/admin/settings/general", admin, general)
	if opt(e, "timezone_string") != "" || opt(e, "gmt_offset") != "5.5" {
		t.Errorf("offset: %q %q", opt(e, "timezone_string"), opt(e, "gmt_offset"))
	}

	// Reading: search engine visibility.
	if res := post(e, "/admin/settings/reading", admin, url.Values{"show_on_front": {"posts"}, "posts_per_page": {"5"},
		"posts_per_rss": {"10"}, "rss_use_excerpt": {"0"}, "blog_public": {"0"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("reading: %d", res.StatusCode)
	}
	if body := e.do("GET", "/", nil, nil, nil).Body.String(); !strings.Contains(body, "noindex") {
		t.Error("discouraging search engines has no effect")
	}
	if res := post(e, "/admin/settings/reading", admin, url.Values{"show_on_front": {"page"}, "page_on_front": {"30"},
		"posts_per_page": {"5"}, "posts_per_rss": {"10"}, "rss_use_excerpt": {"0"}}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("draft as homepage accepted: %d", res.StatusCode)
	}

	// Discussion: closing comments on old posts.
	disc := url.Values{"default_comment_status": {"open"}, "close_comments_for_old_posts": {"1"}, "close_comments_days_old": {"1"},
		"thread_comments_depth": {"5"}, "comments_per_page": {"50"}, "default_comments_page": {"newest"}, "comment_order": {"asc"},
		"comment_max_links": {"2"}, "avatar_rating": {"G"}, "avatar_default": {"mystery"}, "require_name_email": {"1"}}
	e.d.W.Exec(`UPDATE posts SET date_gmt = ? WHERE id = 10`, db.Time{Time: time.Now().Add(-48 * time.Hour)})
	e.pub.Invalidate()
	if !strings.Contains(e.do("GET", "/hello/", nil, nil, nil).Body.String(), `id="commentform"`) {
		t.Fatal("no comment form before")
	}
	if res := post(e, "/admin/settings/discussion", admin, disc); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("discussion: %d", res.StatusCode)
	}
	if strings.Contains(e.do("GET", "/hello/", nil, nil, nil).Body.String(), `id="commentform"`) {
		t.Error("comments still open on an old post")
	}

	// Media.
	if res := post(e, "/admin/settings/media", admin, url.Values{"thumbnail_size_w": {"200"}, "thumbnail_size_h": {"200"},
		"medium_size_w": {"400"}, "medium_size_h": {"400"}, "large_size_w": {"1200"}, "large_size_h": {"1200"},
		"upload_max_mb": {"8"}, "uploads_use_yearmonth_folders": {"1"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("media: %d", res.StatusCode)
	}
	if opt(e, "upload_max_size") != "8388608" || opt(e, "thumbnail_crop") != "0" || opt(e, "thumbnail_size_w") != "200" {
		t.Errorf("media options: %q %q", opt(e, "upload_max_size"), opt(e, "thumbnail_crop"))
	}

	// Date preview.
	if rec := e.do("GET", "/admin/api/date-preview?format=Y", admin, nil, nil); !strings.Contains(rec.Body.String(), time.Now().Format("2006")) {
		t.Errorf("date preview: %s", rec.Body)
	}
}

func TestPermalinkSettings(t *testing.T) {
	e := newEnv(t)
	admin := e.adminWithReauth()
	for _, bad := range []string{"/%foo%/", "/no-tags/", "/%postname", "/<script>/%postname%/", "/%year%/%postname%%/"} {
		if res := post(e, "/admin/settings/permalinks", admin, url.Values{"permalink_structure": {"custom"}, "permalink_custom": {bad}}); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%q accepted: %d", bad, res.StatusCode)
		}
	}
	if res := post(e, "/admin/settings/permalinks", admin, url.Values{"permalink_structure": {"/archives/%post_id%"},
		"category_base": {"topics"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", res.StatusCode)
	}
	if rec := e.do("GET", "/archives/10", nil, nil, nil); rec.Code != 200 && rec.Code != http.StatusMovedPermanently {
		t.Errorf("new permalink: %d", rec.Code)
	}
	// The old URL redirects to the new one.
	rec := e.do("GET", "/hello/", nil, nil, nil)
	if rec.Code != http.StatusMovedPermanently || !regexp.MustCompile(`/archives/10/?$`).MatchString(rec.Header().Get("Location")) {
		t.Errorf("old URL: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.do("GET", "/topics/uncategorized/", nil, nil, nil); rec.Code != 200 {
		t.Errorf("category base: %d", rec.Code)
	}
}

func TestAdminEmailChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	db.SetOption(ctx, e.d.W, "admin_email", "old@example.org")
	e.pub.Invalidate()
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	db.SetSessionReauth(ctx, e.d.W, admin.Session.IDHash, 0)
	f := url.Values{"blogname": {"Test"}, "timezone_string": {"UTC+0"}, "date_format": {"F j, Y"}, "time_format": {"g:i a"},
		"start_of_week": {"1"}, "new_admin_email": {"new@example.org"}}
	if res := post(e, "/admin/settings/general", admin, f); !strings.HasPrefix(res.Header.Get("Location"), "/admin/reauth") {
		t.Fatalf("no reauth: %s", res.Header.Get("Location"))
	}
	e.auth.MarkReauth(ctx, admin)
	if res := post(e, "/admin/settings/general", admin, f); !strings.Contains(res.Header.Get("Location"), "msg=confirm") {
		t.Fatalf("save: %s", res.Header.Get("Location"))
	}
	if opt(e, "admin_email") != "old@example.org" || opt(e, "new_admin_email") != "new@example.org" {
		t.Errorf("changed before confirmation: %q", opt(e, "admin_email"))
	}
	m := e.mails()["new@example.org"]
	if len(m) != 1 {
		t.Fatalf("confirmation mail: %v", m)
	}
	link := regexp.MustCompile(`/admin/settings/general/confirm-email\?token=[A-Za-z0-9_-]{43}`).FindString(m[0])
	if rec := e.do("GET", link, admin, nil, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d", rec.Code)
	}
	if opt(e, "admin_email") != "new@example.org" || opt(e, "new_admin_email") != "" {
		t.Errorf("after confirmation: %q %q", opt(e, "admin_email"), opt(e, "new_admin_email"))
	}
	if len(e.mails()["old@example.org"]) != 1 {
		t.Error("old address not notified")
	}
	if rec := e.do("GET", link, admin, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("link reused: %d", rec.Code)
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
