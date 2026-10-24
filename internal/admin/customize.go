package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/site"
)

// Customizer: site identity, header, background, menu location,
// homepage and additional CSS with a live preview. Unsaved values are a
// draft per user (user_meta); the preview frame shows the site with the
// draft applied (see previewer.Visitor), everybody else sees the
// published site until "Publish".

func (a *Admin) registerCustomize(mux *http.ServeMux) {
	mux.Handle("GET /admin/customize", a.page(auth.CapEditThemeOptions, a.customizePage))
	mux.Handle("POST /admin/api/customize/draft", a.page(auth.CapEditThemeOptions, a.customizeDraft))
	mux.Handle("POST /admin/customize/publish", a.page(auth.CapEditThemeOptions, a.customizePublish))
	mux.Handle("POST /admin/customize/discard", a.page(auth.CapEditThemeOptions, a.customizeDiscard))
}

// customizeValues are the customizer fields as strings.
type customizeValues map[string]string

var customizeFields = []string{"blogname", "blogdescription", "site_icon", "display_header_text", "header_textcolor",
	"header_image_id", "background_color", "background_image_id", "background_position", "background_size",
	"background_repeat", "background_attachment", "menu_primary", "show_on_front", "page_on_front", "page_for_posts", "custom_css"}

// current returns the published values.
func (a *Admin) customizeCurrent(ctx context.Context, st *site.Settings) customizeValues {
	v := customizeValues{
		"blogname": html.UnescapeString(st.Name), "blogdescription": html.UnescapeString(st.Description),
		"site_icon": strconv.FormatInt(st.SiteIcon, 10), "show_on_front": st.ShowOnFront,
		"page_on_front": strconv.FormatInt(st.PageOnFront, 10), "page_for_posts": strconv.FormatInt(st.PageForPosts, 10),
		"menu_primary": strconv.FormatInt(st.MenuLocations["primary"], 10), "custom_css": st.CustomCSS,
	}
	color := st.ThemeMod("header_textcolor")
	if color == "" {
		color = site.DefaultHeaderTextColor
	}
	v["display_header_text"] = "1"
	if color == "blank" {
		v["display_header_text"], color = "", site.DefaultHeaderTextColor
	}
	v["header_textcolor"] = "#" + color
	if data, ok := st.ThemeMods["header_image_data"].(map[string]any); ok && st.ThemeMod("header_image") != "" {
		if id, ok := data["attachment_id"].(float64); ok {
			v["header_image_id"] = strconv.FormatInt(int64(id), 10)
		}
	}
	bg := st.ThemeMod("background_color")
	if bg == "" {
		bg = "e6e6e6"
	}
	v["background_color"] = "#" + bg
	if img := st.ThemeMod("background_image"); img != "" {
		if id := a.attachmentByURL(ctx, img); id != 0 {
			v["background_image_id"] = strconv.FormatInt(id, 10)
		} else {
			v["background_image_url"] = img
		}
	}
	posX, posY := st.ThemeMod("background_position_x"), st.ThemeMod("background_position_y")
	if posX == "" {
		posX = "left"
	}
	if posY == "" {
		posY = "top"
	}
	v["background_position"] = posX + " " + posY
	v["background_size"] = st.ThemeMod("background_size")
	if v["background_size"] == "" {
		v["background_size"] = "auto"
	}
	v["background_repeat"] = "1"
	if st.ThemeMod("background_repeat") == "no-repeat" {
		v["background_repeat"] = ""
	}
	v["background_attachment"] = ""
	if st.ThemeMod("background_attachment") == "fixed" {
		v["background_attachment"] = "1"
	}
	return v
}

// attachmentByURL finds the attachment of an uploads URL (absolute or
// root-relative), 0 if none.
func (a *Admin) attachmentByURL(ctx context.Context, u string) int64 {
	_, file, ok := strings.Cut(u, "/wp-content/uploads/")
	if !ok {
		return 0
	}
	var id int64
	a.db.R.QueryRowContext(ctx, `SELECT post_id FROM media WHERE file = ?`, file).Scan(&id)
	return id
}

var (
	reCSSColor   = regexp.MustCompile(`^#?([0-9a-fA-F]{6}|[0-9a-fA-F]{3})$`)
	bgPositions  = map[string]bool{"left top": true, "center top": true, "right top": true, "left center": true, "center center": true, "right center": true, "left bottom": true, "center bottom": true, "right bottom": true}
	bgSizes      = map[string]bool{"auto": true, "contain": true, "cover": true}
	customCSSMax = 100000
)

// validate checks the submitted values and builds the site overrides.
func (a *Admin) customizeValidate(ctx context.Context, c *reqCtx, r *http.Request) (customizeValues, map[string]any, string) {
	v := customizeValues{}
	for _, k := range customizeFields {
		v[k] = r.PostFormValue(k)
	}
	d := map[string]any{}
	mods := map[string]any{}
	name := strings.TrimSpace(v["blogname"])
	if name == "" || len(name) > 200 || len(v["blogdescription"]) > 500 {
		return v, nil, "Please enter a site title (and a tagline of up to 500 characters)."
	}
	d["blogname"], d["blogdescription"] = escHTML(name), escHTML(strings.TrimSpace(v["blogdescription"]))
	image := func(key string) (*db.Media, string) {
		id := atoi64(v[key])
		if id == 0 {
			return nil, ""
		}
		m, err := db.GetMedia(ctx, a.db.R, id)
		if err != nil || m == nil || !strings.HasPrefix(m.Mime, "image/") {
			return nil, "Please choose an image from the media library."
		}
		return m, ""
	}
	icon, msg := image("site_icon")
	if msg != "" {
		return v, nil, msg
	}
	if icon != nil {
		d["site_icon"] = float64(icon.PostID)
	} else {
		d["site_icon"] = float64(0)
	}
	color := strings.TrimPrefix(v["header_textcolor"], "#")
	if !reCSSColor.MatchString(color) {
		return v, nil, "Please choose a valid header text color."
	}
	if v["display_header_text"] == "" {
		color = "blank"
	}
	mods["header_textcolor"] = strings.ToLower(color)
	header, msg := image("header_image_id")
	if msg != "" {
		return v, nil, msg
	}
	if header != nil {
		mods["header_image"] = "/wp-content/uploads/" + header.File
		mods["header_image_data"] = map[string]any{"attachment_id": float64(header.PostID), "url": "/wp-content/uploads/" + header.File,
			"width": float64(header.Width), "height": float64(header.Height)}
	} else {
		mods["header_image"] = "remove-header"
		mods["header_image_data"] = map[string]any{}
	}
	bg := strings.TrimPrefix(v["background_color"], "#")
	if !reCSSColor.MatchString(bg) {
		return v, nil, "Please choose a valid background color."
	}
	mods["background_color"] = strings.ToLower(bg)
	bgImg, msg := image("background_image_id")
	if msg != "" {
		return v, nil, msg
	}
	if bgImg != nil {
		mods["background_image"] = "/wp-content/uploads/" + bgImg.File
	} else if keep := r.PostFormValue("background_image_url"); keep != "" && keep == c.settings.ThemeMod("background_image") {
		mods["background_image"] = keep // an image outside the library (imported)
		v["background_image_url"] = keep
	} else {
		mods["background_image"] = ""
	}
	if !bgPositions[v["background_position"]] || !bgSizes[v["background_size"]] {
		return v, nil, "Invalid background settings."
	}
	px, py, _ := strings.Cut(v["background_position"], " ")
	mods["background_position_x"], mods["background_position_y"] = px, py
	mods["background_size"] = v["background_size"]
	mods["background_repeat"] = "no-repeat"
	if v["background_repeat"] != "" {
		mods["background_repeat"] = "repeat"
	}
	mods["background_attachment"] = "scroll"
	if v["background_attachment"] != "" {
		mods["background_attachment"] = "fixed"
	}
	d["theme_mods"] = mods
	menu := atoi64(v["menu_primary"])
	if menu != 0 {
		if _, err := db.GetMenu(ctx, a.db.R, menu); err != nil {
			return v, nil, "Please choose a menu."
		}
	}
	d["menu_primary"] = float64(menu)
	front, pof, pfp := v["show_on_front"], atoi64(v["page_on_front"]), atoi64(v["page_for_posts"])
	if front != "posts" && front != "page" {
		return v, nil, "Invalid homepage setting."
	}
	for _, id := range []int64{pof, pfp} {
		if id == 0 {
			continue
		}
		if p, err := db.GetPost(ctx, a.db.R, id); err != nil || p.Type != db.TypePage || p.Status != db.StatusPublish {
			return v, nil, "Please choose published pages."
		}
	}
	if front == "page" && pof == 0 {
		front = "posts" // like WordPress until a page is chosen
	}
	if pof != 0 && pof == pfp {
		return v, nil, "The homepage and the posts page must be different pages."
	}
	d["show_on_front"], d["page_on_front"], d["page_for_posts"] = front, float64(pof), float64(pfp)
	css := strings.ReplaceAll(v["custom_css"], "\r\n", "\n")
	if len(css) > customCSSMax {
		return v, nil, "The additional CSS is too long."
	}
	if auth.Can(c.user, auth.CapUnfilteredHTML) {
		d["custom_css"] = site.SanitizeCSS(css)
	}
	return v, d, ""
}

type customizeData struct {
	basePage
	V         customizeValues
	Error     string
	HasDraft  bool
	Pages     []option
	Menus     []option
	Positions []string
	Images    map[string]string // field → preview URL
	CanCSS    bool
}

// GET /admin/customize
func (a *Admin) customizePage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderCustomize(w, r, c, nil, "")
}

func (a *Admin) renderCustomize(w http.ResponseWriter, r *http.Request, c *reqCtx, form customizeValues, errMsg string) error {
	ctx := r.Context()
	d := &customizeData{basePage: a.base(ctx, c, "Customize", "appearance-customize"), CanCSS: auth.Can(c.user, auth.CapUnfilteredHTML), Error: errMsg,
		Positions: []string{"left top", "center top", "right top", "left center", "center center", "right center", "left bottom", "center bottom", "right bottom"}}
	d.Scripts = []string{"upload.js", "editor/editor.js", "customize.js"}
	d.Styles = []string{"editor/editor.css"}
	d.BodyClass += " gp-customizing"
	if msg, ok := map[string]notice{"published": {"success", "Published."}, "discarded": {"info", "Changes discarded."}}[r.URL.Query().Get("msg")]; ok && d.Notice == nil {
		d.Notice = &msg
	}
	d.V = a.customizeCurrent(ctx, c.settings)
	if raw, _ := db.UserMeta(ctx, a.db.R, c.user.ID, metaCustomizeDraft+"_form"); raw != "" {
		var saved customizeValues
		if json.Unmarshal([]byte(raw), &saved) == nil {
			for k, v := range saved {
				d.V[k] = v
			}
			d.HasDraft = true
		}
	}
	for k, v := range form {
		d.V[k] = v
	}
	pages, err := db.AllPages(ctx, a.db.R)
	if err != nil {
		return err
	}
	for _, p := range pages {
		if p.Status == db.StatusPublish {
			d.Pages = append(d.Pages, option{Value: strconv.FormatInt(p.ID, 10), Label: p.Title})
		}
	}
	menus, err := db.Menus(ctx, a.db.R)
	if err != nil {
		return err
	}
	for _, m := range menus {
		d.Menus = append(d.Menus, option{Value: strconv.FormatInt(m.ID, 10), Label: m.Name})
	}
	d.Images = map[string]string{}
	for _, k := range []string{"site_icon", "header_image_id", "background_image_id"} {
		if id := atoi64(d.V[k]); id != 0 {
			if m, err := db.GetMedia(ctx, a.db.R, id); err == nil && m != nil {
				d.Images[k] = mediaURL(c, m, "medium")
			}
		}
	}
	if d.V["background_image_url"] != "" && d.Images["background_image_id"] == "" {
		d.Images["background_image_id"] = d.V["background_image_url"]
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "customize", d)
}

// POST /admin/api/customize/draft: store the draft for the preview.
func (a *Admin) customizeDraft(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	v, d, msg := a.customizeValidate(ctx, c, r)
	if msg != "" {
		return badRequest(msg)
	}
	draft, err := json.Marshal(d)
	if err != nil {
		return err
	}
	form, _ := json.Marshal(v)
	if err := db.SetUserMeta(ctx, a.db.W, c.user.ID, metaCustomizeDraft, string(draft)); err != nil {
		return err
	}
	if err := db.SetUserMeta(ctx, a.db.W, c.user.ID, metaCustomizeDraft+"_form", string(form)); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
	return nil
}

// POST /admin/customize/publish
func (a *Admin) customizePublish(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	v, d, msg := a.customizeValidate(ctx, c, r)
	if msg != "" {
		return a.renderCustomize(w, r, c, v, msg)
	}
	mods := map[string]any{}
	for k, val := range c.settings.ThemeMods {
		mods[k] = val
	}
	for k, val := range d["theme_mods"].(map[string]any) {
		mods[k] = val
	}
	locs := map[string]int64{}
	db.GetOption(ctx, a.db.R, "nav_menu_locations", &locs)
	if locs == nil {
		locs = map[string]int64{}
	}
	if id := int64(d["menu_primary"].(float64)); id != 0 {
		locs["primary"] = id
	} else {
		delete(locs, "primary")
	}
	icon := int64(d["site_icon"].(float64))
	if icon != 0 && icon != c.settings.SiteIcon {
		if err := a.content.PrepareSiteIcon(ctx, icon); err != nil {
			return err
		}
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		set := map[string]any{"blogname": d["blogname"], "blogdescription": d["blogdescription"], "site_icon": strconv.FormatInt(icon, 10),
			"show_on_front": d["show_on_front"], "page_on_front": strconv.FormatInt(int64(d["page_on_front"].(float64)), 10),
			"page_for_posts": strconv.FormatInt(int64(d["page_for_posts"].(float64)), 10), "theme_mods": mods, "nav_menu_locations": locs}
		if css, ok := d["custom_css"]; ok {
			set["custom_css"] = css
		}
		for k, val := range set {
			if err := db.SetOption(ctx, tx, k, val); err != nil {
				return err
			}
		}
		if err := db.SetUserMeta(ctx, tx, c.user.ID, metaCustomizeDraft, ""); err != nil {
			return err
		}
		return db.SetUserMeta(ctx, tx, c.user.ID, metaCustomizeDraft+"_form", "")
	})
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "customizer_published", "option", 0, nil)
	a.site.Invalidate()
	return seeOther(w, r, "/admin/customize?msg=published")
}

// POST /admin/customize/discard
func (a *Admin) customizeDiscard(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	db.SetUserMeta(ctx, a.db.W, c.user.ID, metaCustomizeDraft, "")
	db.SetUserMeta(ctx, a.db.W, c.user.ID, metaCustomizeDraft+"_form", "")
	return seeOther(w, r, "/admin/customize?msg=discarded")
}
