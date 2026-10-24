package admin

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"html"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/mail"
	"github.com/theinvisible/gopress/internal/theme"
)

// Settings screens (options-*.php). Every screen validates each field and
// stores options as strings, like WordPress does; the site reads them
// through site.LoadSettings after Invalidate.

func (a *Admin) registerOptions(mux *http.ServeMux) {
	for _, s := range []string{"general", "writing", "reading", "discussion", "media", "permalinks"} {
		mux.Handle("GET /admin/settings/"+s, a.page(auth.CapManageOptions, a.optionsPage(s)))
		mux.Handle("POST /admin/settings/"+s, a.page(auth.CapManageOptions, a.optionsSave(s)))
	}
	mux.Handle("GET /admin/settings/general/confirm-email", a.page(auth.CapManageOptions, a.confirmAdminEmail))
	mux.Handle("POST /admin/settings/media/regenerate", a.page(auth.CapManageOptions, a.regenerateSizes))
	mux.Handle("GET /admin/api/date-preview", a.page(auth.CapManageOptions, a.datePreview))
}

var settingsScreens = []struct{ ID, Label string }{
	{"general", "General"}, {"writing", "Writing"}, {"reading", "Reading"}, {"discussion", "Discussion"},
	{"media", "Media"}, {"permalinks", "Permalinks"}, {"email", "Email"},
}

//go:embed zones.txt
var zonesTxt string

// timeZones are the IANA zones offered in Settings › General, grouped by
// region.
var timeZones = func() map[string][]string {
	out := map[string][]string{}
	sc := bufio.NewScanner(strings.NewReader(zonesTxt))
	for sc.Scan() {
		z := strings.TrimSpace(sc.Text())
		if region, _, ok := strings.Cut(z, "/"); ok {
			out[region] = append(out[region], z)
		}
	}
	return out
}()

func validZone(z string) bool {
	region, _, _ := strings.Cut(z, "/")
	for _, x := range timeZones[region] {
		if x == z {
			_, err := time.LoadLocation(z)
			return err == nil
		}
	}
	return false
}

// optionValues returns all options as strings (numbers and booleans
// formatted the WordPress way).
func (a *Admin) optionValues(ctx context.Context) (map[string]string, error) {
	raw, err := db.AllOptions(ctx, a.db.R)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var x any
		if json.Unmarshal(v, &x) != nil {
			continue
		}
		switch t := x.(type) {
		case string:
			out[k] = t
		case float64:
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			if t {
				out[k] = "1"
			} else {
				out[k] = "0"
			}
		}
	}
	return out, nil
}

type zoneGroup struct {
	Region string
	Zones  []string
}

type optionsData struct {
	basePage
	ID       string
	Screens  []struct{ ID, Label string }
	V        map[string]string // current values (with defaults)
	Error    string
	Reauth   bool
	MailOK   bool
	Pending  string // pending admin email change
	Zones    []zoneGroup
	Offsets  []option
	Pages    []option
	Cats     []option
	DateFmts []option
	TimeFmts []option
	Weekdays []option
	Presets  []option
	Regen    regenStatus
	Avatars  []option
	Ratings  []option
}

var optionDefaults = map[string]string{
	"blogname": "GoPress", "blogdescription": "", "date_format": "F j, Y", "time_format": "g:i a", "start_of_week": "1",
	"timezone_string": "", "gmt_offset": "0", "default_category": "1", "show_on_front": "posts", "page_on_front": "0",
	"page_for_posts": "0", "posts_per_page": "10", "posts_per_rss": "10", "rss_use_excerpt": "0", "blog_public": "1",
	"default_comment_status": "open", "require_name_email": "1", "comment_registration": "0",
	"close_comments_for_old_posts": "0", "close_comments_days_old": "14", "show_comments_cookies_opt_in": "1",
	"thread_comments": "1", "thread_comments_depth": "5", "page_comments": "0", "comments_per_page": "50",
	"default_comments_page": "newest", "comment_order": "asc", "comments_notify": "1", "moderation_notify": "1",
	"comment_moderation": "0", "comment_previously_approved": "1", "comment_max_links": "2", "moderation_keys": "",
	"disallowed_keys": "", "show_avatars": "1", "avatar_rating": "G", "avatar_default": "mystery",
	"thumbnail_size_w": "150", "thumbnail_size_h": "150", "thumbnail_crop": "1", "medium_size_w": "300",
	"medium_size_h": "300", "large_size_w": "1024", "large_size_h": "1024", "uploads_use_yearmonth_folders": "1",
	"upload_max_size": strconv.Itoa(64 << 20), "wp_attachment_pages_enabled": "1",
	"permalink_structure": "/%year%/%monthnum%/%postname%/", "category_base": "", "tag_base": "",
}

var optionsMessages = map[string]notice{
	"saved":     {"success", "Settings saved."},
	"confirm":   {"info", "A confirmation link was sent to the new address. The change takes effect when it is opened."},
	"confirmed": {"success", "The new administration email address is active."},
	"regen":     {"success", "Image sizes are being regenerated in the background."},
}

func (a *Admin) optionsPage(screen string) handler {
	return func(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
		return a.renderOptions(w, r, c, screen, nil, "")
	}
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func (a *Admin) renderOptions(w http.ResponseWriter, r *http.Request, c *reqCtx, screen string, form map[string]string, errMsg string) error {
	ctx := r.Context()
	label := ""
	for _, s := range settingsScreens {
		if s.ID == screen {
			label = s.Label
		}
	}
	d := &optionsData{basePage: a.base(ctx, c, label+" Settings", "settings-"+screen), ID: screen, Screens: settingsScreens,
		Error: errMsg, Reauth: a.auth.RecentlyAuthenticated(c.cur), MailOK: a.mail.Ready(ctx)}
	if msg, ok := optionsMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	vals, err := a.optionValues(ctx)
	if err != nil {
		return err
	}
	d.V = map[string]string{}
	for k, v := range optionDefaults {
		d.V[k] = v
	}
	for k, v := range vals {
		d.V[k] = v
	}
	for _, k := range []string{"blogname", "blogdescription"} {
		d.V[k] = html.UnescapeString(d.V[k])
	}
	for k, v := range form {
		d.V[k] = v
	}
	d.V["admin_email"] = c.settings.AdminEmail
	d.V["home"] = c.settings.Home
	d.Pending = vals["new_admin_email"]
	now := time.Now().In(c.settings.Loc)
	switch screen {
	case "general":
		regions := make([]string, 0, len(timeZones))
		for reg := range timeZones {
			regions = append(regions, reg)
		}
		sort.Strings(regions)
		for _, reg := range regions {
			d.Zones = append(d.Zones, zoneGroup{reg, timeZones[reg]})
		}
		for _, off := range []float64{-12, -11, -10, -9.5, -9, -8, -7, -6, -5, -4, -3.5, -3, -2, -1, 0, 1, 2, 3, 3.5, 4, 4.5, 5, 5.5, 5.75, 6, 6.5, 7, 8, 8.75, 9, 9.5, 10, 10.5, 11, 12, 12.75, 13, 14} {
			v := strconv.FormatFloat(off, 'f', -1, 64)
			label := "UTC"
			if off != 0 {
				label = "UTC" + map[bool]string{true: "+", false: ""}[off > 0] + strings.Replace(v, ".5", ":30", 1)
				label = strings.Replace(strings.Replace(label, ".75", ":45", 1), ".25", ":15", 1)
			}
			cur, _ := strconv.ParseFloat(d.V["gmt_offset"], 64)
			d.Offsets = append(d.Offsets, option{Value: "UTC" + map[bool]string{true: "+", false: ""}[off >= 0] + v, Label: label,
				Selected: d.V["timezone_string"] == "" && cur == off})
		}
		for _, f := range []string{"F j, Y", "Y-m-d", "m/d/Y", "d/m/Y"} {
			d.DateFmts = append(d.DateFmts, option{Value: f, Label: theme.FormatPHPDate(f, now), Selected: d.V["date_format"] == f})
		}
		for _, f := range []string{"g:i a", "g:i A", "H:i"} {
			d.TimeFmts = append(d.TimeFmts, option{Value: f, Label: theme.FormatPHPDate(f, now), Selected: d.V["time_format"] == f})
		}
		for i, day := range weekdays {
			d.Weekdays = append(d.Weekdays, option{Value: strconv.Itoa(i), Label: day})
		}
		d.Scripts = []string{"settings.js"}
	case "writing":
		cats, err := db.Terms(ctx, a.db.R, db.TaxCategory)
		if err != nil {
			return err
		}
		tree, lv := termTree(cats)
		for _, t := range tree {
			d.Cats = append(d.Cats, option{Value: strconv.FormatInt(t.ID, 10), Label: t.Name, Level: lv[t.ID]})
		}
	case "reading":
		pages, err := db.AllPages(ctx, a.db.R)
		if err != nil {
			return err
		}
		for _, p := range pages {
			if p.Status == db.StatusPublish {
				d.Pages = append(d.Pages, option{Value: strconv.FormatInt(p.ID, 10), Label: p.Title})
			}
		}
	case "discussion":
		for _, av := range [][2]string{{"mystery", "Mystery Person"}, {"blank", "Blank"}, {"gravatar_default", "Gravatar Logo"},
			{"identicon", "Identicon (Generated)"}, {"wavatar", "Wavatar (Generated)"}, {"monsterid", "MonsterID (Generated)"}, {"retro", "Retro (Generated)"},
			{"robohash", "RoboHash (Generated)"}} {
			d.Avatars = append(d.Avatars, option{Value: av[0], Label: av[1]})
		}
		for _, rt := range [][2]string{{"G", "G — Suitable for all audiences"}, {"PG", "PG — Possibly offensive, usually for audiences 13 and above"},
			{"R", "R — Intended for adult audiences above 17"}, {"X", "X — Even more mature than above"}} {
			d.Ratings = append(d.Ratings, option{Value: rt[0], Label: rt[1]})
		}
	case "media":
		d.Regen = a.regen.status()
		if mb, err := strconv.ParseInt(d.V["upload_max_size"], 10, 64); err == nil {
			d.V["upload_max_mb"] = strconv.FormatInt(mb>>20, 10)
		}
	case "permalinks":
		home := c.settings.Home
		for _, p := range permalinkPresets {
			d.Presets = append(d.Presets, option{Value: p[0], Label: p[1] + "|" + home + examplePermalink(p[0], now)})
		}
		d.Scripts = []string{"settings.js"}
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "settings-"+screen, d)
}

var permalinkPresets = [][2]string{
	{"/%year%/%monthnum%/%day%/%postname%/", "Day and name"},
	{"/%year%/%monthnum%/%postname%/", "Month and name"},
	{"/archives/%post_id%", "Numeric"},
	{"/%postname%/", "Post name"},
}

func examplePermalink(structure string, now time.Time) string {
	return strings.NewReplacer("%year%", now.Format("2006"), "%monthnum%", now.Format("01"), "%day%", now.Format("02"),
		"%hour%", now.Format("15"), "%minute%", now.Format("04"), "%second%", now.Format("05"), "%postname%", "sample-post",
		"%post_id%", "123", "%category%", "uncategorized", "%author%", "admin").Replace(structure)
}

// optionForm collects and validates a screen's fields.
type optionForm struct {
	r    *http.Request
	vals map[string]any
	form map[string]string
	err  string
}

func (f *optionForm) fail(msg string) {
	if f.err == "" {
		f.err = msg
	}
}

func (f *optionForm) raw(key string) string {
	v := strings.TrimSpace(f.r.PostFormValue(key))
	f.form[key] = v
	return v
}

func (f *optionForm) text(key string, max int) string {
	v := strings.Join(strings.Fields(f.raw(key)), " ")
	if len(v) > max {
		f.fail("A field is too long.")
	}
	f.vals[key] = v
	return v
}

func (f *optionForm) textarea(key string, max int) {
	v := strings.ReplaceAll(f.r.PostFormValue(key), "\r\n", "\n")
	f.form[key] = v
	if len(v) > max {
		f.fail("A field is too long.")
	}
	f.vals[key] = strings.TrimSpace(v)
}

func (f *optionForm) check(key string) {
	if f.r.PostFormValue(key) != "" {
		f.vals[key] = "1"
		f.form[key] = "1"
	} else {
		f.vals[key] = "0"
		f.form[key] = "0"
	}
}

func (f *optionForm) number(key string, min, max int, label string) int {
	v := f.raw(key)
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		f.fail(label + " must be a number from " + strconv.Itoa(min) + " to " + strconv.Itoa(max) + ".")
		return 0
	}
	f.vals[key] = strconv.Itoa(n)
	return n
}

func (f *optionForm) choice(key string, allowed ...string) string {
	v := f.raw(key)
	for _, a := range allowed {
		if v == a {
			f.vals[key] = v
			return v
		}
	}
	f.fail("Invalid value.")
	return ""
}

var (
	reFormat    = regexp.MustCompile(`^[^<>]{1,64}$`)
	rePermalink = regexp.MustCompile(`^/[A-Za-z0-9%_./-]*$`)
	reToken     = regexp.MustCompile(`%[a-z_]+%`)
	reBase      = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)
	knownTokens = map[string]bool{"%year%": true, "%monthnum%": true, "%day%": true, "%hour%": true, "%minute%": true,
		"%second%": true, "%postname%": true, "%post_id%": true, "%category%": true, "%author%": true}
)

func (a *Admin) optionsSave(screen string) handler {
	return func(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
		ctx := r.Context()
		f := &optionForm{r: r, vals: map[string]any{}, form: map[string]string{}}
		current, err := a.optionValues(ctx)
		if err != nil {
			return err
		}
		confirmSent := false
		switch screen {
		case "general":
			// Stored HTML-escaped like WordPress (sanitize_option); themes
			// print them as they are.
			if f.text("blogname", 200) == "" {
				f.fail("Please enter a site title.")
			}
			f.text("blogdescription", 500)
			f.vals["blogname"] = escHTML(f.vals["blogname"].(string))
			f.vals["blogdescription"] = escHTML(f.vals["blogdescription"].(string))
			switch tz := f.raw("timezone_string"); {
			case strings.HasPrefix(tz, "UTC"):
				off, err := strconv.ParseFloat(strings.TrimPrefix(tz, "UTC"), 64)
				if tz == "UTC" {
					off, err = 0, nil
				}
				if err != nil || math.IsNaN(off) || math.IsInf(off, 0) || off < -12 || off > 14 {
					f.fail("Please choose a valid time zone.")
				}
				f.vals["timezone_string"], f.vals["gmt_offset"] = "", strconv.FormatFloat(off, 'f', -1, 64)
			case validZone(tz):
				f.vals["timezone_string"], f.vals["gmt_offset"] = tz, "0"
			default:
				f.fail("Please choose a valid time zone.")
			}
			for _, k := range []string{"date_format", "time_format"} {
				v := f.raw(k)
				if v == "custom" {
					v = f.raw(k + "_custom")
				}
				if !reFormat.MatchString(v) {
					f.fail("Please enter a valid date or time format.")
				}
				f.vals[k] = v
			}
			f.number("start_of_week", 0, 6, "Week starts on")
			email := strings.TrimSpace(r.PostFormValue("new_admin_email"))
			f.form["admin_email"] = email
			if !strings.EqualFold(email, c.settings.AdminEmail) && f.err == "" {
				if !validEmail(email) {
					f.fail("Please enter a valid administration email address.")
					break
				}
				if a.needReauth(w, r, c, "/admin/settings/general") {
					return nil
				}
				if a.mail.Ready(ctx) {
					// The new address must confirm (it receives password and
					// moderation mail).
					tok, err := a.auth.NewUserToken(ctx, c.user, auth.TokenAdminEmail, auth.AdminEmailTTL, email)
					if err != nil {
						return err
					}
					if err := a.mail.EnqueueUntil(ctx, email, "email_confirm", mail.Data{"Email": email, "Valid": "48 hours",
						"Link": strings.TrimRight(a.auth.Home(), "/") + "/admin/settings/general/confirm-email?token=" + tok},
						time.Now().Add(auth.AdminEmailTTL)); err != nil {
						return err
					}
					f.vals["new_admin_email"] = email
					confirmSent = true
				} else {
					f.vals["admin_email"] = email
				}
			}
		case "writing":
			id := atoi64(f.raw("default_category"))
			if t, err := db.GetTerm(ctx, a.db.R, id); err != nil || t == nil || t.Taxonomy != db.TaxCategory {
				f.fail("Please choose a category.")
			}
			f.vals["default_category"] = strconv.FormatInt(id, 10)
		case "reading":
			front := f.choice("show_on_front", "posts", "page")
			pof, pfp := atoi64(f.raw("page_on_front")), atoi64(f.raw("page_for_posts"))
			for _, id := range []int64{pof, pfp} {
				if id == 0 {
					continue
				}
				if p, err := db.GetPost(ctx, a.db.R, id); err != nil || p.Type != db.TypePage || p.Status != db.StatusPublish {
					f.fail("Please choose published pages.")
				}
			}
			if front == "page" && pof == 0 {
				f.fail("Please choose the page for the homepage.")
			}
			if pof != 0 && pof == pfp {
				f.fail("The homepage and the posts page must be different pages.")
			}
			f.vals["page_on_front"], f.vals["page_for_posts"] = strconv.FormatInt(pof, 10), strconv.FormatInt(pfp, 10)
			f.number("posts_per_page", 1, 100, "Blog pages show at most")
			f.number("posts_per_rss", 1, 100, "Syndication feeds show the most recent")
			f.choice("rss_use_excerpt", "0", "1")
			if r.PostFormValue("blog_public") != "" { // the checkbox discourages indexing
				f.vals["blog_public"] = "0"
			} else {
				f.vals["blog_public"] = "1"
			}
			f.form["blog_public"] = f.vals["blog_public"].(string)
		case "discussion":
			if r.PostFormValue("default_comment_status") != "" {
				f.vals["default_comment_status"] = "open"
			} else {
				f.vals["default_comment_status"] = "closed"
			}
			for _, k := range []string{"require_name_email", "comment_registration", "close_comments_for_old_posts",
				"show_comments_cookies_opt_in", "thread_comments", "page_comments", "comments_notify", "moderation_notify",
				"comment_moderation", "comment_previously_approved", "show_avatars"} {
				f.check(k)
			}
			f.number("close_comments_days_old", 1, 36500, "Close comments after")
			f.number("thread_comments_depth", 2, 10, "Threaded comment depth")
			f.number("comments_per_page", 1, 500, "Comments per page")
			f.choice("default_comments_page", "newest", "oldest")
			f.choice("comment_order", "asc", "desc")
			f.number("comment_max_links", 0, 100, "Number of links")
			f.textarea("moderation_keys", 100000)
			f.textarea("disallowed_keys", 100000)
			f.choice("avatar_rating", "G", "PG", "R", "X")
			f.choice("avatar_default", "mystery", "blank", "gravatar_default", "identicon", "wavatar", "monsterid", "retro", "robohash")
		case "media":
			for _, k := range []string{"thumbnail_size_w", "thumbnail_size_h", "medium_size_w", "medium_size_h", "large_size_w", "large_size_h"} {
				f.number(k, 0, 10000, "Image sizes")
			}
			f.check("thumbnail_crop")
			f.check("uploads_use_yearmonth_folders")
			f.check("wp_attachment_pages_enabled")
			mb := f.number("upload_max_mb", 1, 1024, "Maximum upload size")
			delete(f.vals, "upload_max_mb")
			f.vals["upload_max_size"] = strconv.Itoa(mb << 20)
		case "permalinks":
			s := f.raw("permalink_structure")
			if s == "custom" {
				s = f.raw("permalink_custom")
			}
			s = "/" + strings.TrimLeft(s, "/")
			switch {
			case !rePermalink.MatchString(s):
				f.fail("The permalink structure may only contain letters, digits, - _ . / and %tags%.")
			case !strings.Contains(s, "%postname%") && !strings.Contains(s, "%post_id%"):
				f.fail("The permalink structure must contain %postname% or %post_id%.")
			}
			for _, tok := range reToken.FindAllString(s, -1) {
				if !knownTokens[tok] {
					f.fail("Unknown tag " + tok + " in the permalink structure.")
				}
			}
			if strings.Count(s, "%") != 2*len(reToken.FindAllString(s, -1)) {
				f.fail("The permalink structure has an incomplete %tag%.")
			}
			f.vals["permalink_structure"] = s
			for _, k := range []string{"category_base", "tag_base"} {
				v := strings.Trim(f.raw(k), "/")
				if v != "" && !reBase.MatchString(v) {
					f.fail("The category and tag bases may only contain letters, digits, - _ and /.")
				}
				f.vals[k] = v
			}
			cb, tb := f.vals["category_base"].(string), f.vals["tag_base"].(string)
			if cb != "" && cb == tb {
				f.fail("The category and tag bases must differ.")
			}
			// Old URLs keep working: the previous structure is remembered
			// and redirects to the new one.
			if old := current["permalink_structure"]; old != "" && old != s && f.err == "" {
				var hist []string
				db.GetOption(ctx, a.db.R, "permalink_history", &hist)
				hist = append([]string{old}, hist...)
				var out []string
				for _, h := range hist {
					if h != s && !contains(out, h) && len(out) < 5 {
						out = append(out, h)
					}
				}
				f.vals["permalink_history"] = out
			}
		default:
			return errNotFound
		}
		if f.err != "" {
			return a.renderOptions(w, r, c, screen, f.form, f.err)
		}
		changed := make([]string, 0, len(f.vals))
		err = a.db.Tx(ctx, func(tx *sql.Tx) error {
			for k, v := range f.vals {
				if s, ok := v.(string); ok && current[k] == s {
					continue
				}
				changed = append(changed, k)
				if err := db.SetOption(ctx, tx, k, v); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(changed) > 0 {
			sort.Strings(changed)
			a.auth.Audit(ctx, r, c.user.ID, "settings_changed", "option", 0, map[string]any{"screen": screen, "keys": changed})
			a.site.Invalidate()
		}
		msg := "saved"
		if confirmSent {
			msg = "confirm"
		}
		return seeOther(w, r, "/admin/settings/"+screen+"?msg="+msg)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// GET /admin/settings/general/confirm-email?token=…
func (a *Admin) confirmAdminEmail(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	tu, tok, err := a.auth.CheckUserToken(ctx, r.URL.Query().Get("token"), auth.TokenAdminEmail)
	if err == nil && tu.ID != c.user.ID {
		return &httpError{http.StatusForbidden, "Please confirm the change while logged in as the administrator who requested it."}
	}
	if err == nil {
		_, tok, err = a.auth.UseUserToken(ctx, r.URL.Query().Get("token"), auth.TokenAdminEmail)
	}
	if err != nil {
		return &httpError{http.StatusNotFound, "This confirmation link is invalid or has expired."}
	}
	old := c.settings.AdminEmail
	if err := db.SetOption(ctx, a.db.W, "admin_email", tok.Data); err != nil {
		return err
	}
	if _, err := a.db.W.ExecContext(ctx, `DELETE FROM options WHERE name = 'new_admin_email'`); err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "admin_email_changed", "option", 0, map[string]any{"new": tok.Data})
	if old != "" {
		a.mail.Enqueue(ctx, old, "admin_email_changed", mail.Data{"Email": tok.Data})
	}
	a.site.Invalidate()
	return seeOther(w, r, "/admin/settings/general?msg=confirmed")
}

// GET /admin/api/date-preview?format=…
func (a *Admin) datePreview(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	f := r.URL.Query().Get("format")
	if !reFormat.MatchString(f) {
		writeJSON(w, http.StatusOK, map[string]string{"text": ""})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": theme.FormatPHPDate(f, time.Now().In(c.settings.Loc))})
	return nil
}

// regenJob is the background regeneration of image sizes.
type regenJob struct {
	mu sync.Mutex
	st regenStatus
}

type regenStatus struct {
	Running      bool
	Done, Failed int
	Finished     string
}

func (j *regenJob) status() regenStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

// POST /admin/settings/media/regenerate
func (a *Admin) regenerateSizes(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	a.regen.mu.Lock()
	if a.regen.st.Running {
		a.regen.mu.Unlock()
		return seeOther(w, r, "/admin/settings/media?msg=regen")
	}
	a.regen.st = regenStatus{Running: true}
	a.regen.mu.Unlock()
	missing := r.PostFormValue("missing") != ""
	a.auth.Audit(r.Context(), r, c.user.ID, "media_regenerate", "media", 0, map[string]any{"missing": missing})
	a.background(func() {
		err := a.content.RegenerateSizes(context.Background(), missing, func(file string, err error) {
			a.regen.mu.Lock()
			a.regen.st.Done++
			if err != nil {
				a.regen.st.Failed++
				a.log.Warn("regenerate", "file", file, "err", err)
			}
			a.regen.mu.Unlock()
		})
		if err != nil {
			a.log.Error("regenerate", "err", err)
		}
		a.regen.mu.Lock()
		a.regen.st.Running = false
		a.regen.st.Finished = time.Now().Format("15:04")
		a.regen.mu.Unlock()
	})
	return seeOther(w, r, "/admin/settings/media?msg=regen")
}
