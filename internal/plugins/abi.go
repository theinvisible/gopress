package plugins

import "encoding/json"

// The JSON protocol between host and plugins. Exports receive one JSON
// document and return one; host functions are called through a single
// import, extism:host/user.gopress, with {"op": …, "args": …} and answer
// {"result": …} or {"error": "…"}. pdk/go mirrors these types.

// Exports.
const (
	ExportShortcode = "render_shortcode"
	ExportWidget    = "render_widget"
	ExportFilter    = "filter"
	ExportComment   = "comment_pre_save"
	ExportAction    = "action"
	ExportRoute     = "http_route"
	ExportAdminPage = "admin_page"
	ExportAdminDo   = "admin_submit"
	ExportAdminAPI  = "admin_api"
	ExportMigrate   = "migrate_wordpress"
	ExportDashboard = "dashboard_widget"
)

// ShortcodeIn is the input of render_shortcode.
type ShortcodeIn struct {
	Tag     string            `json:"tag"`
	Attrs   map[string]string `json:"attrs"`
	Content string            `json:"content,omitempty"`
	PostID  int64             `json:"post_id,omitempty"`
	Feed    bool              `json:"feed,omitempty"`
}

// RenderOut is the output of render_shortcode, render_widget and
// dashboard_widget: HTML (filtered by the host) and the frontend assets it
// needs.
type RenderOut struct {
	HTML   string   `json:"html"`
	Assets []string `json:"assets,omitempty"`
}

// WidgetIn is the input of render_widget.
type WidgetIn struct {
	Type      string         `json:"type"`
	Settings  map[string]any `json:"settings"`
	TitleHTML string         `json:"title_html,omitempty"` // the widget title in the theme's markup
}

// FilterIn is the input of filter (the_content, head, footer).
type FilterIn struct {
	Hook   string `json:"hook"`
	Value  string `json:"value"`
	PostID int64  `json:"post_id,omitempty"`
}

// FilterOut is the output of filter.
type FilterOut struct {
	Value  string   `json:"value"`
	Assets []string `json:"assets,omitempty"`
}

// CommentData describes a comment for hooks and host functions.
type CommentData struct {
	ID       int64  `json:"id,omitempty"`
	PostID   int64  `json:"post_id"`
	ParentID int64  `json:"parent_id,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
	Author   string `json:"author"`
	Email    string `json:"email"`
	URL      string `json:"url,omitempty"`
	IP       string `json:"ip"`
	Agent    string `json:"agent,omitempty"`
	Content  string `json:"content"`
	Type     string `json:"type,omitempty"`
	Status   string `json:"status"`
	Date     string `json:"date,omitempty"`
}

// CommentIn is the input of comment_pre_save.
type CommentIn struct {
	Comment      CommentData       `json:"comment"`
	PostTitle    string            `json:"post_title"`
	PostURL      string            `json:"post_url"`
	PostModified string            `json:"post_modified,omitempty"` // GMT, RFC 3339
	PostTerms    []string          `json:"post_terms,omitempty"`    // category and tag names
	UserRole     string            `json:"user_role,omitempty"`     // of a logged-in commenter
	Referrer     string            `json:"referrer,omitempty"`
	Language     string            `json:"language,omitempty"` // the Accept-Language header
	Headers      map[string]string `json:"headers,omitempty"`  // see commentHeaders
}

// commentHeaders are the request headers comment_pre_save receives
// (spam filters use them); never cookies or credentials.
var commentHeaders = []string{"Accept", "Accept-Charset", "Accept-Encoding"}

// CommentStatusData is the data of the comment_status_changed action.
type CommentStatusData struct {
	Comment  CommentData `json:"comment"`  // after the change
	Status   string      `json:"status"`   // approve, unapprove, spam, unspam, trash, untrash
	Previous string      `json:"previous"` // the status before
	By       *UserData   `json:"by,omitempty"`
}

// CommentOut is the output of comment_pre_save: "" (keep), "approve",
// "hold", "spam" or "discard".
type CommentOut struct {
	Verdict string `json:"verdict"`
}

// ActionIn is the input of action.
type ActionIn struct {
	Action string          `json:"action"` // activate, deactivate, comment_status_changed
	Data   json.RawMessage `json:"data,omitempty"`
}

// RouteIn is the input of http_route.
type RouteIn struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"` // below /plugins/<name>
	Query   map[string][]string `json:"query,omitempty"`
	Headers map[string]string   `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
	IP      string              `json:"ip"`
}

// RouteOut is the output of http_route.
type RouteOut struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// UserData is the acting user of admin calls.
type UserData struct {
	ID          int64  `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// AdminIn is the input of admin_page, admin_submit and admin_api.
type AdminIn struct {
	Page   string            `json:"page"`
	User   UserData          `json:"user"`
	Query  map[string]string `json:"query,omitempty"`  // admin_page: paging, search
	Action string            `json:"action,omitempty"` // admin_submit; "saved" after a settings page was saved
	IDs    []string          `json:"ids,omitempty"`    // admin_submit
	Method string            `json:"method,omitempty"` // admin_api
	Params json.RawMessage   `json:"params,omitempty"` // admin_api
}

// TableRow is a row of a table page.
type TableRow struct {
	ID    string            `json:"id"`
	Cells map[string]string `json:"cells"` // plain text by column name
}

// AdminPageOut is the output of admin_page (table pages).
type AdminPageOut struct {
	Rows    []TableRow `json:"rows"`
	Total   int        `json:"total"`
	PerPage int        `json:"per_page,omitempty"` // rows per page (paging); default: the rows of this page
	Actions []Option   `json:"actions,omitempty"`  // bulk actions (value, label)
	Notice  string     `json:"notice,omitempty"`
}

// AdminDoOut is the output of admin_submit and migrate_wordpress.
type AdminDoOut struct {
	Notice string `json:"notice,omitempty"`
}

// DashboardIn is the input of dashboard_widget.
type DashboardIn struct {
	ID   string   `json:"id"`
	User UserData `json:"user"`
}
