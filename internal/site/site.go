// Package site serves the public website: routing by WordPress permalink
// rules, theme rendering, feeds, sitemaps, comments and the page cache.
package site

import (
	"context"
	"log/slog"
	"net/http"
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
	TrustProxy bool   // take the client IP from X-Real-Ip/X-Forwarded-For
	Log        *slog.Logger
}

// Site is the public website handler.
type Site struct {
	db      *db.DB
	theme   *theme.Theme
	opts    Options
	log     *slog.Logger
	cache   *pageCache
	guard   *antispam.Guard
	static  http.Handler
	uploads http.Handler

	commentFilter CommentFilter
	widgets       map[string]WidgetRenderer

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
	s.uploads = uploadsHandler(opts.UploadsDir)
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
	pipe := render.NewPipeline()
	pipe.UseSmilies = settings.UseSmilies
	st := &state{
		gen:   gen,
		s:     settings,
		ix:    ix,
		links: &Links{home: settings.Home, s: settings, ix: ix},
		pipe:  pipe,
		perma: newPermalinkMatcher(settings.Permalink),
	}
	s.state.Store(st)
	s.cache.reset(gen)
	return st, nil
}

// ServeHTTP implements http.Handler.
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/static/"):
		s.static.ServeHTTP(w, r)
		return
	case strings.HasPrefix(p, "/wp-content/uploads/"):
		s.uploads.ServeHTTP(w, r)
		return
	case p == "/wp-comments-post.php":
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
	if cacheable(r) {
		if s.cache.serve(w, r, st.gen) {
			return
		}
	}
	resp := s.handle(st, r)
	if resp.status == http.StatusInternalServerError {
		s.log.Error("render", "path", r.URL.String(), "err", resp.err)
	}
	if cacheable(r) && resp.cacheable() {
		s.cache.store(r, st.gen, resp)
	}
	resp.write(w, r)
}

// cacheable reports whether a request may be answered from the page cache:
// anonymous visitors without per-visitor content.
func cacheable(r *http.Request) bool {
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, "comment_author_") || strings.HasPrefix(c.Name, "wordpress_logged_in") || c.Name == "gopress_session" {
			return false
		}
	}
	return r.URL.Query().Get("replytocom") == ""
}
