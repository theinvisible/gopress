package admin

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/plugins"
)

// Plugins: the Plugins screen (install, approve, activate), the plugins'
// own admin pages (settings forms, tables, sandboxed iframes with the
// bridge), dashboard boxes, widget forms and editor buttons.

func (a *Admin) registerPlugins(mux *http.ServeMux) {
	if a.plugins == nil {
		return
	}
	mux.Handle("GET /admin/plugins/{$}", a.page(auth.CapActivatePlugins, a.pluginsPage))
	mux.Handle("POST /admin/plugins/upload", a.page(auth.CapActivatePlugins, a.pluginUpload))
	mux.Handle("GET /admin/plugins/{name}/activate", a.page(auth.CapActivatePlugins, a.pluginReview))
	mux.Handle("POST /admin/plugins/{name}/activate", a.page(auth.CapActivatePlugins, a.pluginActivate))
	mux.Handle("POST /admin/plugins/{name}/deactivate", a.page(auth.CapActivatePlugins, a.pluginDeactivate))
	mux.Handle("POST /admin/plugins/{name}/delete", a.page(auth.CapActivatePlugins, a.pluginDelete))
	mux.Handle("POST /admin/plugins/{name}/import", a.page(auth.CapActivatePlugins, a.pluginImport))
	// Plugin pages check the page's own capability.
	mux.Handle("GET /admin/plugin/{name}/{page}", a.page(auth.CapRead, a.pluginPage))
	mux.Handle("POST /admin/plugin/{name}/{page}", a.page(auth.CapRead, a.pluginPageSubmit))
	mux.Handle("POST /admin/api/plugins/{name}/{page}", a.page(auth.CapRead, a.pluginAPI))
	// Files of iframe pages: loaded by a sandboxed document (an opaque
	// origin without cookies), so no session; they are package files.
	mux.HandleFunc("GET /plugin-ui/{name}/{file...}", a.pluginUIFile)
	mux.HandleFunc("GET /plugin-ui/_frame.js", a.pluginFrameScript)
}

var pluginMessages = map[string]notice{
	"activated":   {"success", "Plugin activated."},
	"deactivated": {"success", "Plugin deactivated."},
	"deleted":     {"success", "Plugin deleted."},
	"installed":   {"success", "Plugin installed. Review its permissions to activate it."},
	"updated":     {"success", "Plugin updated."},
	"approve":     {"warning", "The new version asks for more permissions. It stays inactive until you approve them."},
	"import":      {"success", "The import is running in the background; see the activity log."},
}

type pluginRow struct {
	Name, Title, Version, Description, Author, URL string
	Source                                         string
	Active, Paused, Approved, Bundled, Running     bool
	LastError                                      string
	Extra                                          []string
	Pages                                          []pluginPageLink
	CanImport, Migrated                            bool
}

type pluginPageLink struct{ Title, URL string }

type pluginsData struct {
	basePage
	Rows   []pluginRow
	Reauth bool
	Error  string
	MaxMB  int
}

// GET /admin/plugins/
func (a *Admin) pluginsPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	return a.renderPlugins(w, r, c, "")
}

func (a *Admin) renderPlugins(w http.ResponseWriter, r *http.Request, c *reqCtx, errMsg string) error {
	ctx := r.Context()
	d := &pluginsData{basePage: a.base(ctx, c, "Plugins", "plugins"), Reauth: a.auth.RecentlyAuthenticated(c.cur), Error: errMsg,
		MaxMB: plugins.MaxZipBytes >> 20}
	d.Scripts = []string{"plugin-page.js"}
	if msg, ok := pluginMessages[r.URL.Query().Get("msg")]; ok && errMsg == "" && d.Notice == nil {
		d.Notice = &msg
	}
	list, err := a.plugins.List(ctx)
	if err != nil {
		return err
	}
	for _, inf := range list {
		m := inf.Package.Manifest
		row := pluginRow{Name: m.Name, Title: m.Title, Version: m.Version, Description: m.Description, Author: m.Author, URL: m.URL,
			Source: inf.Package.Source, Bundled: inf.Bundled, Active: inf.State != nil && inf.State.Active && inf.Approved,
			Approved: inf.Approved, Extra: inf.Extra, Running: inf.Status.Running, Paused: inf.Status.Paused, LastError: inf.Status.LastError}
		if inf.State != nil {
			row.Migrated = inf.State.Migrated
		}
		if row.Active {
			row.CanImport = a.plugins.HasImport(ctx, m.Name)
			for _, pg := range m.Admin {
				if auth.Can(c.user, pg.Capability) {
					row.Pages = append(row.Pages, pluginPageLink{pg.Title, pluginPageURL(m.Name, pg.ID)})
				}
			}
		}
		d.Rows = append(d.Rows, row)
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "plugins", d)
}

func pluginPageURL(name, page string) string { return "/admin/plugin/" + name + "/" + page }

// POST /admin/plugins/upload (multipart, field "plugin")
func (a *Admin) pluginUpload(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	if a.needReauth(w, r, c, "/admin/plugins/") {
		return nil
	}
	f, _, err := r.FormFile("plugin")
	if err != nil {
		return a.renderPlugins(w, r, c, "Please choose a plugin .zip file (at most "+strconv.Itoa(plugins.MaxZipBytes>>20)+" MB).")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, plugins.MaxZipBytes+1))
	if err != nil {
		return err
	}
	pkg, extra, err := a.plugins.Install(ctx, data)
	if err != nil {
		if errors.Is(err, plugins.ErrOlder) {
			return a.renderPlugins(w, r, c, "The package is older than the bundled version.")
		}
		return a.renderPlugins(w, r, c, "The package was refused: "+err.Error())
	}
	m := pkg.Manifest
	a.auth.Audit(ctx, r, c.user.ID, "plugin_installed", "plugin", 0, map[string]any{"name": m.Name, "version": m.Version,
		"sha256": pkg.SHA256})
	inf := a.pluginInfo(ctx, m.Name)
	switch {
	case inf != nil && inf.State != nil && inf.State.Active && inf.Approved:
		return seeOther(w, r, "/admin/plugins/?msg=updated")
	case inf != nil && inf.State != nil && len(extra) > 0:
		return seeOther(w, r, "/admin/plugins/"+m.Name+"/activate?msg=approve")
	}
	return seeOther(w, r, "/admin/plugins/"+m.Name+"/activate?msg=installed")
}

func (a *Admin) pluginInfo(ctx context.Context, name string) *plugins.Info {
	list, err := a.plugins.List(ctx)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Package.Manifest.Name == name {
			return &list[i]
		}
	}
	return nil
}

type pluginReviewData struct {
	basePage
	Plugin      pluginRow
	Permissions []string
	New         []string
	Shortcodes  []string
	Widgets     []string
	Pages       []string
	Inserts     []string
	Reauth      bool
	SHA256      string
}

// GET /admin/plugins/{name}/activate: what the plugin may do.
func (a *Admin) pluginReview(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	inf := a.pluginInfo(ctx, r.PathValue("name"))
	if inf == nil {
		return errNotFound
	}
	m := inf.Package.Manifest
	d := &pluginReviewData{basePage: a.base(ctx, c, "Activate "+m.Title, "plugins"), Reauth: a.auth.RecentlyAuthenticated(c.cur),
		Plugin: pluginRow{Name: m.Name, Title: m.Title, Version: m.Version, Description: m.Description, Author: m.Author, URL: m.URL,
			Source: inf.Package.Source, Active: inf.State != nil && inf.State.Active && inf.Approved},
		Permissions: m.Permissions.Describe(), SHA256: inf.Package.SHA256}
	if inf.State != nil {
		d.New = inf.Extra
	}
	if msg, ok := pluginMessages[r.URL.Query().Get("msg")]; ok && d.Notice == nil {
		d.Notice = &msg
	}
	for _, s := range m.Shortcodes {
		d.Shortcodes = append(d.Shortcodes, "["+s.Name+"]")
	}
	for _, wd := range m.Widgets {
		d.Widgets = append(d.Widgets, wd.Title)
	}
	for _, pg := range m.Admin {
		kind := map[string]string{"settings": "settings form", "table": "list", "iframe": "custom page (sandboxed)"}[pg.Kind]
		d.Pages = append(d.Pages, pg.Title+" ("+kind+", for users who can "+strings.ReplaceAll(pg.Capability, "_", " ")+")")
	}
	for _, in := range m.Editor {
		d.Inserts = append(d.Inserts, in.Label)
	}
	return a.render(w, "plugin-review", d)
}

// POST /admin/plugins/{name}/activate: approve the permissions and
// activate.
func (a *Admin) pluginActivate(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	name := r.PathValue("name")
	if a.needReauth(w, r, c, "/admin/plugins/"+name+"/activate") {
		return nil
	}
	inf := a.pluginInfo(ctx, name)
	if inf == nil {
		return errNotFound
	}
	// The approval is for exactly the package that was reviewed.
	sha := r.PostFormValue("sha256")
	if sha == "" || sha != inf.Package.SHA256 {
		return badRequest("The plugin changed since you reviewed it. Please review it again.")
	}
	if err := a.plugins.Activate(ctx, name, sha); errors.Is(err, plugins.ErrChanged) {
		return badRequest("The plugin changed since you reviewed it. Please review it again.")
	} else if err != nil {
		return err
	}
	a.auth.Audit(ctx, r, c.user.ID, "plugin_activated", "plugin", 0, map[string]any{"name": name,
		"version": inf.Package.Manifest.Version, "permissions": inf.Package.Manifest.Permissions.Canonical()})
	if (inf.State == nil || !inf.State.Migrated) && a.plugins.HasImport(ctx, name) {
		a.runPluginImport(r, c, name)
	}
	return seeOther(w, r, "/admin/plugins/?msg=activated")
}

// runPluginImport runs the plugin's WordPress import in the background.
func (a *Admin) runPluginImport(r *http.Request, c *reqCtx, name string) {
	userID, ip := c.user.ID, a.auth.ClientIP(r)
	a.background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		notice, err := a.plugins.Migrate(ctx, name)
		detail := map[string]any{"name": name, "result": notice, "ip": ip}
		if err != nil {
			detail["error"] = plugins.ErrorMessage(err)
		}
		a.auth.Audit(ctx, nil, userID, "plugin_import", "plugin", 0, detail)
	})
}

// POST /admin/plugins/{name}/import: run the WordPress import again.
func (a *Admin) pluginImport(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	name := r.PathValue("name")
	if a.needReauth(w, r, c, "/admin/plugins/") {
		return nil
	}
	if !a.plugins.HasImport(r.Context(), name) {
		return badRequest("There is nothing to import.")
	}
	a.runPluginImport(r, c, name)
	return seeOther(w, r, "/admin/plugins/?msg=import")
}

// POST /admin/plugins/{name}/deactivate
func (a *Admin) pluginDeactivate(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	name := r.PathValue("name")
	if a.plugins.Package(name) == nil {
		return errNotFound
	}
	if err := a.plugins.Deactivate(r.Context(), name); err != nil {
		return err
	}
	a.auth.Audit(r.Context(), r, c.user.ID, "plugin_deactivated", "plugin", 0, map[string]any{"name": name})
	return seeOther(w, r, "/admin/plugins/?msg=deactivated")
}

// POST /admin/plugins/{name}/delete (uploaded plugins; data optional)
func (a *Admin) pluginDelete(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	name := r.PathValue("name")
	if a.needReauth(w, r, c, "/admin/plugins/") {
		return nil
	}
	data := r.PostFormValue("delete_data") != ""
	if err := a.plugins.Remove(r.Context(), name, data); err != nil {
		return badRequest(err.Error())
	}
	a.auth.Audit(r.Context(), r, c.user.ID, "plugin_deleted", "plugin", 0, map[string]any{"name": name, "data": data})
	return seeOther(w, r, "/admin/plugins/?msg=deleted")
}

// Plugin pages.

type pluginField struct {
	plugins.Field
	Value    string
	Checked  bool
	MediaIDs string // media, media_list: comma separated ids
}

type pluginPageData struct {
	basePage
	Plugin  string
	Page    *plugins.AdminPage
	Action  string
	Fields  []pluginField
	Error   string
	Columns []plugins.Field
	Table   *plugins.AdminPageOut
	Search  string
	Paged   int
	Pages   int
	Frame   string // iframe src
	APIURL  string
}

// pluginPageFor returns the page and checks the user's capability.
func (a *Admin) pluginPageFor(r *http.Request, c *reqCtx) (string, *plugins.AdminPage, error) {
	name := r.PathValue("name")
	pg, ok := a.plugins.Page(name, r.PathValue("page"))
	if !ok {
		return "", nil, errNotFound
	}
	if !auth.Can(c.user, pg.Capability) {
		return "", nil, errForbidden
	}
	return name, pg, nil
}

// GET /admin/plugin/{name}/{page}
func (a *Admin) pluginPage(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	name, pg, err := a.pluginPageFor(r, c)
	if err != nil {
		return err
	}
	return a.renderPluginPage(w, r, c, name, pg, nil, "")
}

func (a *Admin) renderPluginPage(w http.ResponseWriter, r *http.Request, c *reqCtx, name string, pg *plugins.AdminPage,
	values map[string]any, errMsg string) error {
	ctx := r.Context()
	d := &pluginPageData{basePage: a.base(ctx, c, pg.Title, "plugin-"+name+"-"+pg.ID), Plugin: name, Page: pg,
		Action: pluginPageURL(name, pg.ID), Error: errMsg}
	if r.URL.Query().Get("msg") == "saved" {
		d.Notice = &notice{"success", "Settings saved."}
	}
	// A table action's notice comes from the plugin: kept server-side for
	// this session, never taken from the URL.
	if v, ok := a.flash.take(a.flashKey(c, "plugin")); ok {
		d.Notice = &notice{"success", v.(string)}
	}
	switch pg.Kind {
	case "settings":
		if values == nil {
			var err error
			if values, err = a.plugins.Settings(ctx, name, pg.ID); err != nil {
				return err
			}
		}
		for _, f := range pg.Fields {
			d.Fields = append(d.Fields, fieldValue(f, values[f.Name]))
		}
		// The editor bundle brings the media picker.
		d.Scripts, d.Styles = []string{"upload.js", "editor/editor.js", "plugin-page.js"}, []string{"editor/editor.css"}
	case "table":
		d.Columns = pg.Columns
		d.Scripts = []string{"plugin-page.js"}
		d.Search = r.URL.Query().Get("s")
		d.Paged = max(1, int(atoi64(r.URL.Query().Get("paged"))))
		out, err := a.plugins.Table(ctx, name, pg.ID, c.user, map[string]string{"s": d.Search, "paged": strconv.Itoa(d.Paged)})
		if err != nil {
			d.Error = plugins.ErrorMessage(err)
		} else {
			d.Table = out
			if len(out.Rows) > 0 {
				d.Pages = max(1, (out.Total+len(out.Rows)-1)/max(len(out.Rows), 1))
			}
		}
	case "iframe":
		d.Frame = "/plugin-ui/" + name + "/" + strings.TrimPrefix(pg.File, "admin/")
		d.APIURL = "/admin/api/plugins/" + name + "/" + pg.ID
		d.Scripts, d.Styles = []string{"upload.js", "editor/editor.js", "plugin-bridge.js"}, []string{"editor/editor.css"}
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	return a.render(w, "plugin-page", d)
}

func fieldValue(f plugins.Field, v any) pluginField {
	pf := pluginField{Field: f}
	switch x := v.(type) {
	case string:
		pf.Value = x
	case float64:
		pf.Value = strconv.FormatFloat(x, 'f', -1, 64)
		if f.Type == "media" && x == 0 {
			pf.Value = ""
		}
	case bool:
		pf.Checked = x
	case []any:
		var ids []string
		for _, e := range x {
			if n, ok := e.(float64); ok {
				ids = append(ids, strconv.FormatFloat(n, 'f', -1, 64))
			}
		}
		pf.Value = strings.Join(ids, ",")
	}
	if f.Type == "password" {
		pf.Value = "" // never sent back; empty keeps the stored value
	}
	pf.MediaIDs = pf.Value
	return pf
}

// POST /admin/plugin/{name}/{page}: save a settings form, or run a table
// action.
func (a *Admin) pluginPageSubmit(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	ctx := r.Context()
	name, pg, err := a.pluginPageFor(r, c)
	if err != nil {
		return err
	}
	switch pg.Kind {
	case "settings":
		values, err := plugins.ParseFields(pg.Fields, func(n string) []string { return r.PostForm[n] })
		if err != nil {
			return a.renderPluginPage(w, r, c, name, pg, formValues(pg.Fields, r), err.Error())
		}
		old, err := a.plugins.Settings(ctx, name, pg.ID)
		if err != nil {
			return err
		}
		for _, f := range pg.Fields { // an empty password field keeps the stored one
			if f.Type == "password" && values[f.Name] == "" {
				values[f.Name] = old[f.Name]
			}
		}
		if err := a.plugins.SaveSettings(ctx, name, pg.ID, values); err != nil {
			return err
		}
		a.auth.Audit(ctx, r, c.user.ID, "plugin_settings_saved", "plugin", 0, map[string]any{"name": name, "page": pg.ID})
		return seeOther(w, r, pluginPageURL(name, pg.ID)+"?msg=saved")
	case "table":
		action := r.PostFormValue("action")
		ids := r.PostForm["ids[]"]
		if action == "" || len(ids) == 0 || len(ids) > 500 {
			return seeOther(w, r, pluginPageURL(name, pg.ID))
		}
		n, err := a.plugins.Submit(ctx, name, pg.ID, c.user, action, ids)
		if err != nil {
			return a.renderPluginPage(w, r, c, name, pg, nil, plugins.ErrorMessage(err))
		}
		a.auth.Audit(ctx, r, c.user.ID, "plugin_action", "plugin", 0, map[string]any{"name": name, "page": pg.ID,
			"action": action, "count": len(ids)})
		if n != "" {
			a.flash.put(a.flashKey(c, "plugin"), n)
		}
		return seeOther(w, r, pluginPageURL(name, pg.ID))
	}
	return errNotFound
}

func formValues(fields []plugins.Field, r *http.Request) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if f.Type == "checkbox" {
			out[f.Name] = r.PostFormValue(f.Name) != ""
		} else {
			out[f.Name] = r.PostFormValue(f.Name)
		}
	}
	return out
}

// POST /admin/api/plugins/{name}/{page}: a bridge call of an iframe page
// (JSON {method, params}; X-CSRF-Token).
func (a *Admin) pluginAPI(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	name, pg, err := a.pluginPageFor(r, c)
	if err != nil {
		return err
	}
	if pg.Kind != "iframe" {
		return errNotFound
	}
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Method == "" || len(req.Method) > 100 {
		return badRequest("Invalid request.")
	}
	out, err := a.plugins.API(r.Context(), name, pg.ID, c.user, req.Method, req.Params)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": plugins.ErrorMessage(err)})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"result": out})
	return nil
}

// uiTypes are the content types of iframe page files.
var uiTypes = map[string]string{".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml", ".woff2": "font/woff2", ".woff": "font/woff",
	".json": "application/json"}

// uiCSP isolates iframe pages even when opened directly: an opaque origin
// (no cookies, no access to the admin), scripts only from the site, no
// network access of their own (everything goes through the bridge).
func uiCSP(origin string) string {
	return "sandbox allow-scripts allow-forms allow-modals; default-src 'none'; script-src " + origin +
		"/plugin-ui/; style-src " + origin + "/plugin-ui/ 'unsafe-inline'; img-src " + origin + " data: blob:; font-src " +
		origin + "/plugin-ui/; connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"
}

// GET /plugin-ui/{name}/{file...}
func (a *Admin) pluginUIFile(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), "admin/"+r.PathValue("file")
	data, ok := a.plugins.UIFile(name, file)
	ctype := uiTypes[strings.ToLower(path.Ext(file))]
	if !ok || ctype == "" {
		http.NotFound(w, r)
		return
	}
	a.writeUI(w, r, ctype, data)
}

// GET /plugin-ui/_frame.js: the bridge inside iframe pages.
func (a *Admin) pluginFrameScript(w http.ResponseWriter, r *http.Request) {
	f := a.assets.files["plugin-frame.js"]
	if f == nil {
		http.NotFound(w, r)
		return
	}
	a.writeUI(w, r, "text/javascript; charset=utf-8", f.raw)
}

func (a *Admin) writeUI(w http.ResponseWriter, r *http.Request, ctype string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	// The sandboxed document has an opaque origin: its subresource
	// requests are cross-origin.
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	if strings.HasPrefix(ctype, "text/html") || strings.HasPrefix(ctype, "image/svg") {
		h.Set("Content-Security-Policy", uiCSP(strings.TrimRight(a.auth.Home(), "/")))
		h.Set("X-Frame-Options", "SAMEORIGIN")
	}
	w.Write(data)
}

// Dashboard boxes, widget types and editor buttons of plugins.

type pluginBox struct {
	Title string
	HTML  template.HTML
	Error string
}

func (a *Admin) pluginDashboard(ctx context.Context, c *reqCtx) []pluginBox {
	if a.plugins == nil {
		return nil
	}
	var out []pluginBox
	for _, b := range a.plugins.DashboardBoxes() {
		if !auth.Can(c.user, b.Box.Capability) {
			continue
		}
		box := pluginBox{Title: b.Box.Title}
		html, err := a.plugins.RenderDashboard(ctx, b.Plugin, b.Box.ID, c.user)
		if err != nil {
			box.Error = plugins.ErrorMessage(err)
		}
		box.HTML = template.HTML(html) // filtered by the plugin policy
		out = append(out, box)
	}
	return out
}

// pluginWidgetTypes converts the active plugins' widget types to the
// widgets screen's form schema.
func (a *Admin) pluginWidgetTypes() []widgetType {
	if a.plugins == nil {
		return nil
	}
	var out []widgetType
	for _, wt := range a.plugins.WidgetTypes() {
		t := widgetType{Type: wt.Widget.Type, Label: wt.Widget.Title, Description: wt.Widget.Description}
		for _, f := range wt.Widget.Fields {
			wf := widgetField{Key: f.Name, Label: f.Label, Kind: "text", Help: f.Help}
			switch f.Type {
			case "textarea":
				wf.Kind = "textarea"
			case "checkbox":
				wf.Kind = "checkbox"
			case "number":
				wf.Kind, wf.Min, wf.Max = "number", -1<<31, 1<<31-1
				if f.Min != nil {
					wf.Min = int(*f.Min)
				}
				if f.Max != nil {
					wf.Max = int(*f.Max)
				}
			case "select":
				wf.Kind = "select"
				for _, o := range f.Options {
					wf.Options = append(wf.Options, option{Value: o.Value, Label: o.Label})
				}
			}
			t.Fields = append(t.Fields, wf)
		}
		out = append(out, t)
	}
	return out
}

// editorInsert is a plugin button in the editor configuration.
type editorInsert struct {
	ID        string          `json:"id"`
	Label     string          `json:"label"`
	Icon      string          `json:"icon,omitempty"`
	Shortcode string          `json:"shortcode"`
	Fields    []plugins.Field `json:"fields,omitempty"`
	Frame     string          `json:"frame,omitempty"`
	API       string          `json:"api,omitempty"`
}

func (a *Admin) editorInserts(c *reqCtx) []editorInsert {
	if a.plugins == nil {
		return nil
	}
	var out []editorInsert
	for _, in := range a.plugins.EditorInserts() {
		e := editorInsert{ID: in.Plugin + "-" + in.Insert.ID, Label: in.Insert.Label, Icon: in.Insert.Icon,
			Shortcode: in.Insert.Shortcode, Fields: in.Insert.Fields}
		if in.Insert.Page != "" {
			pg, ok := a.plugins.Page(in.Plugin, in.Insert.Page)
			if !ok || !auth.Can(c.user, pg.Capability) {
				continue
			}
			e.Frame = "/plugin-ui/" + in.Plugin + "/" + strings.TrimPrefix(pg.File, "admin/")
			e.API = "/admin/api/plugins/" + in.Plugin + "/" + pg.ID
		}
		out = append(out, e)
	}
	return out
}

// menuWithPlugins adds the Plugins screen and the plugins' pages.
func (a *Admin) menuWithPlugins(m []menuItem, can func(string) bool) []menuItem {
	if a.plugins == nil {
		return m
	}
	find := func(id string) int {
		return slices.IndexFunc(m, func(it menuItem) bool { return it.ID == id })
	}
	// The Plugins screen goes before Users.
	if can(auth.CapActivatePlugins) {
		at := find("users")
		if at < 0 {
			at = find("settings")
		}
		if at < 0 {
			at = len(m)
		}
		m = slices.Insert(m, at, menuItem{ID: "plugins", Label: "Plugins", Icon: "admin-plugins", URL: "/admin/plugins/",
			Sub: []subItem{{ID: "plugins", Label: "Installed Plugins", URL: "/admin/plugins/"}}})
	}
	for _, ref := range a.plugins.AdminPages() {
		if !can(ref.Page.Capability) {
			continue
		}
		sub := subItem{ID: "plugin-" + ref.Plugin + "-" + ref.Page.ID, Label: ref.Page.Title, URL: pluginPageURL(ref.Plugin, ref.Page.ID)}
		parent := map[string]string{"settings": "settings", "posts": "posts", "comments": "comments", "tools": "tools"}[ref.Page.Menu]
		if parent == "" { // own top-level entry per plugin
			parent = "plugin-" + ref.Plugin
		}
		i := find(parent)
		if i < 0 {
			item := menuItem{ID: parent, Label: ref.PluginTitle, Icon: "admin-plugins", URL: sub.URL}
			if parent == "tools" {
				item.Label, item.Icon = "Tools", "admin-tools"
			}
			at := find("profile")
			if at < 0 {
				at = len(m)
			}
			m = slices.Insert(m, at, item)
			i = at
		}
		if parent == "comments" && len(m[i].Sub) == 0 {
			m[i].Sub = append(m[i].Sub, subItem{ID: "comments", Label: "All Comments", URL: "/admin/comments/"})
		}
		m[i].Sub = append(m[i].Sub, sub)
	}
	return m
}
