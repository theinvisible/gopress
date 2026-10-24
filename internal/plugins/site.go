package plugins

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/httpx"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/site"
)

// The plugins' parts of the public site (site.Extensions).

var _ site.Extensions = (*Host)(nil)

// Shortcodes returns the renderers of the active plugins' shortcodes.
func (h *Host) Shortcodes() map[string]render.ShortcodeFunc {
	out := map[string]render.ShortcodeFunc{}
	for _, p := range h.activePlugins() {
		for _, sc := range p.pkg.Manifest.Shortcodes {
			if _, taken := out[sc.Name]; taken {
				continue // the first plugin (by name) keeps a shortcode
			}
			out[sc.Name] = p.shortcode(sc)
		}
	}
	return out
}

func (p *plugin) shortcode(sc Shortcode) render.ShortcodeFunc {
	return func(env *render.Env, s *render.Shortcode) string {
		in := ShortcodeIn{Tag: s.Tag, Attrs: s.Attrs, Content: s.Content, Feed: env.IsFeed}
		if in.Attrs == nil {
			in.Attrs = map[string]string{}
		}
		for i, v := range s.Positional { // [tag value] becomes attribute "0"
			in.Attrs[strconv.Itoa(i)] = v
		}
		if env.Post != nil {
			in.PostID = env.Post.ID
		}
		var out RenderOut
		if err := p.call(envCtx(env), ExportShortcode, callRender, in, &out); err != nil {
			return ""
		}
		p.addAssets(env, sc.Assets, out.Assets)
		return render.SanitizePluginHTML(out.HTML)
	}
}

func envCtx(env *render.Env) context.Context {
	if env.Ctx != nil {
		return env.Ctx
	}
	return context.Background()
}

// addAssets records the declared assets plus those the output asks for,
// as far as they are approved frontend assets.
func (p *plugin) addAssets(env *render.Env, declared, asked []string) {
	for _, list := range [][]string{declared, asked} {
		for _, a := range list {
			if slices.Contains(p.perms.FrontendAssets, a) {
				env.AddAssets(p.pkg.Manifest.Name + "/" + a)
			}
		}
	}
}

// ContentFilter returns the the_content filter of the active plugins.
func (h *Host) ContentFilter() func(env *render.Env, content string) string {
	var list []*plugin
	for _, p := range h.activePlugins() {
		if slices.Contains(p.perms.Hooks, HookContent) {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return nil
	}
	return func(env *render.Env, content string) string {
		for _, p := range list {
			in := FilterIn{Hook: HookContent, Value: content}
			if env.Post != nil {
				in.PostID = env.Post.ID
			}
			var out FilterOut
			if err := p.call(envCtx(env), ExportFilter, callRender, in, &out); err != nil {
				continue // keep the content as it is
			}
			content = render.SanitizePluginHTML(out.Value)
			p.addAssets(env, nil, out.Assets)
		}
		return content
	}
}

// RenderWidget renders a widget of an active plugin.
func (h *Host) RenderWidget(env *render.Env, typ string, settings map[string]any, titleHTML string) (string, bool) {
	for _, p := range h.activePlugins() {
		for _, w := range p.pkg.Manifest.Widgets {
			if w.Type != typ {
				continue
			}
			var out RenderOut
			if err := p.call(envCtx(env), ExportWidget, callRender, WidgetIn{Type: typ, Settings: settings, TitleHTML: titleHTML}, &out); err != nil {
				return "", true
			}
			p.addAssets(env, w.Assets, out.Assets)
			return render.SanitizePluginHTML(out.HTML), true
		}
	}
	return "", false
}

// HeadFooter returns the head or footer hook output: only <meta> and
// <link> tags (to the plugin's own assets) pass.
func (h *Host) HeadFooter(env *render.Env, hook string) string {
	var b strings.Builder
	for _, p := range h.activePlugins() {
		if !slices.Contains(p.perms.Hooks, hook) {
			continue
		}
		in := FilterIn{Hook: hook}
		if env.Post != nil {
			in.PostID = env.Post.ID
		}
		var out FilterOut
		if err := p.call(envCtx(env), ExportFilter, callRender, in, &out); err != nil {
			continue
		}
		b.WriteString(sanitizeHeadTags(out.Value))
		p.addAssets(env, nil, out.Assets)
	}
	return b.String()
}

// AssetTags returns <link> tags for the stylesheets and <script> tags for
// the scripts among assets ("<plugin>/<path>").
func (h *Host) AssetTags(assets []string) (head, footer string) {
	var hb, fb strings.Builder
	for _, a := range assets {
		name, file, ok := strings.Cut(a, "/")
		p := h.get(name)
		if !ok || p == nil || !slices.Contains(p.perms.FrontendAssets, file) {
			continue
		}
		u := assetURL(p.pkg, file)
		id := "plugin-" + name + "-" + strings.TrimSuffix(path.Base(file), path.Ext(file))
		if strings.HasSuffix(file, ".css") {
			fmt.Fprintf(&hb, "<link rel='stylesheet' id='%s-css' href='%s' media='all' />\n", html.EscapeString(id), html.EscapeString(u))
		} else {
			fmt.Fprintf(&fb, "<script src=\"%s\" id=\"%s-js\"></script>\n", html.EscapeString(u), html.EscapeString(id))
		}
	}
	return hb.String(), fb.String()
}

func assetURL(pkg *Package, file string) string {
	return "/plugins/" + pkg.Manifest.Name + "/" + file + "?v=" + pkg.SHA256[:12]
}

var assetTypes = map[string]string{".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".svg": "image/svg+xml", ".woff2": "font/woff2", ".woff": "font/woff", ".json": "application/json"}

// ServeHTTP answers asset files, routes and legacy URLs of active
// plugins.
func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	if rest, ok := strings.CutPrefix(r.URL.Path, "/plugins/"); ok {
		name, sub, _ := strings.Cut(rest, "/")
		p := h.get(name)
		if p == nil {
			return false
		}
		sub = "/" + sub
		if strings.HasPrefix(sub, "/assets/") {
			p.serveAsset(w, r, strings.TrimPrefix(sub, "/"))
			return true
		}
		if p.hasRoute(sub) {
			p.serveRoute(w, r, sub)
			return true
		}
		return false
	}
	if r.URL.RawQuery == "" {
		return false
	}
	q := r.URL.Query()
	for _, p := range h.activePlugins() {
		for _, lr := range p.perms.LegacyRoutes {
			if matchQuery(q, lr.Query) {
				p.serveRoute(w, r, lr.Route)
				return true
			}
		}
	}
	return false
}

func matchQuery(q map[string][]string, want map[string]string) bool {
	for k, v := range want {
		if got := q[k]; len(got) == 0 || got[0] != v {
			return false
		}
	}
	return true
}

func (p *plugin) hasRoute(sub string) bool {
	for _, r := range p.perms.Routes {
		if prefix, ok := strings.CutSuffix(r, "/*"); ok {
			if strings.HasPrefix(sub, prefix+"/") && !strings.Contains(sub, "..") {
				return true
			}
		} else if sub == r {
			return true
		}
	}
	return false
}

func (p *plugin) serveAsset(w http.ResponseWriter, r *http.Request, file string) {
	ext := strings.ToLower(path.Ext(file))
	ctype := assetTypes[ext]
	data, ok := p.pkg.File(file)
	// Stylesheets and scripts only when approved; images and fonts of
	// assets/ always (they cannot run).
	if !ok || ctype == "" || ((ext == ".css" || ext == ".js") && !slices.Contains(p.perms.FrontendAssets, file)) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", ctype)
	hd.Set("X-Content-Type-Options", "nosniff")
	if ext == ".svg" {
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	etag := `"` + p.pkg.SHA256[:16] + hex.EncodeToString([]byte(path.Base(file)))[:8] + `"`
	hd.Set("ETag", etag)
	if r.URL.Query().Get("v") == p.pkg.SHA256[:12] {
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hd.Set("Cache-Control", "public, max-age=300")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(data)
}

// Route limits.
const (
	routeBodyMax  = 1 << 20
	routePerMin   = 60 // requests per client network and plugin
	routeRespMax  = maxOutputBytes
	routeHdrValue = 4096
)

// routeRequestHeaders are passed to plugins (cookies and proxy headers
// never are; the client address comes separately).
var routeRequestHeaders = []string{"Content-Type", "User-Agent", "Accept", "Accept-Language", "Referer", "Origin"}

func (p *plugin) serveRoute(w http.ResponseWriter, r *http.Request, sub string) {
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	// Route output is data, not a page of the site: it can never run
	// scripts with the site's origin.
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := r.RemoteAddr
	if p.h.opts.ClientIP != nil {
		ip = p.h.opts.ClientIP(r)
	}
	if !p.h.routeLimit.allow(p.pkg.Manifest.Name+"|"+httpx.NetKey(ip), p.h.now()) {
		hd.Set("Retry-After", "60")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, routeBodyMax))
	if err != nil {
		http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
		return
	}
	in := RouteIn{Method: r.Method, Path: sub, Query: r.URL.Query(), Headers: map[string]string{}, Body: string(body), IP: ip}
	for _, k := range routeRequestHeaders {
		if v := r.Header.Get(k); v != "" && len(v) <= routeHdrValue {
			in.Headers[strings.ToLower(k)] = v
		}
	}
	var out RouteOut
	if err := p.call(r.Context(), ExportRoute, callRequest, in, &out); err != nil {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	if out.Status < 200 || out.Status > 599 || (out.Status >= 300 && out.Status < 400 && out.Status != 304) {
		out.Status = http.StatusOK
	}
	ctype := "text/plain; charset=utf-8"
	for k, v := range out.Headers {
		switch strings.ToLower(k) {
		case "content-type":
			if _, _, err := mime.ParseMediaType(v); err == nil && !strings.ContainsAny(v, "\r\n") && len(v) <= 200 {
				ctype = v
			}
		case "content-disposition":
			if !strings.ContainsAny(v, "\r\n") && len(v) <= 300 {
				hd.Set("Content-Disposition", v)
			}
		}
	}
	hd.Set("Content-Type", ctype)
	w.WriteHeader(out.Status)
	if r.Method != http.MethodHead {
		io.WriteString(w, out.Body)
	}
}

// sanitizeHeadTags keeps <meta name|property content> and <link> tags
// with rel stylesheet/preload/alternate to the plugin's own paths.
func sanitizeHeadTags(s string) string {
	var b strings.Builder
	for _, tag := range reTag.FindAllString(s, 50) {
		attrs := map[string]string{}
		for _, m := range reAttr.FindAllStringSubmatch(tag, -1) {
			v := m[2]
			if v == "" {
				v = m[3]
			}
			attrs[strings.ToLower(m[1])] = html.UnescapeString(v)
		}
		switch {
		case strings.HasPrefix(strings.ToLower(tag), "<meta"):
			name, prop := attrs["name"], attrs["property"]
			if (name == "" && prop == "") || attrs["http-equiv"] != "" || len(attrs["content"]) > 1000 {
				continue
			}
			key, val := "name", name
			if name == "" {
				key, val = "property", prop
			}
			fmt.Fprintf(&b, "<meta %s=\"%s\" content=\"%s\" />\n", key, html.EscapeString(val), html.EscapeString(attrs["content"]))
		case strings.HasPrefix(strings.ToLower(tag), "<link"):
			href, rel := attrs["href"], attrs["rel"]
			if !strings.HasPrefix(href, "/plugins/") || strings.Contains(href, "..") ||
				!slices.Contains([]string{"stylesheet", "preload", "alternate", "prefetch"}, rel) {
				continue
			}
			fmt.Fprintf(&b, "<link rel=\"%s\" href=\"%s\" />\n", rel, html.EscapeString(href))
		}
	}
	return b.String()
}

// Comment hooks.

// CommentFilter returns the comment_pre_save filter for the site.
func (h *Host) CommentFilter() site.CommentFilter {
	return func(r *http.Request, c *db.Comment, post *db.Post) string {
		verdict := ""
		rank := map[string]int{"": 0, "approve": 1, "hold": 2, "spam": 3, "discard": 4}
		links, _ := h.opts.Site.Links(r.Context())
		in := CommentIn{Comment: commentData(c), PostTitle: html.UnescapeString(post.Title), Referrer: r.Referer(),
			Language: r.Header.Get("Accept-Language")}
		if links != nil {
			in.PostURL = links.PostURL(post)
		}
		for _, p := range h.activePlugins() {
			if !slices.Contains(p.perms.Hooks, HookCommentSave) {
				continue
			}
			var out CommentOut
			// A failing or slow check keeps the core's decision (fail open):
			// a broken spam filter must not block discussions.
			if err := p.call(r.Context(), ExportComment, callHook, in, &out); err != nil {
				continue
			}
			if v := out.Verdict; rank[v] > rank[verdict] {
				verdict = v
			}
		}
		if verdict == "discard" || verdict == "" {
			return verdict
		}
		return map[string]string{"approve": db.CommentApproved, "hold": db.CommentPending, "spam": db.CommentSpam}[verdict]
	}
}

// CommentStatusChanged tells plugins with the comment_status_changed hook
// about moderation (in the background; errors are logged).
func (h *Host) CommentStatusChanged(ctx context.Context, ids []int64, status string) {
	var list []*plugin
	for _, p := range h.activePlugins() {
		if slices.Contains(p.perms.Hooks, HookCommentStatus) {
			list = append(list, p)
		}
	}
	if len(list) == 0 || len(ids) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		for _, id := range ids {
			c, err := db.GetComment(ctx, h.opts.DB.R, id)
			if err != nil {
				continue
			}
			for _, p := range list {
				data := map[string]any{"comment": commentData(c), "status": status}
				if err := p.call(ctx, ExportAction, callHook, actionIn(HookCommentStatus, data), nil); err != nil &&
					!errors.Is(err, ErrUnavailable) {
					h.log.Warn("plugin comment_status_changed", "plugin", p.pkg.Manifest.Name, "err", err)
				}
			}
		}
	}()
}

var (
	reTag  = regexp.MustCompile(`(?i)<(meta|link)\b[^<>]*>`)
	reAttr = regexp.MustCompile(`(?i)([a-z-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

func actionIn(action string, data any) ActionIn {
	raw, _ := json.Marshal(data)
	return ActionIn{Action: action, Data: raw}
}
