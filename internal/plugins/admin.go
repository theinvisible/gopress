package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
)

// The plugins' parts of the admin. The admin checks capabilities and
// CSRF; these functions find the declared pages and call the plugin.

// PageRef is an admin page of an active plugin.
type PageRef struct {
	Plugin      string
	PluginTitle string
	Page        AdminPage
}

// AdminPages returns the admin pages of the active plugins.
func (h *Host) AdminPages() []PageRef {
	var out []PageRef
	for _, p := range h.activePlugins() {
		for _, pg := range p.pkg.Manifest.Admin {
			out = append(out, PageRef{Plugin: p.pkg.Manifest.Name, PluginTitle: p.pkg.Manifest.Title, Page: pg})
		}
	}
	return out
}

// Page returns an admin page of an active plugin.
func (h *Host) Page(name, id string) (*AdminPage, bool) {
	p := h.get(name)
	if p == nil {
		return nil, false
	}
	for i, pg := range p.pkg.Manifest.Admin {
		if pg.ID == id {
			return &p.pkg.Manifest.Admin[i], true
		}
	}
	return nil, false
}

func userData(u *db.User) UserData {
	return UserData{ID: u.ID, Login: u.Login, DisplayName: u.DisplayName, Role: u.Role}
}

// Table returns the rows of a table page.
func (h *Host) Table(ctx context.Context, name, page string, u *db.User, query map[string]string) (*AdminPageOut, error) {
	var out AdminPageOut
	if err := h.call(ctx, name, ExportAdminPage, callRequest, AdminIn{Page: page, User: userData(u), Query: query}, &out); err != nil {
		return nil, err
	}
	if len(out.Rows) > 500 {
		out.Rows = out.Rows[:500]
	}
	return &out, nil
}

// Submit runs a bulk or row action of a table page.
func (h *Host) Submit(ctx context.Context, name, page string, u *db.User, action string, ids []string) (string, error) {
	var out AdminDoOut
	err := h.call(ctx, name, ExportAdminDo, callRequest, AdminIn{Page: page, User: userData(u), Action: action, IDs: ids}, &out)
	return truncate(out.Notice, 500), err
}

// API runs a bridge call of an iframe page.
func (h *Host) API(ctx context.Context, name, page string, u *db.User, method string, params json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := h.call(ctx, name, ExportAdminAPI, callRequest, AdminIn{Page: page, User: userData(u), Method: method, Params: params}, &out)
	if len(out) == 0 {
		out = json.RawMessage("null")
	}
	return out, err
}

// DashboardBox is a dashboard box of an active plugin.
type DashboardBox struct {
	Plugin string
	Box    Dashboard
}

// DashboardBoxes returns the dashboard boxes of the active plugins.
func (h *Host) DashboardBoxes() []DashboardBox {
	var out []DashboardBox
	for _, p := range h.activePlugins() {
		for _, d := range p.pkg.Manifest.Dashboard {
			out = append(out, DashboardBox{Plugin: p.pkg.Manifest.Name, Box: d})
		}
	}
	return out
}

// RenderDashboard renders a dashboard box (filtered HTML).
func (h *Host) RenderDashboard(ctx context.Context, name, id string, u *db.User) (string, error) {
	var out RenderOut
	if err := h.call(ctx, name, ExportDashboard, callHook, DashboardIn{ID: id, User: userData(u)}, &out); err != nil {
		return "", err
	}
	return render.SanitizePluginHTML(out.HTML), nil
}

// WidgetType is a widget type of an active plugin.
type WidgetType struct {
	Plugin string
	Widget Widget
}

// WidgetTypes returns the widget types of the active plugins.
func (h *Host) WidgetTypes() []WidgetType {
	var out []WidgetType
	for _, p := range h.activePlugins() {
		for _, w := range p.pkg.Manifest.Widgets {
			out = append(out, WidgetType{Plugin: p.pkg.Manifest.Name, Widget: w})
		}
	}
	return out
}

// InsertRef is an editor button of an active plugin.
type InsertRef struct {
	Plugin string
	Insert Insert
}

// EditorInserts returns the editor buttons of the active plugins.
func (h *Host) EditorInserts() []InsertRef {
	var out []InsertRef
	for _, p := range h.activePlugins() {
		for _, in := range p.pkg.Manifest.Editor {
			out = append(out, InsertRef{Plugin: p.pkg.Manifest.Name, Insert: in})
		}
	}
	return out
}

// UIFile returns a file below admin/ of an active plugin that has iframe
// pages.
func (h *Host) UIFile(name, file string) ([]byte, bool) {
	p := h.get(name)
	if p == nil || !strings.HasPrefix(file, "admin/") || !cleanPath(file) {
		return nil, false
	}
	hasFrame := false
	for _, pg := range p.pkg.Manifest.Admin {
		hasFrame = hasFrame || pg.Kind == "iframe"
	}
	if !hasFrame {
		return nil, false
	}
	return p.pkg.File(file)
}

// Migrate runs the plugin's WordPress import and records that it ran.
func (h *Host) Migrate(ctx context.Context, name string) (string, error) {
	p := h.get(name)
	if p == nil {
		return "", ErrUnavailable
	}
	var out AdminDoOut
	if err := p.call(ctx, ExportMigrate, callMigrate, struct{}{}, &out); err != nil {
		return "", err
	}
	states, err := db.Plugins(ctx, h.opts.DB.R)
	if err != nil {
		return "", err
	}
	if st := states[name]; st != nil {
		st.Migrated = true
		if err := db.SavePlugin(ctx, h.opts.DB.W, st); err != nil {
			return "", err
		}
	}
	h.opts.Site.Invalidate()
	return truncate(out.Notice, 500), nil
}

// HasImport reports whether a plugin declares import data and whether
// there is any.
func (h *Host) HasImport(ctx context.Context, name string) bool {
	p := h.get(name)
	if p == nil {
		return false
	}
	for _, d := range p.perms.Data {
		if kind, ok := strings.CutPrefix(d, DataImportPrefix); ok {
			if m, err := db.StashEntries(ctx, h.opts.DB.R, "wordpress", kind); err == nil && len(m) > 0 {
				return true
			}
		}
	}
	return false
}

// ErrorMessage returns a message for an admin user.
func ErrorMessage(err error) string {
	var pe *PluginError
	switch {
	case errors.As(err, &pe):
		return pe.Msg
	case errors.Is(err, ErrUnavailable):
		return "The plugin is not available (inactive, paused after errors or failed to load)."
	}
	return "The plugin failed: " + truncate(err.Error(), 200)
}
