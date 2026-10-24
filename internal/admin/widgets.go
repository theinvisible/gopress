package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// Widgets (widgets.php): the theme's widget areas with drag-and-drop
// ordering and a form per widget. Only users with edit_theme_options
// (administrators) reach it; they may store HTML like WordPress allows
// users with unfiltered_html.

func (a *Admin) registerWidgets(mux *http.ServeMux) {
	mux.Handle("GET /admin/widgets", a.page(auth.CapEditThemeOptions, a.widgetsPage))
	mux.Handle("POST /admin/widgets/add", a.page(auth.CapEditThemeOptions, a.widgetAdd))
	mux.Handle("POST /admin/widgets/order", a.page(auth.CapEditThemeOptions, a.widgetOrder))
	mux.Handle("POST /admin/widgets/{id}", a.page(auth.CapEditThemeOptions, a.widgetSave))
	mux.Handle("POST /admin/widgets/{id}/delete", a.page(auth.CapEditThemeOptions, a.widgetDelete))
}

// widgetArea is a sidebar registered by the theme (Twenty Twelve).
type widgetArea struct {
	ID, Name, Description string
}

var widgetAreas = []widgetArea{
	{"sidebar-1", "Main Sidebar", "Appears on posts and pages except the optional Front Page template, which has its own widgets"},
	{"sidebar-2", "First Front Page Widget Area", "Appears when using the optional Front Page template with a page set as Static Front Page"},
	{"sidebar-3", "Second Front Page Widget Area", "Appears when using the optional Front Page template with a page set as Static Front Page"},
}

type widgetField struct {
	Key, Label, Kind, Help string // kind: text, textarea, html, number, checkbox, select
	Options                []option
	Min, Max               int
}

type widgetType struct {
	Type, Label, Description string
	Fields                   []widgetField
}

var titleField = widgetField{Key: "title", Label: "Title", Kind: "text"}

var widgetTypes = []widgetType{
	{"archives", "Archives", "A monthly archive of your site’s posts.", []widgetField{titleField,
		{Key: "count", Label: "Show post counts", Kind: "checkbox"}}},
	{"block", "HTML Block", "Arbitrary HTML (imported block widgets).", []widgetField{{Key: "content", Label: "HTML", Kind: "html"}}},
	{"categories", "Categories", "A list of categories.", []widgetField{titleField,
		{Key: "count", Label: "Show post counts", Kind: "checkbox"}, {Key: "hierarchical", Label: "Show hierarchy", Kind: "checkbox"}}},
	{"custom_html", "Custom HTML", "Arbitrary HTML code.", []widgetField{titleField, {Key: "content", Label: "Content", Kind: "html"}}},
	{"meta", "Meta", "Login, RSS, & WordPress.org links.", []widgetField{titleField}},
	{"nav_menu", "Navigation Menu", "Add a navigation menu to your sidebar.", []widgetField{titleField,
		{Key: "nav_menu", Label: "Select Menu", Kind: "menu"}}},
	{"pages", "Pages", "A list of your site’s pages.", []widgetField{titleField,
		{Key: "sortby", Label: "Sort by", Kind: "select", Options: []option{{Value: "post_title", Label: "Page title"}, {Value: "menu_order", Label: "Page order"}, {Value: "ID", Label: "Page ID"}}},
		{Key: "exclude", Label: "Exclude", Kind: "text", Help: "Page IDs, separated by commas."}}},
	{"recent-posts", "Recent Posts", "Your site’s most recent posts.", []widgetField{titleField,
		{Key: "number", Label: "Number of posts to show", Kind: "number", Min: 1, Max: 30},
		{Key: "show_date", Label: "Display post date?", Kind: "checkbox"}}},
	{"search", "Search", "A search form for your site.", []widgetField{titleField}},
	{"tag_cloud", "Tag Cloud", "A cloud of your most used tags.", []widgetField{titleField}},
	{"text", "Text", "Arbitrary text.", []widgetField{titleField, {Key: "text", Label: "Content", Kind: "html"},
		{Key: "filter", Label: "Automatically add paragraphs", Kind: "checkbox"}}},
}

func findWidgetType(t string) *widgetType {
	for i := range widgetTypes {
		if widgetTypes[i].Type == t {
			return &widgetTypes[i]
		}
	}
	return nil
}

type widgetCard struct {
	ID, Type, Label, Title string
	Fields                 []widgetFieldValue
	Known                  bool
}

type widgetFieldValue struct {
	widgetField
	Value   string
	Checked bool
}

type widgetAreaData struct {
	widgetArea
	Widgets []widgetCard
}

type widgetsData struct {
	basePage
	Areas    []widgetAreaData
	Inactive widgetAreaData
	Types    []widgetType
	Menus    []option
}

func (a *Admin) widgetCard(w db.Widget, menus []option) widgetCard {
	c := widgetCard{ID: w.ID, Type: w.Type, Label: w.Type}
	if t, _ := w.Settings["title"].(string); t != "" {
		c.Title = t
	}
	wt := findWidgetType(w.Type)
	if wt == nil {
		return c // plugin widget: no form yet (M3)
	}
	c.Known, c.Label = true, wt.Label
	for _, f := range wt.Fields {
		if f.Kind == "menu" {
			f.Kind, f.Options = "select", append([]option{{Value: "0", Label: "— Select —"}}, menus...)
		}
		v := widgetFieldValue{widgetField: f}
		switch x := w.Settings[f.Key].(type) {
		case string:
			v.Value = x
			v.Checked = x != "" && x != "0"
		case float64:
			v.Value = strconv.FormatFloat(x, 'f', -1, 64)
			v.Checked = x != 0
		case bool:
			v.Checked = x
		}
		if f.Key == "filter" && w.Settings[f.Key] == "content" {
			v.Checked = true
		}
		c.Fields = append(c.Fields, v)
	}
	return c
}

// GET /admin/widgets
func (a *Admin) widgetsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	d := &widgetsData{basePage: a.base(ctx, c, "Widgets", "appearance-widgets"), Types: widgetTypes}
	d.Scripts = []string{"widgets.js"}
	if msg, ok := map[string]notice{"saved": {"success", "Widget saved."}, "added": {"success", "Widget added."},
		"deleted": {"success", "Widget deleted."}}[r.URL.Query().Get("msg")]; ok && d.Notice == nil {
		d.Notice = &msg
	}
	all, err := db.AllWidgets(ctx, a.db.R)
	if err != nil {
		return err
	}
	menus, err := db.Menus(ctx, a.db.R)
	if err != nil {
		return err
	}
	var menuOpts []option
	for _, m := range menus {
		menuOpts = append(menuOpts, option{Value: strconv.FormatInt(m.ID, 10), Label: m.Name})
	}
	for _, area := range widgetAreas {
		ad := widgetAreaData{widgetArea: area}
		for _, wd := range all[area.ID] {
			ad.Widgets = append(ad.Widgets, a.widgetCard(wd, menuOpts))
		}
		d.Areas = append(d.Areas, ad)
	}
	d.Inactive = widgetAreaData{widgetArea: widgetArea{db.InactiveWidgets, "Inactive Widgets", "Drag widgets here to remove them from the site but keep their settings."}}
	for _, wd := range all[db.InactiveWidgets] {
		d.Inactive.Widgets = append(d.Inactive.Widgets, a.widgetCard(wd, menuOpts))
	}
	return a.render(w, "widgets", d)
}

func validArea(id string) bool {
	if id == db.InactiveWidgets {
		return true
	}
	for _, a := range widgetAreas {
		if a.ID == id {
			return true
		}
	}
	return false
}

// POST /admin/widgets/add (type, sidebar)
func (a *Admin) widgetAdd(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	wt := findWidgetType(r.PostFormValue("type"))
	area := r.PostFormValue("sidebar")
	if wt == nil || !validArea(area) {
		return badRequest("Unknown widget or widget area.")
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error {
		id, err := db.NextWidgetID(ctx, tx, wt.Type)
		if err != nil {
			return err
		}
		n, err := db.WidgetCount(ctx, tx, area)
		if err != nil {
			return err
		}
		settings := map[string]any{}
		if wt.Type == "recent-posts" {
			settings["number"] = float64(5)
		}
		return db.InsertWidget(ctx, tx, &db.Widget{ID: id, Sidebar: area, Position: n, Type: wt.Type, Settings: settings})
	})
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "widget_added", "widget", 0, map[string]any{"type": wt.Type, "sidebar": area})
	a.site.Invalidate()
	return seeOther(w, r, "/admin/widgets?msg=added#"+area)
}

// POST /admin/widgets/{id}: the widget's form (JSON answer for fetch).
func (a *Admin) widgetSave(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	wd, err := db.GetWidget(ctx, a.db.R, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	wt := findWidgetType(wd.Type)
	if wt == nil {
		return badRequest("This widget has no settings form.")
	}
	settings := map[string]any{}
	for k, v := range wd.Settings { // keep unknown (imported) keys
		settings[k] = v
	}
	for _, f := range wt.Fields {
		v := r.PostFormValue(f.Key)
		switch f.Kind {
		case "text":
			v = strings.TrimSpace(render.SanitizeText(v))
			if len(v) > 500 {
				return badRequest("The " + strings.ToLower(f.Label) + " is too long.")
			}
			settings[f.Key] = v
		case "html":
			if len(v) > 100000 {
				return badRequest("The content is too long.")
			}
			if !auth.Can(c.user, auth.CapUnfilteredHTML) {
				v = render.SanitizePost(v)
			}
			settings[f.Key] = v
		case "number":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < f.Min || n > f.Max {
				return badRequest(f.Label + " must be from " + strconv.Itoa(f.Min) + " to " + strconv.Itoa(f.Max) + ".")
			}
			settings[f.Key] = float64(n)
		case "checkbox":
			settings[f.Key] = v != ""
		case "menu":
			id := atoi64(v)
			if id != 0 {
				if _, err := db.GetMenu(ctx, a.db.R, id); err != nil {
					return badRequest("Please choose a menu.")
				}
			}
			settings[f.Key] = float64(id)
		case "select":
			ok := false
			for _, o := range f.Options {
				ok = ok || o.Value == v
			}
			if !ok {
				return badRequest("Invalid value.")
			}
			settings[f.Key] = v
		}
	}
	if err := db.UpdateWidgetSettings(ctx, a.db.W, wd.ID, settings); err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "widget_saved", "widget", 0, map[string]any{"id": wd.ID})
	a.site.Invalidate()
	if isAPI(r) {
		title, _ := settings["title"].(string)
		writeJSON(w, http.StatusOK, map[string]string{"title": title})
		return nil
	}
	return seeOther(w, r, "/admin/widgets?msg=saved#"+wd.Sidebar)
}

// POST /admin/widgets/{id}/delete
func (a *Admin) widgetDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := db.DeleteWidget(ctx, a.db.W, id); err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "widget_deleted", "widget", 0, map[string]any{"id": id})
	a.site.Invalidate()
	if isAPI(r) {
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return nil
	}
	return seeOther(w, r, "/admin/widgets?msg=deleted")
}

// POST /admin/widgets/order: JSON body {"sidebar-1": ["search-2", …], …}
func (a *Admin) widgetOrder(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	var layout map[string][]string
	if err := json.NewDecoder(r.Body).Decode(&layout); err != nil {
		return badRequest("Invalid request.")
	}
	for area := range layout {
		if !validArea(area) {
			return badRequest("Unknown widget area.")
		}
	}
	err := a.db.Tx(ctx, func(tx *sql.Tx) error { return db.ArrangeWidgets(ctx, tx, layout) })
	if errors.Is(err, db.ErrUnknownWidget) {
		return badRequest("Unknown widget.")
	}
	if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "widgets_arranged", "widget", 0, nil)
	a.site.Invalidate()
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
	return nil
}
