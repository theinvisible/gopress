// Package site serves the public website: routing by WordPress permalink
// rules, theme rendering, feeds, sitemaps, comments and the page cache.
package site

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/theinvisible/gopress/internal/antispam"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/render"
	"github.com/theinvisible/gopress/internal/theme"
)

// Options configures a Site.
type Options struct {
	BaseURL    string // overrides the home option, e.g. http://127.0.0.1:8080
	UploadsDir string
	Secret     []byte // HMAC key for comment form tokens
	CacheBytes int64  // page cache size; 0 disables the cache
	// TrustedProxies are the reverse proxies whose X-Real-Ip and
	// X-Forwarded-For headers are believed.
	TrustedProxies []netip.Prefix
	Log            *slog.Logger
}

// Site is the public website handler.
type Site struct {
	db          *db.DB
	theme       *theme.Theme
	opts        Options
	log         *slog.Logger
	cache       *pageCache
	guard       *antispam.Guard
	static      http.Handler
	uploadsRoot *os.Root

	commentFilter CommentFilter
	commentNotify CommentNotifier
	widgets       map[string]WidgetRenderer
	previewer     Previewer
	ext           Extensions

	gen   atomic.Uint64
	mu    sync.Mutex
	state atomic.Pointer[state]
}

// state is everything derived from the database that requests share. It is
// rebuilt after content changes (generation bump).
type state struct {
	gen   uint64
	s     *Settings
	ix    *contentIndex
	links *Links
	pipe  *render.Pipeline
	perma *permalinkMatcher
	// oldPerma match earlier permalink structures (redirects).
	oldPerma []*permalinkMatcher
	// uploads is the set of servable files below the uploads directory.
	uploads    map[string]bool
	sizeLimits map[string][2]int
}

// New creates the site handler.
func New(d *db.DB, th *theme.Theme, opts Options) *Site {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	s := &Site{db: d, theme: th, opts: opts, log: opts.Log}
	s.cache = newPageCache(opts.CacheBytes)
	s.guard = antispam.New(opts.Secret)
	s.static = staticHandler(th)
	s.uploadsRoot = openUploadsRoot(opts.UploadsDir)
	return s
}

// Invalidate marks all derived state and cached pages as stale; call it
// after every write.
func (s *Site) Invalidate() { s.gen.Add(1) }

func (s *Site) current(ctx context.Context) (*state, error) {
	gen := s.gen.Load()
	if st := s.state.Load(); st != nil && st.gen == gen {
		return st, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.state.Load(); st != nil && st.gen == gen {
		return st, nil
	}
	settings, err := LoadSettings(ctx, s.db.R)
	if err != nil {
		return nil, err
	}
	if s.opts.BaseURL != "" {
		settings.Home = strings.TrimRight(s.opts.BaseURL, "/")
	}
	ix, err := buildIndex(ctx, s.db.R)
	if err != nil {
		return nil, err
	}
	uploads, err := uploadAllowlist(ctx, s.db.R, settings)
	if err != nil {
		return nil, err
	}
	pipe := render.NewPipeline()
	pipe.UseSmilies = settings.UseSmilies
	if s.ext != nil {
		for name, fn := range s.ext.Shortcodes() {
			pipe.Register(name, fn)
		}
		pipe.ContentFilter = s.ext.ContentFilter()
	}
	st := &state{
		gen:        gen,
		s:          settings,
		ix:         ix,
		links:      &Links{home: settings.Home, s: settings, ix: ix},
		pipe:       pipe,
		perma:      newPermalinkMatcher(settings.Permalink),
		oldPerma:   oldPermalinkMatchers(settings.PermalinkHistory),
		uploads:    uploads,
		sizeLimits: settings.ImageSizeLimits(),
	}
	s.state.Store(st)
	s.cache.reset(gen)
	return st, nil
}

// ServeHTTP implements http.Handler.
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.ext != nil && (strings.HasPrefix(r.URL.Path, "/plugins/") || r.URL.RawQuery != "") && s.ext.ServeHTTP(w, r) {
		return
	}
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/static/_core/"):
		serveCoreAsset(w, r)
		return
	case strings.HasPrefix(p, "/static/"):
		s.static.ServeHTTP(w, r)
		return
	case p == "/wp-comments-post.php":
		if r.Method == http.MethodGet && r.URL.Query().Get("gp_token") == "1" {
			s.commentToken(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		s.postComment(w, r)
		return
	case p == "/xmlrpc.php" || p == "/wp-trackback.php":
		http.Error(w, "Gone", http.StatusGone)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	st, err := s.current(r.Context())
	if err != nil {
		s.log.Error("load site state", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/wp-content/uploads/") {
		s.serveUpload(w, r, st)
		return
	}
	if q := r.URL.Query(); q.Get("preview") == "true" && s.previewer != nil {
		s.servePreview(w, r, st)
		return
	}
	// Logged-in visitors (admin bar, customizer preview) and commenters
	// with pending comments get personal, uncached pages.
	vis := s.visitor(r)
	email := s.personalized(r.Context(), r)
	if vis == nil && email == "" && s.cache.serve(w, r, st.gen) {
		return
	}
	if vis != nil && vis.Draft != nil {
		st = st.withDraft(vis.Draft)
	}
	resp := s.handle(st, r, email, vis)
	if resp.status == http.StatusInternalServerError {
		s.log.Error("render", "path", r.URL.String(), "err", resp.err)
	}
	if email != "" || vis != nil {
		resp.header.Set("Cache-Control", "private, no-store")
	} else if resp.cacheable() {
		s.cache.store(r, st.gen, resp)
	}
	resp.write(w, r)
}

// Settings returns the current site settings (for the admin).
func (s *Site) Settings(ctx context.Context) (*Settings, error) {
	st, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return st.s, nil
}

// Links returns the current URL builder (for the admin).
func (s *Site) Links(ctx context.Context) (*Links, error) {
	st, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return st.links, nil
}

// Previewer authorizes previews of unpublished or changed content; the
// admin implements it with the logged-in user's session.
type Previewer interface {
	// PreviewPost returns the post to preview (with unsaved changes
	// applied) if the request may see it.
	PreviewPost(r *http.Request, id int64) (*db.Post, bool)
	// Visitor returns the logged-in user of a request with a session
	// cookie, or nil.
	Visitor(r *http.Request) *Visitor
}

// SetPreviewer installs the previewer.
func (s *Site) SetPreviewer(p Previewer) { s.previewer = p }

// servePreview renders /?p=N&preview=true. Previews are personal: never
// cached, not indexed, and 404 for everyone not allowed to edit the post.
func (s *Site) servePreview(w http.ResponseWriter, r *http.Request, st *state) {
	q := r.URL.Query()
	var id int64
	for _, k := range []string{"preview_id", "p", "page_id"} {
		if n, err := strconv.ParseInt(q.Get(k), 10, 64); err == nil && n > 0 {
			id = n
			break
		}
	}
	rq := newRequest(s, st, r)
	var resp *response
	if p, ok := s.previewer.PreviewPost(r, id); ok && p.Type != db.TypeAttachment {
		resp = rq.singularPost(p, nil)
	} else {
		resp = rq.notFound()
	}
	resp.header.Set("Cache-Control", "no-store")
	resp.header.Set("X-Robots-Tag", "noindex, nofollow")
	resp.write(w, r)
}

// Extensions are the plugins' parts of the public site (internal/plugins).
type Extensions interface {
	// Shortcodes returns the shortcode renderers of the active plugins.
	Shortcodes() map[string]render.ShortcodeFunc
	// ContentFilter returns the the_content filter, or nil if no plugin
	// uses it.
	ContentFilter() func(env *render.Env, content string) string
	// RenderWidget renders a plugin widget type (ok false: unknown type).
	RenderWidget(env *render.Env, typ string, settings map[string]any, titleHTML string) (html string, ok bool)
	// HeadFooter returns the output of the head or footer hook.
	HeadFooter(env *render.Env, hook string) string
	// AssetTags returns the tags for the head (stylesheets) and the end of
	// the body (scripts) of the assets a page needs.
	AssetTags(assets []string) (head, footer string)
	// ServeHTTP answers plugin routes, legacy URLs and asset files; it
	// reports false for every other request.
	ServeHTTP(w http.ResponseWriter, r *http.Request) bool
}

// SetExtensions installs the plugin host.
func (s *Site) SetExtensions(e Extensions) {
	s.ext = e
	s.Invalidate()
}

// ThemeName is the name of the active theme.
func (s *Site) ThemeName() string { return s.theme.Name }

// PluginTemplate returns the active theme's override of a plugin
// template.
func (s *Site) PluginTemplate(plugin, file string) ([]byte, bool) {
	return s.theme.PluginTemplate(plugin, file)
}
