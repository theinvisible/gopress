package gopress

import "encoding/json"

// The JSON protocol between GoPress and plugins (mirrors
// internal/plugins/abi.go in GoPress).

// ShortcodeRequest is a shortcode to render.
type ShortcodeRequest struct {
	Tag     string            `json:"tag"`
	Attrs   map[string]string `json:"attrs"`
	Content string            `json:"content,omitempty"`
	PostID  int64             `json:"post_id,omitempty"`
	Feed    bool              `json:"feed,omitempty"`
}

// Attr returns an attribute or def.
func (r ShortcodeRequest) Attr(name, def string) string {
	if v, ok := r.Attrs[name]; ok {
		return v
	}
	return def
}

// Render is rendered HTML and the frontend assets (paths of the package,
// declared in plugin.json) it needs. GoPress filters the HTML: no
// scripts, event handlers or iframes; behavior belongs in asset files.
type Render struct {
	HTML   string   `json:"html"`
	Assets []string `json:"assets,omitempty"`
}

// WidgetRequest is a widget to render.
type WidgetRequest struct {
	Type      string         `json:"type"`
	Settings  map[string]any `json:"settings"`
	TitleHTML string         `json:"title_html,omitempty"`
}

// FilterRequest is a value passed through a filter hook (the_content,
// head, footer).
type FilterRequest struct {
	Hook   string `json:"hook"`
	Value  string `json:"value"`
	PostID int64  `json:"post_id,omitempty"`
}

// FilterResult is the filtered value.
type FilterResult struct {
	Value  string   `json:"value"`
	Assets []string `json:"assets,omitempty"`
}

// Comment describes a comment.
type Comment struct {
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

// CommentCheck is the input of the comment_pre_save hook.
type CommentCheck struct {
	Comment      Comment           `json:"comment"`
	PostTitle    string            `json:"post_title"`
	PostURL      string            `json:"post_url"`
	PostModified string            `json:"post_modified,omitempty"` // GMT, RFC 3339
	PostTerms    []string          `json:"post_terms,omitempty"`    // category and tag names
	UserRole     string            `json:"user_role,omitempty"`     // of a logged-in commenter
	Referrer     string            `json:"referrer,omitempty"`
	Language     string            `json:"language,omitempty"` // the Accept-Language header
	Headers      map[string]string `json:"headers,omitempty"`  // Accept, Accept-Charset, Accept-Encoding
}

// CommentStatusChange is the data of the comment_status_changed action
// (decode it with json.Unmarshal in a HandleAction handler).
type CommentStatusChange struct {
	Comment  Comment `json:"comment"`  // after the change
	Status   string  `json:"status"`   // approve, unapprove, spam, unspam, trash, untrash
	Previous string  `json:"previous"` // approved, pending, spam or trash
	By       *User   `json:"by,omitempty"`
}

// Comment verdicts.
const (
	Keep    = ""
	Approve = "approve"
	Hold    = "hold"
	Spam    = "spam"
	Discard = "discard"
)

// Request is an HTTP request to one of the plugin's routes.
type Request struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   map[string][]string `json:"query,omitempty"`
	Headers map[string]string   `json:"headers,omitempty"` // lower-case names
	Body    string              `json:"body,omitempty"`
	IP      string              `json:"ip"`
}

// Response answers a route request.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// User is the administrator or editor calling an admin page.
type User struct {
	ID          int64  `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// AdminRequest is a call from an admin page.
type AdminRequest struct {
	Page   string            `json:"page"`
	User   User              `json:"user"`
	Query  map[string]string `json:"query,omitempty"`
	Action string            `json:"action,omitempty"`
	IDs    []string          `json:"ids,omitempty"`
	Method string            `json:"method,omitempty"`
	Params json.RawMessage   `json:"params,omitempty"`
}

// Row is a row of a table page; cells are plain text by column name.
type Row struct {
	ID    string            `json:"id"`
	Cells map[string]string `json:"cells"`
}

// Option is a value and its label.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Table is the content of a table page.
type Table struct {
	Rows    []Row    `json:"rows"`
	Total   int      `json:"total"`
	PerPage int      `json:"per_page,omitempty"` // rows per page; the admin pages with Query["paged"]
	Actions []Option `json:"actions,omitempty"`
	Notice  string   `json:"notice,omitempty"`
}

// DashboardRequest asks for a dashboard box.
type DashboardRequest struct {
	ID   string `json:"id"`
	User User   `json:"user"`
}

type actionIn struct {
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type commentVerdict struct {
	Verdict string `json:"verdict"`
}

type notice struct {
	Notice string `json:"notice,omitempty"`
}
