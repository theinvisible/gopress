// Akismet Anti-spam: checks new comments with the Akismet service
// (comment-check) and reports the corrections of moderators (submit-spam,
// submit-ham), the GoPress port of the WordPress plugin. If Akismet cannot
// be reached, comments are held for moderation, never approved unchecked.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	gp "github.com/theinvisible/gopress/pdk/go"
)

const (
	api       = "https://rest.akismet.com/1.1/"
	userAgent = "GoPress | Akismet-GoPress/2.0.0"
	timeoutMS = 3000 // the hook has 5 s; a slow answer holds the comment
)

func init() {
	gp.HandleComment(check)
	gp.HandleAction("comment_status_changed", statusChanged)
	gp.HandleSettingsSaved("settings", verifyKey)
	gp.HandleDashboard("stats", dashboard)
	gp.HandleMigrate(migrate)
}

type settings struct {
	Key     string
	Discard bool // discard obvious spam instead of keeping it as spam
}

func loadSettings() (settings, error) {
	m, err := gp.Settings("settings")
	if err != nil {
		return settings{}, err
	}
	key, _ := m["api_key"].(string)
	strict, _ := m["strictness"].(string)
	return settings{Key: strings.TrimSpace(key), Discard: strict == "discard"}, nil
}

// post sends a request to the Akismet API.
func post(method string, form url.Values) (gp.HTTPResponse, error) {
	return gp.Fetch(gp.HTTPRequest{Method: "POST", URL: api + method, Body: form.Encode(), TimeoutMS: timeoutMS,
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8", "User-Agent": userAgent}})
}

// siteParams are the parameters every call carries about the site.
func siteParams(key string) (url.Values, error) {
	site, err := gp.Site()
	if err != nil {
		return nil, err
	}
	return url.Values{"api_key": {key}, "blog": {site.URL}, "blog_lang": {strings.ReplaceAll(site.Language, "-", "_")},
		"blog_charset": {"UTF-8"}}, nil
}

// trusted roles are not checked (moderators' comments are approved by
// GoPress anyway).
var trusted = []string{"administrator", "editor"}

// checkParams builds the comment-check request.
func checkParams(key string, c gp.CommentCheck) (url.Values, error) {
	v, err := siteParams(key)
	if err != nil {
		return nil, err
	}
	cm := c.Comment
	typ := "comment"
	if cm.ParentID != 0 {
		typ = "reply"
	}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("user_ip", cm.IP)
	set("user_agent", cm.Agent)
	set("referrer", c.Referrer)
	set("permalink", c.PostURL)
	set("comment_type", typ)
	set("comment_author", cm.Author)
	set("comment_author_email", cm.Email)
	set("comment_author_url", cm.URL)
	set("comment_content", cm.Content)
	set("comment_date_gmt", cm.Date)
	set("comment_post_modified_gmt", c.PostModified)
	set("user_role", c.UserRole)
	for _, t := range c.PostTerms {
		v.Add("comment_context[]", t)
	}
	for _, h := range []string{"Accept", "Accept-Charset", "Accept-Encoding"} {
		set("HTTP_"+strings.ToUpper(strings.ReplaceAll(h, "-", "_")), c.Headers[h])
	}
	set("HTTP_ACCEPT_LANGUAGE", c.Language)
	return v, nil
}

// check is the comment_pre_save hook.
func check(c gp.CommentCheck) (string, error) {
	s, err := loadSettings()
	if err != nil || s.Key == "" || slices.Contains(trusted, c.UserRole) {
		return gp.Keep, err
	}
	form, err := checkParams(s.Key, c)
	if err != nil {
		return gp.Hold, nil
	}
	resp, err := post("comment-check", form)
	verdict := verdictOf(resp, err, s.Discard)
	switch verdict {
	case gp.Spam, gp.Discard:
		count("spam")
	case gp.Keep:
		count("ham")
	default:
		count("errors")
		help := ""
		if err == nil {
			help = resp.Headers["x-akismet-debug-help"]
		}
		gp.Log("warn", "Akismet did not check a comment; it is held for moderation. "+truncate(help, 200))
	}
	return verdict, nil
}

// verdictOf maps Akismet's answer: spam, legitimate, or held when the
// answer is missing or unexpected (never approved unchecked).
func verdictOf(resp gp.HTTPResponse, err error, discard bool) string {
	if err != nil || resp.Status != 200 {
		return gp.Hold
	}
	switch strings.TrimSpace(resp.Body) {
	case "true":
		if discard && strings.EqualFold(resp.Headers["x-akismet-pro-tip"], "discard") {
			return gp.Discard
		}
		return gp.Spam
	case "false":
		return gp.Keep
	}
	return gp.Hold
}

// statusChanged reports moderators' corrections: a comment marked as
// spam (submit-spam), or one taken out of spam (submit-ham).
func statusChanged(data json.RawMessage) error {
	var ch gp.CommentStatusChange
	if err := json.Unmarshal(data, &ch); err != nil {
		return err
	}
	method := ""
	switch {
	case ch.Status == "spam" && ch.Previous != "spam":
		method = "submit-spam"
	case ch.Previous == "spam" && ch.Status != "spam" && ch.Status != "trash":
		// Taken out of spam (unspam, approve, unapprove).
		method = "submit-ham"
	default:
		return nil
	}
	s, err := loadSettings()
	if err != nil || s.Key == "" {
		return err
	}
	form, err := siteParams(s.Key)
	if err != nil {
		return err
	}
	cm := ch.Comment
	for k, v := range map[string]string{"user_ip": cm.IP, "user_agent": cm.Agent, "comment_author": cm.Author,
		"comment_author_email": cm.Email, "comment_author_url": cm.URL, "comment_content": cm.Content, "comment_date_gmt": cm.Date} {
		if v != "" {
			form.Set(k, v)
		}
	}
	form.Set("comment_type", "comment")
	if ch.By != nil {
		form.Set("reporter", ch.By.Login)
	}
	if _, err := post(method, form); err != nil {
		return fmt.Errorf("reporting to Akismet: %w", err)
	}
	count("corrections")
	return nil
}

// verifyKey checks the API key after the settings were saved.
func verifyKey(gp.AdminRequest) (string, error) {
	s, err := loadSettings()
	if err != nil || s.Key == "" {
		return "", err
	}
	site, err := gp.Site()
	if err != nil {
		return "", err
	}
	resp, err := post("verify-key", url.Values{"key": {s.Key}, "blog": {site.URL}})
	if err != nil {
		return "", errors.New("Akismet could not be reached to check the API key. Comments are held for moderation until it answers.")
	}
	switch strings.TrimSpace(resp.Body) {
	case "valid":
		return "The Akismet API key is valid.", nil
	case "invalid":
		return "", errors.New("Akismet does not accept this API key. Comments are held for moderation until the key is fixed.")
	}
	return "", errors.New("Akismet gave an unexpected answer when checking the API key.")
}

// Statistics: totals (KV stats/total) and per month (stats/YYYY-MM).
type counts struct {
	Spam        int `json:"spam"`
	Ham         int `json:"ham"`
	Errors      int `json:"errors"`
	Corrections int `json:"corrections"`
}

func (c *counts) add(what string) {
	switch what {
	case "spam":
		c.Spam++
	case "ham":
		c.Ham++
	case "errors":
		c.Errors++
	case "corrections":
		c.Corrections++
	}
}

func count(what string) {
	for _, key := range []string{"total", time.Now().UTC().Format("2006-01")} {
		var c counts
		gp.KVGet("stats", key, &c)
		c.add(what)
		gp.KVSet("stats", key, c)
	}
}

type monthRow struct{ Month, Spam, Ham, Errors string }

func dashboard(gp.DashboardRequest) (gp.Render, error) {
	s, err := loadSettings()
	if err != nil {
		return gp.Render{}, err
	}
	var total counts
	gp.KVGet("stats", "total", &total)
	var imported int
	gp.KVGet("stats", "imported", &imported)
	var months []monthRow
	now := time.Now().UTC()
	for i := range 6 {
		m := time.Date(now.Year(), now.Month()-time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		var c counts
		if ok, _ := gp.KVGet("stats", m.Format("2006-01"), &c); ok {
			months = append(months, monthRow{m.Format("January 2006"), strconv.Itoa(c.Spam), strconv.Itoa(c.Ham), strconv.Itoa(c.Errors)})
		}
	}
	corrections := ""
	if total.Corrections > 0 {
		corrections = strconv.Itoa(total.Corrections)
	}
	out, err := gp.Template("dashboard.html", map[string]any{"HasKey": s.Key != "", "Spam": strconv.Itoa(total.Spam + imported),
		"Months": months, "Corrections": corrections})
	return gp.Render{HTML: out}, err
}

// migrate brings over the WordPress options: the API key (unless one is
// set), the strictness and the number of spam comments caught.
func migrate() (string, error) {
	opts, err := gp.Stash("option")
	if err != nil {
		return "", err
	}
	str := func(k string) string {
		var s any
		if raw, ok := opts[k]; ok && json.Unmarshal(raw, &s) == nil && s != nil {
			return strings.TrimSpace(fmt.Sprint(s))
		}
		return ""
	}
	s, err := loadSettings()
	if err != nil {
		return "", err
	}
	values := map[string]any{}
	var notes []string
	if key := str("wordpress_api_key"); key != "" && s.Key == "" {
		values["api_key"] = key
		notes = append(notes, "API key imported.")
	}
	if str("akismet_strictness") == "1" {
		values["strictness"] = "discard"
	}
	if len(values) > 0 {
		if err := gp.SetSettings("settings", values); err != nil {
			return "", err
		}
	}
	if n, err := strconv.Atoi(str("akismet_spam_count")); err == nil && n > 0 {
		if err := gp.KVSet("stats", "imported", n); err != nil {
			return "", err
		}
		notes = append(notes, fmt.Sprintf("%d spam comments caught in WordPress.", n))
	}
	return strings.Join(notes, " "), nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func main() {}
