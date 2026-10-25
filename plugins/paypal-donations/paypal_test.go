package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gp "github.com/theinvisible/gopress/pdk/go"
	"github.com/theinvisible/gopress/pdk/go/fakehost"
)

// liveSettings are the options of the source site (shape only; the
// account is a placeholder).
func liveSettings() map[string]any {
	return map[string]any{"paypal_account": "donate@example.org", "currency_code": "EUR", "amount": 0.0, "button": "large",
		"button_localized": "en_US", "new_tab": true, "checkout_language": "", "return_method": "0", "ipn_mail": true}
}

func newHost(t *testing.T) *fakehost.Host {
	t.Helper()
	h := fakehost.New()
	if err := h.LoadTemplates("templates"); err != nil {
		t.Fatal(err)
	}
	h.Settings["settings"] = liveSettings()
	h.Install()
	return h
}

func shortcodeHTML(t *testing.T, attrs map[string]string) string {
	t.Helper()
	var out gp.Render
	if err := gp.CallExport("render_shortcode", gp.ShortcodeRequest{Tag: "paypal-donation", Attrs: attrs}, &out); err != nil {
		t.Fatal(err)
	}
	return out.HTML
}

func TestForm(t *testing.T) {
	h := newHost(t)
	got := shortcodeHTML(t, nil)
	// The markup of the source site's widget (local/ref), in its order.
	want := `<form action="https://www.paypal.com/cgi-bin/webscr" method="post" target="_blank">
    <div class="paypal-donations">
        <input type="hidden" name="cmd" value="_donations" />
        <input type="hidden" name="bn" value="TipsandTricks_SP" />
        <input type="hidden" name="business" value="donate@example.org" />
        <input type="hidden" name="rm" value="0" />
        <input type="hidden" name="currency_code" value="EUR" />
        <input type="image" style="cursor: pointer;" src="https://www.paypalobjects.com/en_US/i/btn/btn_donate_LG.gif" name="submit" alt="PayPal - The safer, easier way to pay online." />
        <img alt="" src="https://www.paypalobjects.com/en_US/i/scr/pixel.gif" width="1" height="1" />
    </div>
</form>
`
	if got != want {
		t.Errorf("form:\n%s\nwant:\n%s", got, want)
	}

	set := h.Settings["settings"]
	set["sandbox"], set["new_tab"], set["disable_stats"], set["center_button"] = true, false, true, true
	set["button"], set["button_localized"], set["set_checkout_language"], set["checkout_language"] = "cards", "de_DE/AT", true, "AT"
	set["amount"], set["purpose"], set["page_style"], set["return_page"] = 5.5, "Default", "primary", "https://example.org/thanks/"
	got = shortcodeHTML(t, map[string]string{"purpose": `Coffee "&" <cake>`, "reference": "R1", "amount": "12"})
	for _, w := range []string{`action="https://www.sandbox.paypal.com/cgi-bin/webscr" method="post">`,
		`<div class="paypal-donations" style="text-align: center">`, `name="page_style" value="primary"`,
		`name="return" value="https://example.org/thanks/"`, `name="item_name" value="Coffee &#34;&amp;&#34; &lt;cake&gt;"`,
		`name="item_number" value="R1"`, `name="amount" value="12"`, `name="lc" value="AT"`,
		`src="https://www.paypalobjects.com/de_DE/AT/i/btn/btn_donateCC_LG.gif"`} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in\n%s", w, got)
		}
	}
	if strings.Contains(got, "pixel.gif") || strings.Contains(got, "notify_url") {
		t.Errorf("pixel or notify_url: %s", got)
	}
	// A bad amount is ignored (the default applies), a javascript: button
	// or return URL too.
	got = shortcodeHTML(t, map[string]string{"amount": "lots", "button_url": "javascript:alert(1)", "return_page": "javascript:x"})
	if !strings.Contains(got, `name="amount" value="5.50"`) || strings.Contains(got, "javascript") {
		t.Errorf("bad attributes: %s", got)
	}
	// IPN on: PayPal is told where to report.
	set["ipn_enabled"] = true
	if got = shortcodeHTML(t, nil); !strings.Contains(got, `name="notify_url" value="https://example.org/plugins/paypal-donations/ipn"`) {
		t.Errorf("notify_url: %s", got)
	}
	// No account: nothing.
	set["paypal_account"] = ""
	if got = shortcodeHTML(t, nil); got != "" {
		t.Errorf("without account: %s", got)
	}
}

func TestWidget(t *testing.T) {
	newHost(t)
	var out gp.Render
	err := gp.CallExport("render_widget", gp.WidgetRequest{Type: "paypal_donations", TitleHTML: `<h3 class="widget-title">Donate</h3>`,
		Settings: map[string]any{"title": "Donate", "text": "Thanks <b>a lot</b>!\nReally.\n\nSecond", "purpose": "Blog"}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`<h3 class="widget-title">Donate</h3><p>Thanks &lt;b&gt;a lot&lt;/b&gt;!<br />
Really.</p>`, `<p>Second</p>`, `name="item_name" value="Blog"`} {
		if !strings.Contains(out.HTML, w) {
			t.Errorf("missing %s in\n%s", w, out.HTML)
		}
	}
}

// ipn sends a notification; paypal answers the postback.
func ipn(t *testing.T, h *fakehost.Host, method, body string, paypal func(gp.HTTPRequest) (gp.HTTPResponse, error)) gp.Response {
	t.Helper()
	h.Fetch = paypal
	var out gp.Response
	if err := gp.CallExport("http_route", gp.Request{Method: method, Path: "/ipn", Body: body}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func verified(body string) func(gp.HTTPRequest) (gp.HTTPResponse, error) {
	return func(r gp.HTTPRequest) (gp.HTTPResponse, error) { return gp.HTTPResponse{Status: 200, Body: body}, nil }
}

const payment = "mc_gross=10.00&mc_fee=0.60&mc_currency=EUR&payment_status=Completed&txn_type=web_accept&txn_id=1AB23456CD789012E" +
	"&receiver_email=Donate%40Example.org&first_name=Ann&last_name=Example&payer_email=ann%40example.net&item_name=Blog" +
	"&payment_date=10%3A23%3A45+Jan+02%2C+2026+PST&address_street=Secret+Street+1&charset=UTF-8"

func TestIPN(t *testing.T) {
	h := newHost(t)
	if r := ipn(t, h, "POST", payment, verified("VERIFIED")); r.Status != 404 {
		t.Errorf("IPN off: %d", r.Status)
	}
	h.Settings["settings"]["ipn_enabled"] = true
	if r := ipn(t, h, "GET", "", verified("VERIFIED")); r.Status != 405 {
		t.Errorf("GET: %d", r.Status)
	}
	// The postback is the message, unchanged, after cmd=_notify-validate.
	r := ipn(t, h, "POST", payment, verified("VERIFIED"))
	if r.Status != 200 {
		t.Fatalf("verified: %d", r.Status)
	}
	last := h.Requests[len(h.Requests)-1]
	if last.URL != "https://ipnpb.paypal.com/cgi-bin/webscr" || last.Body != "cmd=_notify-validate&"+payment || last.Method != "POST" {
		t.Errorf("postback: %+v", last)
	}
	var d Donation
	if !h.Get("donations", "1AB23456CD789012E", &d) {
		t.Fatal("not recorded")
	}
	if d.Amount != "10.00" || d.Currency != "EUR" || d.Name() != "Ann Example" || d.Date != "2026-01-02T18:23:45Z" || !d.Verified {
		t.Errorf("donation: %+v", d)
	}
	if raw, _ := json.Marshal(d); strings.Contains(string(raw), "Secret") {
		t.Error("address stored")
	}
	if len(h.Mails) != 1 || !strings.Contains(h.Mails[0].Subject, "10.00 EUR from Ann Example") || len(h.Notices) != 1 {
		t.Errorf("mails %v, notices %v", h.Mails, h.Notices)
	}
	// Repeated: recorded once.
	ipn(t, h, "POST", payment, verified("VERIFIED"))
	if len(h.Mails) != 1 {
		t.Error("duplicate notification mailed again")
	}
	// Refund of it.
	ipn(t, h, "POST", "payment_status=Refunded&parent_txn_id=1AB23456CD789012E&txn_id=9ZZ&receiver_email=donate%40example.org&mc_gross=-10.00", verified("VERIFIED"))
	h.Get("donations", "1AB23456CD789012E", &d)
	if d.Status != "Refunded" {
		t.Errorf("refund: %s", d.Status)
	}

	other := strings.Replace(strings.Replace(payment, "1AB23456CD789012E", "2AB", 1), "Donate%40Example.org", "evil%40example.com", 1)
	ipn(t, h, "POST", other, verified("VERIFIED"))
	if h.Get("donations", "2AB", &d) {
		t.Error("payment to another account recorded")
	}
	forged := strings.Replace(payment, "1AB23456CD789012E", "3AB", 1)
	if r := ipn(t, h, "POST", forged, verified("INVALID")); r.Status != 200 || h.Get("donations", "3AB", &d) {
		t.Errorf("invalid: %d", r.Status)
	}
	ipn(t, h, "POST", forged, verified("INVALID"))
	warnings := 0
	for _, n := range h.Notices {
		if n.Kind == "warning" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("warnings about invalid notifications: %d", warnings)
	}
	// PayPal unreachable or odd: PayPal should send again.
	if r := ipn(t, h, "POST", forged, func(gp.HTTPRequest) (gp.HTTPResponse, error) { return gp.HTTPResponse{}, errors.New("timeout") }); r.Status != 503 {
		t.Errorf("unreachable: %d", r.Status)
	}
	if r := ipn(t, h, "POST", forged, verified("<html>")); r.Status != 503 {
		t.Errorf("odd answer: %d", r.Status)
	}
	if r := ipn(t, h, "POST", forged, func(gp.HTTPRequest) (gp.HTTPResponse, error) {
		return gp.HTTPResponse{Status: 500, Body: "VERIFIED"}, nil
	}); r.Status != 503 {
		t.Errorf("server error: %d", r.Status)
	}
	// Sandbox messages to the live site are ignored without a postback.
	n := len(h.Requests)
	ipn(t, h, "POST", strings.Replace(payment, "1AB23456CD789012E", "4AB", 1)+"&test_ipn=1", verified("VERIFIED"))
	if len(h.Requests) != n || h.Get("donations", "4AB", &d) {
		t.Error("sandbox notification used")
	}
	// windows-1252 names.
	ipn(t, h, "POST", strings.Replace(strings.Replace(strings.Replace(payment, "1AB23456CD789012E", "5AB", 1), "first_name=Ann", "first_name=J%FCrgen%80", 1),
		"charset=UTF-8", "charset=windows-1252", 1), verified("VERIFIED"))
	if !h.Get("donations", "5AB", &d) || d.FirstName != "Jürgen€" {
		t.Errorf("charset: %+v", d)
	}
}

func TestTableAndDashboard(t *testing.T) {
	h := newHost(t)
	h.Put("donations", "A1", Donation{TxnID: "A1", Date: "2024-01-01T10:00:00Z", Amount: "5.00", Currency: "EUR", Status: "Completed",
		Type: "web_accept", FirstName: "Old", Email: "old@example.net", Verified: true})
	h.Put("donations", "B2", Donation{TxnID: "B2", Date: "2099-01-01T10:00:00Z", Amount: "7.50", Currency: "EUR", Status: "Completed",
		Type: "recurring_payment", FirstName: "<b>New</b>", Verified: true, Memo: "Hi"})
	h.Put("donations", "C3", Donation{TxnID: "C3", Date: "2023-01-01T10:00:00Z", Amount: "1.00", Currency: "EUR", Status: "Completed", Verified: false})
	var tbl gp.Table
	if err := gp.CallExport("admin_page", gp.AdminRequest{Page: "donations", Query: map[string]string{}}, &tbl); err != nil {
		t.Fatal(err)
	}
	if tbl.Total != 3 || tbl.Rows[0].ID != "B2" || tbl.Rows[0].Cells["type"] != "Recurring" || tbl.Rows[2].Cells["status"] != "Completed (not verified)" {
		t.Errorf("table: %+v", tbl)
	}
	gp.CallExport("admin_page", gp.AdminRequest{Page: "donations", Query: map[string]string{"s": "OLD@"}}, &tbl)
	if tbl.Total != 1 || tbl.Rows[0].ID != "A1" {
		t.Errorf("search: %+v", tbl)
	}
	var out struct{ Notice string }
	gp.CallExport("admin_submit", gp.AdminRequest{Page: "donations", Action: "delete", IDs: []string{"A1", "nope"}}, &out)
	if out.Notice != "1 donation deleted." || h.Get("donations", "A1", &Donation{}) {
		t.Errorf("delete: %q", out.Notice)
	}
	var box gp.Render
	if err := gp.CallExport("dashboard_widget", gp.DashboardRequest{ID: "recent"}, &box); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(box.HTML, "&lt;b&gt;New&lt;/b&gt;") || strings.Contains(box.HTML, "1.00 EUR") {
		t.Errorf("dashboard: %s", box.HTML)
	}
}

func TestMigrate(t *testing.T) {
	h := newHost(t)
	h.Settings["settings"] = map[string]any{}
	h.SetStash("option", "paypal_donations_options", map[string]any{"paypal_account": "donate@example.org", "currency_code": "EUR",
		"amount": "0", "button": "large", "new_tab": "1", "return_method": "0", "page_style": ""})
	h.SetStash("post:donation_list", "193", map[string]any{"id": 193, "title": "7XY12345AB678901C", "date_gmt": "2015-04-01 12:00:00",
		"meta": map[string]any{"txn_id": "7XY12345AB678901C", "mc_gross": "20.00", "mc_currency": "EUR", "payment_status": "Completed",
			"txn_type": "web_accept", "first_name": "Bea", "payer_email": "bea@example.net", "address_street": "Hidden 2",
			"payment_date": "08:00:00 Apr 1, 2015 PDT", "IPN_status": "Verified"}})
	h.SetStash("post:donation_list", "584", map[string]any{"id": 584, "title": "bad id!", "meta": map[string]any{"IPN_status": "Invalid", "mc_gross": "1"}})
	if err := gp.CallExport("migrate_wordpress", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	set := h.Settings["settings"]
	if set["paypal_account"] != "donate@example.org" || set["new_tab"] != true || set["sandbox"] != false || set["amount"] != 0.0 {
		t.Errorf("settings: %v", set)
	}
	var d Donation
	if !h.Get("donations", "7XY12345AB678901C", &d) || d.Date != "2015-04-01T15:00:00Z" || !d.Verified || d.Source != "wordpress" {
		t.Errorf("donation: %+v", d)
	}
	if raw, _ := json.Marshal(h.KV["donations"]); strings.Contains(string(raw), "Hidden") {
		t.Error("address imported")
	}
	if !h.Get("donations", "wp-584", &d) || d.Verified {
		t.Errorf("invalid one: %+v", d)
	}
}
