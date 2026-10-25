package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/live"
)

// Live updates for open admin pages (internal/live) over Server-Sent
// Events, and the editors' presence on posts.

const liveMaxTopics = 5

// The stream's heartbeat (and session check) and lifetime: afterwards the
// browser reconnects and is checked again. Variables for tests.
var (
	liveHeartbeat = 25 * time.Second
	liveLifetime  = 30 * time.Minute
)

func (a *Admin) registerLive(mux *http.ServeMux) {
	if a.live == nil {
		return
	}
	mux.Handle("GET /admin/events", a.passive(auth.CapRead, a.events))
	mux.Handle("POST /admin/api/posts/{id}/presence", a.passive(auth.CapEditPosts+"|"+auth.CapEditPages, a.presence))
}

// StopLive ends the open streams (server shutdown); the browsers
// reconnect to the next server.
func (a *Admin) StopLive() {
	a.liveStopOnce.Do(func() { close(a.liveStop) })
}

type passiveKey struct{}

// passive wraps background requests of open pages: their session check
// does not count as activity (an open tab must not keep an idle session
// alive).
func (a *Admin) passive(capability string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.serve(w, r.WithContext(context.WithValue(r.Context(), passiveKey{}, true)), isFull, capability, h)
	})
}

// GET /admin/events?topics=post:12,plugin:name:page
func (a *Admin) events(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return errForbidden
	}
	topicList := r.URL.Query().Get("topics")
	topics, err := a.liveTopics(r.Context(), c.user, topicList)
	if err != nil {
		return err
	}
	sub, err := a.live.Subscribe(c.user, topics)
	if errors.Is(err, live.ErrTooMany) {
		w.Header().Set("Retry-After", "60")
		return &httpError{http.StatusTooManyRequests, "Too many open admin pages."}
	}
	if err != nil {
		return err
	}
	defer a.live.Unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	// The server's read timeout would end the stream (its background read
	// cancels the request); there is no body to read.
	rc.SetReadDeadline(time.Time{})
	write := func(s []byte) bool {
		rc.SetWriteDeadline(time.Now().Add(liveHeartbeat + 10*time.Second))
		if _, err := w.Write(s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write([]byte("retry: 5000\n\n")) {
		return nil
	}
	ctx := r.Context()
	ticker := time.NewTicker(liveHeartbeat)
	defer ticker.Stop()
	end := time.NewTimer(liveLifetime)
	defer end.Stop()
	cur := c.cur
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-end.C:
			return nil
		case <-a.liveStop:
			return nil
		case msg := <-sub.C:
			if !write(msg) {
				return nil
			}
		case <-ticker.C:
			// The session may have ended (logout, expiry, revocation) or
			// the user changed (role): checked without counting as
			// activity.
			next, err := a.auth.Recheck(ctx, cur)
			if err != nil {
				return nil // the browser reconnects (and is checked again)
			}
			if next == nil || next.Session.State != db.SessionFull {
				write([]byte("event: logout\ndata: {}\n\n"))
				return nil
			}
			// Topics were authorized for the user as they were: a user
			// who may no longer edit the post loses the stream.
			if _, err := a.liveTopics(ctx, next.User, topicList); err != nil {
				return nil
			}
			cur = next
			sub.SetUser(next.User)
			msg := []byte(": ping\n\n")
			if sub.TakeMissed() {
				msg = []byte("event: resync\ndata: {}\n\n")
			}
			if !write(msg) {
				return nil
			}
		}
	}
}

// liveTopics checks the topics a stream asks for: a post the user may
// edit, or a plugin page the user may open.
func (a *Admin) liveTopics(ctx context.Context, u *db.User, list string) ([]string, error) {
	if list == "" {
		return nil, nil
	}
	parts := strings.Split(list, ",")
	if len(parts) > liveMaxTopics {
		return nil, badRequest("Too many topics.")
	}
	var out []string
	for _, t := range parts {
		switch {
		case strings.HasPrefix(t, "post:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(t, "post:"), 10, 64)
			if err != nil || id <= 0 {
				return nil, badRequest("Invalid topic.")
			}
			p, err := db.GetPost(ctx, a.db.R, id)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && !auth.CanEditPost(u, p)) {
				return nil, errForbidden
			}
			if err != nil {
				return nil, err
			}
			out = append(out, live.PostTopic(id))
		case strings.HasPrefix(t, "plugin:") && a.plugins != nil:
			name, page, _ := strings.Cut(strings.TrimPrefix(t, "plugin:"), ":")
			pg, ok := a.plugins.Page(name, page)
			if !ok || !auth.Can(u, pg.Capability) {
				return nil, errForbidden
			}
			out = append(out, live.PluginTopic(name, page))
		default:
			return nil, badRequest("Invalid topic.")
		}
	}
	return out, nil
}

// POST /admin/api/posts/{id}/presence (form editing=1|0): the
// editor of a post says it is open (every 30 s) or closed; the answer
// lists the others editing it.
func (a *Admin) presence(w http.ResponseWriter, r *http.Request, c *reqCtx) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return errNotFound
	}
	p, err := db.GetPost(r.Context(), a.db.R, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if !auth.CanEditPost(c.user, p) {
		return errForbidden
	}
	others := a.presenceOf.Beat(id, c.user, r.PostFormValue("editing") == "1")
	writeJSON(w, http.StatusOK, map[string]any{"editors": others, "modified": p.ModifiedGMT.UTC().Format(time.RFC3339)})
	return nil
}
