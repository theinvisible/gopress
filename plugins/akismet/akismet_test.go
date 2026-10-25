package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	gp "github.com/theinvisible/gopress/pdk/go"
	"github.com/theinvisible/gopress/pdk/go/fakehost"
)

const testKey = "0123456789ab"

func newHost(t *testing.T) *fakehost.Host {
	t.Helper()
	h := fakehost.New()
	if err := h.LoadTemplates("templates"); err != nil {
		t.Fatal(err)
	}
	h.Settings["settings"] = map[string]any{"api_key": testKey, "strictness": "spam"}
	h.Install()
	return h
}

func answer(status int, body string, hdr map[string]string) func(gp.HTTPRequest) (gp.HTTPResponse, error) {
	return func(gp.HTTPRequest) (gp.HTTPResponse, error) {
		return gp.HTTPResponse{Status: status, Body: body, Headers: hdr}, nil
	}
}

var comment = gp.CommentCheck{
	Comment: gp.Comment{PostID: 10, Author: "Visitor", Email: "v@example.net", URL: "https://v.example", IP: "203.0.113.5",
		Agent: "Mozilla/5.0", Content: "Nice post", Status: "approved", Date: "2026-10-25T08:00:00Z"},
	PostURL: "https://example.org/hello/", PostModified: "2026-10-01T00:00:00Z", PostTerms: []string{"News", "Go"},
	Referrer: "https://example.org/hello/", Language: "de-AT", Headers: map[string]string{"Accept": "text/html"},
}

func verdict(t *testing.T, c gp.CommentCheck) string {
	t.Helper()
	var out struct{ Verdict string }
	if err := gp.CallExport("comment_pre_save", c, &out); err != nil {
		t.Fatal(err)
	}
	return out.Verdict
}

func TestCommentCheck(t *testing.T) {
	h := newHost(t)
	h.Fetch = answer(200, "false", nil)
	if v := verdict(t, comment); v != gp.Keep {
		t.Errorf("ham: %q", v)
	}
	r := h.Requests[0]
	if r.URL != "https://rest.akismet.com/1.1/comment-check" || r.Method != "POST" || r.TimeoutMS != 3000 ||
		r.Headers["User-Agent"] != userAgent {
		t.Errorf("request: %+v", r)
	}
	form, _ := url.ParseQuery(r.Body)
	for k, want := range map[string]string{"api_key": testKey, "blog": "https://example.org/", "blog_lang": "en_US",
		"user_ip": "203.0.113.5", "user_agent": "Mozilla/5.0", "referrer": "https://example.org/hello/",
		"permalink": "https://example.org/hello/", "comment_type": "comment", "comment_author": "Visitor",
		"comment_author_email": "v@example.net", "comment_content": "Nice post", "comment_post_modified_gmt": "2026-10-01T00:00:00Z",
		"HTTP_ACCEPT": "text/html", "HTTP_ACCEPT_LANGUAGE": "de-AT", "blog_charset": "UTF-8"} {
		if form.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, form.Get(k), want)
		}
	}
	if got := form["comment_context[]"]; len(got) != 2 {
		t.Errorf("context: %v", got)
	}

	h.Fetch = answer(200, "true", nil)
	if v := verdict(t, comment); v != gp.Spam {
		t.Errorf("spam: %q", v)
	}
	h.Fetch = answer(200, "true", map[string]string{"x-akismet-pro-tip": "discard"})
	if v := verdict(t, comment); v != gp.Spam {
		t.Errorf("discard without strictness: %q", v)
	}
	h.Settings["settings"]["strictness"] = "discard"
	if v := verdict(t, comment); v != gp.Discard {
		t.Errorf("discard: %q", v)
	}
	// Failures hold the comment, never approve it.
	for name, f := range map[string]func(gp.HTTPRequest) (gp.HTTPResponse, error){
		"network": func(gp.HTTPRequest) (gp.HTTPResponse, error) { return gp.HTTPResponse{}, errors.New("timeout") },
		"500":     answer(500, "false", nil),
		"invalid": answer(200, "Invalid API key.", map[string]string{"x-akismet-debug-help": "We were unable to verify"}),
	} {
		h.Fetch = f
		if v := verdict(t, comment); v != gp.Hold {
			t.Errorf("%s: %q", name, v)
		}
	}
	var c counts
	h.Get("stats", "total", &c)
	if c.Ham != 1 || c.Spam != 3 || c.Errors != 3 {
		t.Errorf("stats: %+v", c)
	}
	// Editors and administrators, or no key: not checked.
	n := len(h.Requests)
	ed := comment
	ed.UserRole = "editor"
	verdict(t, ed)
	h.Settings["settings"]["api_key"] = ""
	verdict(t, comment)
	if len(h.Requests) != n {
		t.Error("checked without need")
	}
	for _, l := range h.Logs {
		if strings.Contains(l, testKey) {
			t.Error("key logged")
		}
	}
}

func TestStatusChanged(t *testing.T) {
	h := newHost(t)
	h.Fetch = answer(200, "Thanks for making the web a better place.", nil)
	send := func(status, previous string) {
		t.Helper()
		data, _ := json.Marshal(gp.CommentStatusChange{Comment: comment.Comment, Status: status, Previous: previous,
			By: &gp.User{Login: "mod"}})
		if err := gp.CallExport("action", map[string]any{"action": "comment_status_changed", "data": json.RawMessage(data)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	send("spam", "approved")
	send("unspam", "spam")
	send("approve", "spam")
	send("unapprove", "spam")  // out of spam, to moderation
	send("approve", "pending") // no correction
	send("trash", "approved")  // no correction
	if len(h.Requests) != 4 {
		t.Fatalf("requests: %d", len(h.Requests))
	}
	for i, want := range []string{"submit-spam", "submit-ham", "submit-ham", "submit-ham"} {
		if !strings.HasSuffix(h.Requests[i].URL, "/"+want) {
			t.Errorf("request %d: %s", i, h.Requests[i].URL)
		}
	}
	if form, _ := url.ParseQuery(h.Requests[0].Body); form.Get("reporter") != "mod" || form.Get("user_ip") != "203.0.113.5" {
		t.Errorf("report: %v", form)
	}
}

func TestVerifyKey(t *testing.T) {
	h := newHost(t)
	saved := func() (string, error) {
		var out struct{ Notice string }
		err := gp.CallExport("admin_submit", gp.AdminRequest{Page: "settings", Action: "saved"}, &out)
		return out.Notice, err
	}
	h.Fetch = answer(200, "valid", nil)
	if n, err := saved(); err != nil || !strings.Contains(n, "valid") {
		t.Errorf("valid: %q %v", n, err)
	}
	if form, _ := url.ParseQuery(h.Requests[0].Body); form.Get("key") != testKey || !strings.HasSuffix(h.Requests[0].URL, "/verify-key") {
		t.Errorf("verify request: %+v", h.Requests[0])
	}
	h.Fetch = answer(200, "invalid", nil)
	if _, err := saved(); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Errorf("invalid: %v", err)
	}
	h.Fetch = func(gp.HTTPRequest) (gp.HTTPResponse, error) { return gp.HTTPResponse{}, errors.New("down") }
	if _, err := saved(); err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestMigrateAndDashboard(t *testing.T) {
	h := newHost(t)
	h.Settings["settings"] = map[string]any{}
	h.SetStash("option", "wordpress_api_key", testKey)
	h.SetStash("option", "akismet_strictness", "1")
	h.SetStash("option", "akismet_spam_count", "1234")
	if err := gp.CallExport("migrate_wordpress", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	if h.Settings["settings"]["api_key"] != testKey || h.Settings["settings"]["strictness"] != "discard" {
		t.Errorf("settings: %v", h.Settings["settings"])
	}
	// A key set in GoPress is kept.
	h.Settings["settings"]["api_key"] = "newkey"
	gp.CallExport("migrate_wordpress", struct{}{}, nil)
	if h.Settings["settings"]["api_key"] != "newkey" {
		t.Error("key replaced by the import")
	}
	count("spam")
	var box gp.Render
	if err := gp.CallExport("dashboard_widget", gp.DashboardRequest{ID: "stats"}, &box); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(box.HTML, "<strong>1235</strong> spam comments") || strings.Contains(box.HTML, "not checked:") {
		t.Errorf("dashboard: %s", box.HTML)
	}
}
