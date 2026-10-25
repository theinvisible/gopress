package plugins

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/plugins/bundled"
)

// bundledPermissions are the approved permissions of the bundled plugins:
// a change must be deliberate (and is shown again for approval).
var bundledPermissions = map[string]string{
	"akismet": `{"http":["rest.akismet.com"],"hooks":["comment_pre_save","comment_status_changed"],` +
		`"data":["import:option:akismet_spam_count","import:option:akismet_strictness","import:option:wordpress_api_key"]}`,
	"foto-slider-lightbox": `{"data":["import:post:fslb_gallery","media:read"],"frontend_assets":["assets/swiper/swiper-bundle.min.css",` +
		`"assets/swiper/swiper-bundle.min.js","assets/view.css","assets/view.js"]}`,
	"paypal-donations": `{"http":["ipnpb.paypal.com","ipnpb.sandbox.paypal.com"],"routes":["/ipn"],` +
		`"legacy_routes":[{"query":{"ppd_paypal_ipn":"process"},"route":"/ipn"}],` +
		`"data":["import:option:paypal_donations_options","import:post:donation_list","mail:admins","notify:admins"]}`,
}

func TestBundledPackages(t *testing.T) {
	files, err := fs.Glob(bundled.FS(), "*.zip")
	if err != nil || len(files) != len(bundledPermissions) {
		t.Fatalf("bundled zips: %v %v", files, err)
	}
	for _, f := range files {
		b, _ := fs.ReadFile(bundled.FS(), f)
		p, err := ReadZip(b, "bundled")
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if got := p.Manifest.Permissions.Canonical(); got != bundledPermissions[p.Manifest.Name] {
			t.Errorf("%s permissions changed:\n%s", p.Manifest.Name, got)
		}
	}
}

// The bundled plugins on the real host: rendering through the site, the
// IPN through its old URL, the comment check.
func TestBundledPlugins(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles three plugins")
	}
	e := newHostEnv(t)
	ctx := context.Background()
	e.site.SetExtensions(e.h)
	e.site.SetCommentFilter(e.h.CommentFilter())
	for _, f := range []string{"foto-slider-lightbox.zip", "paypal-donations.zip", "akismet.zip"} {
		b, _ := fs.ReadFile(bundled.FS(), f)
		p, _, err := e.h.Install(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.h.Activate(ctx, p.Manifest.Name, ""); err != nil {
			t.Fatal(err)
		}
	}
	db.InsertUser(ctx, e.d.W, &db.User{ID: 2, Login: "admin", Email: "admin@example.org", Role: "administrator"})

	// Gallery: imported, then rendered on a page with its assets.
	db.Stash(ctx, e.d.W, "wordpress", "post:fslb_gallery", "791", map[string]any{"id": 791, "status": "publish", "title": "Trip",
		"meta": map[string]any{"_fslb_image_ids": []any{20, 21}, "_fslb_show_captions": "1"}})
	if _, err := e.h.Migrate(ctx, "foto-slider-lightbox"); err != nil {
		t.Fatal(err)
	}
	// PayPal: settings imported from the WordPress option.
	db.Stash(ctx, e.d.W, "wordpress", "option", "paypal_donations_options", map[string]any{"paypal_account": "donate@example.org",
		"currency_code": "EUR", "amount": "0", "button": "large", "new_tab": "1", "return_method": "0"})
	if _, err := e.h.Migrate(ctx, "paypal-donations"); err != nil {
		t.Fatal(err)
	}
	now := db.Time{Time: time.Now().UTC()}
	db.InsertPost(ctx, e.d.W, &db.Post{ID: 50, Type: db.TypePage, Status: db.StatusPublish, Slug: "plugins", Title: "Plugins",
		Content: "<p>Gallery:</p>\n[fotogalerie id=\"791\"]\n<p>Donate:</p>\n[paypal-donation purpose=\"Blog\"]", ContentFormat: db.FormatHTML,
		Date: now, DateGMT: now, Modified: now, ModifiedGMT: now})
	e.site.Invalidate()
	page := e.get("/plugins/").Body.String()
	for _, want := range []string{`<div class="fslb-gallery fslb-gallery--shortcode" data-captions="1"`, `data-full="https://example.org/wp-content/uploads/2026/10/pic.png"`,
		`/plugins/foto-slider-lightbox/assets/swiper/swiper-bundle.min.js`, `/plugins/foto-slider-lightbox/assets/view.js`,
		`<form action="https://www.paypal.com/cgi-bin/webscr" method="post" target="_blank">`, `name="business" value="donate@example.org"`,
		`name="item_name" value="Blog"`, `<input type="image" style="cursor: pointer"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(page, "secret.png") {
		t.Error("image of a draft shown")
	}

	// IPN through the old URL, after turning recording on.
	set, _ := e.h.Settings(ctx, "paypal-donations", "settings")
	set["ipn_enabled"] = true
	e.h.SaveSettings(ctx, "paypal-donations", "settings", set)
	ipn := func(txn string) int {
		body := url.Values{"txn_id": {txn}, "payment_status": {"Completed"}, "txn_type": {"web_accept"}, "mc_gross": {"5.00"},
			"mc_currency": {"EUR"}, "receiver_email": {"donate@example.org"}, "first_name": {"Ann"}}.Encode()
		r := httptest.NewRequest("POST", "https://example.org/?ppd_paypal_ipn=process", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.site.ServeHTTP(rec, r)
		return rec.Code
	}
	if code := ipn("GOOD1"); code != http.StatusOK {
		t.Errorf("IPN: %d", code)
	}
	if v, ok, _ := db.KVGet(ctx, e.d.R, "paypal-donations", "donations", "GOOD1"); !ok || !strings.Contains(v, `"verified":true`) {
		t.Errorf("donation: %s", v)
	}
	ipn("FORGED1")
	if _, ok, _ := db.KVGet(ctx, e.d.R, "paypal-donations", "donations", "FORGED1"); ok {
		t.Error("forged notification recorded")
	}
	if len(e.mail.sent) == 0 || !strings.Contains(e.mail.sent[0], "New donation: 5.00 EUR from Ann") {
		t.Errorf("mail: %v", e.mail.sent)
	}

	// Akismet: spam goes to the spam folder.
	e.h.SaveSettings(ctx, "akismet", "settings", map[string]any{"api_key": "0123456789ab", "strictness": "spam"})
	filter := e.h.CommentFilter()
	post, _ := db.GetPost(ctx, e.d.R, 10)
	r := httptest.NewRequest("POST", "https://example.org/wp-comments-post.php", nil)
	if v := filter(r, &db.Comment{PostID: 10, Content: "best casino", AuthorIP: "203.0.113.9"}, post); v != db.CommentSpam {
		t.Errorf("spam: %q", v)
	}
	if v := filter(r, &db.Comment{PostID: 10, Content: "nice", AuthorIP: "203.0.113.9"}, post); v != "" {
		t.Errorf("ham: %q", v)
	}
}
