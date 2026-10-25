// Package plugins is the WASM plugin host: packages (plugin.json +
// plugin.wasm + assets), their validation and storage, the Extism runtime
// with limits, the host functions and the hooks into the site and admin.
//
// Security model: plugin code runs in a WebAssembly sandbox without file
// system, environment or network access; everything it can do goes
// through host functions that check the permissions an administrator
// approved. What a plugin delivers to browsers (HTML is filtered, scripts
// only from declared asset files, admin UIs only in a sandboxed iframe) is
// declared in its manifest and shown before activation.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// APIVersion is the host API plugins are built against.
const APIVersion = 1

// Manifest is plugin.json.
type Manifest struct {
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Author      string      `json:"author,omitempty"`
	URL         string      `json:"url,omitempty"`
	API         int         `json:"api"`
	Permissions Permissions `json:"permissions"`
	Shortcodes  []Shortcode `json:"shortcodes,omitempty"`
	Widgets     []Widget    `json:"widgets,omitempty"`
	Admin       []AdminPage `json:"admin,omitempty"`
	Editor      []Insert    `json:"editor,omitempty"`
	Dashboard   []Dashboard `json:"dashboard,omitempty"`
}

// Permissions are what a plugin may do beyond rendering its shortcodes
// and widgets. An administrator approves them before activation.
type Permissions struct {
	// HTTP lists host names the plugin may send HTTPS requests to.
	HTTP []string `json:"http,omitempty"`
	// Routes are paths below /plugins/<name>/ the plugin answers ("/ipn",
	// or "/api/*" for a prefix).
	Routes []string `json:"routes,omitempty"`
	// LegacyRoutes map old URLs (query parameters on any path, as plugins
	// of the source site used them) to one of the routes.
	LegacyRoutes []LegacyRoute `json:"legacy_routes,omitempty"`
	// Hooks the plugin is called for.
	Hooks []string `json:"hooks,omitempty"`
	// Data the plugin may read or change.
	Data []string `json:"data,omitempty"`
	// FrontendAssets are CSS and JavaScript files of the package the
	// public site includes where the plugin's output needs them.
	FrontendAssets []string `json:"frontend_assets,omitempty"`
}

// LegacyRoute maps requests carrying all Query parameters to Route.
type LegacyRoute struct {
	Query map[string]string `json:"query"`
	Route string            `json:"route"`
}

// Shortcode is a shortcode the plugin renders.
type Shortcode struct {
	Name   string   `json:"name"`
	Assets []string `json:"assets,omitempty"`
}

// Widget is a widget type the plugin renders.
type Widget struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Fields      []Field  `json:"fields,omitempty"`
	Assets      []string `json:"assets,omitempty"`
}

// AdminPage is a page in the admin menu.
type AdminPage struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Menu       string  `json:"menu"`       // "plugin" (own top level entry), "settings", "tools", "posts", "comments", "none"
	Capability string  `json:"capability"` // see adminCapabilities
	Kind       string  `json:"kind"`       // "settings", "table", "iframe"
	Fields     []Field `json:"fields,omitempty"`
	Columns    []Field `json:"columns,omitempty"` // table: columns (name, label)
	File       string  `json:"file,omitempty"`    // iframe: HTML file below admin/
}

// Insert is an editor button that inserts one of the plugin's shortcodes.
type Insert struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Icon      string  `json:"icon,omitempty"` // dashicon name
	Shortcode string  `json:"shortcode"`
	Fields    []Field `json:"fields,omitempty"` // dialog form; the values become attributes
	Page      string  `json:"page,omitempty"`   // or an iframe admin page as the dialog
}

// Dashboard is a dashboard box.
type Dashboard struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Capability string `json:"capability"`
}

// Field is a form field of settings pages, widget settings and dialogs, or
// a column of a table page.
type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type,omitempty"` // see fieldTypes; default text
	Help     string   `json:"help,omitempty"`
	Default  any      `json:"default,omitempty"`
	Options  []Option `json:"options,omitempty"` // select
	Required bool     `json:"required,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	MaxLen   int      `json:"max_length,omitempty"`
}

// Option is a choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Hooks plugins can register for.
const (
	HookContent       = "the_content"
	HookHead          = "head"
	HookFooter        = "footer"
	HookCommentSave   = "comment_pre_save"
	HookCommentStatus = "comment_status_changed"
)

var knownHooks = []string{HookContent, HookHead, HookFooter, HookCommentSave, HookCommentStatus}

// Data permissions.
const (
	DataPostsRead      = "posts:read"
	DataMediaRead      = "media:read"
	DataCommentsRead   = "comments:read"
	DataCommentsModify = "comments:moderate"
	DataImportPrefix   = "import:"        // import:<kind> of the import stash, e.g. import:post:fslb_gallery
	DataImportOption   = "import:option:" // import:option:<name>: one imported option
	DataMailAdmins     = "mail:admins"    // send plain-text mail to the administrators
)

var fieldTypes = []string{"text", "textarea", "number", "checkbox", "select", "url", "email", "password", "color", "media", "media_list"}

// adminCapabilities may guard plugin pages and dashboard boxes.
var adminCapabilities = []string{"manage_options", "edit_posts", "edit_others_posts", "moderate_comments", "upload_files"}

// adminMenus: "none" is a page only editor dialogs open.
var adminMenus = []string{"plugin", "settings", "tools", "posts", "comments", "none"}

// coreShortcodes cannot be taken over by plugins.
var coreShortcodes = []string{"gallery", "caption", "wp_caption", "embed", "audio", "video", "playlist"}

// coreQueryVars are query parameters the site itself routes on; legacy
// routes must not use them.
var coreQueryVars = []string{"p", "page_id", "attachment_id", "s", "cat", "tag", "category_name", "author", "author_name",
	"m", "year", "monthnum", "day", "w", "paged", "page", "feed", "preview", "preview_id", "name", "pagename", "post_type",
	"comments_popup", "replytocom", "cpage", "orderby", "order", "sitemap", "customize_preview"}

var (
	reName    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	reVersion = regexp.MustCompile(`^(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})(-[0-9A-Za-z.-]{1,30})?$`)
	reIdent   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	reShort   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	reRoute   = regexp.MustCompile(`^(/[a-z0-9][a-z0-9._-]{0,63}){1,4}(/\*)?$`)
	reQuery   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	reQVal    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	reHost    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	reKind    = regexp.MustCompile(`^[a-z]+(:[a-z0-9_-]{1,64})?$`)
	reOption  = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	reAsset   = regexp.MustCompile(`^assets/[A-Za-z0-9_-][A-Za-z0-9._/-]{0,127}\.(css|js)$`)
	reIcon    = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	reAdminHT = regexp.MustCompile(`^admin/[A-Za-z0-9_-][A-Za-z0-9._/-]{0,127}\.html$`)
)

// ParseManifest decodes and validates plugin.json. Unknown keys are
// errors (a typo must not silently drop a permission).
func ParseManifest(b []byte) (*Manifest, error) {
	if len(b) > 256<<10 {
		return nil, errors.New("plugin.json is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	if dec.More() {
		return nil, errors.New("plugin.json: trailing data")
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	return &m, nil
}

func text(s string, max int) bool {
	return s != "" && len(s) <= max && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func optText(s string, max int) bool { return s == "" || text(s, max) }

func (m *Manifest) validate() error {
	if !reName.MatchString(m.Name) {
		return errors.New("name must be 2–40 lower-case letters, digits or dashes")
	}
	if !reVersion.MatchString(m.Version) {
		return errors.New("version must be semantic (1.2.3)")
	}
	if m.API != APIVersion {
		return fmt.Errorf("api must be %d", APIVersion)
	}
	if !text(m.Title, 100) || !optText(m.Description, 1000) || !optText(m.Author, 100) || !optText(m.URL, 300) {
		return errors.New("title, description, author or url missing or too long")
	}
	if m.URL != "" && !strings.HasPrefix(m.URL, "https://") {
		return errors.New("url must be https")
	}
	if err := m.Permissions.validate(); err != nil {
		return err
	}
	assets := m.Permissions.FrontendAssets
	seen := map[string]bool{}
	ident := func(kind, id string) error {
		if seen[kind+id] {
			return fmt.Errorf("duplicate %s %q", kind, id)
		}
		seen[kind+id] = true
		return nil
	}
	checkAssets := func(list []string) error {
		for _, a := range list {
			if !slices.Contains(assets, a) {
				return fmt.Errorf("asset %q is not in permissions.frontend_assets", a)
			}
		}
		return nil
	}
	for _, s := range m.Shortcodes {
		if !reShort.MatchString(s.Name) || slices.Contains(coreShortcodes, s.Name) {
			return fmt.Errorf("invalid shortcode name %q", s.Name)
		}
		if err := ident("shortcode", s.Name); err != nil {
			return err
		}
		if err := checkAssets(s.Assets); err != nil {
			return err
		}
	}
	for _, w := range m.Widgets {
		if !reIdent.MatchString(w.Type) || !text(w.Title, 100) || !optText(w.Description, 300) {
			return fmt.Errorf("invalid widget %q", w.Type)
		}
		if err := ident("widget", w.Type); err != nil {
			return err
		}
		if err := checkFields(w.Fields); err != nil {
			return fmt.Errorf("widget %s: %w", w.Type, err)
		}
		if err := checkAssets(w.Assets); err != nil {
			return err
		}
	}
	for _, p := range m.Admin {
		if !reIdent.MatchString(p.ID) || !text(p.Title, 100) || !slices.Contains(adminMenus, p.Menu) ||
			!slices.Contains(adminCapabilities, p.Capability) {
			return fmt.Errorf("invalid admin page %q", p.ID)
		}
		if err := ident("page", p.ID); err != nil {
			return err
		}
		switch p.Kind {
		case "settings":
			if len(p.Fields) == 0 {
				return fmt.Errorf("admin page %s: settings need fields", p.ID)
			}
			if err := checkFields(p.Fields); err != nil {
				return fmt.Errorf("admin page %s: %w", p.ID, err)
			}
		case "table":
			if len(p.Columns) == 0 || len(p.Columns) > 12 {
				return fmt.Errorf("admin page %s: a table needs 1–12 columns", p.ID)
			}
			if err := checkFields(p.Columns); err != nil {
				return fmt.Errorf("admin page %s: %w", p.ID, err)
			}
		case "iframe":
			if !reAdminHT.MatchString(p.File) || strings.Contains(p.File, "..") {
				return fmt.Errorf("admin page %s: file must be an .html file below admin/", p.ID)
			}
		default:
			return fmt.Errorf("admin page %s: kind must be settings, table or iframe", p.ID)
		}
		if p.Menu == "none" && p.Kind != "iframe" {
			return fmt.Errorf("admin page %s: only iframe pages (editor dialogs) can be outside the menu", p.ID)
		}
	}
	for _, in := range m.Editor {
		if !reIdent.MatchString(in.ID) || !text(in.Label, 60) || (in.Icon != "" && !reIcon.MatchString(in.Icon)) {
			return fmt.Errorf("invalid editor insert %q", in.ID)
		}
		if err := ident("insert", in.ID); err != nil {
			return err
		}
		if !seen["shortcode"+in.Shortcode] {
			return fmt.Errorf("editor insert %s: shortcode %q is not one of the plugin's", in.ID, in.Shortcode)
		}
		if in.Page != "" {
			if !slices.ContainsFunc(m.Admin, func(p AdminPage) bool { return p.ID == in.Page && p.Kind == "iframe" }) {
				return fmt.Errorf("editor insert %s: page %q is not an iframe page", in.ID, in.Page)
			}
		} else if err := checkFields(in.Fields); err != nil {
			return fmt.Errorf("editor insert %s: %w", in.ID, err)
		}
	}
	for _, d := range m.Dashboard {
		if !reIdent.MatchString(d.ID) || !text(d.Title, 100) || !slices.Contains(adminCapabilities, d.Capability) {
			return fmt.Errorf("invalid dashboard box %q", d.ID)
		}
		if err := ident("dashboard", d.ID); err != nil {
			return err
		}
	}
	if len(seen) > 100 {
		return errors.New("too many shortcodes, widgets, pages and inserts")
	}
	return nil
}

func (p *Permissions) validate() error {
	if len(p.HTTP) > 20 || len(p.Routes) > 20 || len(p.LegacyRoutes) > 10 || len(p.Data) > 20 || len(p.FrontendAssets) > 30 {
		return errors.New("too many permissions")
	}
	for _, h := range p.HTTP {
		if !reHost.MatchString(h) || net.ParseIP(h) != nil || h == "localhost" || strings.HasSuffix(h, ".localhost") ||
			strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
			return fmt.Errorf("invalid http host %q (a public host name, no IP addresses or ports)", h)
		}
	}
	for _, r := range p.Routes {
		if !reRoute.MatchString(r) || strings.Contains(r, "..") {
			return fmt.Errorf("invalid route %q", r)
		}
	}
	for _, lr := range p.LegacyRoutes {
		if len(lr.Query) == 0 || len(lr.Query) > 4 || !slices.Contains(p.Routes, lr.Route) {
			return fmt.Errorf("legacy route to %q: 1–4 query parameters and one of the routes", lr.Route)
		}
		for k, v := range lr.Query {
			if !reQuery.MatchString(k) || !reQVal.MatchString(v) || slices.Contains(coreQueryVars, k) {
				return fmt.Errorf("legacy route: query parameter %q is not allowed", k)
			}
		}
	}
	for _, h := range p.Hooks {
		if !slices.Contains(knownHooks, h) {
			return fmt.Errorf("unknown hook %q", h)
		}
	}
	for _, d := range p.Data {
		switch {
		case d == DataPostsRead, d == DataMediaRead, d == DataCommentsRead, d == DataCommentsModify, d == DataMailAdmins:
		case strings.HasPrefix(d, DataImportOption):
			// Imported options are granted one by one: the stash holds the
			// options of every plugin of the source site (API keys too).
			if !reOption.MatchString(strings.TrimPrefix(d, DataImportOption)) {
				return fmt.Errorf("invalid data permission %q", d)
			}
		case d == DataImportPrefix+"option":
			return fmt.Errorf("data permission %q: name the options (import:option:<name>)", d)
		case strings.HasPrefix(d, DataImportPrefix) && reKind.MatchString(strings.TrimPrefix(d, DataImportPrefix)):
		default:
			return fmt.Errorf("unknown data permission %q", d)
		}
	}
	for _, a := range p.FrontendAssets {
		if !reAsset.MatchString(a) || strings.Contains(a, "..") || strings.Contains(a, "//") {
			return fmt.Errorf("invalid frontend asset %q (a .css or .js file below assets/)", a)
		}
	}
	for _, list := range [][]string{p.HTTP, p.Routes, p.Hooks, p.Data, p.FrontendAssets} {
		if hasDup(list) {
			return errors.New("duplicate permission")
		}
	}
	return nil
}

func hasDup(list []string) bool {
	s := slices.Clone(list)
	slices.Sort(s)
	return len(slices.Compact(s)) != len(list)
}

func checkFields(fields []Field) error {
	if len(fields) > 50 {
		return errors.New("too many fields")
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if !reQuery.MatchString(f.Name) || seen[f.Name] || !text(f.Label, 100) || !optText(f.Help, 500) {
			return fmt.Errorf("invalid field %q", f.Name)
		}
		seen[f.Name] = true
		if f.Type != "" && !slices.Contains(fieldTypes, f.Type) {
			return fmt.Errorf("field %s: unknown type %q", f.Name, f.Type)
		}
		if f.Type == "select" && (len(f.Options) == 0 || len(f.Options) > 200) {
			return fmt.Errorf("field %s: a select needs 1–200 options", f.Name)
		}
		for _, o := range f.Options {
			if len(o.Value) > 200 || !text(o.Label, 200) {
				return fmt.Errorf("field %s: invalid option", f.Name)
			}
		}
		if f.MaxLen < 0 || f.MaxLen > 1<<20 {
			return fmt.Errorf("field %s: invalid max_length", f.Name)
		}
	}
	return nil
}

// Canonical returns the permissions in a stable JSON form (sorted lists),
// as stored when an administrator approves them.
func (p Permissions) Canonical() string {
	c := Permissions{HTTP: sorted(p.HTTP), Routes: sorted(p.Routes), Hooks: sorted(p.Hooks), Data: sorted(p.Data),
		FrontendAssets: sorted(p.FrontendAssets)}
	c.LegacyRoutes = append(c.LegacyRoutes, p.LegacyRoutes...)
	slices.SortFunc(c.LegacyRoutes, func(a, b LegacyRoute) int { return strings.Compare(lr2s(a), lr2s(b)) })
	b, _ := json.Marshal(c)
	return string(b)
}

func lr2s(lr LegacyRoute) string {
	keys := make([]string, 0, len(lr.Query))
	for k := range lr.Query {
		keys = append(keys, k+"="+lr.Query[k])
	}
	slices.Sort(keys)
	return strings.Join(keys, "&") + " -> " + lr.Route
}

func sorted(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

// ParsePermissions decodes an approved permission set.
func ParsePermissions(s string) (Permissions, error) {
	var p Permissions
	if s == "" {
		return p, nil
	}
	err := json.Unmarshal([]byte(s), &p)
	return p, err
}

// Covers reports whether approved includes everything p asks for, and
// lists what is new.
func (p Permissions) Covers(approved Permissions) (bool, []string) {
	var extra []string
	add := func(kind string, want, have []string) {
		for _, x := range want {
			if !slices.Contains(have, x) {
				extra = append(extra, kind+" "+x)
			}
		}
	}
	add("HTTP requests to", p.HTTP, approved.HTTP)
	add("route", p.Routes, approved.Routes)
	add("hook", p.Hooks, approved.Hooks)
	add("data", p.Data, approved.Data)
	add("frontend asset", p.FrontendAssets, approved.FrontendAssets)
	var have []string
	for _, lr := range approved.LegacyRoutes {
		have = append(have, lr2s(lr))
	}
	for _, lr := range p.LegacyRoutes {
		if !slices.Contains(have, lr2s(lr)) {
			extra = append(extra, "legacy URL "+lr2s(lr))
		}
	}
	return len(extra) == 0, extra
}

// Describe lists the permissions in words for the approval dialog.
func (p Permissions) Describe() []string {
	var out []string
	for _, h := range p.HTTP {
		out = append(out, "Send HTTPS requests to "+h)
	}
	for _, r := range p.Routes {
		out = append(out, "Answer requests to /plugins/…"+r)
	}
	for _, lr := range p.LegacyRoutes {
		out = append(out, "Answer old URLs with ?"+strings.Split(lr2s(lr), " ")[0])
	}
	for _, h := range p.Hooks {
		out = append(out, hookLabels[h])
	}
	for _, d := range p.Data {
		if name, ok := strings.CutPrefix(d, DataImportOption); ok {
			out = append(out, "Read the imported WordPress option "+name)
			continue
		}
		if strings.HasPrefix(d, DataImportPrefix) {
			out = append(out, "Read imported WordPress data ("+strings.TrimPrefix(d, DataImportPrefix)+")")
			continue
		}
		out = append(out, dataLabels[d])
	}
	for _, a := range p.FrontendAssets {
		kind := "stylesheet"
		if strings.HasSuffix(a, ".js") {
			kind = "JavaScript (runs in visitors' browsers)"
		}
		out = append(out, "Add "+kind+" "+a+" to public pages")
	}
	return out
}

var hookLabels = map[string]string{
	HookContent:       "Change the content of posts and pages",
	HookHead:          "Add tags to the page head",
	HookFooter:        "Add tags to the page footer",
	HookCommentSave:   "Check new comments before they are stored (e.g. spam filter)",
	HookCommentStatus: "Be told when comments are approved or marked as spam",
}

var dataLabels = map[string]string{
	DataPostsRead:      "Read published posts and pages",
	DataMediaRead:      "Read media details (URLs, sizes, captions)",
	DataCommentsRead:   "Read comments",
	DataCommentsModify: "Change the status of comments (approve, spam, trash)",
	DataMailAdmins:     "Send e-mail to the administrators",
}

// CompareVersions compares two semantic versions (-1, 0, 1); pre-releases
// sort before their release.
func CompareVersions(a, b string) int {
	pa, preA := splitVersion(a)
	pb, preB := splitVersion(b)
	for i := range 3 {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	return strings.Compare(preA, preB)
}

func splitVersion(v string) ([3]int, string) {
	var out [3]int
	main, pre, _ := strings.Cut(v, "-")
	for i, p := range strings.SplitN(main, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out, pre
}

// cleanPath reports whether p is a clean relative path inside a package.
func cleanPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.HasPrefix(p, "../") && p != ".."
}
