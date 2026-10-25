package admin

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/theinvisible/gopress/internal/auth"
	"github.com/theinvisible/gopress/internal/db"
	"github.com/theinvisible/gopress/internal/live"
)

// stream opens /admin/events on a real server and returns its lines.
func (e *testEnv) stream(srv *httptest.Server, cur *auth.Current, query string, hdr map[string]string) (*http.Response, <-chan string) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/admin/events"+query, nil)
	if cur != nil {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cur.Session.UserAgent})
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		e.t.Fatal(err)
	}
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return res, lines
}

// waitFor reads lines until one contains want.
func waitFor(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q", want)
			}
			if strings.Contains(l, want) {
				return
			}
		case <-timeout:
			t.Fatalf("no %q", want)
		}
	}
}

func TestLiveStream(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close) // after the streams' cleanups (they end them)
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	author := e.session(auth.RoleAuthor, db.SessionFull)

	if res, _ := e.stream(srv, nil, "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a session: %d", res.StatusCode)
	}
	if res, _ := e.stream(srv, admin, "", map[string]string{"Sec-Fetch-Site": "cross-site"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site: %d", res.StatusCode)
	}
	for _, q := range []string{"?topics=post:10", "?topics=plugin:x:y", "?topics=other", "?topics=post:1,post:2,post:3,post:4,post:5,post:6"} {
		if res, _ := e.stream(srv, author, q, nil); res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusBadRequest {
			t.Errorf("author %s: %d", q, res.StatusCode)
		}
	}

	res, lines := e.stream(srv, admin, "?topics=post:10", nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("stream: %d %v", res.StatusCode, res.Header)
	}
	waitFor(t, lines, "retry: 5000")
	// A comment awaiting moderation, and a save by another editor.
	e.cs.NotifyComment(context.Background(), &db.Comment{ID: 99, PostID: 10, Author: "Visitor", Status: db.CommentPending}, false)
	waitFor(t, lines, `"author":"Visitor"`)
	editor := e.users[auth.RoleEditor]
	p, _ := db.GetPost(context.Background(), e.d.R, 10)
	ed := e.cs.As(editor, nil)
	in := ed.InputFrom(context.Background(), p)
	in.KeepContent, in.KeepExcerpt = true, true
	if _, err := ed.Save(context.Background(), p, in); err != nil {
		t.Fatal(err)
	}
	waitFor(t, lines, `"by":"editor"`)

	// Too many streams of one user.
	for range live.MaxStreamsPerUser - 1 {
		e.stream(srv, admin, "", nil)
	}
	if res, _ := e.stream(srv, admin, "", nil); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("sixth stream: %d", res.StatusCode)
	}
}

func TestLiveStreamEndsWithTheSession(t *testing.T) {
	old := liveHeartbeat
	liveHeartbeat = 100 * time.Millisecond
	defer func() { liveHeartbeat = old }()
	e := newEnv(t)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close) // after the streams' cleanups (they end them)
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	_, lines := e.stream(srv, admin, "", nil)
	waitFor(t, lines, ": ping")
	db.DeleteSession(context.Background(), e.d.W, admin.Session.IDHash)
	waitFor(t, lines, "event: logout")
}

func TestPresence(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.session(auth.RoleAdministrator, db.SessionFull)
	editor := e.session(auth.RoleEditor, db.SessionFull)
	author := e.session(auth.RoleAuthor, db.SessionFull)
	beat := func(cur *auth.Current, id string) *http.Response {
		return post(e, "/admin/api/posts/"+id+"/presence", cur, url.Values{"editing": {"1"}})
	}
	if res := beat(editor, "10"); res.StatusCode != 200 {
		t.Fatalf("editor: %d", res.StatusCode)
	}
	res := beat(admin, "10")
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), `"name":"editor"`) {
		t.Errorf("admin does not see the editor: %s", b)
	}
	if res := beat(author, "10"); res.StatusCode != http.StatusForbidden {
		t.Errorf("author on another's post: %d", res.StatusCode)
	}
	if res := beat(author, "999"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown post: %d", res.StatusCode)
	}
	// Background requests do not keep an idle session alive.
	e.d.W.ExecContext(ctx, `UPDATE sessions SET last_seen = last_seen - 900 WHERE id_hash = ?`, editor.Session.IDHash)
	before, _ := db.GetSession(ctx, e.d.R, editor.Session.IDHash)
	beat(editor, "10")
	after, _ := db.GetSession(ctx, e.d.R, editor.Session.IDHash)
	if after.LastSeen != before.LastSeen {
		t.Error("presence counted as activity")
	}
	e.do("GET", "/admin/", editor, nil, nil)
	if now, _ := db.GetSession(ctx, e.d.R, editor.Session.IDHash); now.LastSeen == before.LastSeen {
		t.Error("a page view did not count as activity")
	}
}
